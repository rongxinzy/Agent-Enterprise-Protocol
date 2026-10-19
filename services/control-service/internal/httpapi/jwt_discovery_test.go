package httpapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
)

func TestJWTDiscoveryAcrossSurfacesAndUntrustedHeaders(t *testing.T) {
	for _, surface := range []string{"all", "enterprise", "agent"} {
		t.Run(surface, func(t *testing.T) {
			application, _, _ := testHTTPApplication(t)
			// Discovery must follow the live signer, not a stale Config field.
			application.Config.Issuer = "https://stale.example"
			server := New(application)
			if surface == "enterprise" {
				server = NewEnterpriseAPI(application)
			} else if surface == "agent" {
				server = NewAgentControlAPI(application)
			}
			request := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/.well-known/openid-configuration", nil)
			request.Header.Set("Forwarded", "host=attacker.invalid;proto=http")
			request.Header.Set("X-Forwarded-Host", "attacker.invalid")
			request.Header.Set("X-Forwarded-Proto", "http")
			request.Header.Set("X-Forwarded-Prefix", "/attacker")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("discovery status = %d", response.Code)
			}
			var configuration map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &configuration); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"issuer": "https://issuer.example", "jwks_uri": "https://issuer.example/.well-known/jwks.json",
				"id_token_signing_alg_values_supported": []any{"EdDSA"}}
			if !reflect.DeepEqual(configuration, want) || response.Header().Get("X-Request-ID") == "" {
				t.Fatalf("untrusted discovery metadata or absent request ID: %#v", configuration)
			}
			// Verify a real issued JWT with the public key from the existing endpoint,
			// independently of the signer's ParseModel implementation.
			jwks := httptest.NewRecorder()
			server.Handler().ServeHTTP(jwks, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
			var keys struct {
				Keys []map[string]string `json:"keys"`
			}
			if jwks.Code != http.StatusOK || json.Unmarshal(jwks.Body.Bytes(), &keys) != nil || len(keys.Keys) != 1 {
				t.Fatal("public JWKS unavailable")
			}
			if keys.Keys[0]["alg"] != "EdDSA" || keys.Keys[0]["d"] != "" {
				t.Fatal("unexpected signing algorithm or private key in JWKS")
			}
			public, err := base64.RawURLEncoding.DecodeString(keys.Keys[0]["x"])
			if err != nil || len(public) != ed25519.PublicKeySize {
				t.Fatal("invalid public key")
			}
			_, model, err := application.Tokens.IssueWithDeploymentSession("user", "deployment", "session", false, false, nil, []string{"model"})
			if err != nil {
				t.Fatal("cannot issue model token")
			}
			parts := strings.Split(model, ".")
			signature, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil || !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), signature) {
				t.Fatal("published public key cannot verify AEP model JWT")
			}
			metadata := httptest.NewRecorder()
			server.Handler().ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil))
			if metadata.Code != http.StatusOK || strings.Contains(metadata.Body.String(), "federated_auth") {
				t.Fatal("verification discovery enabled federated authentication")
			}
		})
	}
}

func TestJWTDiscoveryPreservesIssuerPrefixAndRejectsSensitiveURLs(t *testing.T) {
	for _, issuer := range []string{"https://issuer.example/control/", "https://user:private@issuer.example", "https://issuer.example?private=value"} {
		application, _, _ := testHTTPApplication(t)
		signer, err := auth.NewService(issuer, "", time.Minute, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		application.Tokens = signer
		response := httptest.NewRecorder()
		New(application).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
		if strings.HasSuffix(issuer, "/control/") {
			var configuration auth.VerifierConfiguration
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &configuration) != nil ||
				configuration.Issuer != issuer || configuration.JWKSURI != "https://issuer.example/control/.well-known/jwks.json" {
				t.Fatal("configured issuer prefix was not preserved")
			}
		} else if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/problem+json" ||
			!strings.Contains(response.Body.String(), "JWT_DISCOVERY_UNAVAILABLE") || strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "issuer.example") {
			t.Fatal("unpublishable issuer did not fail closed with a generic problem")
		}
	}
}
