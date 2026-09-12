package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func testHTTPApplication(t *testing.T) (*app.App, string, string) {
	t.Helper()
	tokens, err := auth.NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	provider, enabled, err := credential.NewProvider(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")), "")
	if err != nil || !enabled {
		t.Fatalf("NewProvider() = %v, %v", enabled, err)
	}
	adminToken, _, err := tokens.IssueWithDeploymentSession("admin-user", "deployment-a", "session-admin", true, false, []string{"admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	userToken, _, err := tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-user", false, false, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application := &app.App{
		Config: config.Config{
			DeploymentID:              "deployment-a",
			BootstrapDeploymentID:     "deployment-a",
			GatewayLicenseStatusToken: "gateway-secret",
		},
		Tokens:      tokens,
		Credentials: credential.NewSealer(provider),
	}
	return application, adminToken, userToken
}

func TestMalformedJSONIsRejectedAcrossWriteRoutes(t *testing.T) {
	application, adminToken, userToken := testHTTPApplication(t)
	handler := New(application).Handler()
	tests := []struct {
		name   string
		method string
		path   string
		token  string
	}{
		{name: "password login", method: http.MethodPost, path: "/aep/v1/auth/password/login"},
		{name: "refresh", method: http.MethodPost, path: "/aep/v1/auth/refresh"},
		{name: "heartbeat", method: http.MethodPost, path: "/aep/v1/user/heartbeat", token: userToken},
		{name: "create role", method: http.MethodPost, path: "/aep/v1/admin/roles", token: adminToken},
		{name: "update role", method: http.MethodPatch, path: "/aep/v1/admin/roles/role-a", token: adminToken},
		{name: "create team", method: http.MethodPost, path: "/aep/v1/admin/teams", token: adminToken},
		{name: "update team", method: http.MethodPatch, path: "/aep/v1/admin/teams/team-a", token: adminToken},
		{name: "create user", method: http.MethodPost, path: "/aep/v1/admin/users", token: adminToken},
		{name: "import users", method: http.MethodPost, path: "/aep/v1/admin/users/import", token: adminToken},
		{name: "update user", method: http.MethodPatch, path: "/aep/v1/admin/users/user-a", token: adminToken},
		{name: "reset password", method: http.MethodPost, path: "/aep/v1/admin/users/user-a/reset-password", token: adminToken},
		{name: "create Skill", method: http.MethodPost, path: "/aep/v1/admin/skills", token: adminToken},
		{name: "update Skill", method: http.MethodPatch, path: "/aep/v1/admin/skills/skill-a", token: adminToken},
		{name: "assign Skill", method: http.MethodPost, path: "/aep/v1/admin/skill-assignments", token: adminToken},
		{name: "create event", method: http.MethodPost, path: "/aep/v1/admin/control-events", token: adminToken},
		{name: "import License", method: http.MethodPost, path: "/aep/v1/admin/licenses/import", token: adminToken},
		{name: "create model", method: http.MethodPost, path: "/aep/v1/admin/models", token: adminToken},
		{name: "update model", method: http.MethodPatch, path: "/aep/v1/admin/models/model-a", token: adminToken},
		{name: "assign model", method: http.MethodPost, path: "/aep/v1/admin/model-assignments", token: adminToken},
		{name: "put data plane", method: http.MethodPut, path: "/aep/v1/admin/data-plane/desired-state", token: adminToken},
		{name: "create Credential", method: http.MethodPost, path: "/aep/v1/admin/credentials", token: adminToken},
		{name: "update Credential", method: http.MethodPatch, path: "/aep/v1/admin/credentials/credential-a", token: adminToken},
		{name: "rotate Credential", method: http.MethodPost, path: "/aep/v1/admin/credentials/credential-a/rotate", token: adminToken},
		{name: "assign Credential", method: http.MethodPost, path: "/aep/v1/admin/credential-assignments", token: adminToken},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader("{"))
			request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			var problem map[string]any
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil || problem["code"] != "INVALID_REQUEST" || problem["requestId"] == "" {
				t.Fatalf("problem = %#v, %v", problem, err)
			}
		})
	}
}

