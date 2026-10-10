package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// auditPayloadLimit bounds how much of a mutating request body is retained in
// the audit trail; a runaway payload must not become an audit row.
const auditPayloadLimit = 64 << 10

// auditSensitiveKeys are replaced before a body is stored. Comparison is
// case-insensitive and exact, so unrelated identifiers are preserved.
var auditSensitiveKeys = map[string]struct{}{
	"password":        {},
	"currentpassword": {},
	"newpassword":     {},
	"secret":          {},
	"clientsecret":    {},
	"apikey":          {},
	"apisecret":       {},
	"token":           {},
	"accesstoken":     {},
	"refreshtoken":    {},
	"privatekey":      {},
	"signingkey":      {},
	"webhookurl":      {},
	"privatekeypem":   {},
}

type auditStatusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (writer *auditStatusWriter) WriteHeader(status int) {
	if !writer.wrote {
		writer.status = status
		writer.wrote = true
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *auditStatusWriter) Write(body []byte) (int, error) {
	if !writer.wrote {
		writer.status = http.StatusOK
		writer.wrote = true
	}
	return writer.ResponseWriter.Write(body)
}

// auditAdminWrites records every successful administrative write. It is
// deliberately best-effort: an audit failure never turns a completed operation
// into an error response, but it is logged so the gap is visible.
func (s *Server) auditAdminWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
			next.ServeHTTP(response, request)
			return
		}
		var payload any
		if request.Body != nil {
			raw, err := io.ReadAll(io.LimitReader(request.Body, auditPayloadLimit))
			_ = request.Body.Close()
			request.Body = io.NopCloser(bytes.NewReader(raw))
			if err == nil && len(raw) > 0 {
				payload = maskAuditPayload(raw)
			}
		}
		recorder := &auditStatusWriter{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		if recorder.status < 200 || recorder.status >= 300 {
			return
		}
		if s.app.Database() == nil {
			return
		}
		claims := claimsFrom(request)
		action, resourceType, resourceID := adminAuditTarget(request.Method, request.URL.Path)
		var encoded any
		if payload != nil {
			if raw, err := json.Marshal(payload); err == nil {
				encoded = raw
			}
		}
		if _, err := s.app.Database().Exec(request.Context(),
			`INSERT INTO admin_audit_events (deployment_id,actor_user_id,action,resource_type,resource_id,result,reason,payload) VALUES ($1,$2,$3,$4,$5,'success',$6,$7)`,
			claims.DeploymentID, nullableString(claims.Subject), action, resourceType, nullableString(resourceID), nil, encoded); err != nil {
			slog.Error("administrative audit failed", "error", err, "action", action, "resource", resourceType)
		}
	})
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// maskAuditPayload decodes a JSON body and replaces values of known sensitive
// keys so secrets never reach the audit table.
func maskAuditPayload(raw []byte) any {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		// Not JSON (or truncated): retain nothing rather than an opaque blob.
		return nil
	}
	return maskAuditValue(decoded)
}

func maskAuditValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		masked := make(map[string]any, len(typed))
		for key, item := range typed {
			if _, sensitive := auditSensitiveKeys[strings.ToLower(key)]; sensitive {
				masked[key] = "[redacted]"
				continue
			}
			masked[key] = maskAuditValue(item)
		}
		return masked
	case []any:
		masked := make([]any, len(typed))
		for index, item := range typed {
			masked[index] = maskAuditValue(item)
		}
		return masked
	default:
		return value
	}
}

// adminAuditTarget derives the action, resource type and resource id from the
// route. Unknown paths still produce a usable row (the collection name).
func adminAuditTarget(method, path string) (action, resourceType, resourceID string) {
	trimmed := strings.TrimPrefix(path, "/aep/v1/admin/")
	segments := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return "update", "unknown", ""
	}
	collection := segments[0]
	resourceType = adminAuditResourceType(collection, segments)
	switch method {
	case http.MethodPost:
		action = "create"
	case http.MethodDelete:
		action = "delete"
	default:
		action = "update"
	}
	// A trailing verb names the operation; the segment before it is the id.
	switch segments[len(segments)-1] {
	case "import":
		action = "import"
	case "publish":
		action = "publish"
	case "cancel":
		action = "cancel"
	case "revoke":
		action = "revoke"
	case "rotate":
		action = "rotate"
	case "reset-password":
		action = "reset_password"
	case "refresh":
		action = "refresh"
	case "delta", "settings", "desired-state", "rbac":
		action = "update"
	case "test-access":
		action = "test"
	}
	if len(segments) > 1 && !adminAuditVerbSegment(segments[1]) {
		resourceID = segments[1]
	}
	return action, resourceType, resourceID
}

