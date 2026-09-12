package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
)

func TestLoginBackoffProgressesAndCaps(t *testing.T) {
	base := 30 * time.Second
	maximum := 2 * time.Minute
	tests := []struct {
		failures int
		want     time.Duration
	}{{4, 0}, {5, base}, {6, time.Minute}, {7, maximum}, {20, maximum}}
	for _, test := range tests {
		if got := loginBackoff(test.failures, 5, base, maximum); got != test.want {
			t.Fatalf("loginBackoff(%d) = %s, want %s", test.failures, got, test.want)
		}
	}
}

func TestLoginFingerprintDoesNotExposeInputs(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/auth/password/login", nil)
	request.RemoteAddr = "192.0.2.10:42000"
	server := &Server{app: &app.App{Config: config.Config{}}}
	fingerprint := server.loginFingerprint(request, "enterprise-a", "Alice")
	if fingerprint.PrincipalSourceKeyHash == fingerprint.PrincipalHash || fingerprint.SourceKeyHash == fingerprint.SourceHash || fingerprint.SourceHash == "192.0.2.10" || fingerprint.PrincipalHash == "alice" {
		t.Fatalf("fingerprint exposed or reused an input: %#v", fingerprint)
	}
	if fingerprint != server.loginFingerprint(request, "enterprise-a", " alice ") {
		t.Fatal("fingerprint is not stable")
	}
	request.RemoteAddr = "198.51.100.20:43000"
	otherSource := server.loginFingerprint(request, "enterprise-a", "alice")
	if otherSource.PrincipalSourceKeyHash == fingerprint.PrincipalSourceKeyHash || otherSource.SourceKeyHash == fingerprint.SourceKeyHash || otherSource.SourceHash == fingerprint.SourceHash {
		t.Fatal("source-specific login keys were reused across sources")
	}
	request.RemoteAddr = "192.0.2.10:44000"
	otherPrincipal := server.loginFingerprint(request, "enterprise-a", "bob")
	if otherPrincipal.PrincipalSourceKeyHash == fingerprint.PrincipalSourceKeyHash || otherPrincipal.SourceKeyHash != fingerprint.SourceKeyHash {
		t.Fatal("source and principal-source login keys are not independently scoped")
	}
}

func TestLoginSourceTrustsOnlyConfiguredProxyChains(t *testing.T) {
	server := &Server{app: &app.App{Config: config.Config{TrustedProxyCIDRs: []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8:1::/48"),
	}}}}

	untrusted := httptest.NewRequest(http.MethodPost, "/aep/v1/auth/password/login", nil)
	untrusted.RemoteAddr = "192.0.2.20:44000"
	untrusted.Header.Set("X-Forwarded-For", "198.51.100.7")
	if source := server.loginSource(untrusted); source != "192.0.2.20" {
		t.Fatalf("untrusted proxy source = %q", source)
	}

	trusted := httptest.NewRequest(http.MethodPost, "/aep/v1/auth/password/login", nil)
	trusted.RemoteAddr = "10.0.0.8:443"
	trusted.Header.Set("X-Forwarded-For", "198.51.100.7, 2001:db8:1::4, 10.0.0.9")
	if source := server.loginSource(trusted); source != "198.51.100.7" {
		t.Fatalf("trusted proxy source = %q", source)
	}

	trusted.Header.Set("X-Forwarded-For", "198.51.100.7, invalid")
	if source := server.loginSource(trusted); source != "10.0.0.8" {
		t.Fatalf("invalid forwarded chain source = %q", source)
	}
}

func TestPasswordChangeRouteAllowlist(t *testing.T) {
	tests := []struct {
		method  string
		path    string
		allowed bool
	}{
		{http.MethodPost, "/aep/v1/auth/password/change", true},
		{http.MethodPost, "/aep/v1/auth/logout", true},
		{http.MethodGet, "/aep/v1/user/me", true},
		{http.MethodGet, "/aep/v1/user/models", false},
		{http.MethodPost, "/aep/v1/admin/users", false},
		{http.MethodGet, "/aep/v1/auth/password/change", false},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := passwordChangeRouteAllowed(request); got != test.allowed {
			t.Fatalf("%s %s allowed = %v", test.method, test.path, got)
		}
	}
}

func TestRetryAfterRoundsUp(t *testing.T) {
	if retryAfterSeconds(time.Millisecond) != 1 || retryAfterSeconds(1500*time.Millisecond) != 2 {
		t.Fatal("Retry-After did not round up to whole seconds")
	}
}
