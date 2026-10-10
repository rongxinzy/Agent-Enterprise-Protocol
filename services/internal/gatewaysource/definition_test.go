package gatewaysource

import (
	"strings"
	"testing"
)

func TestMetricDefinitionsFollowNativeQuerySemantics(t *testing.T) {
	for _, tc := range []struct {
		source, metric, id, queryPart, unit string
		window                              int
	}{
		{"prometheus", "calls", "ai_usage_completed_calls", "llm_duration_count", "requests", 300},
		{"loki", "calls", "gateway_access_requests", "count_over_time", "requests", 300},
		{"prometheus", "failures", "ai_detected_failures", "llm_failure_count", "requests", 300},
		{"loki", "failures", "gateway_http_errors", "status >= 400", "requests", 300},
		{"prometheus", "service_duration", "ai_usage_mean_service_duration", "[2m]", "milliseconds", 120},
		{"loki", "service_duration", "gateway_log_mean_service_duration", "[300s]", "milliseconds", 300},
		{"prometheus", "downstream_success_rate", "envoy_downstream_non_5xx_ratio", `response_code_class="5xx"`, "ratio", 120},
		{"prometheus", "auth_requests", "authorizer_http_requests", "sum by (status)", "requests", 300},
	} {
		t.Run(tc.id, func(t *testing.T) {
			values := window()
			values.Set("step", "300")
			values.Set("metric", tc.metric)
			query, err := MetricQuery("deployment-a", values)
			if tc.source == "loki" {
				query, err = LogMetricQuery("deployment-a", values)
			} else if err != nil {
				query, err = InfrastructureQuery(values)
			}
			if err != nil || !strings.Contains(query.Get("query"), tc.queryPart) {
				t.Fatalf("native query: %s %v", query.Get("query"), err)
			}
			definition, err := DescribeMetric(tc.source, values)
			if err != nil || definition.ID != tc.id || definition.Unit != tc.unit || definition.WindowSeconds != tc.window {
				t.Fatalf("incorrect definition: %+v %v", definition, err)
			}
		})
	}
}

func TestMetricDefinitionCoverageAndModelDimensions(t *testing.T) {
	for _, source := range []string{"prometheus", "loki"} {
		metrics := append([]string(nil), Metrics...)
		if source == "prometheus" {
			metrics = append(metrics, InfrastructureMetrics...)
		}
		for _, metric := range metrics {
			values := window()
			values.Set("metric", metric)
			definition, err := DescribeMetric(source, values)
			if err != nil || definition.ID == "" || definition.Unit == "" || definition.Aggregation == "" || definition.WindowSeconds < 1 || definition.GroupBy != "none" {
				t.Fatalf("missing definition: %s %s %+v %v", source, metric, definition, err)
			}
		}
	}
	values := window()
	values.Set("metric", "input_tokens")
	values.Set("groupBy", "model")
	for _, tc := range []struct{ source, dimension string }{{"prometheus", "upstream_model"}, {"loki", "catalog_model"}} {
		definition, err := DescribeMetric(tc.source, values)
		if err != nil || definition.ModelDimension != tc.dimension {
			t.Fatalf("model dimension: %+v %v", definition, err)
		}
	}
	for _, tc := range []struct{ source, metric string }{{"other", "calls"}, {"prometheus", "cost"}, {"loki", "downstream_qps"}} {
		values.Set("metric", tc.metric)
		if _, err := DescribeMetric(tc.source, values); err == nil {
			t.Fatal("unsupported definition accepted")
		}
	}
	if _, err := DescribeMetric("prometheus", nil); err == nil {
		t.Fatal("invalid window accepted")
	}
}
