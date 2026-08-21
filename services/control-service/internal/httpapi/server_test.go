package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
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

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
