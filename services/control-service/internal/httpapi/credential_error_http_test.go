package httpapi

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

type failingCredentialKeyProvider struct {
	err error
}

func (p failingCredentialKeyProvider) Active(context.Context) (credential.MasterKey, error) {
	return credential.MasterKey{}, p.err
}

func (p failingCredentialKeyProvider) ByID(context.Context, string) (credential.MasterKey, error) {
	return credential.MasterKey{}, p.err
}

func requireCredentialProblem(t *testing.T, responseStatus int, body string, status int, code string) {
	t.Helper()
	if responseStatus != status || !strings.Contains(body, `"code":"`+code+`"`) {
		t.Fatalf("response = %d %s, want status %d and code %s", responseStatus, body, status, code)
	}
}

func credentialRuntimeRow(now time.Time, id, deliveryMode string, enabled bool) []any {
	return []any{id, "Provider", "provider", "api_key", deliveryMode, []byte("cipher"), []byte("nonce"), "key-a", "****cret", enabled, now, now, now}
}

func TestCredentialServiceMustBeConfigured(t *testing.T) {
	application, _, _, token := newUserHTTPApplication(t)
	application.Credentials = nil
	response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/credentials", "")
	requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusServiceUnavailable, "CREDENTIALS_NOT_CONFIGURED")
}

