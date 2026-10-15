package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// auditPayloadLimit bounds how much of a mutating request body is retained in
// the audit trail; a runaway payload must not become an audit row.
const auditPayloadLimit = 64 << 10

// auditOperationDefaultWindow is the default operations-log window: the audit
// page loads the last seven days unless the caller narrows the range.
const auditOperationDefaultWindow = 7 * 24 * time.Hour

// auditFailureWindow collapses an identical repeated failing administrative
// write into a single row: a client stuck retrying a denied or rejected call
// must not turn one policy decision into unbounded audit traffic. Distinct
// attempts (another actor, target, error code or body) are always recorded, and
// the first occurrence of any attempt is never suppressed.
const auditFailureWindow = 30 * time.Second

// auditFailureKeys bounds the limiter's memory. Past it the limiter stops
// caching new fingerprints rather than evicting live ones, so a burst can slow
// the limiter down but can never make the trail drop an attempt.
const auditFailureKeys = 4096

// auditFailureLimiter is a process-local window limiter. It is intentionally
// not shared state: replicas each admit their own share of a repeat, which
// bounds the row rate per process without coordinating through the database the
// limiter is protecting.
type auditFailureLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	maxKeys int
	seen    map[string]time.Time
}

func newAuditFailureLimiter(window time.Duration) *auditFailureLimiter {
	return &auditFailureLimiter{window: window, maxKeys: auditFailureKeys, seen: make(map[string]time.Time)}
}

