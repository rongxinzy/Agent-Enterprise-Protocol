package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
)

func TestActivateLicenseIssuesBoundEntitlement(t *testing.T) {
	tokens, err := auth.NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := tokens.Issue("user-1", "tenant-1", "agent-1", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	body, _ := json.Marshal(map[string]any{
		"licenseId": "lic-1", "licenseDigest": "sha256:" + strings.Repeat("a", 64),
		"deploymentId": "deployment-1", "expiresAt": expiresAt.Format(time.RFC3339),
		"features": []string{"enterprise.models", "enterprise.models"},
	})
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/agent/activation", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	response := httptest.NewRecorder()
	New(&app.App{Tokens: tokens}).Handler().ServeHTTP(response, request)
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
	if claims.Subject != "user-1" || claims.Tenant != "tenant-1" || claims.AgentID != "agent-1" || claims.DeploymentID != "deployment-1" || claims.LicenseID != "lic-1" {
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
	access, _, err := tokens.Issue("user-1", "tenant-1", "agent-1", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"licenseId":"lic-1","licenseDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deploymentId":"deployment-1","expiresAt":"2020-01-01T00:00:00Z","features":[]}`)
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/agent/activation", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	response := httptest.NewRecorder()
	New(&app.App{Tokens: tokens}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expired activation status = %d, body = %s", response.Code, response.Body.String())
	}
}
