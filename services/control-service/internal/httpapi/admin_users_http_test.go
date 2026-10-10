package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"gorm.io/gorm"

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
	return []string{"id", "deployment_id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "kind", "created_at", "updated_at"}
}

func TestAdminUserListAndCreate(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND kind = \$2 AND id > \$3 ORDER BY id LIMIT \$4`).
		WithArgs("deployment-a", "human", "user-0", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", "secret-hash", "active", true, false, "human", now, now))
	mock.ExpectQuery(`SELECT \* FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id IN \(\$2\) ORDER BY user_id, role_id`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "role_id", "is_primary", "created_at"}).AddRow("deployment-a", "user-a", "member", true, now))
	mock.ExpectQuery(`SELECT \* FROM "user_team_bindings" WHERE deployment_id = \$1 AND user_id IN \(\$2\) ORDER BY user_id, team_id`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "team_id", "is_primary", "created_at"}).AddRow("deployment-a", "user-a", "engineering", true, now))
	listed := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/users?cursor=user-0&limit=1", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"nextCursor":"user-a"`) || !strings.Contains(listed.Body.String(), `"kind":"human"`) || !strings.Contains(listed.Body.String(), `"roleIds":["member"]`) || strings.Contains(listed.Body.String(), "secret-hash") {
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

func TestAdminUserCreateAndImportAssignDefaultPassword(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()

	// Create without a temporary password: the server assigns the default.
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "users"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"deployment-a","username":"bob","displayName":"Bob","roleIds":["member"],"teamIds":["engineering"]}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"username":"bob"`) || strings.Contains(created.Body.String(), "password") {
		t.Fatalf("create without password = %d %s", created.Code, created.Body.String())
	}

	// Import: a row without a password gets the default; padded and
	// whitespace-only passwords are trimmed (and fall back to the default when
	// nothing remains), so all three rows are created.
	for range 3 {
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO "users"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}
	body := `{"deploymentId":"deployment-a","users":[` +
		`{"externalRowId":"row-1","username":"alice","displayName":"Alice","roleIds":["member"],"teamIds":["engineering"]},` +
		`{"externalRowId":"row-2","username":"padded","displayName":"Padded","temporaryPassword":" pad-password-123 ","roleIds":["member"],"teamIds":["engineering"]},` +
		`{"externalRowId":"row-3","username":"spaces","displayName":"Spaces","temporaryPassword":"   ","roleIds":["member"],"teamIds":["engineering"]}]}`
	imported := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users/import", body)
	if imported.Code != http.StatusOK || !strings.Contains(imported.Body.String(), `"created":3`) || !strings.Contains(imported.Body.String(), `"rejected":0`) {
		t.Fatalf("import default and trim = %d %s", imported.Code, imported.Body.String())
	}
}

func TestAdminUserUpdateDisableAndResetPassword(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()

	// Membership fields must be rejected, not silently dropped (A3).
	membership := userRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/users/user-a", `{"displayName":"Alice","teamIds":["engineering"],"roleIds":["member"]}`)
	if membership.Code != http.StatusBadRequest || !strings.Contains(membership.Body.String(), "MEMBERSHIP_UPDATE_UNSUPPORTED") {
		t.Fatalf("membership update = %d %s", membership.Code, membership.Body.String())
	}

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WithArgs("Disabled Alice", "disabled", sqlmock.AnyArg(), "deployment-a", "user-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Disabled Alice", nil, "hash", "disabled", false, false, "human", now, now))
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

func TestAdminSelfPasswordResetCannotRequireChange(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()

	// The admin token subject is admin-user; flagging yourself would lock the
	// caller into a restricted session, so both the explicit and the defaulted
	// flag are rejected before any write happens.
	for _, body := range []string{
		`{"temporaryPassword":"another-long-password","requirePasswordChange":true}`,
		`{"temporaryPassword":"another-long-password"}`,
	} {
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users/admin-user/reset-password", body)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "SELF_PASSWORD_RESET_RESTRICTED") {
			t.Fatalf("self reset with forced change = %d %s", response.Code, response.Body.String())
		}
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs("admin-user").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE user_sessions SET revoked_at=now\(\)`).WithArgs("admin-user").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	allowed := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users/admin-user/reset-password", `{"temporaryPassword":"another-long-password","requirePasswordChange":false}`)
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("self reset without forced change = %d %s", allowed.Code, allowed.Body.String())
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

	edgeWhitespace := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"deployment-a","temporaryPassword":" pad-password-123 ",`+base+`}`)
	if edgeWhitespace.Code != http.StatusBadRequest || !strings.Contains(edgeWhitespace.Body.String(), `"code":"PASSWORD_POLICY_VIOLATION"`) || !strings.Contains(edgeWhitespace.Body.String(), "whitespace") {
		t.Fatalf("edge whitespace password = %d %s", edgeWhitespace.Code, edgeWhitespace.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "users"`).WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()
	duplicate := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/users", `{"deploymentId":"deployment-a","temporaryPassword":"long-temporary-password",`+base+`}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"USER_ALREADY_EXISTS"`) {
		t.Fatalf("duplicate user = %d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestGetUserByIDRoutes(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("found returns the public user", func(t *testing.T) {
		rows := sqlmock.NewRows(userColumns()).
			AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", "hash", "active", false, false, "human", now, now)
		mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
			WithArgs("deployment-a", "user-a", 1).WillReturnRows(rows)
		// Role/team reads for the record shape.
		mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
			WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("member"))
		mock.ExpectQuery(`SELECT "team_id" FROM "user_team_bindings"`).
			WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"team_id"}))

		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/users/user-a", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"user-a"`) {
			t.Fatalf("getUser = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("missing user is a 404 problem", func(t *testing.T) {
		// GORM translates driver no-rows into ErrRecordNotFound; sqlmock
		// injects the mapped form directly.
		mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
			WithArgs("deployment-a", "user-a", 1).WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/users/user-a", "")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "RESOURCE_NOT_FOUND") {
			t.Fatalf("getUser missing = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestDeleteUserRoutes(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	userRows := func(username string) *sqlmock.Rows {
		return sqlmock.NewRows(userColumns()).
			AddRow("u1", "deployment-a", username, username, nil, "hash", "active", false, false, "human", now, now)
	}

	t.Run("missing user is a 404", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "users"`).
			WithArgs("deployment-a", "u1", 1).WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/users/u1", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("delete missing = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("bootstrap admin cannot be deleted", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "users"`).
			WithArgs("deployment-a", "u1", 1).WillReturnRows(userRows("admin"))
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/users/u1", "")
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "bootstrap admin") {
			t.Fatalf("delete admin = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("deletion revokes sessions then removes the user", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "users"`).
			WithArgs("deployment-a", "u1", 1).WillReturnRows(userRows("bob"))
		pool.ExpectExec(`UPDATE user_session_tokens`).
			WithArgs("u1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
		pool.ExpectExec(`UPDATE user_sessions`).
			WithArgs("u1").WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM "users"`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DELETE FROM "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DELETE FROM "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/users/u1", "")
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestTelemetryEventSearchRoute(t *testing.T) {
	application, _, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()

	t.Run("happy path returns telemetry rows", func(t *testing.T) {
		pool.ExpectQuery(`FROM telemetry_events`).WithArgs("deployment-a", 51).
			WillReturnRows(pgxmock.NewRows([]string{"event_id", "user_id", "session_id", "type", "resource_type", "resource_id", "result", "payload", "occurred_at", "received_at"}).
				AddRow("evt-1", "user-a", "sess-1", "model.call", "model", "bench-glm", "success", nil, time.Now(), time.Now()))
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/events", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items"`) {
			t.Fatalf("search = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("bad result filter is rejected", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/events?result=nonsense", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad filter = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("bad time filter is rejected", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/events?occurredAfter=not-a-time", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad time = %d %s", response.Code, response.Body.String())
		}
	})
}
