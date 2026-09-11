package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
)

func TestPasswordChangeGateUsesRuntimeDatabase(t *testing.T) {
	application, _, pool, _ := newUserHTTPApplication(t)
	server := New(application)
	claims := &auth.Claims{DeploymentID: "deployment-a"}
	claims.Subject = "user-a"
	pool.ExpectQuery(`SELECT require_password_change FROM users WHERE deployment_id=\$1 AND id=\$2`).
		WithArgs("deployment-a", "user-a").WillReturnRows(pgxmock.NewRows([]string{"require_password_change"}).AddRow(false))
	if server.passwordChangeStillRequired(httptest.NewRequest(http.MethodGet, "/aep/v1/user/models", nil), claims) {
		t.Fatal("cleared password-change flag remained restricted")
	}

	requiredToken, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-required", false, true, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectQuery(`SELECT require_password_change FROM users WHERE deployment_id=\$1 AND id=\$2`).
		WithArgs("deployment-a", "user-a").WillReturnRows(pgxmock.NewRows([]string{"require_password_change"}).AddRow(true))
	required := userRequest(New(application).Handler(), requiredToken, http.MethodGet, "/aep/v1/user/models", "")
	if required.Code != http.StatusForbidden || !strings.Contains(required.Body.String(), `"code":"PASSWORD_CHANGE_REQUIRED"`) {
		t.Fatalf("required password-change gate = %d %s", required.Code, required.Body.String())
	}

	failureToken, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-failure", false, true, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectQuery(`SELECT require_password_change FROM users WHERE deployment_id=\$1 AND id=\$2`).
		WithArgs("deployment-a", "user-a").WillReturnError(errors.New("database unavailable"))
	failure := userRequest(New(application).Handler(), failureToken, http.MethodGet, "/aep/v1/user/models", "")
	if failure.Code != http.StatusForbidden || !strings.Contains(failure.Body.String(), `"code":"PASSWORD_CHANGE_REQUIRED"`) {
		t.Fatalf("password-change database failure = %d %s", failure.Code, failure.Body.String())
	}
}
