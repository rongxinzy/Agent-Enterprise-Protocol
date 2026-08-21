package runtime

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type HTTPMetrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

func NewHTTPMetrics(service string) *HTTPMetrics {
	subsystem := strings.NewReplacer("-", "_", ".", "_").Replace(service)
	registry := prometheus.NewRegistry()
	metrics := &HTTPMetrics{
		registry: registry,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "aep", Subsystem: subsystem, Name: "http_requests_total",
			Help: "Total HTTP requests by stable route, method, and status.",
		}, []string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "aep", Subsystem: subsystem, Name: "http_request_duration_seconds",
			Help:    "HTTP request duration by stable route and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aep", Subsystem: subsystem, Name: "http_requests_in_flight",
			Help: "Current in-flight HTTP requests.",
		}),
	}
	registry.MustRegister(metrics.requests, metrics.duration, metrics.inFlight)
	return metrics
}

func (m *HTTPMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		m.inFlight.Inc()
		defer m.inFlight.Dec()
		wrapped := chimiddleware.NewWrapResponseWriter(response, request.ProtoMajor)
		next.ServeHTTP(wrapped, request)

		status := wrapped.Status()
		if status == 0 {
			status = http.StatusOK
		}
		route := stableRoute(request)
		m.requests.WithLabelValues(route, request.Method, strconv.Itoa(status)).Inc()
		m.duration.WithLabelValues(route, request.Method).Observe(time.Since(startedAt).Seconds())

		if !quietProbe(route, status) {
			slog.Info("http request",
				"request_id", wrapped.Header().Get("X-Request-ID"),
				"method", request.Method,
				"route", route,
				"status", status,
				"bytes", wrapped.BytesWritten(),
				"duration_ms", time.Since(startedAt).Milliseconds(),
			)
		}
	})
}

func stableRoute(request *http.Request) string {
	if routeContext := chi.RouteContext(request.Context()); routeContext != nil {
		if pattern := routeContext.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	switch request.URL.Path {
	case "/healthz", "/livez", "/readyz", "/metrics":
		return request.URL.Path
	default:
		if strings.HasPrefix(request.URL.Path, "/v1/") {
			return "/v1/*"
		}
		return "unmatched"
	}
}

func quietProbe(route string, status int) bool {
	return status < http.StatusBadRequest &&
		(route == "/healthz" || route == "/livez" || route == "/readyz" || route == "/metrics")
}
