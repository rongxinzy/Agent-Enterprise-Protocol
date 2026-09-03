package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifierAcceptsAEPModelTokenAndCachesJWKS(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(response).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "kid": "test-key", "use": "sig", "alg": "EdDSA", "crv": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()

	verifier := NewVerifier(server.URL, "https://control.example.test", time.Hour, time.Second)
	raw := signModelToken(t, private, "test-key", ModelClaims{
		Tenant: "enterprise-a", AgentID: "agent-a", ModelScopes: []string{"model-a"}, TokenUse: "model",
		RegisteredClaims: validRegisteredClaims("https://control.example.test", "model-gateway"),
	})
	for range 2 {
		claims, err := verifier.Verify(context.Background(), raw)
		if err != nil {
			t.Fatalf("verify token: %v", err)
		}
		if claims.Subject != "user-a" || claims.ModelScopes[0] != "model-a" {
			t.Fatalf("unexpected claims: %#v", claims)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one JWKS request, got %d", requests.Load())
	}
}

func TestVerifierAcceptsDeploymentEntitlementToken(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "kid": "test-key", "use": "sig", "alg": "EdDSA", "crv": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()
	verifier := NewVerifier(server.URL, "https://control.example.test", time.Hour, time.Second)
	raw := signModelToken(t, private, "test-key", ModelClaims{
		Tenant: "enterprise-a", AgentID: "agent-a", LicenseID: "license-a", LicenseDigest: "sha256:abc", DeploymentID: "deployment-a",
		ModelScopes: []string{"model-a"}, TokenUse: "entitlement",
		RegisteredClaims: validRegisteredClaims("https://control.example.test", "aep-entitlement"),
	})
	claims, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify entitlement token: %v", err)
	}
	if claims.TokenUse != "entitlement" || claims.LicenseID != "license-a" || !contains(claims.ModelScopes, "model-a") {
		t.Fatalf("unexpected entitlement claims: %#v", claims)
	}
}

func TestVerifierChecksAndCachesDeploymentLicenseStatus(t *testing.T) {
	var calls atomic.Int32
	status := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("X-AEP-Gateway-Token") != "gateway-secret" || request.Header.Get("X-AEP-Tenant-ID") != "enterprise-a" {
			t.Fatalf("missing internal status headers: %v", request.Header)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"active": true, "digest": "sha256:digest", "deploymentId": "deployment-a"})
	}))
	defer status.Close()
	verifier := NewVerifier("http://unused.example/jwks", "issuer", time.Hour, time.Second)
	verifier.ConfigureLicenseStatus(status.URL, "gateway-secret", time.Minute)
	claims := &ModelClaims{Tenant: "enterprise-a", LicenseID: "license-a", LicenseDigest: "sha256:digest", DeploymentID: "deployment-a"}
	if err := verifier.CheckEntitlement(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
	if err := verifier.CheckEntitlement(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("license status endpoint called %d times, want cached once", calls.Load())
	}
}

func TestVerifierRefreshesFreshJWKSForUnknownKID(t *testing.T) {
	firstPublic, firstPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, secondPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		request := requests.Add(1)
		kid, public := "first-key", firstPublic
		if request > 1 {
			kid, public = "second-key", secondPublic
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "kid": kid, "use": "sig", "alg": "EdDSA", "crv": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()

	verifier := NewVerifier(server.URL, "https://control.example.test", time.Hour, time.Second)
	first := signModelToken(t, firstPrivate, "first-key", ModelClaims{
		Tenant: "enterprise-a", AgentID: "agent-a", ModelScopes: []string{"model-a"}, TokenUse: "model",
		RegisteredClaims: validRegisteredClaims("https://control.example.test", "model-gateway"),
	})
	if _, err := verifier.Verify(context.Background(), first); err != nil {
		t.Fatalf("verify first token: %v", err)
	}

	second := signModelToken(t, secondPrivate, "second-key", ModelClaims{
		Tenant: "enterprise-a", AgentID: "agent-a", ModelScopes: []string{"model-a"}, TokenUse: "model",
		RegisteredClaims: validRegisteredClaims("https://control.example.test", "model-gateway"),
	})
	if _, err := verifier.Verify(context.Background(), second); err != nil {
		t.Fatalf("verify rotated token: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("expected an immediate JWKS refresh for the rotated kid, got %d requests", requests.Load())
	}
}

func TestVerifierReadinessRefreshDoesNotBlockCachedVerification(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	release := func() {
		select {
		case <-releaseRefresh:
		default:
			close(releaseRefresh)
		}
	}
	defer release()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) > 1 {
			close(refreshStarted)
			<-releaseRefresh
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "kid": "test-key", "use": "sig", "alg": "EdDSA", "crv": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()

	verifier := NewVerifier(server.URL, "https://control.example.test", time.Hour, time.Second)
	raw := signModelToken(t, private, "test-key", ModelClaims{
		Tenant: "enterprise-a", AgentID: "agent-a", TokenUse: "model",
		RegisteredClaims: validRegisteredClaims("https://control.example.test", "model-gateway"),
	})
	if _, err := verifier.Verify(context.Background(), raw); err != nil {
		t.Fatalf("prime verifier: %v", err)
	}
	readyResult := make(chan error, 1)
	go func() { readyResult <- verifier.Ready(context.Background()) }()
	<-refreshStarted

	verificationResult := make(chan error, 1)
	go func() {
		_, err := verifier.Verify(context.Background(), raw)
		verificationResult <- err
	}()
	select {
	case err := <-verificationResult:
		if err != nil {
			t.Fatalf("verify cached token: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cached token verification blocked on readiness refresh")
	}
	release()
	if err := <-readyResult; err != nil {
		t.Fatalf("readiness refresh: %v", err)
	}
}

func TestVerifierRejectsInvalidModelClaims(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "kid": "test-key", "use": "sig", "alg": "EdDSA", "crv": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()
	verifier := NewVerifier(server.URL, "https://control.example.test", time.Hour, time.Second)

	tests := []struct {
		name   string
		claims ModelClaims
	}{
		{name: "wrong audience", claims: ModelClaims{Tenant: "enterprise-a", AgentID: "agent-a", TokenUse: "model", RegisteredClaims: validRegisteredClaims("https://control.example.test", "aep-control")}},
		{name: "wrong token use", claims: ModelClaims{Tenant: "enterprise-a", AgentID: "agent-a", TokenUse: "aep", RegisteredClaims: validRegisteredClaims("https://control.example.test", "model-gateway")}},
		{name: "wrong issuer", claims: ModelClaims{Tenant: "enterprise-a", AgentID: "agent-a", TokenUse: "model", RegisteredClaims: validRegisteredClaims("https://other.example.test", "model-gateway")}},
		{name: "missing identity", claims: ModelClaims{TokenUse: "model", RegisteredClaims: validRegisteredClaims("https://control.example.test", "model-gateway")}},
		{name: "expired", claims: ModelClaims{Tenant: "enterprise-a", AgentID: "agent-a", TokenUse: "model", RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "https://control.example.test", Subject: "user-a", Audience: jwt.ClaimStrings{"model-gateway"},
			IssuedAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)), ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := signModelToken(t, private, "test-key", test.claims)
			if _, err := verifier.Verify(context.Background(), raw); err == nil {
				t.Fatal("expected token rejection")
			}
		})
	}
}

func validRegisteredClaims(issuer, audience string) jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Issuer: issuer, Subject: "user-a", Audience: jwt.ClaimStrings{audience},
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

func signModelToken(t *testing.T, key ed25519.PrivateKey, kid string, claims ModelClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = kid
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
