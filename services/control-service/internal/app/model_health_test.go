package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
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
			_, _ = w.Write([]byte(`{"choices":[]}`))
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