// allow reports whether this attempt may be written, and remembers it.
func (limiter *auditFailureLimiter) allow(fingerprint string, now time.Time) bool {
	if limiter == nil || limiter.window <= 0 {
		return true
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if last, seen := limiter.seen[fingerprint]; seen && now.Sub(last) < limiter.window {
		return false
	}
	if len(limiter.seen) >= limiter.maxKeys {
		for candidate, at := range limiter.seen {
			if now.Sub(at) >= limiter.window {
				delete(limiter.seen, candidate)
			}
		}
		if len(limiter.seen) >= limiter.maxKeys {
			// Every entry is still live. Record the attempt and skip caching,
			// so saturated memory degrades the limiter, never the evidence.
			return true
		}
	}
	limiter.seen[fingerprint] = now
	return true
}

// auditFailureFingerprint identifies one attempt: the same actor repeating the
// same operation with the same body and the same error is one decision, while a
// different body is a different attempt worth its own row. The request
// identifier is deliberately absent: every attempt carries a fresh one, so
// including it would defeat the throttle entirely.
func auditFailureFingerprint(deploymentID, actor, action, resourceType, resourceID, code string, attempt []byte) string {
	digest := sha256.New()
	for _, part := range []string{deploymentID, actor, action, resourceType, resourceID, code} {
		_, _ = digest.Write([]byte(part))
		_, _ = digest.Write([]byte{0})
	}
	_, _ = digest.Write(attempt)
	return hex.EncodeToString(digest.Sum(nil))
}

// auditSafeKeys is a fail-closed allowlist: only these request fields keep
// their value, and every other key is redacted. A denylist would leak any
// sensitive field it did not name (a credential "value", for example), so the
// default is to withhold. Comparison is case-insensitive.
var auditSafeKeys = map[string]struct{}{
	"name":                   {},
	"displayname":            {},
	"description":            {},
	"enabled":                {},
	"status":                 {},
	"kind":                   {},
	"type":                   {},
	"protocol":               {},
	"subjecttype":            {},
	"subjectid":              {},
	"roleids":                {},
	"defaultroleids":         {},
	"teamids":                {},
	"userids":                {},
	"username":               {},
	"modelid":                {},
	"modelids":               {},
	"resourcetype":           {},
	"resourceid":             {},
	"revision":               {},
	"scopes":                 {},
	"tags":                   {},
	"parentid":               {},
	"expiresat":              {},
	"service":                {},
	"deliverymode":           {},
	"capabilities":           {},
	"reason":                 {},
	"quota":                  {},
	"action":                 {},
	"sourcetype":             {},
	"identifier":             {},
	"version":                {},
	"title":                  {},
	"path":                   {},
	"email":                  {},
	"before":                 {},
	"after":                  {},
	"rotated":                {},
	"endpoint":               {},
	"upstreammodel":          {},
	"localmodelref":          {},
	"contextwindow":          {},
	"isdefault":              {},
	"credentialid":           {},
	"hometeamid":             {},
	"promptskillid":          {},
	"rulekind":               {},
	"resourcekind":           {},
	"startsat":               {},
	"localsubjectid":         {},
	"externalid":             {},
	"scopetype":              {},
	"scopeid":                {},
	"maximum":                {},
	"interval":               {},
	"permissions":            {},
	"displaytitle":           {},
	"reasoningcompatibility": {},
	"skillid":                {},
	// A failed operation is audited with the row shape
	// {"requestId","status","request"}. Keeping requestId readable ties the
	// retained evidence back to the request that produced it, and keeping
	// request readable preserves the body that was rejected. "request" is
	// recursed like any other allowlisted value, so unknown keys inside it are
	// still withheld and an endpoint nested in it is still sanitized; only the
	// envelope key is trusted, not its contents.
	"requestid": {},
	"request":   {},
}

// auditChanges documents a mutation's before/after state, masked by the
// fail-closed allowlist so an unlisted field can never reach the audit row.
func auditChanges(before, after map[string]any) map[string]any {
	masked, _ := maskAuditValue(map[string]any{"before": before, "after": after}).(map[string]any)
	return masked
}

// auditRemoval documents the state a delete removed. It runs through the same
// allowlist as every other snapshot, so a delete cannot leak a field that an
// update would have redacted.
func auditRemoval(before map[string]any) map[string]any {
	masked, _ := maskAuditValue(map[string]any{"before": before}).(map[string]any)
	return masked
}

// auditCreation documents the state a create established; there is no before.
func auditCreation(after map[string]any) map[string]any {
	masked, _ := maskAuditValue(map[string]any{"after": after}).(map[string]any)
	return masked
}

// stripURLQuery drops a URL's query and fragment before it is audited: an
// endpoint may carry an inline credential in its query string.
func stripURLQuery(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.RawQuery == "" && parsed.Fragment == "") {
		return raw
	}
	parsed.RawQuery, parsed.Fragment = "", ""
	return parsed.String()
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

// auditRecorded marks a request whose handler already wrote the audit row
// inside its own transaction, so the fallback middleware must not duplicate it.
type auditRecorded struct{ done bool }

const auditRecordedKey contextKey = "aep-audit-recorded"

// recordAdminAuditTx appends an administrative audit row through an open pgx
// transaction, so the row commits with the change it describes. Handlers that
// own a raw pgx transaction use this instead of the store's gorm writer.
func recordAdminAuditTx(ctx context.Context, tx pgx.Tx, deploymentID string, entry repository.AdminAuditEntry) error {
	var payload any
	if entry.Changes != nil {
		raw, err := json.Marshal(entry.Changes)
		if err != nil {
			return err
		}
		payload = raw
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO admin_audit_events (deployment_id,actor_user_id,action,resource_type,resource_id,result,reason,payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		deploymentID, nullableString(entry.ActorUserID), entry.Action, entry.ResourceType, nullableString(entry.ResourceID), entry.Result, nullableString(entry.Reason), payload)
	return err
}

func markAdminAuditRecorded(request *http.Request) {
	if flag, ok := request.Context().Value(auditRecordedKey).(*auditRecorded); ok {
		flag.done = true
	}
}

// auditProblem carries the stable problem code a request was rejected with to
// the audit middleware, so a failure row names the reason without buffering the
// response body. Only the first code wins: it is the one the client was told.
type auditProblem struct{ code string }

const auditProblemKey contextKey = "aep-audit-problem"

// noteAuditProblem records the problem code a response reports. It is a no-op
// unless an administrator-write audit is capturing this request.
func noteAuditProblem(request *http.Request, code string) {
	problem, ok := request.Context().Value(auditProblemKey).(*auditProblem)
	if !ok || problem.code != "" {
		return
	}
	problem.code = code
}

// requestIDFrom returns the request identifier stamped on every response, which
// correlates an audit row with the request log and the client-visible problem.
func requestIDFrom(request *http.Request) string {
	if id, ok := request.Context().Value(contextKey("request-id")).(string); ok {
		return id
	}
	return ""
}

// auditAdminWrites records administrative writes: the successes a handler did
// not record itself, and the write attempts that were rejected or failed. It is
// deliberately best-effort: an audit failure never turns a completed operation
// into an error response (and never upgrades a 400 into a 500), but it is logged
// so the gap is visible. Handlers that write the audit in their own transaction
// take precedence and are not duplicated here.
//
// Scope: authenticated, non-read requests that reach the administrative surface.
// Requests refused before authentication (no token, invalid token, a session
// that is not bound to a user, a temporary password that must be changed first)
// carry no authenticated actor and so cannot be attributed here; those belong to
// the authentication audit. Reads (GET/HEAD/OPTIONS) are not audited.
func (s *Server) auditAdminWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
			next.ServeHTTP(response, request)
			return
		}
		var payload any
		// Only peek at bodies known to fit the capture limit. A larger or
		// unknown-length body is left untouched, otherwise the handler would
		// receive a truncated request.
		if request.Body != nil && request.ContentLength > 0 && request.ContentLength <= auditPayloadLimit {
			raw, err := io.ReadAll(request.Body)
			_ = request.Body.Close()
			request.Body = io.NopCloser(bytes.NewReader(raw))
			if err == nil && len(raw) > 0 {
				payload = maskAuditPayload(raw)
			}
		}
		recorder := &auditStatusWriter{ResponseWriter: response, status: http.StatusOK}
		recorded := &auditRecorded{}
		problem := &auditProblem{}
		ctx := context.WithValue(request.Context(), auditRecordedKey, recorded)
		ctx = context.WithValue(ctx, auditProblemKey, problem)
		next.ServeHTTP(recorder, request.WithContext(ctx))
		if recorded.done {
			return
		}
		if s.app.Database() == nil {
			return
		}
		if recorder.status < 200 || recorder.status >= 300 {
			s.recordAdminAuditFailure(request, recorder.status, problem.code, payload)
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

// recordAdminAuditFailure persists one rejected or failed administrative write
// attempt: who tried, against what, the stable error code, the request
// identifier, and the masked body that was attempted. The payload keeps the
// wrapped shape documented for failure rows so an attempted field can never be
// confused with the audit metadata around it.
func (s *Server) recordAdminAuditFailure(request *http.Request, status int, code string, attempted any) {
	claims := claimsFrom(request)
	if claims.DeploymentID == "" {
		// Nothing to attribute the attempt to: the row would be invisible to
		// every deployment-scoped audit query anyway.
		return
	}
	action, resourceType, resourceID := adminAuditTarget(request.Method, request.URL.Path)
	if code == "" {
		code = fmt.Sprintf("HTTP_%d", status)
	}
	var attempt []byte
	if attempted != nil {
		if raw, err := json.Marshal(attempted); err == nil {
			attempt = raw
		}
	}
	fingerprint := auditFailureFingerprint(claims.DeploymentID, claims.Subject, action, resourceType, resourceID, code, attempt)
	if !s.auditFailures.allow(fingerprint, time.Now()) {
		slog.Warn("administrative failure audit suppressed as a repeat",
			"action", action, "resource", resourceType, "code", code, "actor", claims.Subject,
			"request_id", requestIDFrom(request))
		return
	}
	entry := map[string]any{"requestId": requestIDFrom(request), "status": status}
	if attempted != nil {
		entry["request"] = attempted
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		slog.Error("administrative failure audit payload was not encodable", "error", err, "action", action, "resource", resourceType)
		return
	}
	if _, err := s.app.Database().Exec(request.Context(),
		`INSERT INTO admin_audit_events (deployment_id,actor_user_id,action,resource_type,resource_id,result,reason,payload) VALUES ($1,$2,$3,$4,$5,'failure',$6,$7)`,
		claims.DeploymentID, nullableString(claims.Subject), action, resourceType, nullableString(resourceID), code, encoded); err != nil {
		slog.Error("administrative failure audit failed", "error", err, "action", action, "resource", resourceType)
	}
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
			if _, safe := auditSafeKeys[strings.ToLower(key)]; !safe {
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
	if collection == "compute" && len(segments) > 2 && segments[1] == "assignments" {
		resourceID = segments[2]
	} else if len(segments) > 1 && !adminAuditVerbSegment(segments[1]) {
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
	case "compute":
		if len(segments) > 1 && segments[1] == "assignments" {
			return "compute_assignment"
		}
		return "compute_tier"
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
		{"result", "result"},
	} {
		if value := strings.TrimSpace(filters.Get(filter.name)); value != "" {
			args = append(args, value)
			conditions = append(conditions, fmt.Sprintf("%s=$%d", filter.column, len(args)))
		}
	}
	var after, before *time.Time
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
		if filter.op == ">=" {
			after = &parsed
		} else {
			before = &parsed
		}
	}
	// Without an explicit bound the operations log defaults to the last seven
	// days; the effective window is echoed back so the console can state it.
	if after == nil && before == nil {
		defaultAfter := time.Now().UTC().Add(-auditOperationDefaultWindow)
		after = &defaultAfter
		args = append(args, defaultAfter)
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)))
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
	writeJSON(response, http.StatusOK, map[string]any{
		"items": items, "nextCursor": nextCursor,
		"timeRange": map[string]any{"after": after, "before": before},
	})
}
