package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAdminAuditTargetMapping(t *testing.T) {
	cases := []struct {
		method, path         string
		action, resource, id string
	}{
		{http.MethodPost, "/aep/v1/admin/users", "create", "user", ""},
		{http.MethodPatch, "/aep/v1/admin/users/u-1", "update", "user", "u-1"},
		{http.MethodDelete, "/aep/v1/admin/users/u-1", "delete", "user", "u-1"},
		{http.MethodPut, "/aep/v1/admin/users/u-1/rbac", "update", "user_rbac", "u-1"},
		{http.MethodPost, "/aep/v1/admin/users/u-1/reset-password", "reset_password", "user", "u-1"},
		{http.MethodPost, "/aep/v1/admin/users/import", "import", "user", ""},
		{http.MethodPost, "/aep/v1/admin/models", "create", "model", ""},
		{http.MethodPost, "/aep/v1/admin/model-assignments", "create", "model_assignment", ""},
		{http.MethodDelete, "/aep/v1/admin/skill-assignments/as-1", "delete", "skill_assignment", "as-1"},
		{http.MethodPost, "/aep/v1/admin/credentials/c-1/rotate", "rotate", "credential", "c-1"},
		{http.MethodPost, "/aep/v1/admin/licenses/import", "import", "license", ""},
		{http.MethodPost, "/aep/v1/admin/licenses/l-1/revoke", "revoke", "license", "l-1"},
		{http.MethodPut, "/aep/v1/admin/deployment/settings", "update", "deployment_settings", "settings"},
		{http.MethodPost, "/aep/v1/admin/control-events/e-1/cancel", "cancel", "control_event", "e-1"},
	}
	for _, item := range cases {
		action, resource, id := adminAuditTarget(item.method, item.path)
		if action != item.action || resource != item.resource || id != item.id {
			t.Fatalf("%s %s = (%s,%s,%s), want (%s,%s,%s)", item.method, item.path, action, resource, id, item.action, item.resource, item.id)
		}
	}
}

func TestMaskAuditPayloadRedactsSecrets(t *testing.T) {
	masked := maskAuditPayload([]byte(`{"displayName":"New Name","password":"hunter2","config":{"apiKey":"abc","region":"cn"}}`))
	record, ok := masked.(map[string]any)
	if !ok {
		t.Fatalf("masked payload = %#v", masked)
	}
	// Only allowlisted fields keep their value; every other key is withheld.
	if record["displayName"] != "New Name" || record["password"] != "[redacted]" || record["config"] != "[redacted]" {
		t.Fatalf("masked payload = %#v", record)
	}
}

func TestMaskAuditPayloadRedactsCredentialValue(t *testing.T) {
	masked := maskAuditPayload([]byte(`{"name":"prod","service":"openai","deliveryMode":"server_only","value":"sk-secret","enabled":true}`))
	record, ok := masked.(map[string]any)
	if !ok {
		t.Fatalf("masked payload = %#v", masked)
	}
	if record["value"] != "[redacted]" {
		t.Fatalf("credential value leaked: %#v", record)
	}
	if record["name"] != "prod" || record["service"] != "openai" || record["enabled"] != true {
		t.Fatalf("allowlisted fields changed: %#v", record)
	}
}

func TestAuditAdminWritesRecordsSuccessfulWrite(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	server := &Server{app: application}
	token, _, err := application.Tokens.IssueWithDeploymentSession("admin-user", "deployment-a", "session-admin", true, false, []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := application.Tokens.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "update", "user_rbac", "u-9", nil, []byte(`{"password":"[redacted]","roleIds":["admin"]}`)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	request := httptest.NewRequest(http.MethodPut, "/aep/v1/admin/users/u-9/rbac", strings.NewReader(`{"roleIds":["admin"],"password":"secret"}`))
	request = request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims))
	response := httptest.NewRecorder()
	server.auditAdminWrites(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("audited write = %d", response.Code)
	}
}

func TestAuditAdminWritesLeavesLargeBodiesIntact(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	server := &Server{app: application}
	token, _, err := application.Tokens.IssueWithDeploymentSession("admin-user", "deployment-a", "session-admin", true, false, []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := application.Tokens.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "create", "user", nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	body := strings.Repeat("a", auditPayloadLimit+1024)
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/admin/users", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims))
	read := 0
	response := httptest.NewRecorder()
	server.auditAdminWrites(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		got, _ := io.ReadAll(incoming.Body)
		read = len(got)
		writer.WriteHeader(http.StatusCreated)
	})).ServeHTTP(response, request)

	if read != len(body) {
		t.Fatalf("handler read %d of %d body bytes", read, len(body))
	}
}

func TestListAdminAudit(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT cursor,actor_user_id,action,resource_type,resource_id,result,reason,payload,created_at FROM admin_audit_events`).
		WithArgs("deployment-a", auditWindowArg{time.Now().UTC().Add(-7 * 24 * time.Hour)}, 51).
		WillReturnRows(pgxmock.NewRows([]string{"cursor", "actor_user_id", "action", "resource_type", "resource_id", "result", "reason", "payload", "created_at"}).
			AddRow(int64(3), "admin-user", "update", "model", "m-1", "success", nil, []byte(`{"enabled":false}`), now))

	response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/audit/operations", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"action":"update"`) ||
		!strings.Contains(response.Body.String(), `"resourceType":"model"`) ||
		!strings.Contains(response.Body.String(), `"nextCursor":null`) ||
		!strings.Contains(response.Body.String(), `"timeRange":{"after":`) {
		t.Fatalf("admin audit = %d %s", response.Code, response.Body.String())
	}
}

