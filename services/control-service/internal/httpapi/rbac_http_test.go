package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

func roleHTTPColumns() []string {
	return []string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}
}

func teamHTTPColumns() []string {
	return []string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at", "member_count"}
}

func expectRoleRecord(mock sqlmock.Sqlmock, id, name, description string, enabled bool, permissions []string, now time.Time) {
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", id, 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", id, name, description, false, enabled, now, now))
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", id, name, description, false, enabled, now, now))
	permissionRows := sqlmock.NewRows([]string{"deployment_id", "role_id", "permission_id"})
	for _, permission := range permissions {
		permissionRows.AddRow("deployment-a", id, permission)
	}
	mock.ExpectQuery(`SELECT \* FROM "role_permissions" WHERE deployment_id = \$1 AND role_id IN \(\$2\) ORDER BY role_id, permission_id`).
		WithArgs("deployment-a", id).WillReturnRows(permissionRows)
}

func expectTeamRecord(mock sqlmock.Sqlmock, id, name, description string, enabled bool, memberCount int64, now time.Time) {
	mock.ExpectQuery(`SELECT teams\.\*, COUNT\(user_team_bindings\.user_id\) AS member_count FROM "teams" LEFT JOIN user_team_bindings`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows(teamHTTPColumns()).AddRow("deployment-a", id, name, description, false, enabled, now, now, memberCount))
}

func TestAdminRoleLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "roles"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "permissions" WHERE id IN \(\$1,\$2\)`).
		WithArgs("models.read", "models.write").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`INSERT INTO "role_permissions"`).WillReturnResult(sqlmock.NewResult(2, 2))
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/roles", `{"id":"model-operator","name":" Model Operator ","description":"Manages models","permissions":["models.read","models.write"]}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"model-operator"`) || !strings.Contains(created.Body.String(), `"name":"Model Operator"`) {
		t.Fatalf("create role = %d %s", created.Code, created.Body.String())
	}

	expectRoleRecord(mock, "model-operator", "Model Operator", "Manages models", true, []string{"models.read", "models.write"}, now)
	got := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/roles/model-operator", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"permissions":["models.read","models.write"]`) || !strings.Contains(got.Body.String(), `"builtIn":false`) {
		t.Fatalf("get role = %d %s", got.Code, got.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "model-operator", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "model-operator", "Model Operator", "Manages models", false, true, now, now))
	mock.ExpectExec(`UPDATE "roles" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "permissions" WHERE id IN \(\$1\)`).
		WithArgs("models.read").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`DELETE FROM "role_permissions" WHERE deployment_id = \$1 AND role_id = \$2`).
		WithArgs("deployment-a", "model-operator").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`INSERT INTO "role_permissions"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	expectRoleRecord(mock, "model-operator", "Model Reader", "Read-only access", false, []string{"models.read"}, now)
	updated := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/roles/model-operator", `{"name":"Model Reader","description":"Read-only access","enabled":false,"permissions":["models.read"]}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Model Reader"`) || !strings.Contains(updated.Body.String(), `"enabled":false`) || !strings.Contains(updated.Body.String(), `"permissions":["models.read"]`) {
		t.Fatalf("update role = %d %s", updated.Code, updated.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "model-operator", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "model-operator", "Model Reader", "Read-only access", false, false, now, now))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "roles" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "model-operator").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/roles/model-operator", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete role = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestAdminTeamLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "teams"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams", `{"id":"platform","name":" Platform Team ","description":"Platform engineering"}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"platform"`) || !strings.Contains(created.Body.String(), `"name":"Platform Team"`) {
		t.Fatalf("create team = %d %s", created.Code, created.Body.String())
	}

	expectTeamRecord(mock, "platform", "Platform Team", "Platform engineering", true, 3, now)
	got := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/teams/platform", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"memberCount":3`) || !strings.Contains(got.Body.String(), `"builtIn":false`) {
		t.Fatalf("get team = %d %s", got.Code, got.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "teams" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectTeamRecord(mock, "platform", "Platform Core", "Core platform", false, 3, now)
	updated := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/teams/platform", `{"name":"Platform Core","description":"Core platform","enabled":false}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Platform Core"`) || !strings.Contains(updated.Body.String(), `"enabled":false`) {
		t.Fatalf("update team = %d %s", updated.Code, updated.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "teams" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "platform", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "platform", "Platform Core", "Core platform", false, false, now, now))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "teams" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "platform").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/teams/platform", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete team = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestAdminRoleErrorMappings(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "roles"`).WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()
	duplicate := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/roles", `{"id":"operator","name":"Operator","permissions":[]}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"ROLE_EXISTS"`) {
		t.Fatalf("duplicate role = %d %s", duplicate.Code, duplicate.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "roles"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "permissions" WHERE id IN \(\$1\)`).
		WithArgs("unknown.permission").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	unknownPermission := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/roles", `{"id":"operator","name":"Operator","permissions":["unknown.permission"]}`)
	if unknownPermission.Code != http.StatusBadRequest || !strings.Contains(unknownPermission.Body.String(), `"code":"INVALID_PERMISSION"`) {
		t.Fatalf("unknown permission = %d %s", unknownPermission.Code, unknownPermission.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "missing", 1).WillReturnRows(sqlmock.NewRows(roleHTTPColumns()))
	missing := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/roles/missing", "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing role = %d %s", missing.Code, missing.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "admin", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "admin", "Administrator", "Built in", true, true, now, now))
	mock.ExpectRollback()
	builtInUpdate := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/roles/admin", `{"enabled":false}`)
	if builtInUpdate.Code != http.StatusConflict || !strings.Contains(builtInUpdate.Body.String(), `"code":"BUILT_IN_RESOURCE"`) {
		t.Fatalf("update built-in role = %d %s", builtInUpdate.Code, builtInUpdate.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "admin", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "admin", "Administrator", "Built in", true, true, now, now))
	builtInDelete := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/roles/admin", "")
	if builtInDelete.Code != http.StatusConflict || !strings.Contains(builtInDelete.Body.String(), `"code":"BUILT_IN_RESOURCE"`) {
		t.Fatalf("delete built-in role = %d %s", builtInDelete.Code, builtInDelete.Body.String())
	}
}

func TestAdminTeamErrorMappings(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "teams"`).WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()
	duplicate := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams", `{"id":"platform","name":"Platform"}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"TEAM_EXISTS"`) {
		t.Fatalf("duplicate team = %d %s", duplicate.Code, duplicate.Body.String())
	}

	mock.ExpectQuery(`SELECT teams\.\*, COUNT\(user_team_bindings\.user_id\) AS member_count FROM "teams" LEFT JOIN user_team_bindings`).
		WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(teamHTTPColumns()))
	missing := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/teams/missing", "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing team = %d %s", missing.Code, missing.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "teams" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "all-users", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "all-users", "All users", "Built in", true, true, now, now))
	builtInDelete := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/teams/all-users", "")
	if builtInDelete.Code != http.StatusConflict || !strings.Contains(builtInDelete.Body.String(), `"code":"BUILT_IN_RESOURCE"`) {
		t.Fatalf("delete built-in team = %d %s", builtInDelete.Code, builtInDelete.Body.String())
	}
}

