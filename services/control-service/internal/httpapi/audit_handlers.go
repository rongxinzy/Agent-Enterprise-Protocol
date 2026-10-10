package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// The persisted authentication event vocabulary. Kept in sync with the CHECK
// constraint on authentication_audit_events so an unknown filter is rejected
// with 400 instead of silently returning an empty page.
var authenticationAuditEventTypes = map[string]struct{}{
	"login.succeeded":  {},
	"login.failed":     {},
	"login.throttled":  {},
	"password.changed": {},
}

var authenticationAuditOutcomes = map[string]struct{}{
	"success": {},
	"failure": {},
	"denied":  {},
}

// listAuthenticationAudit exposes the persisted login/password audit trail
// (success, failure, throttle, password change) that has no other read path.
// It is read-only, ordered newest-first by the monotonic cursor, and always
// scoped to the caller's deployment. principal_hash is never returned; the
// one-way source fingerprint is exposed so repeated attempts from one source
// can be correlated without revealing the raw address.
func (s *Server) listAuthenticationAudit(response http.ResponseWriter, request *http.Request) {
	filters := request.URL.Query()
	conditions := []string{"deployment_id=$1"}
	args := []any{claimsFrom(request).DeploymentID}

	if value := strings.TrimSpace(filters.Get("eventType")); value != "" {
		if _, ok := authenticationAuditEventTypes[value]; !ok {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The eventType filter is invalid.")
			return
		}
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("event_type=$%d", len(args)))
	}
	if value := strings.TrimSpace(filters.Get("outcome")); value != "" {
		if _, ok := authenticationAuditOutcomes[value]; !ok {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The outcome filter is invalid.")
			return
		}
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("outcome=$%d", len(args)))
	}
	if value := strings.TrimSpace(filters.Get("userId")); value != "" {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("user_id=$%d", len(args)))
	}
	for _, filter := range []struct {
		name   string
		column string
		op     string
	}{
		{"createdAfter", "created_at", ">="},
		{"createdBefore", "created_at", "<="},
	} {
		value := strings.TrimSpace(filters.Get(filter.name))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("The %s filter must be RFC3339.", filter.name))
			return
		}
		args = append(args, parsed)
		conditions = append(conditions, fmt.Sprintf("%s %s $%d", filter.column, filter.op, len(args)))
	}
	if raw := strings.TrimSpace(filters.Get("cursor")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The authentication audit cursor is invalid.")
			return
		}
		args = append(args, parsed)
		conditions = append(conditions, fmt.Sprintf("cursor < $%d", len(args)))
	}

	pageLimit := int(limit(request))
	args = append(args, pageLimit+1)
	query := `SELECT cursor,user_id,event_type,outcome,reason,source_hash,created_at FROM authentication_audit_events WHERE ` +
		strings.Join(conditions, " AND ") + fmt.Sprintf(" ORDER BY cursor DESC LIMIT $%d", len(args))

	rows, err := s.app.Database().Query(request.Context(), query, args...)
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
		var userID pgtype.Text
		var eventType, outcome, sourceHash string
		var reason pgtype.Text
		var createdAt time.Time
		if err := rows.Scan(&cursor, &userID, &eventType, &outcome, &reason, &sourceHash, &createdAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if len(items) == pageLimit {
			// The extra row only signals that a further page exists.
			value := lastCursor
			nextCursor = &value
			break
		}
		items = append(items, map[string]any{
			"cursor":     strconv.FormatInt(cursor, 10),
			"userId":     nullablePGText(userID),
			"eventType":  eventType,
			"outcome":    outcome,
			"reason":     nullablePGText(reason),
			"sourceHash": sourceHash,
			"createdAt":  createdAt,
		})
		lastCursor = strconv.FormatInt(cursor, 10)
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}
