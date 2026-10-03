package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

// The split constructors must partition the surface exactly: the agent
// control API serves auth and the user runtime but never the admin API nor
// the internal data-plane endpoints; the enterprise API serves admin and
// internal but never the user runtime. Everything shares jwks, metadata,
// and health.
func TestSurfaceSplitConstructors(t *testing.T) {
	t.Parallel()
	application := &app.App{}

	agent := NewAgentControlAPI(application).Handler()
	enterprise := NewEnterpriseAPI(application).Handler()

	probe := func(handler http.Handler, method, path string) int {
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("X-AEP-Protocol-Version", "1.0")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}

	// Discovery and health are shared (metadata is unauthenticated; the
	// authenticated surfaces answer 401 rather than 404).
	for _, surface := range []http.Handler{agent, enterprise} {
		if code := probe(surface, http.MethodGet, "/aep/v1/metadata"); code != http.StatusOK {
			t.Fatalf("metadata on split surface = %d", code)
		}
		if code := probe(surface, http.MethodGet, "/livez"); code != http.StatusOK {
			t.Fatalf("livez on split surface = %d", code)
		}
	}

	// Agent surface: user runtime authenticated, admin absent. (Auth-methods
	// reads the database, so its presence is asserted through the login
	// route answering 400-for-empty-body rather than 404.)
	if code := probe(agent, http.MethodPost, "/aep/v1/auth/password/login"); code != http.StatusBadRequest {
		t.Fatalf("login route on agent surface = %d", code)
	}
	if code := probe(agent, http.MethodPost, "/aep/v1/user/heartbeat"); code != http.StatusUnauthorized {
		t.Fatalf("heartbeat on agent surface = %d", code)
	}
	for _, path := range []string{"/aep/v1/admin/models", "/aep/v1/admin/roles", "/internal/data-plane/desired-state"} {
		if code := probe(agent, http.MethodGet, path); code != http.StatusNotFound {
			t.Fatalf("enterprise path %s leaked onto the agent surface: %d", path, code)
		}
	}

	// Enterprise surface: admin authenticated, internal present, user absent.
	if code := probe(enterprise, http.MethodGet, "/aep/v1/admin/models"); code != http.StatusUnauthorized {
		t.Fatalf("admin on enterprise surface = %d", code)
	}
	if code := probe(enterprise, http.MethodGet, "/internal/data-plane/desired-state"); code != http.StatusUnauthorized {
		t.Fatalf("internal data-plane on enterprise surface = %d", code)
	}
	for _, path := range []string{"/aep/v1/user/heartbeat", "/aep/v1/user/control-events", "/aep/v1/user/skills/manifest"} {
		if code := probe(enterprise, http.MethodGet, path); code != http.StatusNotFound {
			t.Fatalf("agent path %s leaked onto the enterprise surface: %d", path, code)
		}
	}
}

// Metadata advertises agentControl only when an endpoint is configured —
// the all-in-one deployment stays silent so clients keep the single base.
func TestMetadataAdvertisesAgentControlWhenConfigured(t *testing.T) {
	t.Parallel()
	application := &app.App{}
	probe := func() string {
		response := httptest.NewRecorder()
		New(application).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil))
		if body := response.Body.String(); !strings.Contains(body, "agentControl") {
			return ""
		} else {
			return body
		}
	}
	if body := probe(); body != "" {
		t.Fatalf("all-in-one metadata advertised agentControl: %s", body)
	}
	application.Config.AgentControlBaseURL = "https://agents.example.com"
	if body := probe(); !strings.Contains(body, `"agentControl":{"baseUrl":"https://agents.example.com"}`) {
		t.Fatalf("split metadata did not advertise agentControl: %s", body)
	}
}
