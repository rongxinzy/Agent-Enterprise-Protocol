package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Server) heartbeat(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Status                 string  `json:"status"`
		LastControlEventCursor *string `json:"lastControlEventCursor"`
		// Optional client identity refresh; applied to the session as-is with no
		// User-Agent fallback, so version upgrades can overwrite a stale label.
		Client *sessionClientInput `json:"client"`
		// Legacy fields are accepted during the SDK/RBAC identity cutover.
		AgentVersion         string   `json:"agentVersion"`
		Platform             string   `json:"platform"`
		AppliedSkillRevision *string  `json:"appliedSkillRevision"`
		InstalledSkillIDs    []string `json:"installedSkillIds"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.Client != nil && !input.Client.valid() {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The client identity is invalid.")
		return
	}
	claims := claimsFrom(request)
	if claims.SessionID == "" {
		writeProblem(response, request, http.StatusUnauthorized, "SESSION_REQUIRED", "A user session is required.")
		return
	}
	if input.Client != nil {
		clientName, clientVersion, clientDeviceID := input.Client.identity().NullableArgs()
		if _, err := s.app.Database().Exec(request.Context(), `UPDATE user_sessions SET client_name=$2,client_version=$3,client_device_id=$4 WHERE session_id=$1`, claims.SessionID, clientName, clientVersion, clientDeviceID); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	s.heartbeatUserSession(response, request, claims.SessionID, input.LastControlEventCursor)
}

func (s *Server) heartbeatUserSession(response http.ResponseWriter, request *http.Request, sessionID string, _ *string) {
	var pending bool
	var watermark *string
	err := s.app.Database().QueryRow(request.Context(), `SELECT EXISTS(SELECT 1 FROM session_control_deliveries d JOIN control_events e ON e.event_id=d.event_id WHERE d.session_id=$1 AND d.state='pending' AND e.state='active' AND e.expires_at>now()), (SELECT max(cursor)::text FROM session_control_deliveries WHERE session_id=$1)`, sessionID).Scan(&pending, &watermark)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	_, _ = s.app.Database().Exec(request.Context(), `UPDATE session_control_deliveries d SET state='expired',updated_at=now() FROM control_events e WHERE d.event_id=e.event_id AND d.session_id=$1 AND d.state='pending' AND e.expires_at<=now()`, sessionID)
	writeJSON(response, http.StatusOK, map[string]any{
		"serverTime":    time.Now().UTC(),
		"controlEvents": map[string]any{"pending": pending, "watermark": stringValue(watermark)},
		// Kept until all clients consume the canonical controlEvents object.
		"hasPendingControlEvents":   pending,
		"controlEventWatermark":     watermark,
		"nextHeartbeatAfterSeconds": 30,
	})
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Server) listAgentControlEvents(response http.ResponseWriter, request *http.Request) {
	after, _ := strconv.ParseInt(request.URL.Query().Get("afterCursor"), 10, 64)
	claims := claimsFrom(request)
	if claims.SessionID == "" {
		writeProblem(response, request, http.StatusUnauthorized, "SESSION_REQUIRED", "A user session is required.")
		return
	}
	s.listUserSessionControlEvents(response, request, claims.SessionID, after)
}

func (s *Server) listUserSessionControlEvents(response http.ResponseWriter, request *http.Request, sessionID string, after int64) {
	_, err := s.app.Database().Exec(request.Context(), `UPDATE session_control_deliveries d SET state='expired',updated_at=now() FROM control_events e WHERE d.event_id=e.event_id AND d.session_id=$1 AND d.state='pending' AND e.expires_at<=now()`, sessionID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	rows, err := s.app.Database().Query(request.Context(), `SELECT d.delivery_id,e.event_id,d.cursor,e.type,e.scope_type,e.scope_id,e.resource_type,e.resource_id,e.resource_revision,e.task_type,e.created_at,e.expires_at FROM session_control_deliveries d JOIN control_events e ON e.event_id=d.event_id WHERE d.session_id=$1 AND d.state='pending' AND e.state='active' AND e.expires_at>now() AND d.cursor>$2 ORDER BY d.cursor LIMIT $3`, sessionID, after, limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	var next *string
	for rows.Next() {
		var deliveryID, eventID, eventType, scopeType, taskType string
		var cursor int64
		var scopeID, resourceType, resourceID, resourceRevision *string
		var createdAt, expiresAt time.Time
		if err := rows.Scan(&deliveryID, &eventID, &cursor, &eventType, &scopeType, &scopeID, &resourceType, &resourceID, &resourceRevision, &taskType, &createdAt, &expiresAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		scope := map[string]any{"type": scopeType}
		if scopeID != nil {
			scope["id"] = *scopeID
		}
		item := map[string]any{"deliveryId": deliveryID, "eventId": eventID, "cursor": strconv.FormatInt(cursor, 10), "type": eventType, "scope": scope, "task": map[string]string{"type": taskType}, "createdAt": createdAt, "expiresAt": expiresAt}
		if resourceID != nil {
			item["resource"] = map[string]any{"type": resourceType, "id": resourceID, "revision": resourceRevision}
		}
		items = append(items, item)
		value := strconv.FormatInt(cursor, 10)
		next = &value
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (s *Server) acknowledgeControlEvent(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Status     string    `json:"status"`
		ReceivedAt time.Time `json:"receivedAt"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.Status != "received" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The acknowledgement status must be received.")
		return
	}
	deliveryID := chi.URLParam(request, "deliveryId")
	claims := claimsFrom(request)
	if claims.SessionID == "" {
		writeProblem(response, request, http.StatusUnauthorized, "SESSION_REQUIRED", "A user session is required.")
		return
	}
	s.acknowledgeUserSessionDelivery(response, request, deliveryID, claims.SessionID, input.ReceivedAt)
}

