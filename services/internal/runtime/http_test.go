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
