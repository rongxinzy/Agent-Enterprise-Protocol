package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const authenticationAuditQuery = `SELECT cursor,user_id,event_type,outcome,reason,source_hash,created_at FROM authentication_audit_events`

func authenticationAuditRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"cursor", "user_id", "event_type", "outcome", "reason", "source_hash", "created_at"})
}

func TestListAuthenticationAuditRecords(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	pool.ExpectQuery(authenticationAuditQuery).WithArgs("deployment-a", 51).
		WillReturnRows(authenticationAuditRows().
			AddRow(int64(7), "user-a", "login.failed", "failure", "invalid_credentials", "source-fingerprint", now))

	response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/audit/authentication", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"cursor":"7"`) ||
		!strings.Contains(response.Body.String(), `"eventType":"login.failed"`) ||
		!strings.Contains(response.Body.String(), `"reason":"invalid_credentials"`) ||
		!strings.Contains(response.Body.String(), `"sourceHash":"source-fingerprint"`) ||
		!strings.Contains(response.Body.String(), `"nextCursor":null`) {
		t.Fatalf("authentication audit = %d %s", response.Code, response.Body.String())
	}
}

func TestListAuthenticationAuditFiltersAndCursor(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	// limit=1 with two rows: one item plus a cursor for the next page.
	pool.ExpectQuery(authenticationAuditQuery).WithArgs("deployment-a", "login.failed", "failure", "user-a", int64(9), 2).
		WillReturnRows(authenticationAuditRows().
			AddRow(int64(7), "user-a", "login.failed", "failure", "invalid_credentials", "src", now).
			AddRow(int64(6), "user-a", "login.failed", "failure", "invalid_credentials", "src", now))

	response := userRequest(handler, adminToken, http.MethodGet,
		"/aep/v1/admin/audit/authentication?eventType=login.failed&outcome=failure&userId=user-a&cursor=9&limit=1", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"cursor":"7"`) ||
		!strings.Contains(response.Body.String(), `"nextCursor":"7"`) {
		t.Fatalf("filtered authentication audit = %d %s", response.Code, response.Body.String())
	}
}

func TestListAuthenticationAuditRejectsInvalidFilters(t *testing.T) {
	application, _, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	for _, path := range []string{
		"/aep/v1/admin/audit/authentication?eventType=nope",
		"/aep/v1/admin/audit/authentication?outcome=nope",
		"/aep/v1/admin/audit/authentication?cursor=abc",
		"/aep/v1/admin/audit/authentication?createdAfter=not-a-time",
	} {
		response := userRequest(handler, adminToken, http.MethodGet, path, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid filter %s = %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestListAuthenticationAuditRequiresPermission(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "audit.read").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	response := userRequest(handler, userToken, http.MethodGet, "/aep/v1/admin/audit/authentication", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("unauthorized audit read = %d %s", response.Code, response.Body.String())
	}
}

func TestListAuthenticationAuditHandlesNullUserID(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	// Unknown-username failures and throttling persist a NULL user_id; the row
	// must scan and serialize instead of failing the whole page.
	pool.ExpectQuery(authenticationAuditQuery).WithArgs("deployment-a", 51).
		WillReturnRows(authenticationAuditRows().
			AddRow(int64(9), nil, "login.throttled", "denied", "backoff_active", "src", now))

	response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/audit/authentication", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"userId":null`) ||
		!strings.Contains(response.Body.String(), `"eventType":"login.throttled"`) {
		t.Fatalf("null user id audit = %d %s", response.Code, response.Body.String())
	}
}
