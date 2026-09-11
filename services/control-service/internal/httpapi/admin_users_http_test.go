package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

func newUserHTTPApplication(t *testing.T) (*app.App, sqlmock.Sqlmock, pgxmock.PgxPoolIface, string) {
	t.Helper()
	application, sqlMock, adminToken := newStoreBackedHTTPApplication(t)
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	application.SetRuntimeDatabase(pool)
	t.Cleanup(func() {
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		pool.Close()
	})
	return application, sqlMock, pool, adminToken
}

func userColumns() []string {
	return []string{"id", "deployment_id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "created_at", "updated_at"}
}

func TestAdminUserListAndCreate(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id > \$2 ORDER BY id LIMIT \$3`).
		WithArgs("deployment-a", "user-0", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", "secret-hash", "active", true, false, now, now))
	mock.ExpectQuery(`SELECT \* FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id IN \(\$2\) ORDER BY user_id, role_id`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "role_id", "is_primary", "created_at"}).AddRow("deployment-a", "user-a", "member", true, now))
	mock.ExpectQuery(`SELECT \* FROM "user_team_bindings" WHERE deployment_id = \$1 AND user_id IN \(\$2\) ORDER BY user_id, team_id`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "team_id", "is_primary", "created_at"}).AddRow("deployment-a", "user-a", "engineering", true, now))
	listed := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/users?cursor=user-0&limit=1", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"nextCursor":"user-a"`) || !strings.Contains(listed.Body.String(), `"roleIds":["member"]`) || strings.Contains(listed.Body.String(), "secret-hash") {
		t.Fatalf("user list = %d %s", listed.Code, listed.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "users"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"deployment-a","username":"bob","displayName":"Bob","temporaryPassword":"long-temporary-password","roleIds":["member"],"teamIds":["engineering"]}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"username":"bob"`) || !strings.Contains(created.Body.String(), `"status":"active"`) || strings.Contains(created.Body.String(), "password") {
		t.Fatalf("create user = %d %s", created.Code, created.Body.String())
	}
}

func TestAdminUserImportReportsPartialResults(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "users"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	body := `{"deploymentId":"deployment-a","users":[` +
		`{"externalRowId":"row-1","username":"alice","displayName":"Alice","temporaryPassword":"long-temporary-password","roleIds":["member"],"teamIds":["engineering"]},` +
		`{"externalRowId":"row-2","username":"missing-membership","displayName":"Invalid","temporaryPassword":"long-temporary-password","roleIds":[],"teamIds":[]},` +
		`{"externalRowId":"row-3","username":"weak-password","displayName":"Weak","temporaryPassword":"short","roleIds":["member"],"teamIds":["engineering"]}]}`
	response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users/import", body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"created":1`) || !strings.Contains(response.Body.String(), `"rejected":2`) || !strings.Contains(response.Body.String(), `"PASSWORD_POLICY_VIOLATION"`) || !strings.Contains(response.Body.String(), `"USER_RBAC_REQUIRED"`) {
		t.Fatalf("import users = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminUserUpdateDisableAndResetPassword(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WithArgs("Disabled Alice", "disabled", sqlmock.AnyArg(), "deployment-a", "user-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Disabled Alice", nil, "hash", "disabled", false, false, now, now))
	mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("member"))
	mock.ExpectQuery(`SELECT "team_id" FROM "user_team_bindings"`).WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("engineering"))
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs("user-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE user_sessions SET revoked_at=now\(\)`).WithArgs("user-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	updated := userRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/users/user-a", `{"displayName":"Disabled Alice","status":"disabled"}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"status":"disabled"`) {
		t.Fatalf("update user = %d %s", updated.Code, updated.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs("user-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE user_sessions SET revoked_at=now\(\)`).WithArgs("user-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	reset := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users/user-a/reset-password", `{"temporaryPassword":"another-long-password","requirePasswordChange":true}`)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset password = %d %s", reset.Code, reset.Body.String())
	}
}

func TestAdminUserRBACReplacement(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM "users"`).WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "roles"`).WithArgs("deployment-a", "member").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "teams"`).WithArgs("deployment-a", "engineering").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`DELETE FROM "user_role_bindings"`).WithArgs("deployment-a", "user-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "user_team_bindings"`).WithArgs("deployment-a", "user-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	response := userRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/users/user-a/rbac", `{"roleIds":["member"],"teamIds":["engineering"]}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"userId":"user-a"`) {
		t.Fatalf("replace user RBAC = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminUserCreateErrorsAreStable(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	base := `"username":"alice","displayName":"Alice","roleIds":["member"],"teamIds":["engineering"]`

	wrongDeployment := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"other","temporaryPassword":"long-temporary-password",`+base+`}`)
	if wrongDeployment.Code != http.StatusForbidden || !strings.Contains(wrongDeployment.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("wrong deployment = %d %s", wrongDeployment.Code, wrongDeployment.Body.String())
	}

	weakPassword := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"deployment-a","temporaryPassword":"short",`+base+`}`)
	if weakPassword.Code != http.StatusBadRequest || !strings.Contains(weakPassword.Body.String(), `"code":"PASSWORD_POLICY_VIOLATION"`) {
		t.Fatalf("weak password = %d %s", weakPassword.Code, weakPassword.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "users"`).WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()
	duplicate := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"deployment-a","temporaryPassword":"long-temporary-password",`+base+`}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"USER_ALREADY_EXISTS"`) {
		t.Fatalf("duplicate user = %d %s", duplicate.Code, duplicate.Body.String())
	}
}