func TestDelegatedRolePermissionsRejectEscalation(t *testing.T) {
	application, mock, _ := newStoreBackedHTTPApplication(t)
	server := &Server{app: application}
	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-admin", "deployment-a", "session-delegated", false, false, []string{"role-manager"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := application.Tokens.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/admin/roles", nil)
	request = request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims))

	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM role_permissions AS rp JOIN user_role_bindings AS urb`).
		WithArgs("deployment-a", "delegated-admin").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("models.read").AddRow("skills.read"))
	allowedResponse := httptest.NewRecorder()
	if !server.authorizeDelegatedPermissions(allowedResponse, request, []string{"models.read"}) || allowedResponse.Code != http.StatusOK {
		t.Fatalf("permission subset rejected = %d %s", allowedResponse.Code, allowedResponse.Body.String())
	}

	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM role_permissions AS rp JOIN user_role_bindings AS urb`).
		WithArgs("deployment-a", "delegated-admin").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("models.read").AddRow("skills.read"))
	deniedResponse := httptest.NewRecorder()
	if server.authorizeDelegatedPermissions(deniedResponse, request, []string{"models.write"}) || deniedResponse.Code != http.StatusForbidden || !strings.Contains(deniedResponse.Body.String(), `"code":"ROLE_PERMISSION_ESCALATION"`) {
		t.Fatalf("permission escalation = %d %s", deniedResponse.Code, deniedResponse.Body.String())
	}
}
