package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db/generated"
)

func (s *Server) heartbeat(response http.ResponseWriter, request *http.Request) {
	var input struct {
		AgentVersion         string   `json:"agentVersion"`
		Platform             string   `json:"platform"`
		AppliedSkillRevision *string  `json:"appliedSkillRevision"`
		InstalledSkillIDs    []string `json:"installedSkillIds"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	claims := claimsFrom(request)
	agent, err := s.app.DB.GetAgent(request.Context(), claims.AgentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if input.AgentVersion == "" {
		input.AgentVersion = agent.AgentVersion
	}
	if input.Platform == "" {
		input.Platform = agent.Platform
	}
	_, err = s.app.Pool.Exec(request.Context(), `UPDATE agents SET agent_version=$2,platform=$3,last_seen_at=now(),applied_skill_revision=COALESCE($4,applied_skill_revision),installed_skill_ids=CASE WHEN cardinality($5::text[])>0 THEN $5 ELSE installed_skill_ids END WHERE agent_id=$1`, claims.AgentID, input.AgentVersion, input.Platform, input.AppliedSkillRevision, input.InstalledSkillIDs)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	var pending bool
	var watermark *string
	err = s.app.Pool.QueryRow(request.Context(), `SELECT EXISTS(SELECT 1 FROM control_deliveries d JOIN control_events e ON e.event_id=d.event_id WHERE d.agent_id=$1 AND d.state='pending' AND e.state='active' AND e.expires_at>now()), (SELECT max(cursor)::text FROM control_deliveries WHERE agent_id=$1)`, claims.AgentID).Scan(&pending, &watermark)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"serverTime": time.Now().UTC(), "hasPendingControlEvents": pending, "controlEventWatermark": watermark, "nextHeartbeatAfterSeconds": 30})
}

func (s *Server) listAgentControlEvents(response http.ResponseWriter, request *http.Request) {
	after, _ := strconv.ParseInt(request.URL.Query().Get("afterCursor"), 10, 64)
	claims := claimsFrom(request)
	rows, err := s.app.Pool.Query(request.Context(), `SELECT d.delivery_id,e.event_id,d.cursor,e.type,e.scope_type,e.scope_id,e.resource_type,e.resource_id,e.resource_revision,e.task_type,e.created_at,e.expires_at FROM control_deliveries d JOIN control_events e ON e.event_id=d.event_id WHERE d.agent_id=$1 AND d.state='pending' AND e.state='active' AND e.expires_at>now() AND d.cursor>$2 ORDER BY d.cursor LIMIT $3`, claims.AgentID, after, limit(request))
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
	result, err := s.app.Pool.Exec(request.Context(), `UPDATE control_deliveries SET state='received',received_at=COALESCE(received_at,$3),updated_at=now(),attempt_count=attempt_count+1 WHERE delivery_id=$1 AND agent_id=$2 AND state='pending'`, deliveryID, claims.AgentID, input.ReceivedAt)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		var state string
		err = s.app.Pool.QueryRow(request.Context(), `SELECT state FROM control_deliveries WHERE delivery_id=$1 AND agent_id=$2`, deliveryID, claims.AgentID).Scan(&state)
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
	result, err := s.app.Pool.Exec(request.Context(), `UPDATE control_deliveries SET state=$3,started_at=COALESCE($4,started_at),completed_at=COALESCE($5,completed_at),applied_revision=COALESCE($6,applied_revision),error_code=COALESCE($7,error_code),message=COALESCE($8,message),updated_at=now() WHERE delivery_id=$1 AND agent_id=$2 AND (state IN ('received','running') OR state=$3)`, deliveryID, claims.AgentID, input.Status, input.StartedAt, input.CompletedAt, input.AppliedRevision, input.ErrorCode, input.Message)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
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
	claims := claimsFrom(request)
	eventID := uuid.NewString()
	tx, err := s.app.Pool.Begin(request.Context())
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
	_, err = tx.Exec(request.Context(), `INSERT INTO control_events (event_id,enterprise_id,type,scope_type,scope_id,resource_type,resource_id,resource_revision,task_type,supersedes_key,expires_at,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, eventID, claims.Tenant, input.Type, input.Scope.Type, input.Scope.ID, resourceType, resourceID, resourceRevision, input.Task.Type, input.SupersedesKey, input.ExpiresAt, claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if input.SupersedesKey != nil {
		_, err = tx.Exec(request.Context(), `WITH old AS (UPDATE control_events SET state='superseded' WHERE enterprise_id=$1 AND supersedes_key=$2 AND event_id<>$3 AND state='active' RETURNING event_id) UPDATE control_deliveries SET state='superseded',updated_at=now() WHERE event_id IN (SELECT event_id FROM old) AND state='pending'`, claims.Tenant, *input.SupersedesKey, eventID)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	rows, err := tx.Query(request.Context(), `SELECT a.agent_id FROM agents a JOIN users u ON u.id=a.user_id WHERE a.enterprise_id=$1 AND ($2='global' OR ($2='agent' AND a.agent_id=$3) OR ($2='user' AND a.user_id=$3) OR ($2='organization' AND $3=ANY(u.organization_ids)))`, claims.Tenant, input.Scope.Type, input.Scope.ID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	agentIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			databaseFailure(response, request, err)
			return
		}
		agentIDs = append(agentIDs, id)
	}
	rows.Close()
	for _, agentID := range agentIDs {
		if _, err = tx.Exec(request.Context(), `INSERT INTO control_deliveries (delivery_id,event_id,agent_id) VALUES ($1,$2,$3)`, uuid.NewString(), eventID, agentID); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"eventId": eventID, "type": input.Type, "scope": input.Scope, "resource": input.Resource, "task": input.Task, "expiresAt": input.ExpiresAt, "state": "active", "createdAt": time.Now().UTC(), "createdBy": claims.Subject, "deliverySummary": map[string]int{"pending": len(agentIDs), "received": 0, "running": 0, "succeeded": 0, "failed": 0, "expired": 0, "superseded": 0}})
}

func (s *Server) listAdminControlEvents(response http.ResponseWriter, request *http.Request) {
	s.adminEvents(response, request, "")
}
func (s *Server) getAdminControlEvent(response http.ResponseWriter, request *http.Request) {
	s.adminEvents(response, request, chi.URLParam(request, "eventId"))
}

func (s *Server) adminEvents(response http.ResponseWriter, request *http.Request, eventID string) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT e.event_id,e.type,e.scope_type,e.scope_id,e.resource_type,e.resource_id,e.resource_revision,e.task_type,e.expires_at,e.state,e.created_at,e.created_by,
count(*) FILTER(WHERE d.state='pending'),count(*) FILTER(WHERE d.state='received'),count(*) FILTER(WHERE d.state='running'),count(*) FILTER(WHERE d.state='succeeded'),count(*) FILTER(WHERE d.state='failed'),count(*) FILTER(WHERE d.state='expired'),count(*) FILTER(WHERE d.state='superseded')
FROM control_events e LEFT JOIN control_deliveries d ON d.event_id=e.event_id WHERE e.enterprise_id=$1 AND ($2='' OR e.event_id=$2) GROUP BY e.event_id ORDER BY e.created_at DESC LIMIT $3`, claimsFrom(request).Tenant, eventID, limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, eventType, scopeType, taskType, state, createdBy string
		var scopeID, resourceType, resourceID, resourceRevision *string
		var expiresAt, createdAt time.Time
		counts := make([]int64, 7)
		if err := rows.Scan(&id, &eventType, &scopeType, &scopeID, &resourceType, &resourceID, &resourceRevision, &taskType, &expiresAt, &state, &createdAt, &createdBy, &counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6]); err != nil {
			databaseFailure(response, request, err)
			return
		}
		item := map[string]any{"eventId": id, "type": eventType, "scope": map[string]any{"type": scopeType, "id": scopeID}, "resource": map[string]any{"type": resourceType, "id": resourceID, "revision": resourceRevision}, "task": map[string]string{"type": taskType}, "expiresAt": expiresAt, "state": state, "createdAt": createdAt, "createdBy": createdBy, "deliverySummary": map[string]int64{"pending": counts[0], "received": counts[1], "running": counts[2], "succeeded": counts[3], "failed": counts[4], "expired": counts[5], "superseded": counts[6]}}
		items = append(items, item)
	}
	if eventID != "" {
		if len(items) == 0 {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The control event was not found.")
			return
		}
		writeJSON(response, http.StatusOK, items[0])
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nil})
}

func (s *Server) cancelControlEvent(response http.ResponseWriter, request *http.Request) {
	eventID := chi.URLParam(request, "eventId")
	result, err := s.app.Pool.Exec(request.Context(), `WITH cancelled AS (UPDATE control_events SET state='cancelled' WHERE event_id=$1 AND enterprise_id=$2 AND state='active' RETURNING event_id) UPDATE control_deliveries SET state='superseded',updated_at=now() WHERE event_id IN(SELECT event_id FROM cancelled) AND state='pending'`, eventID, claimsFrom(request).Tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeProblem(response, request, http.StatusConflict, "EVENT_STATE_CONFLICT", "The event cannot be cancelled.")
		return
	}
	s.adminEvents(response, request, eventID)
}

func (s *Server) listControlEventDeliveries(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT d.delivery_id,d.event_id,d.agent_id,d.state,d.attempt_count,d.received_at,d.completed_at,d.updated_at,d.error_code,d.message FROM control_deliveries d JOIN control_events e ON e.event_id=d.event_id WHERE d.event_id=$1 AND e.enterprise_id=$2 ORDER BY d.cursor LIMIT $3`, chi.URLParam(request, "eventId"), claimsFrom(request).Tenant, limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var deliveryID, eventID, agentID, state string
		var attempts int
		var receivedAt, completedAt *time.Time
		var updatedAt time.Time
		var errorCode, message *string
		if err := rows.Scan(&deliveryID, &eventID, &agentID, &state, &attempts, &receivedAt, &completedAt, &updatedAt, &errorCode, &message); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"deliveryId": deliveryID, "eventId": eventID, "agentId": agentID, "state": state, "attemptCount": attempts, "receivedAt": receivedAt, "completedAt": completedAt, "updatedAt": updatedAt, "errorCode": errorCode, "message": message})
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nil})
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
	for _, event := range input.Events {
		payload, _ := json.Marshal(event.Data)
		var resourceType, resourceID *string
		if event.Resource != nil {
			resourceType = &event.Resource.Type
			resourceID = &event.Resource.ID
		}
		_, err := s.app.Pool.Exec(request.Context(), `INSERT INTO telemetry_events(event_id,enterprise_id,user_id,agent_id,type,resource_type,resource_id,result,payload,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(event_id) DO NOTHING`, event.EventID, claims.Tenant, claims.Subject, claims.AgentID, event.Type, resourceType, resourceID, event.Result, payload, event.OccurredAt)
		if err != nil {
			rejected = append(rejected, map[string]string{"eventId": event.EventID, "code": "INTERNAL_ERROR"})
			continue
		}
		accepted = append(accepted, event.EventID)
	}
	writeJSON(response, http.StatusOK, map[string]any{"accepted": accepted, "rejected": rejected})
}

