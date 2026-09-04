package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
)

func TestActivateLicenseIssuesBoundEntitlement(t *testing.T) {
	tokens, err := auth.NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := tokens.IssueWithDeploymentSession("user-1", "deployment-1", "session-1", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	envelope, publicKey := signedLicense(t, expiresAt)
	verifier, err := license.NewVerifier(map[string]string{"license-prod-1": publicKey}, "deployment-1")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(envelope)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"license": json.RawMessage(envelope)})
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/user/activation", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	response := httptest.NewRecorder()
	New(&app.App{Tokens: tokens, LicenseVerifier: verifier, License: &verified}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("activation status = %d, body = %s", response.Code, response.Body.String())
	}
	var document struct {
		EntitlementToken string   `json:"entitlementToken"`
		Features         []string `json:"features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	claims, err := tokens.ParseEntitlement(document.EntitlementToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.DeploymentID != "deployment-1" || claims.SessionID != "" || claims.LicenseID != "lic-1" {
		t.Fatalf("entitlement identity binding = %#v", claims)
	}
	if len(document.Features) != 1 || document.Features[0] != "enterprise.models" {
		t.Fatalf("normalized features = %#v", document.Features)
	}
}

func TestActivateLicenseRejectsExpiredEvidence(t *testing.T) {
	tokens, err := auth.NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := tokens.IssueWithDeploymentSession("user-1", "deployment-1", "session-1", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope, publicKey := signedLicense(t, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	verifier, err := license.NewVerifier(map[string]string{"license-prod-1": publicKey}, "deployment-1")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(envelope)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"license": json.RawMessage(envelope)})
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/user/activation", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	response := httptest.NewRecorder()
	New(&app.App{Tokens: tokens, LicenseVerifier: verifier, License: &verified}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expired activation status = %d, body = %s", response.Code, response.Body.String())
	}
}

func signedLicense(t *testing.T, expiresAt time.Time) ([]byte, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	issued := "2026-01-01T00:00:00.000Z"
	if expiresAt.Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		issued = "2019-01-01T00:00:00.000Z"
	}
	expires := expiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
	payload := []byte(`{"customerId":"customer-1","deploymentId":"deployment-1","edition":"enterprise","expiresAt":"` + expires + `","features":["enterprise.models"],"graceDays":0,"issuedAt":"` + issued + `","licenseId":"lic-1","limits":{"activations":10,"users":10}}`)
	signature := ed25519.Sign(private, payload)
	envelope := []byte(`{"format":"zhiyuan-license-v1","keyId":"license-prod-1","payload":` + string(payload) + `,"signature":"` + base64.RawURLEncoding.EncodeToString(signature) + `"}`)
	return envelope, base64.RawURLEncoding.EncodeToString(public)
}
