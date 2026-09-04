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
		"limits": map[string]any{"users": json.Number("10"), "activations": json.Number("5")}, "features": []any{"enterprise.models"},
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
	payload := map[string]any{"licenseId": "lic-1", "customerId": "customer-1", "deploymentId": "deployment-2", "edition": "enterprise", "issuedAt": "2026-01-01T00:00:00.000Z", "expiresAt": "2027-01-01T00:00:00.000Z", "graceDays": json.Number("0"), "limits": map[string]any{"users": json.Number("1"), "activations": json.Number("1")}, "features": []any{}}
	canonical, _ := canonicalizeValue(payload)
	envelope := map[string]any{"format": formatV1, "keyId": "key-1", "payload": payload, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(canonical)))}
	raw, _ := json.Marshal(envelope)
	verifier, _ := NewVerifier(map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(public)}, "deployment-1")
	if _, err := verifier.Verify(raw); err == nil {
		t.Fatal("deployment mismatch was accepted")
	}
}

func TestVerifyLicenseLifecycleBoundaries(t *testing.T) {
	issued := "2026-01-01T00:00:00.000Z"
	expires := "2026-02-01T00:00:00.000Z"
	graceEnds := time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		now       time.Time
		expected  string
		wantError string
	}{
		{name: "before issued", now: time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC), wantError: "license is not yet valid"},
		{name: "at expiry", now: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), expected: "enterprise-active"},
		{name: "within grace", now: time.Date(2026, 2, 3, 12, 0, 0, 0, time.UTC), expected: "enterprise-grace"},
		{name: "at grace end", now: graceEnds, expected: "enterprise-grace"},
		{name: "after grace", now: graceEnds.Add(time.Millisecond), expected: "enterprise-expired"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, public := signedLicense(t, map[string]any{
				"issuedAt": issued, "expiresAt": expires, "graceDays": json.Number("7"),
			})
			verifier, err := NewVerifier(map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(public)}, "deployment-1")
			if err != nil {
				t.Fatal(err)
			}
			verifier.Now = func() time.Time { return test.now }
			result, err := verifier.Verify(raw)
			if test.wantError != "" {
				if err == nil || err.Error() != test.wantError {
					t.Fatalf("expected %q, got %v", test.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.expected || !result.GraceEndsAt.Equal(graceEnds) {
				t.Fatalf("unexpected lifecycle result: status=%q graceEndsAt=%s", result.Status, result.GraceEndsAt)
			}
		})
	}
}

func TestVerifyRejectsNotBeforeAfterIssuedAt(t *testing.T) {
	raw, public := signedLicense(t, map[string]any{
		"issuedAt": "2026-01-01T00:00:00.000Z", "notBefore": "2026-01-15T00:00:00.000Z",
		"expiresAt": "2027-01-01T00:00:00.000Z", "graceDays": json.Number("0"),
	})
	verifier, err := NewVerifier(map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(public)}, "deployment-1")
	if err != nil {
		t.Fatal(err)
	}
	verifier.Now = func() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) }
	if _, err := verifier.Verify(raw); err == nil || err.Error() != "license is not yet valid" {
		t.Fatalf("expected not-before rejection, got %v", err)
	}
}

func signedLicense(t *testing.T, overrides map[string]any) ([]byte, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"licenseId": "lic-lifecycle", "customerId": "customer-1", "deploymentId": "deployment-1", "edition": "enterprise",
		"issuedAt": "2026-01-01T00:00:00.000Z", "expiresAt": "2027-01-01T00:00:00.000Z", "graceDays": json.Number("0"),
		"limits": map[string]any{"users": json.Number("10"), "activations": json.Number("5")}, "features": []any{"enterprise.models"},
	}
	for key, value := range overrides {
		payload[key] = value
	}
	canonical, err := canonicalizeValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{
		"format": formatV1, "keyId": "key-1", "payload": payload,
		"signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(canonical))),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw, public
}
