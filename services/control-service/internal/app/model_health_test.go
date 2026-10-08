package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

func TestProbeModelClassifiesOpenAICatalog(t *testing.T) {
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.8-flash-next","aliases":["GSQ-RCO-IQ3_S"]}]}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "Qwen3.8-Flash-Next", "secret-key")
	if outcome.Status != ModelHealthHealthy {
		t.Fatalf("case-insensitive catalog match should be healthy: %#v", outcome)
	}
	if sawAuth != "Bearer secret-key" {
		t.Fatalf("probe must send the stored credential: %q", sawAuth)
	}

	outcome = probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "GSQ-RCO-IQ3_S", "")
	if outcome.Status != ModelHealthHealthy {
		t.Fatalf("alias match should be healthy: %#v", outcome)
	}

	outcome = probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "bench-glm", "")
	if outcome.Status != ModelHealthModelMissing || !strings.Contains(outcome.Detail, "bench-glm") {
		t.Fatalf("missing catalog entry should classify model_missing: %#v", outcome)
	}
}

func TestProbeModelClassifiesOpenAIAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"missing or wrong API key"}}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "stale-key")
	if outcome.Status != ModelHealthCredentialInvalid || !strings.Contains(outcome.Detail, "401") {
		t.Fatalf("401 should classify credential_invalid: %#v", outcome)
	}
}

func TestProbeModelOpenAIWithoutCatalogFallsBackToCompletion(t *testing.T) {
	var completionProbed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.WriteHeader(http.StatusNotFound)
		case "/chat/completions":
			completionProbed = true
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"p"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "")
	if outcome.Status != ModelHealthHealthy || !completionProbed {
		t.Fatalf("a catalog-less provider should fall back to a completion probe: %#v", outcome)
	}
}

func TestProbeModelClassifiesAnthropicUpstream(t *testing.T) {
	var sawKey, sawVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawKey = r.Header.Get("x-api-key")
		sawVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"content":[]}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "anthropic", server.URL, "Qwen3.8-Flash-Next", "llc-key")
	if outcome.Status != ModelHealthHealthy {
		t.Fatalf("a 200 messages call should be healthy: %#v", outcome)
	}
	if sawKey != "llc-key" || sawVersion != "2023-06-01" {
		t.Fatalf("probe headers = key %q version %q", sawKey, sawVersion)
	}
}

func TestProbeModelAnthropicClassifiesModelMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "anthropic", server.URL, "gone-model", "key")
	if outcome.Status != ModelHealthModelMissing {
		t.Fatalf("404 should classify model_missing: %#v", outcome)
	}
}

func TestProbeModelClassifies403AsDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited, come back later"}}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "key")
	if outcome.Status != ModelHealthDenied || !strings.Contains(outcome.Detail, "403") {
		t.Fatalf("403 should classify denied (likely throttling): %#v", outcome)
	}
}

func TestProbeModelScrubsCredentialEchoesFromDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-secret-12345. Rotate it."}}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "sk-secret-12345")
	if strings.Contains(outcome.Detail, "sk-secret-12345") || !strings.Contains(outcome.Detail, "***") {
		t.Fatalf("credential echoes must be scrubbed from details: %#v", outcome)
	}
}

func TestProbeModelUnreachableEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := server.Client()
	url := server.URL
	server.Close()

	outcome := probeModel(context.Background(), client, "anthropic", url, "model-a", "key")
	if outcome.Status != ModelHealthUnreachable {
		t.Fatalf("a refused connection should classify unreachable: %#v", outcome)
	}
}

func TestProbeModelRejectsHTML200InsteadOfHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body>login required</body></html>"))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "key")
	if outcome.Status != ModelHealthError || !strings.Contains(outcome.Detail, "200 without a completion payload") {
		t.Fatalf("an HTML 200 must not read as healthy: %#v", outcome)
	}
}

func TestProbeModelRejectsHTML200OnAnthropic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>sso</html>"))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "anthropic", server.URL, "model-a", "key")
	if outcome.Status != ModelHealthError || !strings.Contains(outcome.Detail, "200 without a messages payload") {
		t.Fatalf("an HTML 200 must not read as healthy: %#v", outcome)
	}
}

func TestProbeModelRedirectToLoginPageIsNotHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>sign in</html>"))
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer server.Close()

	for _, protocol := range []string{"openai-compatible", "anthropic"} {
		outcome := probeModel(context.Background(), server.Client(), protocol, server.URL, "model-a", "key")
		if outcome.Status == ModelHealthHealthy {
			t.Fatalf("%s: a login-page redirect must not read as healthy: %#v", protocol, outcome)
		}
	}
}

