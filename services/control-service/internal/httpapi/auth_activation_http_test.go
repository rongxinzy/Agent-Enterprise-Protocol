package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAuthenticationMethodsReturnsPasswordMethod(t *testing.T) {
	application, mock, _ := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).
		WithArgs("deployment-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow("deployment-a", "Deployment A", now))
	request := userRequest(handler, "", http.MethodGet, "/aep/v1/auth/methods?deploymentHint=deployment-a", "")
	if request.Code != http.StatusOK || !strings.Contains(request.Body.String(), `"preferredMethodId":"zhiyuan-password"`) || !strings.Contains(request.Body.String(), `"type":"password"`) {
		t.Fatalf("authentication methods = %d %s", request.Code, request.Body.String())
	}

	invalid := userRequest(handler, "", http.MethodGet, "/aep/v1/auth/methods?deploymentHint=other", "")
	if invalid.Code != http.StatusNotFound || !strings.Contains(invalid.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("invalid deployment hint = %d %s", invalid.Code, invalid.Body.String())
	}
}
