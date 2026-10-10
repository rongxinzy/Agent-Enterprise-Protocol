package httpapi

import (
	"context"
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
	if record["password"] != "[redacted]" || record["displayName"] != "New Name" {
		t.Fatalf("masked payload = %#v", record)
	}
	config, ok := record["config"].(map[string]any)
	if !ok || config["apiKey"] != "[redacted]" || config["region"] != "cn" {
		t.Fatalf("masked config = %#v", record["config"])
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
		WithArgs("deployment-a", 51).
		WillReturnRows(pgxmock.NewRows([]string{"cursor", "actor_user_id", "action", "resource_type", "resource_id", "result", "reason", "payload", "created_at"}).
			AddRow(int64(3), "admin-user", "update", "model", "m-1", "success", nil, []byte(`{"enabled":false}`), now))

	response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/audit/operations", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"action":"update"`) ||
		!strings.Contains(response.Body.String(), `"resourceType":"model"`) ||
		!strings.Contains(response.Body.String(), `"nextCursor":null`) {
		t.Fatalf("admin audit = %d %s", response.Code, response.Body.String())
	}
}
