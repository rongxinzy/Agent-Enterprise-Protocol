package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestIssueModelGatewayTokenDerivesUserScopes(t *testing.T) {
	a, mock, token := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, a)
	mock.ExpectQuery(`SELECT \* FROM "users"`).WithArgs("deployment-a", "user-b", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "status"}).AddRow("deployment-a", "user-b", "active"))
	pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "user-b").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("qwen3-vl-4b").AddRow("flux-2-klein-4b"))
	pool.ExpectExec(`INSERT INTO admin_audit_events`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	got := adminRequest(New(a).Handler(), token, http.MethodPost, "/aep/v1/admin/model-gateway/tokens",
		`{"userId":"user-b","sessionId":"conversation-42"}`)
	if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(got.Code, got.Body.String())
	}
	var issued struct {
		UserID           string   `json:"userId"`
		ModelAccessToken string   `json:"modelAccessToken"`
		ExpiresIn        int      `json:"expiresIn"`
		SessionID        string   `json:"sessionId"`
		ModelScopes      []string `json:"modelScopes"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.UserID != "user-b" || issued.SessionID != "conversation-42" || len(issued.ModelScopes) != 2 {
		t.Fatal(issued)
	}
	claims, err := a.Tokens.ParseModel(issued.ModelAccessToken)
	if err != nil || claims.TokenUse != "model" || claims.SessionID != "conversation-42" ||
		claims.Subject != "user-b" || claims.Admin || claims.DeploymentID != "deployment-a" {
		t.Fatal("claims must name the target user with a model-use token")
	}
	if len(claims.ModelScopes) != 2 || claims.ModelScopes[0] != "qwen3-vl-4b" {
		t.Fatal("scopes must mirror the target user's assignments")
	}
	if claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) <= 0 || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 15*time.Minute {
		t.Fatal("token must be short-lived")
	}
}

func TestIssueModelGatewayTokenMintsSyntheticSession(t *testing.T) {
	a, mock, token := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, a)
	mock.ExpectQuery(`SELECT \* FROM "users"`).WithArgs("deployment-a", "user-b", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "status"}).AddRow("deployment-a", "user-b", "active"))
	pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "user-b").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("qwen3-vl-4b"))
	pool.ExpectExec(`INSERT INTO admin_audit_events`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	got := adminRequest(New(a).Handler(), token, http.MethodPost, "/aep/v1/admin/model-gateway/tokens", `{"userId":"user-b"}`)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var issued struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.SessionID, "m2m-") || len(issued.SessionID) <= len("m2m-") {
		t.Fatal("omitted sessionId must mint an m2m-prefixed synthetic session:", issued.SessionID)
	}
}

func TestIssueModelGatewayTokenRejectsUnknownAndInactiveUser(t *testing.T) {
	a, mock, token := newStoreBackedHTTPApplication(t)
	mock.ExpectQuery(`SELECT \* FROM "users"`).WithArgs("deployment-a", "ghost", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "status"}))
	got := adminRequest(New(a).Handler(), token, http.MethodPost, "/aep/v1/admin/model-gateway/tokens", `{"userId":"ghost"}`)
	if got.Code != 404 || !strings.Contains(got.Body.String(), "RESOURCE_NOT_FOUND") {
		t.Fatal(got.Code, got.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "users"`).WithArgs("deployment-a", "suspended", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "status"}).AddRow("deployment-a", "suspended", "suspended"))
	got = adminRequest(New(a).Handler(), token, http.MethodPost, "/aep/v1/admin/model-gateway/tokens", `{"userId":"suspended"}`)
	if got.Code != 422 || !strings.Contains(got.Body.String(), "USER_INACTIVE") {
		t.Fatal(got.Code, got.Body.String())
	}

	got = adminRequest(New(a).Handler(), token, http.MethodPost, "/aep/v1/admin/model-gateway/tokens", `{}`)
	if got.Code != 400 || !strings.Contains(got.Body.String(), "INVALID_REQUEST") {
		t.Fatal(got.Code, got.Body.String())
	}
}
