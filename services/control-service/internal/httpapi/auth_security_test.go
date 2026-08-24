package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	fingerprint := (&Server{}).loginFingerprint(request, "enterprise-a", "alice")
	if fingerprint.KeyHash == fingerprint.PrincipalHash || fingerprint.SourceHash == "192.0.2.10" || fingerprint.PrincipalHash == "alice" {
		t.Fatalf("fingerprint exposed or reused an input: %#v", fingerprint)
	}
	if fingerprint != (&Server{}).loginFingerprint(request, "enterprise-a", "alice") {
		t.Fatal("fingerprint is not stable")
	}
	request.RemoteAddr = "198.51.100.20:43000"
	otherSource := (&Server{}).loginFingerprint(request, "enterprise-a", "alice")
	if otherSource.KeyHash != fingerprint.KeyHash || otherSource.SourceHash == fingerprint.SourceHash {
		t.Fatal("principal backoff did not span sources or source audit hash was reused")
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
		{http.MethodGet, "/aep/v1/agent/me", true},
		{http.MethodGet, "/aep/v1/agent/models", false},
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
