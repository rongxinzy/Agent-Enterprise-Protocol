package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestEntitlementClaimsAndJWKS(t *testing.T) {
	service, err := NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	licenseExpiry := time.Now().UTC().Add(48 * time.Hour)
	raw, expiresAt, err := service.IssueEntitlement("user-1", "deployment-1", "license-1", "sha256:digest", []string{"enterprise.models"}, []string{"chat-a"}, &licenseExpiry)
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt.After(time.Now().UTC().Add(24*time.Hour+time.Second)) || !expiresAt.Before(licenseExpiry) {
		t.Fatalf("entitlement expiry was not bounded: %s", expiresAt)
	}
	claims, err := service.ParseEntitlement(raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.DeploymentID != "deployment-1" || claims.LicenseID != "license-1" || claims.LicenseDigest != "sha256:digest" || claims.TokenUse != "entitlement" || len(claims.Features) != 1 || claims.Features[0] != "enterprise.models" || len(claims.ModelScopes) != 1 || claims.ModelScopes[0] != "chat-a" {
		t.Fatalf("entitlement claims = %#v", claims)
	}
	keys := service.JWKS()
	entries, ok := keys["keys"].([]map[string]string)
	if !ok || len(entries) != 1 || entries[0]["alg"] != "EdDSA" || entries[0]["kid"] == "" || entries[0]["x"] == "" {
		t.Fatalf("JWKS = %#v", keys)
	}
}

func TestEntitlementValidationAndExpiry(t *testing.T) {
	service, err := NewService("https://issuer.example", "", time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		userID        string
		deploymentID  string
		licenseID     string
		digest        string
		licenseExpiry *time.Time
	}{
		{name: "missing deployment", userID: "user-1", licenseID: "license-1", digest: "digest"},
		{name: "missing license", userID: "user-1", deploymentID: "deployment-1", digest: "digest"},
		{name: "missing digest", userID: "user-1", deploymentID: "deployment-1", licenseID: "license-1"},
		{name: "expired license", userID: "user-1", deploymentID: "deployment-1", licenseID: "license-1", digest: "digest", licenseExpiry: func() *time.Time { value := time.Now().UTC().Add(-time.Minute); return &value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := service.IssueEntitlement(test.userID, test.deploymentID, test.licenseID, test.digest, nil, nil, test.licenseExpiry); err == nil {
				t.Fatal("IssueEntitlement accepted invalid input")
			}
		})
	}

	if _, err := service.ParseEntitlement("not-a-token"); err == nil {
		t.Fatal("ParseEntitlement accepted malformed token")
	}
}

func TestNewServiceRejectsInvalidSigningSeed(t *testing.T) {
	if _, err := NewService("https://issuer.example", "not-base64", time.Minute, time.Minute); err == nil {
		t.Fatal("NewService accepted malformed signing seed")
	}
	shortSeed := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 31)))
	if _, err := NewService("https://issuer.example", shortSeed, time.Minute, time.Minute); err == nil {
		t.Fatal("NewService accepted short signing seed")
	}
}
