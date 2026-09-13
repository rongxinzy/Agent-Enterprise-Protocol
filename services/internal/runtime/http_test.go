package runtime

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHTTPMetricsUseStableRoutesAndRedactedLogs(t *testing.T) {
	original := slog.Default()
	defer slog.SetDefault(original)
	var logs bytes.Buffer
	slog.SetDefault(NewTestLogger(&logs))

	metrics := NewHTTPMetrics("test_service")
	router := chi.NewRouter()
	router.Use(metrics.Middleware)
	router.Get("/resources/{resourceId}", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Request-ID", "request-1")
		response.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/resources/secret-id?token=secret-query", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}

	metricsResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsResponse.Body.String()
	if !strings.Contains(body, `aep_test_service_http_requests_total{method="GET",route="/resources/{resourceId}",status="204"} 1`) {
		t.Fatalf("stable route metric was not recorded:\n%s", body)
	}
	logOutput := logs.String()
	for _, forbidden := range []string{"secret-id", "secret-query", "token"} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("access log leaked %q: %s", forbidden, logOutput)
		}
	}
	if !strings.Contains(logOutput, `"route":"/resources/{resourceId}"`) ||
		!strings.Contains(logOutput, `"request_id":"request-1"`) {
		t.Fatalf("safe access-log fields missing: %s", logOutput)
	}
}

func TestHTTPMetricsFallbackRoutesAndProbeFailures(t *testing.T) {
	previous := slog.Default()
	defer slog.SetDefault(previous)
	var logs bytes.Buffer
	slog.SetDefault(NewTestLogger(&logs))
	metrics := NewHTTPMetrics("gateway.authorizer")
	handler := metrics.Middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/readyz" {
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/v1/chat/completions?token=secret", 200},
		{"/other/secret-path", 200},
		{"/readyz", 503},
		{"/healthz", 200},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status {
			t.Fatalf("%s returned %d", test.path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, metric := range []string{
		`route="/v1/*",status="200"`, `route="unmatched",status="200"`,
		`route="/readyz",status="503"`, `route="/healthz",status="200"`,
	} {
		if !strings.Contains(response.Body.String(), metric) {
			t.Fatalf("missing %s in metrics: %s", metric, response.Body.String())
		}
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), `"route":"/healthz"`) || !strings.Contains(logs.String(), `"route":"/readyz"`) {
		t.Fatalf("unsafe or missing access log: %s", logs.String())
	}
}