func TestMaskAuditPayloadKeepsUsername(t *testing.T) {
	masked := maskAuditPayload([]byte(`{"username":"new-user","password":"hunter2"}`))
	record, ok := masked.(map[string]any)
	if !ok {
		t.Fatalf("masked payload = %#v", masked)
	}
	if record["username"] != "new-user" || record["password"] != "[redacted]" {
		t.Fatalf("masked payload = %#v", record)
	}
}

// TestMaskAuditValueKeepsFailedOperationEvidence preserves the row shape the
// failed-operation audit writes, {"requestId","status","request"}: the read
// path re-runs every stored row through maskAuditValue, so without requestId
// and request in the allowlist the whole evidence block would be withheld.
// The nested request stays recursive, so a secret inside it is still masked.
func TestMaskAuditValueKeepsFailedOperationEvidence(t *testing.T) {
	masked := maskAuditValue(map[string]any{
		"requestId": "audit-request-1",
		"status":    400,
		"request": map[string]any{
			"displayName": "X",
			"unknownKey":  "v",
			"password":    "hunter2",
		},
	})
	record, ok := masked.(map[string]any)
	if !ok {
		t.Fatalf("masked payload = %#v", masked)
	}
	if record["requestId"] != "audit-request-1" || record["status"] != 400 {
		t.Fatalf("failure envelope redacted: %#v", record)
	}
	request, ok := record["request"].(map[string]any)
	if !ok {
		t.Fatalf("request must stay an object, got %#v", record["request"])
	}
	if request["displayName"] != "X" {
		t.Fatalf("allowlisted nested field changed: %#v", request)
	}
	if request["password"] != "[redacted]" || request["unknownKey"] != "[redacted]" {
		t.Fatalf("fail-closed masking degraded inside request: %#v", request)
	}
}

func TestListAdminAuditAllowsMissingPayload(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	pool.ExpectQuery(`SELECT cursor,actor_user_id,action,resource_type,resource_id,result,reason,payload,created_at FROM admin_audit_events`).
		WithArgs("deployment-a", auditWindowArg{time.Now().UTC().Add(-7 * 24 * time.Hour)}, 51).
		WillReturnRows(pgxmock.NewRows([]string{"cursor", "actor_user_id", "action", "resource_type", "resource_id", "result", "reason", "payload", "created_at"}).
			AddRow(int64(3), nil, "delete", "user", "deleted-user", "success", nil, nil, time.Now().UTC()))
	response := userRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/audit/operations", "")
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("missing payload = %d %s, error = %v", response.Code, response.Body.String(), err)
	}
	if changes, exists := page.Items[0]["changes"]; !exists || changes != nil {
		t.Fatalf("changes must be explicit null, got %v", page.Items[0])
	}
}

// auditWindowArg matches the timestamp the default operations-log window
// injects, without freezing the clock.
type auditWindowArg struct{ want time.Time }

func (arg auditWindowArg) Match(value interface{}) bool {
	stamp, ok := value.(time.Time)
	if !ok {
		return false
	}
	delta := stamp.Sub(arg.want)
	return delta < time.Minute && delta > -time.Minute
}

func TestListAdminAuditFiltersByResult(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	pool.ExpectQuery(`FROM admin_audit_events WHERE deployment_id=\$1 AND result=\$2 AND created_at >= \$3 ORDER BY cursor DESC LIMIT \$4`).
		WithArgs("deployment-a", "failure", auditWindowArg{time.Now().UTC().Add(-7 * 24 * time.Hour)}, 51).
		WillReturnRows(pgxmock.NewRows([]string{"cursor", "actor_user_id", "action", "resource_type", "resource_id", "result", "reason", "payload", "created_at"}).
			AddRow(int64(3), "admin-user", "delete", "model", "m-1", "failure", nil, nil, time.Now().UTC()))

	response := userRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/audit/operations?result=failure", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"result":"failure"`) {
		t.Fatalf("result filter = %d %s", response.Code, response.Body.String())
	}
}

func TestListAdminAuditHonoursExplicitRange(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	after := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`FROM admin_audit_events WHERE deployment_id=\$1 AND created_at >= \$2 AND created_at <= \$3 ORDER BY cursor DESC LIMIT \$4`).
		WithArgs("deployment-a", after, before, 51).
		WillReturnRows(pgxmock.NewRows([]string{"cursor", "actor_user_id", "action", "resource_type", "resource_id", "result", "reason", "payload", "created_at"}))

	response := userRequest(New(application).Handler(), adminToken, http.MethodGet,
		"/aep/v1/admin/audit/operations?createdAfter=2026-10-01T00:00:00Z&createdBefore=2026-10-02T00:00:00Z", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"after":"2026-10-01T00:00:00Z"`) ||
		!strings.Contains(response.Body.String(), `"before":"2026-10-02T00:00:00Z"`) {
		t.Fatalf("explicit range = %d %s", response.Code, response.Body.String())
	}
}

func TestListAdminAuditRejectsInvalidCreatedAfter(t *testing.T) {
	application, _, adminToken, _ := newRuntimeHTTPApplication(t)
	response := userRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/audit/operations?createdAfter=yesterday", "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("invalid createdAfter = %d %s", response.Code, response.Body.String())
	}
}