// adminAuditVerbSegment reports whether a path segment is a collection-level
// verb rather than a resource identifier.
func adminAuditVerbSegment(segment string) bool {
	switch segment {
	case "import", "publish", "cancel", "revoke", "rotate", "reset-password", "refresh", "delta", "test-access":
		return true
	default:
		return false
	}
}

func adminAuditResourceType(collection string, segments []string) string {
	switch collection {
	case "users":
		if len(segments) > 2 && segments[2] == "rbac" {
			return "user_rbac"
		}
		return "user"
	case "roles":
		return "role"
	case "teams":
		return "team"
	case "models":
		return "model"
	case "model-assignments":
		return "model_assignment"
	case "skills":
		return "skill"
	case "skill-assignments":
		return "skill_assignment"
	case "credentials":
		return "credential"
	case "credential-assignments":
		return "credential_assignment"
	case "licenses":
		return "license"
	case "control-events":
		return "control_event"
	case "sessions":
		return "session"
	case "agents":
		return "agent"
	case "identity-sources":
		return "identity_source"
	case "data-scope-rules":
		return "data_scope_rule"
	case "data-plane":
		return "data_plane"
	case "deployment":
		return "deployment_settings"
	case "model-gateway":
		if len(segments) > 1 {
			switch segments[1] {
			case "limits":
				return "gateway_limit"
			case "quotas":
				return "gateway_quota"
			case "models":
				return "gateway_model"
			}
		}
		return "gateway"
	default:
		return collection
	}
}

// listAdminAudit exposes the recorded administrative write operations.
func (s *Server) listAdminAudit(response http.ResponseWriter, request *http.Request) {
	filters := request.URL.Query()
	conditions := []string{"deployment_id=$1"}
	args := []any{claimsFrom(request).DeploymentID}
	for _, filter := range []struct {
		name   string
		column string
	}{
		{"action", "action"},
		{"resourceType", "resource_type"},
		{"resourceId", "resource_id"},
		{"actorUserId", "actor_user_id"},
	} {
		if value := strings.TrimSpace(filters.Get(filter.name)); value != "" {
			args = append(args, value)
			conditions = append(conditions, fmt.Sprintf("%s=$%d", filter.column, len(args)))
		}
	}
	for _, filter := range []struct {
		name string
		op   string
	}{
		{"createdAfter", ">="},
		{"createdBefore", "<="},
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
		conditions = append(conditions, fmt.Sprintf("created_at %s $%d", filter.op, len(args)))
	}
	if raw := strings.TrimSpace(filters.Get("cursor")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The administrative audit cursor is invalid.")
			return
		}
		args = append(args, parsed)
		conditions = append(conditions, fmt.Sprintf("cursor < $%d", len(args)))
	}

	pageLimit := int(limit(request))
	args = append(args, pageLimit+1)
	query := `SELECT cursor,actor_user_id,action,resource_type,resource_id,result,reason,payload,created_at FROM admin_audit_events WHERE ` +
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
		var action, resourceType, result string
		var actorUserID, resourceID, reason pgtype.Text
		var payload []byte
		var createdAt time.Time
		if err := rows.Scan(&cursor, &actorUserID, &action, &resourceType, &resourceID, &result, &reason, &payload, &createdAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		if len(items) == pageLimit {
			value := lastCursor
			nextCursor = &value
			break
		}
		var changes any
		_ = json.Unmarshal(payload, &changes)
		items = append(items, map[string]any{
			"cursor":       strconv.FormatInt(cursor, 10),
			"actorUserId":  nullablePGText(actorUserID),
			"action":       action,
			"resourceType": resourceType,
			"resourceId":   nullablePGText(resourceID),
			"result":       result,
			"reason":       nullablePGText(reason),
			"changes":      changes,
			"createdAt":    createdAt,
		})
		lastCursor = strconv.FormatInt(cursor, 10)
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}
