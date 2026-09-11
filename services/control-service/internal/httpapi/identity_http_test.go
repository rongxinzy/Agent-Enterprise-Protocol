package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCurrentIdentityRejectsForeignDeploymentToken(t *testing.T) {
	application, _, _ := testHTTPApplication(t)
	token, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "other-deployment", "session-user", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/me", "")
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"INVALID_TOKEN"`) {
		t.Fatalf("foreign deployment identity = %d %s", response.Code, response.Body.String())
	}
}

func TestCurrentIdentityMapsStorageFailure(t *testing.T) {
	application, mock, _, _ := newUserHTTPApplication(t)
	token := issueHTTPUserToken(t, application)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "user-a", 1).WillReturnError(errors.New("database unavailable"))

	response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/me", "")
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) || strings.Contains(response.Body.String(), "database unavailable") {
		t.Fatalf("identity storage failure = %d %s", response.Code, response.Body.String())
	}
}
