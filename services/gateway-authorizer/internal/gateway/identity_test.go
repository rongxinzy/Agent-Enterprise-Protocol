package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

func TestCallTimeIdentityAndConsumer(t *testing.T) {
	team, foreign := "team-a", "foreign-team"
	teamHeader := gatewaypolicy.Header(gatewaypolicy.Configuration{ScopeType: "team", ScopeID: &team})
	foreignHeader := gatewaypolicy.Header(gatewaypolicy.Configuration{ScopeType: "team", ScopeID: &foreign})
	calls := 0
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-AEP-Gateway-Token") != "service-token" || r.Header.Get("X-AEP-User-ID") != "user-a" || r.Header.Get("X-AEP-Session-ID") != "session-a" || r.Header.Get("X-AEP-Model-ID") != "model-a" {
			t.Error("untrusted lookup context")
		}
		_, _ = fmt.Fprintf(w, `{"deploymentId":"deployment-a","userId":"user-a","consumer":%q,"teamIds":["team-b","team-a","team-a"],"roleIds":["role-a"],"observedAt":%q}`, gatewaysource.Consumer("deployment-a", "user-a"), time.Now().UTC().Format(time.RFC3339Nano))
	}))
	defer identity.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(teamHeader) != "1" || r.Header.Get(foreignHeader) != "" {
			t.Error("native policy subject presence was not trusted")
		}
		if r.Header.Get("X-Mse-Consumer") != gatewaysource.Consumer("deployment-a", "user-a") || r.Header.Get("X-AEP-Team-IDs") != "|dGVhbS1h|dGVhbS1i|" || r.Header.Get("X-AEP-Role-IDs") != "|cm9sZS1h|" {
			t.Errorf("trusted metadata: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: ok\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	claims := &ModelClaims{DeploymentID: "deployment-a", SessionID: "session-a", ModelScopes: []string{"model-a"}, RegisteredClaims: jwt.RegisteredClaims{Subject: "user-a"}}
	h, err := NewHandler(Config{UpstreamURL: upstream.URL, RequestLimit: 1024, IdentityURL: identity.URL, LicenseStatusToken: "service-token"}, verifierStub{claims: claims})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a","stream":true}`))
		r.Header.Set("Authorization", "Bearer model-token")
		r.Header.Set("X-Mse-Consumer", "quota-admin")
		r.Header.Set("X-AEP-Team-IDs", "forged")
		r.Header.Set(teamHeader, "forged")
		r.Header.Set(foreignHeader, "1")
		r.Header.Set("Connection", "X-Mse-Consumer, X-AEP-Team-IDs")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !w.Flushed {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatal("membership lookup cached")
	}
}

func TestIdentityFailuresFailClosed(t *testing.T) {
	claims := &ModelClaims{DeploymentID: "a", SessionID: "s", RegisteredClaims: jwt.RegisteredClaims{Subject: "u"}}
	for _, response := range []struct {
		status int
		body   string
	}{{403, `{}`}, {500, `{"secret":"hidden"}`}, {200, `not json`}, {200, `{"deploymentId":"foreign"}`}, {200, strings.Repeat(" ", 33<<10)}} {
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(response.status)
			_, _ = fmt.Fprint(w, response.body)
		}))
		h := &Handler{identityURL: source.URL, identityToken: "token"}
		if _, err := h.identity(context.Background(), claims, "m"); err == nil {
			t.Fatal("invalid identity accepted")
		}
		source.Close()
	}
	if _, err := (&Handler{identityURL: "http://127.0.0.1:1"}).identity(context.Background(), claims, "m"); err == nil {
		t.Fatal("outage accepted")
	}
	if _, err := membershipHeader([]string{""}); err == nil {
		t.Fatal("empty id accepted")
	}
	if _, err := membershipHeader([]string{strings.Repeat("x", 257)}); err == nil {
		t.Fatal("long id accepted")
	}
	if value, err := membershipHeader(nil); value != "" || err != nil {
		t.Fatal(value, err)
	}
}

func TestIdentityFailureStopsInference(t *testing.T) {
	for _, status := range []int{403, 500} {
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("failed identity forwarded to provider") }))
		h, err := NewHandler(Config{UpstreamURL: upstream.URL, RequestLimit: 1024, IdentityURL: source.URL, LicenseStatusToken: "service-token"}, verifierStub{claims: &ModelClaims{DeploymentID: "a", SessionID: "s", ModelScopes: []string{"m"}, RegisteredClaims: jwt.RegisteredClaims{Subject: "u"}}})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 503
		if status == 403 {
			want = 403
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		source.Close()
		upstream.Close()
	}
}
