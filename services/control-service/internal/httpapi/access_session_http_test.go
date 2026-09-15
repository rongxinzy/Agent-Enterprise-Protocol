package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

func TestProtectedRoutesRejectInactiveOrRevokedSessions(t *testing.T) {
	for _, test := range []struct {
		name  string
		state app.AccessSessionState
		err   error
		code  int
		body  string
	}{
		{name: "disabled user", err: app.ErrAccessSessionInvalid, code: http.StatusUnauthorized, body: `"code":"SESSION_REVOKED"`},
		{name: "revoked session", err: app.ErrAccessSessionInvalid, code: http.StatusUnauthorized, body: `"code":"SESSION_REVOKED"`},
		{name: "database failure", err: errors.New("database unavailable"), code: http.StatusInternalServerError, body: `"code":"INTERNAL_ERROR"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, _, userToken := testHTTPApplication(t)
			application.SetAccessSessionValidator(accessSessionValidatorFunc(func(context.Context, string, string, string) (app.AccessSessionState, error) {
				return test.state, test.err
			}))
			response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/heartbeat", `{}`)
			if response.Code != test.code || !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("protected route = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAdminTokenUsesCurrentAccountPrivilege(t *testing.T) {
	application, adminToken, _ := testHTTPApplication(t)
	application.SetAccessSessionValidator(accessSessionValidatorFunc(func(context.Context, string, string, string) (app.AccessSessionState, error) {
		return app.AccessSessionState{Admin: false}, nil
	}))
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	application.SetRuntimeDatabase(pool)
	pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "admin-user", "roles.read").
		WillReturnRows(pgxmock.NewRows([]string{"allowed"}).AddRow(false))
	response := userRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/permissions", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("demoted administrator = %d %s", response.Code, response.Body.String())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentPasswordRequirementOverridesAccessToken(t *testing.T) {
	t.Run("current requirement restricts stale token", func(t *testing.T) {
		application, _, userToken := testHTTPApplication(t)
		application.SetAccessSessionValidator(accessSessionValidatorFunc(func(context.Context, string, string, string) (app.AccessSessionState, error) {
			return app.AccessSessionState{PasswordChangeRequired: true}, nil
		}))
		response := userRequest(New(application).Handler(), userToken, http.MethodGet, "/aep/v1/user/models", "")
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"PASSWORD_CHANGE_REQUIRED"`) {
			t.Fatalf("current password gate = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("cleared requirement releases stale token", func(t *testing.T) {
		application, _, _ := testHTTPApplication(t)
		staleToken, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-a", false, true, []string{"member"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		response := userRequest(New(application).Handler(), staleToken, http.MethodGet, "/aep/v1/user/models", "")
		if response.Code == http.StatusForbidden && strings.Contains(response.Body.String(), `"code":"PASSWORD_CHANGE_REQUIRED"`) {
			t.Fatalf("cleared password gate remained active = %d %s", response.Code, response.Body.String())
		}
	})
}