func TestProbeModelClassifies429AsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "key")
	if outcome.Status != ModelHealthError || !strings.Contains(outcome.Detail, "429") {
		t.Fatalf("429 rides the hysteresis path, not a hard failover: %#v", outcome)
	}
}

func TestProbeModelOpenAIEmptyCatalogFallsBackToCompletion(t *testing.T) {
	var completionProbed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		case "/chat/completions":
			completionProbed = true
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"p"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	outcome := probeModel(context.Background(), server.Client(), "openai-compatible", server.URL, "model-a", "")
	if outcome.Status != ModelHealthHealthy || !completionProbed {
		t.Fatalf("an empty catalog should fall back to a completion probe: %#v", outcome)
	}
}

func TestProbeModelRelativeEndpointIsConfigurationError(t *testing.T) {
	outcome := probeModel(context.Background(), http.DefaultClient, "openai-compatible", "/v1", "model-a", "key")
	if outcome.Status != ModelHealthError || !strings.Contains(outcome.Detail, "not an absolute URL") {
		t.Fatalf("a relative endpoint is a configuration problem, not unreachability: %#v", outcome)
	}
}

// staticKeyProvider serves one fixed master key to the Sealer.
type staticKeyProvider struct{ key credential.MasterKey }

func (p staticKeyProvider) Active(context.Context) (credential.MasterKey, error) { return p.key, nil }
func (p staticKeyProvider) ByID(context.Context, string) (credential.MasterKey, error) {
	return p.key, nil
}

func TestCheckModelHealthResolvesSealedCredential(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	key := credential.MasterKey{ID: "key-1", Bytes: []byte("0123456789abcdef0123456789abcdef")}
	sealer := credential.NewSealer(staticKeyProvider{key: key})
	envelope, err := sealer.Seal(context.Background(), []byte("secret-key"), credential.AssociatedData("demo", "cred-a"))
	if err != nil {
		t.Fatal(err)
	}
	application.Credentials = sealer

	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"}]}`))
	}))
	defer upstream.Close()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	credentialID := "cred-a"
	pool.ExpectQuery(`SELECT deployment_id,id,protocol,endpoint,upstream_model,credential_id,health_status`).
		WillReturnRows(pgxmock.NewRows([]string{"deployment_id", "id", "protocol", "endpoint", "upstream_model", "credential_id", "health_status"}).
			AddRow("demo", "bench-a", "openai-compatible", upstream.URL, "model-a", &credentialID, "unknown"))
	pool.ExpectQuery(`SELECT encrypted_value,nonce,key_id FROM credentials`).
		WithArgs("demo", "cred-a").
		WillReturnRows(pgxmock.NewRows([]string{"encrypted_value", "nonce", "key_id"}).
			AddRow(envelope.Ciphertext, envelope.Nonce, envelope.KeyID))
	pool.ExpectExec(`UPDATE models SET health_status`).
		WithArgs("demo", "bench-a", ModelHealthHealthy, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	result, err := application.CheckModelHealth(context.Background(), now, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result.Healthy != 1 || sawAuth != "Bearer secret-key" {
		t.Fatalf("the sealed credential must reach the upstream: result=%#v auth=%q", result, sawAuth)
	}
}

func TestCheckModelHealthPersistsAndCountsTransitions(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"}]}`))
	}))
	defer upstream.Close()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	pool.ExpectQuery(`SELECT deployment_id,id,protocol,endpoint,upstream_model,credential_id,health_status`).
		WillReturnRows(pgxmock.NewRows([]string{"deployment_id", "id", "protocol", "endpoint", "upstream_model", "credential_id", "health_status"}).
			AddRow("demo", "bench-a", "openai-compatible", upstream.URL, "model-a", nil, "unknown").
			AddRow("demo", "bench-b", "openai-compatible", upstream.URL, "model-b", nil, "healthy"))
	pool.ExpectExec(`UPDATE models SET health_status`).
		WithArgs("demo", "bench-a", ModelHealthHealthy, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE models SET health_status`).
		WithArgs("demo", "bench-b", ModelHealthModelMissing, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	result, err := application.CheckModelHealth(context.Background(), now, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result.Checked != 2 || result.Healthy != 1 || result.Unhealthy != 1 || result.Changed != 2 {
		t.Fatalf("unexpected health result: %#v", result)
	}
}
