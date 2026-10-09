package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

func (s *Server) gatewayCapabilities(response http.ResponseWriter, request *http.Request) {
	cfg := s.app.Config
	metrics := append([]string(nil), gatewaysource.Metrics...)
	unsupported := []string{"p95", "p99", "cost", "team_metrics", "role_metrics"}
	dimensions := []string{"none", "model", "route", "user"}
	if cfg.GatewayOrganizationLogs && cfg.GatewayLokiURL != "" {
		dimensions = append(dimensions, "team", "role")
		unsupported = []string{"p95", "p99", "cost"}
	}
	if cfg.GatewayMetricsDeployment == claimsFrom(request).DeploymentID {
		metrics = append(metrics, gatewaysource.InfrastructureMetrics...)
	} else {
		unsupported = append(unsupported, gatewaysource.InfrastructureMetrics...)
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"sources":    map[string]bool{"prometheus": cfg.GatewayPrometheusURL != "", "loki": cfg.GatewayLokiURL != "", "quota": cfg.GatewayQuotaURL != "", "testAccess": cfg.ModelGatewayBaseURL != ""},
		"dimensions": dimensions, "metrics": metrics,
		"unsupported": unsupported,
	})
}

func gatewayQueryFailure(response http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, gatewaysource.ErrDimension) {
		writeProblem(response, request, http.StatusUnprocessableEntity, "GATEWAY_DIMENSION_UNSUPPORTED", "The native source does not expose this metric or dimension.")
		return
	}
	writeProblem(response, request, http.StatusBadRequest, "INVALID_GATEWAY_QUERY", "Use a valid bounded time range and supported query parameters.")
}

func (s *Server) gatewayMetrics(response http.ResponseWriter, request *http.Request) {
	values := request.URL.Query()
	// Catalog aliases can share an upstream model. The stock ai_model label
	// cannot distinguish them; catalog filtering must use trusted access logs.
	if values.Get("modelId") != "" && values.Get("teamId") == "" && values.Get("roleId") == "" && values.Get("groupBy") != "team" && values.Get("groupBy") != "role" {
		query, err := gatewaysource.LogMetricQuery(claimsFrom(request).DeploymentID, values)
		if err != nil {
			gatewayQueryFailure(response, request, err)
			return
		}
		s.gatewayNative(response, request, "loki", s.app.Config.GatewayLokiURL, s.app.Config.GatewayLokiToken, "/loki/api/v1/query_range", query)
		return
	}
	if values.Get("teamId") != "" || values.Get("roleId") != "" || values.Get("groupBy") == "team" || values.Get("groupBy") == "role" {
		if !s.app.Config.GatewayOrganizationLogs {
			gatewayQueryFailure(response, request, gatewaysource.ErrDimension)
			return
		}
		query, err := gatewaysource.LogMetricQuery(claimsFrom(request).DeploymentID, values)
		if err != nil {
			gatewayQueryFailure(response, request, err)
			return
		}
		s.gatewayNative(response, request, "loki", s.app.Config.GatewayLokiURL, s.app.Config.GatewayLokiToken, "/loki/api/v1/query_range", query)
		return
	}
	query, err := gatewaysource.MetricQuery(claimsFrom(request).DeploymentID, request.URL.Query())
	if errors.Is(err, gatewaysource.ErrDimension) && s.app.Config.GatewayMetricsDeployment == claimsFrom(request).DeploymentID {
		query, err = gatewaysource.InfrastructureQuery(request.URL.Query())
	}
	if err != nil {
		gatewayQueryFailure(response, request, err)
		return
	}
	s.gatewayNative(response, request, "prometheus", s.app.Config.GatewayPrometheusURL, s.app.Config.GatewayPrometheusToken, "/api/v1/query_range", query)
}

func (s *Server) gatewayRequests(response http.ResponseWriter, request *http.Request) {
	query, err := gatewaysource.LogQuery(claimsFrom(request).DeploymentID, chi.URLParam(request, "requestId"), request.URL.Query())
	if err != nil {
		gatewayQueryFailure(response, request, err)
		return
	}
	s.gatewayNative(response, request, "loki", s.app.Config.GatewayLokiURL, s.app.Config.GatewayLokiToken, "/loki/api/v1/query_range", query)
}

func (s *Server) gatewayNative(response http.ResponseWriter, request *http.Request, source, endpoint, token, path string, values url.Values) {
	data, err := gatewaysource.Fetch(request.Context(), endpoint, token, claimsFrom(request).DeploymentID, path, http.MethodGet, values)
	if err != nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "GATEWAY_SOURCE_UNAVAILABLE", "The configured gateway data source is unavailable.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]any{"source": source, "queriedAt": time.Now().UTC(), "data": data})
}

func (s *Server) gatewayHealth(response http.ResponseWriter, request *http.Request) {
	items := make([]map[string]any, 0, 2)
	for _, source := range []struct{ name, endpoint, token, path string }{
		{"prometheus", s.app.Config.GatewayPrometheusURL, s.app.Config.GatewayPrometheusToken, "/api/v1/targets"},
		{"loki", s.app.Config.GatewayLokiURL, s.app.Config.GatewayLokiToken, "/loki/api/v1/labels"},
	} {
		item := map[string]any{"source": source.name, "state": "disabled", "checkedAt": time.Now().UTC(), "targets": []map[string]any{}}
		if source.endpoint != "" {
			item["state"] = "unavailable"
			data, err := gatewaysource.Fetch(request.Context(), source.endpoint, source.token, claimsFrom(request).DeploymentID, source.path, http.MethodGet, nil)
			if err == nil {
				item["state"] = "healthy"
				var document struct {
					Data struct {
						Targets []struct {
							Labels     map[string]string `json:"labels"`
							Health     string            `json:"health"`
							LastScrape string            `json:"lastScrape"`
							Duration   float64           `json:"lastScrapeDuration"`
						} `json:"activeTargets"`
					} `json:"data"`
				}
				if json.Unmarshal(data, &document) == nil {
					targets := make([]map[string]any, 0)
					for _, target := range document.Data.Targets {
						if target.Labels["aep_deployment_id"] == claimsFrom(request).DeploymentID {
							targets = append(targets, map[string]any{"health": target.Health, "lastScrape": target.LastScrape, "lastScrapeDuration": target.Duration})
						}
					}
					item["targets"] = targets
				}
			}
		}
		items = append(items, item)
	}
	writeJSON(response, http.StatusOK, map[string]any{"sources": items})
}
