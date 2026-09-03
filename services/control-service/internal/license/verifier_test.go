package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestVerifySignedEnvelopeAndDigest(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"licenseId": "lic-1", "customerId": "customer-1", "deploymentId": "deployment-1", "edition": "enterprise",
		"issuedAt": "2026-01-01T00:00:00.000Z", "expiresAt": "2027-01-01T00:00:00.000Z", "graceDays": json.Number("7"),
		"limits": map[string]any{"users": json.Number("10"), "agents": json.Number("5")}, "features": []any{"enterprise.models"},
	}
	canonical, err := canonicalizeValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{"format": formatV1, "keyId": "key-1", "payload": payload, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(canonical)))}
	raw, _ := json.Marshal(envelope)
	verifier, err := NewVerifier(map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(public)}, "deployment-1")
	if err != nil {
		t.Fatal(err)
	}
	verifier.Now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
	result, err := verifier.Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "enterprise-active" || result.Claims.LicenseID != "lic-1" || result.Digest == "" {
		t.Fatalf("unexpected verification result: %#v", result)
	}

	envelope["payload"].(map[string]any)["features"] = []any{"enterprise.admin"}
	tampered, _ := json.Marshal(envelope)
	if _, err := verifier.Verify(tampered); err == nil {
		t.Fatal("tampered license was accepted")
	}
}

func TestVerifyRejectsDeploymentMismatch(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	payload := map[string]any{"licenseId": "lic-1", "customerId": "customer-1", "deploymentId": "deployment-2", "edition": "enterprise", "issuedAt": "2026-01-01T00:00:00.000Z", "expiresAt": "2027-01-01T00:00:00.000Z", "graceDays": json.Number("0"), "limits": map[string]any{"users": json.Number("1"), "agents": json.Number("1")}, "features": []any{}}
	canonical, _ := canonicalizeValue(payload)
	envelope := map[string]any{"format": formatV1, "keyId": "key-1", "payload": payload, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(canonical)))}
	raw, _ := json.Marshal(envelope)
	verifier, _ := NewVerifier(map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(public)}, "deployment-1")
	if _, err := verifier.Verify(raw); err == nil {
		t.Fatal("deployment mismatch was accepted")
	}
}