func (s *Server) acknowledgeUserSessionDelivery(response http.ResponseWriter, request *http.Request, deliveryID, sessionID string, receivedAt time.Time) {
	result, err := s.app.Database().Exec(request.Context(), `UPDATE session_control_deliveries SET state='received',received_at=COALESCE(received_at,$3),updated_at=now(),attempt_count=attempt_count+1 WHERE delivery_id=$1 AND session_id=$2 AND state IN ('pending','failed')`, deliveryID, sessionID, receivedAt)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		var state string
		err = s.app.Database().QueryRow(request.Context(), `SELECT state FROM session_control_deliveries WHERE delivery_id=$1 AND session_id=$2`, deliveryID, sessionID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The delivery was not found.")
			return
		}
		if state != "received" && state != "running" && state != "succeeded" && state != "failed" {
			writeProblem(response, request, http.StatusConflict, "DELIVERY_STATE_CONFLICT", "The delivery cannot be acknowledged in its current state.")
			return
		}
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) reportControlEventResult(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Status          string     `json:"status"`
		StartedAt       *time.Time `json:"startedAt"`
		CompletedAt     *time.Time `json:"completedAt"`
		AppliedRevision *string    `json:"appliedRevision"`
		ErrorCode       *string    `json:"errorCode"`
		Message         *string    `json:"message"`
		Retryable       *bool      `json:"retryable"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.Status != "running" && input.Status != "succeeded" && input.Status != "failed" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The result status is invalid.")
		return
	}
	deliveryID := chi.URLParam(request, "deliveryId")
	claims := claimsFrom(request)
	if claims.SessionID == "" {
		writeProblem(response, request, http.StatusUnauthorized, "SESSION_REQUIRED", "A user session is required.")
		return
	}
	s.reportUserSessionDeliveryResult(response, request, deliveryID, claims.SessionID, input.Status, input.StartedAt, input.CompletedAt, input.AppliedRevision, input.ErrorCode, input.Message)
}

func (s *Server) reportUserSessionDeliveryResult(response http.ResponseWriter, request *http.Request, deliveryID, sessionID, status string, startedAt, completedAt *time.Time, appliedRevision, errorCode, message *string) {
	result, err := s.app.Database().Exec(request.Context(), `UPDATE session_control_deliveries SET state=$3,started_at=CASE WHEN $3='running' THEN COALESCE($4,now()) ELSE COALESCE($4,started_at) END,completed_at=CASE WHEN $3='running' THEN NULL ELSE COALESCE($5,completed_at) END,applied_revision=CASE WHEN $3='running' THEN NULL ELSE COALESCE($6,applied_revision) END,error_code=CASE WHEN $3 IN ('running','succeeded') THEN NULL ELSE COALESCE($7,error_code) END,message=CASE WHEN $3 IN ('running','succeeded') THEN NULL ELSE COALESCE($8,message) END,updated_at=now() WHERE delivery_id=$1 AND session_id=$2 AND state IN ('received','running','failed')`, deliveryID, sessionID, status, startedAt, completedAt, appliedRevision, errorCode, message)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		var state string
		err = s.app.Database().QueryRow(request.Context(), `SELECT state FROM session_control_deliveries WHERE delivery_id=$1 AND session_id=$2`, deliveryID, sessionID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The delivery was not found.")
			return
		}
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		// A recorded success is immutable: a repeat report is a no-op rather
		// than an overwrite of the evidence (completed_at, applied_revision).
		if state == "succeeded" {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		writeProblem(response, request, http.StatusConflict, "DELIVERY_STATE_CONFLICT", "The delivery cannot accept this result.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type createControlEventRequest struct {
	Type  string `json:"type"`
	Scope struct {
		Type               string  `json:"type"`
		ID                 *string `json:"id"`
		IncludeDescendants bool    `json:"includeDescendants"`
	} `json:"scope"`
	Resource *struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"resource"`
	Task struct {
		Type string `json:"type"`
	} `json:"task"`
	ExpiresAt     time.Time `json:"expiresAt"`
	SupersedesKey *string   `json:"supersedesKey"`
}

