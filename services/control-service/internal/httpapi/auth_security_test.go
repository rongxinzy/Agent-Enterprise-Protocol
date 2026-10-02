package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
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

func TestParseRemoteAddressShapes(t *testing.T) {
	if address, ok := parseRemoteAddress("10.0.0.1:5678"); !ok || address.String() != "10.0.0.1" {
		t.Fatalf("addr:port = %v, %v", address, ok)
	}
	if address, ok := parseRemoteAddress("2001:db8::1"); !ok || address.String() != "2001:db8::1" {
		t.Fatalf("bare v6 = %v, %v", address, ok)
	}
	// IPv4-mapped v6 needs brackets as an addr:port literal; the bare form
	// parses as an address and unmaps to plain v4.
	if address, ok := parseRemoteAddress("[::ffff:10.0.0.2]:80"); !ok || address.String() != "10.0.0.2" {
		t.Fatalf("mapped v6 = %v, %v", address, ok)
	}
	if _, ok := parseRemoteAddress("not-an-address"); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestBoundedAuditID(t *testing.T) {
	if boundedAuditID("short") != "short" {
		t.Fatal("short ids pass through unchanged")
	}
	long := strings.Repeat("x", 300)
	hashed := boundedAuditID(long)
	if !strings.HasPrefix(hashed, "sha256:") || len(hashed) >= len(long) {
		t.Fatalf("long id must collapse to a hash, got %d chars", len(hashed))
	}
}

func TestRequiredAdminPermission(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
	}{
		{"GET", "/aep/v1/admin/roles", "roles.read"},
		{"POST", "/aep/v1/admin/roles", "roles.write"},
		{"GET", "/aep/v1/admin/permissions", "roles.read"},
		{"GET", "/aep/v1/admin/teams", "teams.read"},
		{"DELETE", "/aep/v1/admin/teams/x", "teams.write"},
		{"PUT", "/aep/v1/admin/users/u/rbac", "users.write"},
		{"GET", "/aep/v1/admin/users", "users.read"},
		{"GET", "/aep/v1/admin/sessions/u/revoke", "sessions.write"},
		{"GET", "/aep/v1/admin/sessions", "users.read"},
		{"POST", "/aep/v1/admin/models/x/assignment", "models.assign"},
		{"GET", "/aep/v1/admin/models", "models.read"},
		{"PUT", "/aep/v1/admin/models", "models.write"},
		{"POST", "/aep/v1/admin/skills/x/assignment", "skills.assign"},
		{"GET", "/aep/v1/admin/skills", "skills.read"},
		{"DELETE", "/aep/v1/admin/skills/s", "skills.write"},
		{"POST", "/aep/v1/admin/credentials", "credentials.write"},
		{"GET", "/aep/v1/admin/credentials", "credentials.read"},
		{"PUT", "/aep/v1/admin/credentials/c/rotate", "credentials.write"},
		{"GET", "/aep/v1/admin/agents", "users.read"},
		{"GET", "/aep/v1/admin/identity-sources", "identity.read"},
		{"GET", "/aep/v1/admin/events", "events.read"},
		{"GET", "/aep/v1/admin/nothing", ""},
	}
	for _, tc := range cases {
		got := requiredAdminPermission(tc.method, tc.path)
		if len(got) == 0 && tc.want != "" {
			t.Fatalf("%s %s: expected %q, got none", tc.method, tc.path, tc.want)
		}
		if len(got) > 0 && got[0] != tc.want {
			t.Fatalf("%s %s: expected %q, got %q", tc.method, tc.path, tc.want, got[0])
		}
	}
}

func TestReadinessProbes(t *testing.T) {
	t.Run("missing dependencies report unavailable", func(t *testing.T) {
		application, _, _ := newStoreBackedHTTPApplication(t)
		handler := New(application).Handler()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("readyz = %d", response.Code)
		}
	})
}