func TestUserCredentialListFailureBoundaries(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`SELECT c\.id,c\.name,c\.service`).WithArgs("deployment-a", "user-a").
			WillReturnError(errors.New("credentials unavailable"))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/credentials", "")
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("scan", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`SELECT c\.id,c\.name,c\.service`).WithArgs("deployment-a", "user-a").
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("credential-a"))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/credentials", "")
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("row iteration", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		now := time.Now().UTC()
		rows := pgxmock.NewRows(credentialRuntimeColumns()).
			AddRow(credentialRuntimeRow(now, "credential-a", "client", true)...).
			RowError(0, errors.New("row unavailable"))
		pool.ExpectQuery(`SELECT c\.id,c\.name,c\.service`).WithArgs("deployment-a", "user-a").WillReturnRows(rows)
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/credentials", "")
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func TestUserCredentialResolutionFailureBoundaries(t *testing.T) {
	t.Run("malformed body", func(t *testing.T) {
		application, _, _, token := newRuntimeHTTPApplication(t)
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
	})

	t.Run("long purpose", func(t *testing.T) {
		application, _, _, token := newRuntimeHTTPApplication(t)
		body := `{"purpose":"` + strings.Repeat("x", 501) + `"}`
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", body)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_PURPOSE")
	})

	t.Run("begin", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin unavailable"))
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("lookup", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-a").
			WillReturnError(errors.New("lookup unavailable"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("disabled", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		now := time.Now().UTC()
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow(credentialRuntimeRow(now, "credential-a", "client", false)...))
		pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "credential-a", "user-a", "session-user", "use provider", "denied", "disabled").
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectCommit()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusForbidden, "CREDENTIAL_DISABLED")
	})

	t.Run("authorization query", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		now := time.Now().UTC()
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow(credentialRuntimeRow(now, "credential-a", "client", true)...))
		pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "credential-a").
			WillReturnError(errors.New("authorization unavailable"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("not assigned", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		now := time.Now().UTC()
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow(credentialRuntimeRow(now, "credential-a", "client", true)...))
		pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "credential-a").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "credential-a", "user-a", "session-user", "use provider", "denied", "not_assigned").
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectCommit()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusForbidden, "ACCESS_DENIED")
	})

	t.Run("decrypt", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		now := time.Now().UTC()
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow(credentialRuntimeRow(now, "credential-a", "client", true)...))
		pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "credential-a").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
		pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "credential-a", "user-a", "session-user", "use provider", "denied", "decrypt_failed").
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectCommit()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "CREDENTIAL_DECRYPT_FAILED")
	})

	t.Run("audit insert", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "missing").WillReturnError(pgx.ErrNoRows)
		pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(
			pgxmock.AnyArg(), "deployment-a", "missing", "user-a", "session-user", "use provider", "denied", "not_found",
		).WillReturnError(errors.New("audit unavailable"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/missing/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("audit commit", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "missing").WillReturnError(pgx.ErrNoRows)
		pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "missing", "user-a", "session-user", "use provider", "denied", "not_found").
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectCommit().WillReturnError(errors.New("commit unavailable"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/credentials/missing/resolve", `{"purpose":"use provider"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func TestAdminCredentialReadAndCreateFailureBoundaries(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).WithArgs("deployment-a", 51).
			WillReturnError(errors.New("credentials unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/credentials", "")
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("invalid create", func(t *testing.T) {
		application, _, token := newStoreBackedHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials", `{"name":"Provider","service":"provider","type":"password","deliveryMode":"client","value":"secret","enabled":true}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_CREDENTIAL")
	})

	t.Run("encrypt create", func(t *testing.T) {
		application, _, token := newStoreBackedHTTPApplication(t)
		application.Credentials = credential.NewSealer(failingCredentialKeyProvider{err: errors.New("key unavailable")})
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials", `{"name":"Provider","service":"provider","type":"api_key","deliveryMode":"client","value":"secret","enabled":true}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "CREDENTIAL_ENCRYPT_FAILED")
	})

	t.Run("store create", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO "credentials"`).WillReturnError(errors.New("insert unavailable"))
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials", `{"name":"Provider","service":"provider","type":"api_key","deliveryMode":"client","value":"secret","enabled":true}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	for _, test := range []struct {
		name        string
		rows        *sqlmock.Rows
		err         error
		status      int
		problemCode string
	}{
		{name: "get missing", rows: sqlmock.NewRows(credentialHTTPColumns()), status: http.StatusNotFound, problemCode: "RESOURCE_NOT_FOUND"},
		{name: "get database error", err: errors.New("lookup unavailable"), status: http.StatusInternalServerError, problemCode: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, token := newStoreBackedHTTPApplication(t)
			expectation := mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).WithArgs("deployment-a", "credential-a", 1)
			if test.err != nil {
				expectation.WillReturnError(test.err)
			} else {
				expectation.WillReturnRows(test.rows)
			}
			response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/credentials/credential-a", "")
			requireCredentialProblem(t, response.Code, response.Body.String(), test.status, test.problemCode)
		})
	}
}

func TestAdminCredentialMutationFailureBoundaries(t *testing.T) {
	t.Run("update missing", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "credentials" SET`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/credentials/credential-a", `{"name":"Provider 2"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("update database error", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "credentials" SET`).WillReturnError(errors.New("update unavailable"))
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/credentials/credential-a", `{"service":"provider-2"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("rotate lookup", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnError(errors.New("lookup unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials/credential-a/rotate", `{"value":"new-secret"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("rotate missing", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials/credential-a/rotate", `{"value":"new-secret"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("rotate encrypt", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		application.Credentials = credential.NewSealer(failingCredentialKeyProvider{err: errors.New("key unavailable")})
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials/credential-a/rotate", `{"value":"new-secret"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "CREDENTIAL_ENCRYPT_FAILED")
	})

	t.Run("rotate store", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "credentials" SET`).WillReturnError(errors.New("rotate unavailable"))
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credentials/credential-a/rotate", `{"value":"new-secret"}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("delete database error", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnError(errors.New("delete unavailable"))
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/credentials/credential-a", "")
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func TestAdminCredentialAssignmentFailureBoundaries(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "credential_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id`).WithArgs("deployment-a").
			WillReturnError(errors.New("assignments unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/credential-assignments", "")
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("lookup", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnError(errors.New("lookup unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credential-assignments", `{"credentialId":"credential-a","subject":{"type":"user","id":"user-a"}}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("missing credential", func(t *testing.T) {
		application, mock, token := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credential-assignments", `{"credentialId":"credential-a","subject":{"type":"user","id":"user-a"}}`)
		requireCredentialProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	for _, test := range []struct {
		name        string
		err         error
		status      int
		problemCode string
	}{
		{name: "duplicate", err: &pgconn.PgError{Code: "23505"}, status: http.StatusConflict, problemCode: "ASSIGNMENT_EXISTS"},
		{name: "database error", err: errors.New("insert unavailable"), status: http.StatusInternalServerError, problemCode: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, token := newStoreBackedHTTPApplication(t)
			mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "credential-a").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "credential_assignments"`).WillReturnError(test.err)
			mock.ExpectRollback()
			response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/credential-assignments", `{"credentialId":"credential-a","subject":{"type":"user","id":"user-a"}}`)
			requireCredentialProblem(t, response.Code, response.Body.String(), test.status, test.problemCode)
		})
	}

	for _, test := range []struct {
		name        string
		result      driver.Result
		err         error
		status      int
		problemCode string
	}{
		{name: "delete missing", result: sqlmock.NewResult(0, 0), status: http.StatusNotFound, problemCode: "RESOURCE_NOT_FOUND"},
		{name: "delete database error", err: errors.New("delete unavailable"), status: http.StatusInternalServerError, problemCode: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, token := newStoreBackedHTTPApplication(t)
			mock.ExpectBegin()
			expectation := mock.ExpectExec(`DELETE FROM "credential_assignments" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "assignment-a")
			if test.err != nil {
				expectation.WillReturnError(test.err)
				mock.ExpectRollback()
			} else {
				expectation.WillReturnResult(test.result)
				mock.ExpectCommit()
			}
			response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/credential-assignments/assignment-a", "")
			requireCredentialProblem(t, response.Code, response.Body.String(), test.status, test.problemCode)
		})
	}
}
