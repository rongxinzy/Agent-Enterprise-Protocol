package gatewaysource

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

// Evaluate the generated query, not a copy of its implementation. Point
// AEP_PROMTOOL at the official Prometheus binary to run native fixture tests.
func TestInfrastructureQueriesWithNativePrometheus(t *testing.T) {
	promtool := os.Getenv("AEP_PROMTOOL")
	if promtool == "" {
		t.Skip("AEP_PROMTOOL is not configured")
	}
	series := []map[string]string{
		{"series": `envoy_http_downstream_rq_total{http_conn_manager_prefix="outbound_0.0.0.0_80"}`, "values": "0+10x10"},
		{"series": `envoy_http_downstream_rq_total{http_conn_manager_prefix="http"}`, "values": "0+5x10"},
		{"series": `envoy_http_downstream_rq_total{http_conn_manager_prefix="admin"}`, "values": "0+100x10"},
		{"series": `envoy_http_downstream_rq_total{http_conn_manager_prefix="agent"}`, "values": "0+100x10"},
		{"series": `envoy_http_downstream_rq_total{http_conn_manager_prefix="stats"}`, "values": "0+100x10"},
		{"series": `envoy_cluster_upstream_rq_total{cluster_name="outbound|80||model-provider"}`, "values": "0+15x10"},
		{"series": `envoy_cluster_upstream_rq_total{cluster_name="agent"}`, "values": "0+100x10"},
		{"series": `envoy_cluster_upstream_rq_total{cluster_name="prometheus_stats"}`, "values": "0+100x10"},
		{"series": `envoy_cluster_upstream_rq_total{cluster_name="xds-grpc.internal"}`, "values": "0+100x10"},
	}
	for _, scenario := range []string{"no-5xx", "mixed", "all-5xx", "idle", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			input := append([]map[string]string(nil), series...)
			qps, ratio := float64(15), float64(1)
			if scenario == "mixed" || scenario == "all-5xx" {
				failures := 3
				if scenario == "all-5xx" {
					failures = 15
				}
				ratio = 1 - float64(failures)/15
				input = append(input,
					map[string]string{"series": `envoy_http_downstream_rq{http_conn_manager_prefix="outbound_0.0.0.0_80",response_code_class="5xx"}`, "values": fmt.Sprintf("0+%dx10", failures)},
					map[string]string{"series": `envoy_cluster_upstream_rq{cluster_name="outbound|80||model-provider",response_code_class="5xx"}`, "values": fmt.Sprintf("0+%dx10", failures)})
			}
			if scenario == "idle" {
				qps = 0
				input = []map[string]string{{"series": series[0]["series"], "values": "0+0x10"}, {"series": series[5]["series"], "values": "0+0x10"}}
			}
			if scenario == "absent" {
				input = nil
			}
			var assertions []map[string]any
			for _, metric := range []string{"downstream_qps", "upstream_qps", "downstream_success_rate", "upstream_success_rate"} {
				values := window()
				values.Set("metric", metric)
				query, err := InfrastructureQuery(values)
				if err != nil {
					t.Fatal(err)
				}
				var expected []map[string]any
				if scenario != "absent" && (scenario != "idle" || !strings.Contains(metric, "success")) {
					value := qps
					if strings.Contains(metric, "success") {
						value = ratio
					}
					expected = []map[string]any{{"labels": "{}", "value": value}}
				}
				assertions = append(assertions, map[string]any{"expr": query.Get("query"), "eval_time": "10s", "exp_samples": expected})
			}
			document, err := yaml.Marshal(map[string]any{"evaluation_interval": "1s", "tests": []any{map[string]any{"interval": "1s", "input_series": input, "promql_expr_test": assertions}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "native.yml")
			if err := os.WriteFile(path, document, 0600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(promtool, "test", "rules", path).CombinedOutput()
			if err != nil {
				t.Fatalf("native evaluation failed: %v\n%s", err, output)
			}
		})
	}
}
