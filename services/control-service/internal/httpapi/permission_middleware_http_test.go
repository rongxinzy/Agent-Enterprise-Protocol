package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAdminPermissionMiddlewareRejectsUnauthorizedUser(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-user", "deployment-a", "session-delegated", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectQuery(`SELECT EXISTS \(`).
		WithArgs("deployment-a", "delegated-user", "roles.read").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/permissions", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("unauthorized admin request = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminPermissionMiddlewareMapsDatabaseFailure(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-user", "deployment-a", "session-delegated", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectQuery(`SELECT EXISTS \(`).
		WithArgs("deployment-a", "delegated-user", "roles.read").
		WillReturnError(errors.New("database unavailable"))

	response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/permissions", "")
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("permission database failure = %d %s", response.Code, response.Body.String())
	}
}
