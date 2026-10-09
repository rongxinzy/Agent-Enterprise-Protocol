package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

func TestGatewayMetricDefinitionGuard(t *testing.T) {
	application, token, _ := testHTTPApplication(t)
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"values":[[1,"9.876543210123456789"]]}]}}`)
	}))
	defer source.Close()
	application.Config.GatewayPrometheusURL = source.URL
	application.Config.GatewayLokiURL = source.URL
	application.Config.GatewayOrganizationLogs = true
	h := New(application).Handler()
	window := "&start=" + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + "&end=" + time.Now().UTC().Format(time.RFC3339) + "&step=300"
	for _, tc := range []struct{ query, source, id string }{
		{"metric=calls", "prometheus", "ai_usage_completed_calls"},
		{"metric=failures", "prometheus", "ai_detected_failures"},
		{"metric=calls&groupBy=team", "loki", "gateway_access_requests"},
		{"metric=failures&roleId=role-a", "loki", "gateway_http_errors"},
	} {
		got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/metrics?"+tc.query+window+"&expectedDefinition="+tc.id, "")
		var result struct {
			Source     string                         `json:"source"`
			Definition gatewaysource.MetricDefinition `json:"definition"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil || got.Code != http.StatusOK || result.Source != tc.source || result.Definition.ID != tc.id || result.Definition.WindowSeconds != 300 || !strings.Contains(got.Body.String(), "9.876543210123456789") {
			t.Fatalf("metric definition/native precision: %d %s %v", got.Code, got.Body.String(), err)
		}
	}
	before := sourceCalls.Load()
	for _, query := range []string{
		"metric=calls&groupBy=team&expectedDefinition=ai_usage_completed_calls",
		"metric=failures&roleId=role-a&expectedDefinition=ai_detected_failures",
		"metric=calls&expectedDefinition=gateway_access_requests",
	} {
		got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/metrics?"+query+window, "")
		if got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), "GATEWAY_METRIC_DEFINITION_MISMATCH") {
			t.Fatalf("definition change accepted: %d %s", got.Code, got.Body.String())
		}
	}
	if sourceCalls.Load() != before {
		t.Fatal("definition mismatch contacted the source")
	}
	got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/requests?source=gateway"+window, "")
	if got.Code != http.StatusOK || strings.Contains(got.Body.String(), `"definition"`) {
		t.Fatalf("log result changed: %d %s", got.Code, got.Body.String())
	}
}

func TestGatewayNativeEndpoints(t *testing.T) {
	application, token, _ := testHTTPApplication(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer native-source" || r.Header.Get("X-Scope-OrgID") != "deployment-a" {
			t.Error("source auth/tenant")
		}
		switch r.URL.Path {
		case "/api/v1/targets":
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"activeTargets":[{"labels":{"aep_deployment_id":"deployment-a"},"health":"up","lastScrape":"2026-10-08T00:00:00Z","lastScrapeDuration":0.123},{"labels":{"aep_deployment_id":"other"},"health":"foreign-secret"}]}}`)
		default:
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"values":[[1,"9.876543210123456789"]]}]}}`)
		}
	}))
	defer source.Close()
	application.Config.GatewayPrometheusURL = source.URL
	application.Config.GatewayLokiURL = source.URL
	application.Config.GatewayPrometheusToken = "native-source"
	application.Config.GatewayLokiToken = "native-source"
	h := New(application).Handler()
	for _, path := range []string{"metrics?metric=input_tokens", "metrics?metric=input_tokens&modelId=catalog-alias", "requests?source=gateway", "requests/request-a?source=authorizer"} {
		got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/"+path+"&start=2026-10-08T00:00:00Z&end=2026-10-08T00:05:00Z", "")
		if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "9.876543210123456789") || got.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("native: %d %s", got.Code, got.Body.String())
		}
	}
	got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/health", "")
	if got.Code != http.StatusOK || strings.Contains(got.Body.String(), "foreign-secret") || !strings.Contains(got.Body.String(), "0.123") {
		t.Fatal(got.Body.String())
	}
	if got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/capabilities", ""); got.Code != http.StatusOK {
		t.Fatal(got.Body.String())
	}
	if got := adminRequest(h, "", http.MethodGet, "/aep/v1/admin/model-gateway/metrics", ""); got.Code != http.StatusUnauthorized {
		t.Fatal("unauthorized read", got.Code)
	}
	for _, path := range []string{"metrics?metric=p95", "metrics?metric=calls&groupBy=team", "requests?limit=201"} {
		got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/"+path+"&start=2026-10-08T00:00:00Z&end=2026-10-08T00:05:00Z", "")
		if got.Code != http.StatusBadRequest && got.Code != http.StatusUnprocessableEntity {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	application.Config.GatewayMetricsDeployment = "deployment-a"
	application.Config.GatewayOrganizationLogs = true
	if got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/metrics?metric=input_tokens&groupBy=team&start=2026-10-08T00:00:00Z&end=2026-10-08T00:05:00Z", ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"source":"loki"`) {
		t.Fatal(got.Body.String())
	}
	if got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/metrics?metric=upstream_qps&start=2026-10-08T00:00:00Z&end=2026-10-08T00:05:00Z", ""); got.Code != http.StatusOK {
		t.Fatal(got.Body.String())
	}
	application.Config.GatewayPrometheusURL = ""
	application.Config.GatewayLokiURL = "http://127.0.0.1:1"
	if got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/metrics?metric=calls&start=2026-10-08T00:00:00Z&end=2026-10-08T00:05:00Z", ""); got.Code != http.StatusServiceUnavailable {
		t.Fatal(got.Body.String())
	}
	got = adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/health", "")
	if !strings.Contains(got.Body.String(), "disabled") || !strings.Contains(got.Body.String(), "unavailable") {
		t.Fatal(got.Body.String())
	}
}
