package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestLicenseReadHandlersMapDatabaseFailures(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectQuery(`SELECT l\.license_id,l\.deployment_id`).WithArgs("deployment-a", int32(50)).
		WillReturnError(errors.New("license list unavailable"))
	list := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/licenses?limit=50", "")
	if list.Code != http.StatusInternalServerError || !strings.Contains(list.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("license list failure = %d %s", list.Code, list.Body.String())
	}

	pool.ExpectQuery(`SELECT l\.license_id,l\.deployment_id`).WithArgs("deployment-a", "missing").
		WillReturnError(errors.New("license lookup unavailable"))
	get := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/licenses/missing", "")
	if get.Code != http.StatusInternalServerError || !strings.Contains(get.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("license get failure = %d %s", get.Code, get.Body.String())
	}
}

func TestLicenseRevokeMapsTransactionFailures(t *testing.T) {
	t.Run("begin error", func(t *testing.T) {
		application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin failed"))
		response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/licenses/lic-1/revoke", "")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("revoke begin failure = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("query error", func(t *testing.T) {
		application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnError(errors.New("query failed"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/licenses/lic-1/revoke", "")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("revoke query failure = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("already revoked skips activation updates", func(t *testing.T) {
		application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("revoked"))
		pool.ExpectExec(`INSERT INTO license_audit_events`).WithArgs(pgxmock.AnyArg(), "deployment-a", "lic-1", "admin-user").WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectCommit()
		response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/licenses/lic-1/revoke", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"revoked"`) {
			t.Fatalf("already revoked = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("audit error", func(t *testing.T) {
		application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("revoked"))
		pool.ExpectExec(`INSERT INTO license_audit_events`).WithArgs(pgxmock.AnyArg(), "deployment-a", "lic-1", "admin-user").WillReturnError(errors.New("audit failed"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/licenses/lic-1/revoke", "")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("revoke audit failure = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("commit error", func(t *testing.T) {
		application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("revoked"))
		pool.ExpectExec(`INSERT INTO license_audit_events`).WithArgs(pgxmock.AnyArg(), "deployment-a", "lic-1", "admin-user").WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectCommit().WillReturnError(errors.New("commit failed"))
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/licenses/lic-1/revoke", "")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("revoke commit failure = %d %s", response.Code, response.Body.String())
		}
	})
}
