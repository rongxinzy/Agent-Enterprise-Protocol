package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAdminSessionListAndRevoke(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT session_id,user_id,topic,created_at,last_seen_at,revoked_at`).
		WithArgs("deployment-a", "user-a", int32(2)).
		WillReturnRows(pgxmock.NewRows([]string{"session_id", "user_id", "topic", "created_at", "last_seen_at", "revoked_at"}).
			AddRow("session-a", "user-a", "aep:user-a", now, now, nil))
	response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/sessions?userId=user-a&limit=2", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sessionId":"session-a"`) || !strings.Contains(response.Body.String(), `"topic":"aep:user-a"`) {
		t.Fatalf("session list = %d %s", response.Code, response.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM user_sessions`).WithArgs("deployment-a", "session-a").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at`).WithArgs("session-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE user_sessions SET revoked_at`).WithArgs("deployment-a", "session-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()
	revoked := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/sessions/session-a/revoke", "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("session revoke = %d %s", revoked.Code, revoked.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM user_sessions`).WithArgs("deployment-a", "missing").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectRollback()
	notFound := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/sessions/missing/revoke", "")
	if notFound.Code != http.StatusNotFound || !strings.Contains(notFound.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing session revoke = %d %s", notFound.Code, notFound.Body.String())
	}
}

func TestAdminSessionRoutesMapDatabaseFailures(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectQuery(`SELECT session_id,user_id,topic,created_at,last_seen_at,revoked_at`).
		WithArgs("deployment-a", "", int32(50)).WillReturnError(errors.New("database unavailable"))
	list := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/sessions", "")
	if list.Code != http.StatusInternalServerError || !strings.Contains(list.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("session list database error = %d %s", list.Code, list.Body.String())
	}

	pool.ExpectBegin().WillReturnError(errors.New("database unavailable"))
	revoke := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/sessions/session-a/revoke", "")
	if revoke.Code != http.StatusInternalServerError || !strings.Contains(revoke.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("session revoke database error = %d %s", revoke.Code, revoke.Body.String())
	}
}