func (s *Server) searchTelemetryEvents(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT event_id,user_id,agent_id,type,resource_type,resource_id,result,payload,occurred_at,received_at FROM telemetry_events WHERE enterprise_id=$1 AND ($2='' OR agent_id=$2) ORDER BY occurred_at DESC LIMIT $3`, claimsFrom(request).Tenant, request.URL.Query().Get("agentId"), limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var eventID, userID, agentID, eventType string
		var resourceType, resourceID, result pgtype.Text
		var payload []byte
		var occurredAt, receivedAt time.Time
		if err := rows.Scan(&eventID, &userID, &agentID, &eventType, &resourceType, &resourceID, &result, &payload, &occurredAt, &receivedAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		var data any
		_ = json.Unmarshal(payload, &data)
		items = append(items, map[string]any{"eventId": eventID, "userId": userID, "agentId": agentID, "type": eventType, "resourceType": nullablePGText(resourceType), "resourceId": nullablePGText(resourceID), "result": nullablePGText(result), "data": data, "occurredAt": occurredAt, "receivedAt": receivedAt})
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nil})
}

func (s *Server) listAgents(response http.ResponseWriter, request *http.Request) {
	items, err := s.app.DB.ListAgents(request.Context(), db.ListAgentsParams{EnterpriseID: claimsFrom(request).Tenant, Column2: request.URL.Query().Get("cursor"), Column3: request.URL.Query().Get("userId"), Limit: limit(request)})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, agent := range items {
		result = append(result, publicAgent(agent))
	}
	var next any
	if len(items) == int(limit(request)) {
		next = items[len(items)-1].AgentID
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": result, "nextCursor": next})
}
func (s *Server) getAgent(response http.ResponseWriter, request *http.Request) {
	agent, err := s.app.DB.GetAgent(request.Context(), chi.URLParam(request, "agentId"))
	if errors.Is(err, pgx.ErrNoRows) || agent.EnterpriseID != claimsFrom(request).Tenant {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Agent was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicAgent(agent))
}
func publicAgent(agent db.Agent) map[string]any {
	return map[string]any{"agentId": agent.AgentID, "enterpriseId": agent.EnterpriseID, "userId": agent.UserID, "agentVersion": agent.AgentVersion, "platform": agent.Platform, "firstSeenAt": agent.FirstSeenAt.Time, "lastSeenAt": agent.LastSeenAt.Time, "appliedSkillRevision": nullablePGText(agent.AppliedSkillRevision), "installedSkillIds": agent.InstalledSkillIds}
}
func nullablePGText(value pgtype.Text) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