func TestAuthenticationAndInternalServiceBoundaries(t *testing.T) {
	application, _, _ := testHTTPApplication(t)
	handler := New(application).Handler()
	tests := []struct {
		name    string
		method  string
		path    string
		token   string
		headers map[string]string
		status  int
		code    string
	}{
		{name: "missing user token", method: http.MethodGet, path: "/aep/v1/user/me", status: http.StatusUnauthorized, code: "TOKEN_INVALID"},
		{name: "invalid user token", method: http.MethodGet, path: "/aep/v1/user/me", token: "invalid", status: http.StatusUnauthorized, code: "TOKEN_INVALID"},
		{name: "missing gateway secret", method: http.MethodGet, path: "/internal/gateway/licenses/license-a", status: http.StatusUnauthorized, code: "INTERNAL_AUTH_REQUIRED"},
		{name: "missing entitlement context", method: http.MethodGet, path: "/internal/gateway/licenses/license-a", headers: map[string]string{"X-AEP-Gateway-Token": "gateway-secret"}, status: http.StatusBadRequest, code: "ENTITLEMENT_CONTEXT_REQUIRED"},
		{name: "missing reconciler secret", method: http.MethodGet, path: "/internal/data-plane/desired-state", status: http.StatusUnauthorized, code: "INTERNAL_AUTH_REQUIRED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if strings.HasPrefix(test.path, "/aep/v1/") {
				request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
			}
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestReadinessJWKSAndRequestIDs(t *testing.T) {
	application, _, _ := testHTTPApplication(t)
	handler := New(application).Handler()

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"code":"DEPENDENCY_UNAVAILABLE"`) {
		t.Fatalf("readiness response = %d %s", ready.Code, ready.Body.String())
	}

	jwksRequest := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	jwksRequest.Header.Set("X-Request-ID", "valid-request-42")
	jwks := httptest.NewRecorder()
	handler.ServeHTTP(jwks, jwksRequest)
	if jwks.Code != http.StatusOK || jwks.Header().Get("X-Request-ID") != "valid-request-42" || !strings.Contains(jwks.Body.String(), `"kty":"OKP"`) {
		t.Fatalf("JWKS response = %d %s", jwks.Code, jwks.Body.String())
	}

	invalidID := httptest.NewRequest(http.MethodGet, "/livez", nil)
	invalidID.Header.Set("X-Request-ID", "invalid request id")
	live := httptest.NewRecorder()
	handler.ServeHTTP(live, invalidID)
	if live.Code != http.StatusOK || live.Header().Get("X-Request-ID") == "invalid request id" || live.Header().Get("X-Request-ID") == "" {
		t.Fatalf("request ID was not replaced: %q", live.Header().Get("X-Request-ID"))
	}
}

func TestHTTPValidationHelpers(t *testing.T) {
	if !validCredentialValue("secret") || validCredentialValue("") || validCredentialValue(strings.Repeat("x", 32769)) {
		t.Fatal("Credential value bounds are incorrect")
	}
	for _, mode := range []string{"server_only", "client"} {
		if !validDeliveryMode(mode) {
			t.Fatalf("valid delivery mode rejected: %s", mode)
		}
	}
	if validDeliveryMode("public") || !validCredentialSubject("role") || validCredentialSubject("agent") {
		t.Fatal("Credential enum validation is incorrect")
	}
	if !validRBACID("team.a-1_ok") || validRBACID("") || validRBACID("bad/id") || validRBACID(strings.Repeat("a", 101)) {
		t.Fatal("RBAC ID validation is incorrect")
	}
	if stringValue(nil) != "" {
		t.Fatal("nil string pointer must map to an empty string")
	}
	value := "cursor"
	if stringValue(&value) != value || nullablePGText(pgtype.Text{}) != nil || nullablePGText(pgtype.Text{String: "value", Valid: true}) != "value" {
		t.Fatal("nullable value conversion is incorrect")
	}
	if optionalAuditReason("") != nil || optionalAuditReason("operator request") != "operator request" {
		t.Fatal("optional audit reason conversion is incorrect")
	}
	scope, scopeID := skillAssignmentEventScope("team", "team-a")
	if scope != "team" || scopeID == nil || *scopeID != "team-a" {
		t.Fatalf("Skill assignment scope = %q/%v", scope, scopeID)
	}
}

func TestModelNormalizationAndJSONContract(t *testing.T) {
	capabilities := normalizeCapabilities([]string{" streaming ", "text", "", "text"})
	if strings.Join(capabilities, ",") != "streaming,text" {
		t.Fatalf("normalized capabilities = %#v", capabilities)
	}
	contextWindow, isDefault, enabled := int32(8192), true, true
	input := modelWrite{
		ID: "chat", DisplayName: "Enterprise Chat", SourceType: "gateway", Protocol: "openai-compatible",
		Capabilities: &capabilities, ContextWindow: &contextWindow, IsDefault: &isDefault, Enabled: &enabled,
	}
	if !validModelWrite(input) {
		t.Fatal("valid model was rejected")
	}
	input.Protocol = "custom"
	if validModelWrite(input) {
		t.Fatal("unsupported protocol was accepted")
	}

	model := repository.Model{
		ID: "chat", DisplayName: "Enterprise Chat", SourceType: "gateway", Protocol: "openai-compatible",
		Endpoint: pgtype.Text{String: "http://gateway/v1", Valid: true}, CredentialID: pgtype.Text{String: "credential-a", Valid: true},
		Capabilities: repository.StringArray{"text", "reasoning"}, ContextWindow: pgtype.Int4{Int32: 8192, Valid: true}, Enabled: true,
		ReasoningCompatibility: json.RawMessage(`{"thinkingFormat":"deepseek","supportsReasoningEffort":true,"requiresReasoningContentOnAssistantMessages":true}`),
	}
	public := modelJSON(model, false)
	if _, found := public["credentialId"]; found || public["endpoint"] != "http://gateway/v1" || public["contextWindow"] != int32(8192) {
		t.Fatalf("public model JSON = %#v", public)
	}
	admin := modelJSON(model, true)
	if admin["credentialId"] != "credential-a" || admin["reasoningCompatibility"] == nil {
		t.Fatalf("admin model JSON = %#v", admin)
	}
}

func TestServerUtilityErrorContracts(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/?limit=200", nil)
	if limit(request) != 200 {
		t.Fatalf("limit = %d", limit(request))
	}
	for _, raw := range []string{"", "0", "201", "invalid"} {
		request := httptest.NewRequest(http.MethodGet, "/?limit="+raw, nil)
		if limit(request) != 50 {
			t.Fatalf("limit %q = %d", raw, limit(request))
		}
	}
	if !validRequestID("request-1_OK:test") || validRequestID("") || validRequestID("has space") || validRequestID(strings.Repeat("a", 129)) {
		t.Fatal("request ID validation is incorrect")
	}

	canceled := httptest.NewRecorder()
	databaseFailure(canceled, httptest.NewRequest(http.MethodGet, "/", nil), context.Canceled)
	if canceled.Code != http.StatusOK || canceled.Body.Len() != 0 {
		t.Fatalf("canceled request wrote a response: %d %s", canceled.Code, canceled.Body.String())
	}
	failure := httptest.NewRecorder()
	databaseFailure(failure, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("database unavailable"))
	if failure.Code != http.StatusInternalServerError || !strings.Contains(failure.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("database failure response = %d %s", failure.Code, failure.Body.String())
	}
}

func TestDeploymentAliasResolution(t *testing.T) {
	server := &Server{app: &app.App{Config: config.Config{
		DeploymentID: "public-deployment", BootstrapDeploymentID: "storage-deployment",
	}}}
	if server.storageTenantID() != "storage-deployment" || !server.acceptsDeployment("public-deployment") || !server.acceptsDeployment("storage-deployment") || server.acceptsDeployment("other") {
		t.Fatal("deployment aliases were resolved incorrectly")
	}
	if tenant, ok := server.resolveTenant(""); !ok || tenant != "storage-deployment" {
		t.Fatalf("empty deployment resolved to %q/%v", tenant, ok)
	}
	if _, ok := server.resolveTenant("other"); ok {
		t.Fatal("unknown deployment was accepted")
	}
}
