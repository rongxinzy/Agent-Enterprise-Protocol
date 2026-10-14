package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestTrustedRequestCorrelation(t *testing.T) {
	for _, id := range []string{"", "client.test:123_a-b", "invalid id", strings.Repeat("x", 129)} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("id=%q/stream=%t", id, stream), func(t *testing.T) {
				var trusted string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					trusted = r.Header.Get("X-AEP-Request-ID")
					if !validRequestID(trusted) || trusted == "spoofed" || trusted != r.Header.Get("X-Request-ID") || len(r.Header.Values("X-AEP-Request-ID")) != 1 {
						t.Errorf("invalid correlation metadata: %#v", r.Header)
					}
					w.Header().Add("X-Request-ID", "gateway-rewritten")
					w.Header().Add("X-Request-ID", "provider-rewritten")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: ok\n\n")
						w.(http.Flusher).Flush()
					} else {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"error":"upstream unavailable"}`)
					}
				}))
				defer upstream.Close()
				h, err := NewHandler(Config{UpstreamURL: upstream.URL, RequestLimit: 1024}, verifierStub{claims: &ModelClaims{
					DeploymentID: "demo", ModelScopes: []string{"model-a"}, RegisteredClaims: jwt.RegisteredClaims{Subject: "user-a"},
				}})
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a"}`))
				r.Header.Set("Authorization", "Bearer disposable-test-token")
				r.Header.Set("X-Request-ID", id)
				r.Header.Add("X-AEP-Request-ID", "spoofed")
				r.Header.Add("X-AEP-Request-ID", "duplicate")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Header().Get("X-Request-ID") != trusted || len(w.Header().Values("X-Request-ID")) != 1 || requestID(r) != trusted {
					t.Fatalf("response/log ID disagrees with trusted ID: %q / %q", w.Header().Values("X-Request-ID"), trusted)
				}
				if validRequestID(id) && trusted != id {
					t.Fatalf("valid client ID changed: %q", trusted)
				}
				if stream && (w.Body.String() != "data: ok\n\n" || w.Code != http.StatusOK) {
					t.Fatal("streaming response changed")
				}
				if !stream && w.Code != http.StatusBadGateway {
					t.Fatal("upstream status changed")
				}
			})
		}
	}
}

func TestRejectedRequestKeepsCorrelationID(t *testing.T) {
	h, err := NewHandler(Config{UpstreamURL: "http://example.test", RequestLimit: 1024}, verifierStub{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a"}`))
	r.Header.Set("X-Request-ID", "rejected-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get("X-Request-ID") != "rejected-request" || !strings.Contains(w.Body.String(), `"requestId":"rejected-request"`) {
		t.Fatalf("rejection correlation changed: %d %s", w.Code, w.Body.String())
	}
}
