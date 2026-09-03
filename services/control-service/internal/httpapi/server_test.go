package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

func TestMetadataAdvertisesCredentialsOnlyWhenConfigured(t *testing.T) {
	testMetadata := func(application *app.App) []string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil)
		response := httptest.NewRecorder()
		New(application).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("metadata status = %d", response.Code)
		}
		var document struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		return document.Capabilities
	}

	if contains(testMetadata(&app.App{}), "credentials") {
		t.Fatal("metadata advertised Credentials without a configured master key")
	}
	provider, enabled, err := credential.NewProvider(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")), "")
	if err != nil || !enabled {
		t.Fatalf("NewProvider() = %v, %v", enabled, err)
	}
	if !contains(testMetadata(&app.App{Credentials: credential.NewSealer(provider)}), "credentials") {
		t.Fatal("metadata omitted Credentials with a configured master key")
	}
}

func TestPasswordChangeRequiredSessionIsRestricted(t *testing.T) {
	tokens, err := auth.NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := tokens.Issue("user-1", "tenant-1", "agent-1", false, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(&app.App{Tokens: tokens}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/aep/v1/agent/models", nil)
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("restricted session status = %d, body = %s", response.Code, response.Body.String())
	}
	var problem map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem["code"] != "PASSWORD_CHANGE_REQUIRED" {
		t.Fatalf("restricted session problem = %#v, %v", problem, err)
	}
}

func TestMetadataAdvertisesMockFederatedAuthOnlyWhenEnabled(t *testing.T) {
	requestCapabilities := func(environment string, enabled bool) []string {
		t.Helper()
		application := &app.App{Config: config.Config{Environment: environment, EnableMockFederatedAuth: enabled}}
		request := httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil)
		response := httptest.NewRecorder()
		New(application).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("metadata status = %d", response.Code)
		}
		var document map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		rawCapabilities, ok := document["capabilities"].([]any)
		if !ok {
			t.Fatalf("metadata capabilities = %#v", document["capabilities"])
		}
		capabilities := make([]string, 0, len(rawCapabilities))
		for _, value := range rawCapabilities {
			capability, ok := value.(string)
			if !ok {
				t.Fatalf("metadata capability = %#v", value)
			}
			capabilities = append(capabilities, capability)
		}
		return capabilities
	}

	if contains(requestCapabilities("development", false), "federated_auth") {
		t.Fatal("metadata advertised disabled mock federated authentication")
	}
	if !contains(requestCapabilities("development", true), "federated_auth") {
		t.Fatal("metadata omitted enabled mock federated authentication")
	}
	if contains(requestCapabilities("production", true), "federated_auth") {
		t.Fatal("metadata advertised mock federated authentication in production")
	}
}

func TestMetadataAdvertisesDeploymentIdentity(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil)
	response := httptest.NewRecorder()
	application := &app.App{Config: config.Config{DeploymentID: "deployment-42", DeploymentName: "Zhiyuan deployment"}}
	New(application).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metadata status = %d", response.Code)
	}
	var document struct {
		DeploymentID string `json:"deploymentId"`
		Deployment   struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.DeploymentID != "deployment-42" || document.Deployment.ID != "deployment-42" || document.Deployment.Name != "Zhiyuan deployment" {
		t.Fatalf("metadata deployment = %#v", document)
	}
}

func TestProductionNeverEnablesMockFederatedAuth(t *testing.T) {
	if mockFederatedAuthEnabled(config.Config{Environment: "production", EnableMockFederatedAuth: true}) {
		t.Fatal("production enabled mock federated authentication")
	}
	if !mockFederatedAuthEnabled(config.Config{Environment: "test", EnableMockFederatedAuth: true}) {
		t.Fatal("test environment omitted explicitly enabled mock federated authentication")
	}
}

func TestDisabledMockFederatedAuthEndpointsReturnNotFound(t *testing.T) {
	handler := New(&app.App{}).Handler()
	for _, path := range []string{"/aep/v1/auth/federated/start", "/aep/v1/auth/federated/exchange"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("disabled endpoint %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}
}

func TestInternalDataPlaneEndpointRequiresServiceToken(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/internal/data-plane/desired-state", nil)
	response := httptest.NewRecorder()
	New(&app.App{}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("internal data-plane endpoint status = %d", response.Code)
	}
}

func TestProtocolVersionGate(t *testing.T) {
	observedResponses := 0
	observe := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			next.ServeHTTP(response, request)
			observedResponses++
		})
	}
	handler := New(&app.App{}, observe).Handler()
	for _, version := range []string{"", "2.0"} {
		request := httptest.NewRequest(http.MethodGet, "/aep/v1/agent/me", nil)
		if version != "" {
			request.Header.Set("X-AEP-Protocol-Version", version)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUpgradeRequired {
			t.Fatalf("version %q status = %d, body = %s", version, response.Code, response.Body.String())
		}
		if response.Header().Get("X-AEP-Supported-Protocol-Versions") != supportedProtocolVersion {
			t.Fatalf("version %q omitted supported protocol response header", version)
		}
		var problem map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if problem["code"] != "PROTOCOL_VERSION_UNSUPPORTED" || problem["requestId"] == "" {
			t.Fatalf("version %q problem = %#v", version, problem)
		}
	}

	if observedResponses != 2 {
		t.Fatalf("runtime middleware observed %d protocol rejections", observedResponses)
	}
	request := httptest.NewRequest(http.MethodGet, "/aep/v1/agent/me", nil)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("supported version did not reach authentication: %d", response.Code)
	}

	for _, path := range []string{"/aep/v1/metadata", "/livez"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("exempt path %s status = %d", path, response.Code)
		}
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