func (s *Server) createControlEvent(response http.ResponseWriter, request *http.Request) {
	var input createControlEventRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.ExpiresAt.Before(time.Now()) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The event expiry must be in the future.")
		return
	}
	if input.Scope.Type != "global" && input.Scope.Type != "team" && input.Scope.Type != "role" && input.Scope.Type != "user" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_SCOPE", "Control event scope must be global, team, role, or user.")
		return
	}
	claims := claimsFrom(request)
	eventID := uuid.NewString()
	tx, err := s.app.Database().Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	var resourceType, resourceID, resourceRevision *string
	if input.Resource != nil {
		resourceType = &input.Resource.Type
		resourceID = &input.Resource.ID
		resourceRevision = &input.Resource.Revision
	}
	_, err = tx.Exec(request.Context(), `INSERT INTO control_events (event_id,deployment_id,type,scope_type,scope_id,resource_type,resource_id,resource_revision,task_type,supersedes_key,expires_at,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, eventID, claims.DeploymentID, input.Type, input.Scope.Type, input.Scope.ID, resourceType, resourceID, resourceRevision, input.Task.Type, input.SupersedesKey, input.ExpiresAt, claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if input.SupersedesKey != nil {
		_, err = tx.Exec(request.Context(), `WITH old AS (UPDATE control_events SET state='superseded' WHERE deployment_id=$1 AND supersedes_key=$2 AND event_id<>$3 AND state='active' RETURNING event_id) UPDATE session_control_deliveries SET state='superseded',updated_at=now() WHERE event_id IN (SELECT event_id FROM old) AND state='pending'`, claims.DeploymentID, *input.SupersedesKey, eventID)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	// New user sessions receive one durable delivery per terminal. The user
	// topic is represented by the session rows, so every active terminal gets
	// an independent cursor and acknowledgement state. The fan-out is one
	// INSERT..SELECT (the same shape as createSkillAssignmentEvent): the
	// DISTINCT-then-loop path shipped the whole session set over the wire
	// and re-inserted it row by row — 135ms at 383 sessions, growing
	// linearly. RowsAffected counts deliveries actually created, which is
	// the pending summary (ON CONFLICT dedupes; session_id is unique).
	tag, err := tx.Exec(request.Context(), `INSERT INTO session_control_deliveries (delivery_id,event_id,session_id)
SELECT gen_random_uuid()::text,$1,s.session_id
FROM user_sessions s
JOIN users u ON u.id=s.user_id AND u.deployment_id=$2
WHERE s.deployment_id=$2 AND s.revoked_at IS NULL
  AND ($3='global' OR ($3='user' AND s.user_id=$4) OR ($3='team' AND EXISTS (
    SELECT 1 FROM user_team_bindings utb
    WHERE utb.deployment_id=$2 AND utb.user_id=s.user_id AND utb.team_id=$4
  )) OR ($3='role' AND EXISTS (
    SELECT 1 FROM user_role_bindings urb
    JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true
    WHERE urb.deployment_id=$2 AND urb.user_id=s.user_id AND urb.role_id=$4
  )))
ON CONFLICT (event_id,session_id) DO NOTHING`, eventID, claims.DeploymentID, input.Scope.Type, input.Scope.ID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	pending := int(tag.RowsAffected())
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"eventId": eventID, "type": input.Type, "scope": input.Scope, "resource": input.Resource, "task": input.Task, "expiresAt": input.ExpiresAt, "state": "active", "createdAt": time.Now().UTC(), "createdBy": claims.Subject, "deliverySummary": map[string]int{"pending": pending, "received": 0, "running": 0, "succeeded": 0, "failed": 0, "expired": 0, "superseded": 0}})
}

func (s *Server) listAdminControlEvents(response http.ResponseWriter, request *http.Request) {
	s.adminEvents(response, request, "")
}
func (s *Server) getAdminControlEvent(response http.ResponseWriter, request *http.Request) {
	s.adminEvents(response, request, chi.URLParam(request, "eventId"))
}

func (s *Server) adminEvents(response http.ResponseWriter, request *http.Request, eventID string) {
	conditions := []string{"e.deployment_id=$1", "($2='' OR e.event_id=$2)"}
	args := []any{claimsFrom(request).DeploymentID, eventID}
	if eventID == "" {
		if raw := strings.TrimSpace(request.URL.Query().Get("cursor")); raw != "" {
			cursor, err := decodePageCursor(raw)
			if err != nil {
				writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The control event cursor is invalid.")
				return
			}
			args = append(args, cursor.OccurredAt, cursor.EventID)
			conditions = append(conditions, fmt.Sprintf("(e.created_at,e.event_id) < ($%d,$%d)", len(args)-1, len(args)))
		}
	}
	pageLimit := int(limit(request))
	fetch := pageLimit + 1
	if eventID != "" {
		fetch = 1
	}
	args = append(args, fetch)
	query := `SELECT e.event_id,e.type,e.scope_type,e.scope_id,e.resource_type,e.resource_id,e.resource_revision,e.task_type,e.expires_at,e.state,e.created_at,e.created_by,
count(*) FILTER(WHERE d.state='pending'),count(*) FILTER(WHERE d.state='received'),count(*) FILTER(WHERE d.state='running'),count(*) FILTER(WHERE d.state='succeeded'),count(*) FILTER(WHERE d.state='failed'),count(*) FILTER(WHERE d.state='expired'),count(*) FILTER(WHERE d.state='superseded')
FROM control_events e LEFT JOIN session_control_deliveries d ON d.event_id=e.event_id WHERE ` + strings.Join(conditions, " AND ") + fmt.Sprintf(" GROUP BY e.event_id ORDER BY e.created_at DESC, e.event_id DESC LIMIT $%d", len(args))
	rows, err := s.app.Database().Query(request.Context(), query, args...)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	var nextCursor *string
	var lastCreatedAt time.Time
	lastEventID := ""
	for rows.Next() {
		var id, eventType, scopeType, taskType, state, createdBy string
		var scopeID, resourceType, resourceID, resourceRevision *string
		var expiresAt, createdAt time.Time
		counts := make([]int64, 7)
		if err := rows.Scan(&id, &eventType, &scopeType, &scopeID, &resourceType, &resourceID, &resourceRevision, &taskType, &expiresAt, &state, &createdAt, &createdBy, &counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6]); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if eventID == "" && len(items) == pageLimit {
			value := encodePageCursor(pageCursor{OccurredAt: lastCreatedAt, EventID: lastEventID})
			nextCursor = &value
			break
		}
		item := map[string]any{"eventId": id, "type": eventType, "scope": map[string]any{"type": scopeType, "id": scopeID}, "resource": map[string]any{"type": resourceType, "id": resourceID, "revision": resourceRevision}, "task": map[string]string{"type": taskType}, "expiresAt": expiresAt, "state": state, "createdAt": createdAt, "createdBy": createdBy, "deliverySummary": map[string]int64{"pending": counts[0], "received": counts[1], "running": counts[2], "succeeded": counts[3], "failed": counts[4], "expired": counts[5], "superseded": counts[6]}}
		items = append(items, item)
		lastCreatedAt, lastEventID = createdAt, id
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if eventID != "" {
		if len(items) == 0 {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The control event was not found.")
			return
		}
		writeJSON(response, http.StatusOK, items[0])
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func (s *Server) cancelControlEvent(response http.ResponseWriter, request *http.Request) {
	eventID := chi.URLParam(request, "eventId")
	result, err := s.app.Database().Exec(request.Context(), `UPDATE control_events SET state='cancelled' WHERE event_id=$1 AND deployment_id=$2 AND state='active'`, eventID, claimsFrom(request).DeploymentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeProblem(response, request, http.StatusConflict, "EVENT_STATE_CONFLICT", "The event cannot be cancelled.")
		return
	}
	_, _ = s.app.Database().Exec(request.Context(), `UPDATE session_control_deliveries SET state='superseded',updated_at=now() WHERE event_id=$1 AND state='pending'`, eventID)
	s.adminEvents(response, request, eventID)
}

func (s *Server) listControlEventDeliveries(response http.ResponseWriter, request *http.Request) {
	eventID := chi.URLParam(request, "eventId")
	tenant := claimsFrom(request).DeploymentID
	_, err := s.app.Database().Exec(request.Context(), `UPDATE session_control_deliveries d SET state='expired',updated_at=now() FROM control_events e WHERE d.event_id=e.event_id AND d.event_id=$1 AND e.deployment_id=$2 AND d.state='pending' AND e.expires_at<=now()`, eventID, tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("cursor")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The delivery cursor is invalid.")
			return
		}
		after = parsed
	}
	pageLimit := int(limit(request))
	rows, err := s.app.Database().Query(request.Context(), `SELECT d.cursor,d.delivery_id,d.event_id,d.session_id,d.state,d.attempt_count,d.received_at,d.completed_at,d.updated_at,d.error_code,d.message FROM session_control_deliveries d JOIN control_events e ON e.event_id=d.event_id WHERE d.event_id=$1 AND e.deployment_id=$2 AND d.cursor>$3 ORDER BY d.cursor LIMIT $4`, eventID, tenant, after, pageLimit+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	var nextCursor *string
	lastCursor := ""
	for rows.Next() {
		var cursor int64
		var deliveryID, rowEventID, state string
		var sessionID pgtype.Text
		var attempts int
		var receivedAt, completedAt *time.Time
		var updatedAt time.Time
		var errorCode, message *string
		if err := rows.Scan(&cursor, &deliveryID, &rowEventID, &sessionID, &state, &attempts, &receivedAt, &completedAt, &updatedAt, &errorCode, &message); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if len(items) == pageLimit {
			value := lastCursor
			nextCursor = &value
			break
		}
		items = append(items, map[string]any{"deliveryId": deliveryID, "eventId": rowEventID, "sessionId": nullablePGText(sessionID), "state": state, "attemptCount": attempts, "receivedAt": receivedAt, "completedAt": completedAt, "updatedAt": updatedAt, "errorCode": errorCode, "message": message})
		lastCursor = strconv.FormatInt(cursor, 10)
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func (s *Server) uploadTelemetryBatch(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Events []struct {
			EventID    string    `json:"eventId"`
			Type       string    `json:"type"`
			OccurredAt time.Time `json:"occurredAt"`
			Resource   *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"resource"`
			Result *string        `json:"result"`
			Data   map[string]any `json:"data"`
		} `json:"events"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if len(input.Events) > 100 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "At most 100 telemetry events are accepted per batch.")
		return
	}
	claims := claimsFrom(request)
	accepted := make([]string, 0, len(input.Events))
	rejected := make([]map[string]string, 0)
	// Rows land in ONE multi-VALUES statement: a single network round trip
	// and a single transaction, where the per-row path autocommitted up to
	// a hundred inserts per batch (571ms p50 per 100 rows at scale; see the
	// scale-round findings). Duplicates stay "accepted" via ON CONFLICT
	// DO NOTHING, matching the previous per-row semantics.
	rows := make([][]any, 0, len(input.Events))
	for _, event := range input.Events {
		if claims.SessionID == "" {
			rejected = append(rejected, map[string]string{"eventId": event.EventID, "code": "SESSION_REQUIRED", "message": "A user session is required."})
			continue
		}
		payload, _ := json.Marshal(event.Data)
		var resourceType, resourceID *string
		if event.Resource != nil {
			resourceType = &event.Resource.Type
			resourceID = &event.Resource.ID
		}
		rows = append(rows, []any{event.EventID, claims.DeploymentID, claims.Subject, claims.SessionID, event.Type, resourceType, resourceID, event.Result, payload, event.OccurredAt})
		accepted = append(accepted, event.EventID)
	}
	if len(rows) > 0 {
		values := make([]string, 0, len(rows))
		var args []any
		for _, row := range rows {
			placeholders := make([]string, 0, len(row))
			for _, value := range row {
				args = append(args, value)
				placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
			}
			values = append(values, "("+strings.Join(placeholders, ",")+")")
		}
		if _, err := s.app.Database().Exec(request.Context(), `INSERT INTO telemetry_events(event_id,deployment_id,user_id,session_id,type,resource_type,resource_id,result,payload,occurred_at) VALUES `+strings.Join(values, ",")+` ON CONFLICT(event_id) DO NOTHING`, args...); err != nil {
			// A single statement cannot fail per row, so a database error
			// rejects the whole insertable set at once.
			accepted = accepted[:0]
			for _, row := range rows {
				rejected = append(rejected, map[string]string{"eventId": fmt.Sprint(row[0]), "code": "INTERNAL_ERROR", "message": "The telemetry batch could not be stored."})
			}
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"accepted": accepted, "rejected": rejected})
}

func (s *Server) searchTelemetryEvents(response http.ResponseWriter, request *http.Request) {
	filters := request.URL.Query()
	conditions := []string{"deployment_id=$1"}
	args := []any{claimsFrom(request).DeploymentID}
	addTextFilter := func(name, column string) {
		if value := strings.TrimSpace(filters.Get(name)); value != "" {
			args = append(args, value)
			conditions = append(conditions, fmt.Sprintf("%s=$%d", column, len(args)))
		}
	}
	addTextFilter("userId", "user_id")
	addTextFilter("sessionId", "session_id")
	addTextFilter("type", "type")
	addTextFilter("resourceType", "resource_type")
	addTextFilter("resourceId", "resource_id")
	if result := strings.TrimSpace(filters.Get("result")); result != "" {
		if result != "success" && result != "failure" && result != "info" {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The telemetry result filter is invalid.")
			return
		}
		args = append(args, result)
		conditions = append(conditions, fmt.Sprintf("result=$%d", len(args)))
	}
	var occurredAfter, occurredBefore *time.Time
	for name, target := range map[string]**time.Time{"occurredAfter": &occurredAfter, "occurredBefore": &occurredBefore} {
		value := strings.TrimSpace(filters.Get(name))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("The %s filter must be RFC3339.", name))
			return
		}
		*target = &parsed
	}
	if occurredAfter != nil {
		args = append(args, *occurredAfter)
		conditions = append(conditions, fmt.Sprintf("occurred_at >= $%d", len(args)))
	}
	if occurredBefore != nil {
		args = append(args, *occurredBefore)
		conditions = append(conditions, fmt.Sprintf("occurred_at <= $%d", len(args)))
	}
	if occurredAfter != nil && occurredBefore != nil && occurredAfter.After(*occurredBefore) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "occurredAfter must not be later than occurredBefore.")
		return
	}
	if rawCursor := strings.TrimSpace(filters.Get("cursor")); rawCursor != "" {
		cursor, err := decodePageCursor(rawCursor)
		if err != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The telemetry cursor is invalid.")
			return
		}
		args = append(args, cursor.OccurredAt, cursor.EventID)
		conditions = append(conditions, fmt.Sprintf("(occurred_at,event_id) < ($%d,$%d)", len(args)-1, len(args)))
	}
	pageLimit := int(limit(request))
	args = append(args, pageLimit+1)
	query := `SELECT event_id,user_id,session_id,type,resource_type,resource_id,result,payload,occurred_at,received_at FROM telemetry_events WHERE ` + strings.Join(conditions, " AND ") + fmt.Sprintf(" ORDER BY occurred_at DESC,event_id DESC LIMIT $%d", len(args))
	rows, err := s.app.Database().Query(request.Context(), query, args...)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	var nextCursor *string
	var lastCursor pageCursor
	for rows.Next() {
		var eventID, userID, eventType string
		var sessionID pgtype.Text
		var resourceType, resourceID, result pgtype.Text
		var payload []byte
		var occurredAt, receivedAt time.Time
		if err := rows.Scan(&eventID, &userID, &sessionID, &eventType, &resourceType, &resourceID, &result, &payload, &occurredAt, &receivedAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if len(items) == pageLimit {
			value := encodePageCursor(lastCursor)
			nextCursor = &value
			break
		}
		var data any
		_ = json.Unmarshal(payload, &data)
		items = append(items, map[string]any{"eventId": eventID, "userId": userID, "sessionId": nullablePGText(sessionID), "type": eventType, "resourceType": nullablePGText(resourceType), "resourceId": nullablePGText(resourceID), "result": nullablePGText(result), "data": data, "occurredAt": occurredAt, "receivedAt": receivedAt})
		lastCursor = pageCursor{OccurredAt: occurredAt, EventID: eventID}
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

type pageCursor struct {
	OccurredAt time.Time
	EventID    string
}

func encodePageCursor(cursor pageCursor) string {
	value := cursor.OccurredAt.UTC().Format(time.RFC3339Nano) + "\x00" + cursor.EventID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodePageCursor(raw string) (pageCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return pageCursor{}, err
	}
	parts := strings.SplitN(string(decoded), "\x00", 2)
	if len(parts) != 2 || parts[1] == "" {
		return pageCursor{}, errors.New("invalid telemetry cursor")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return pageCursor{}, err
	}
	return pageCursor{OccurredAt: occurredAt, EventID: parts[1]}, nil
}

func nullablePGText(value pgtype.Text) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
