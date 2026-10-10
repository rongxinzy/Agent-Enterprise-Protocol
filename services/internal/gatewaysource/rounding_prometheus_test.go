package gatewaysource

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v2"
)

// Execute the production queries in Prometheus. Two Pods each extrapolate to
// 1.333... requests, so rounding after aggregation must return 3, not 2.
func TestCounterRoundingWithNativePrometheus(t *testing.T) {
	promtool := os.Getenv("AEP_PROMTOOL")
	if promtool == "" {
		t.Skip("AEP_PROMTOOL is not configured")
	}
	for _, scenario := range []string{"fractional", "reset", "zero", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			var input []map[string]string
			var assertions []map[string]any
			for _, metric := range []struct{ name, counter string }{
				{"calls", "llm_duration_count"}, {"failures", "llm_failure_count"},
				{"input_tokens", "input_token"}, {"output_tokens", "output_token"},
			} {
				for _, pod := range []string{"a", "b"} {
					values := "100 100 101 101 101"
					if scenario == "reset" && pod == "a" {
						values = "99 100 0 0 1"
					} else if scenario == "zero" {
						values = "100 100 100 100 100"
					}
					if scenario != "absent" {
						input = append(input, map[string]string{
							"series": fmt.Sprintf(`route_upstream_model_consumer_metric_%s{ai_consumer=%q,ai_model="shared-model",ai_route="shared-route",pod=%q}`, metric.counter, Consumer("demo", "user-a"), pod),
							"values": values,
						})
					}
				}
				// A large unrelated deployment must not enter any aggregate.
				input = append(input, map[string]string{
					"series": fmt.Sprintf(`route_upstream_model_consumer_metric_%s{ai_consumer=%q,ai_model="shared-model",ai_route="shared-route"}`, metric.counter, Consumer("other", "user-a")),
					"values": "0+100x4",
				})
				for _, group := range []struct{ name, labels string }{
					{"none", "{}"}, {"model", `{ai_model="shared-model"}`},
					{"route", `{ai_route="shared-route"}`}, {"user", fmt.Sprintf(`{ai_consumer=%q}`, Consumer("demo", "user-a"))},
				} {
					v := window()
					v.Set("metric", metric.name)
					v.Set("groupBy", group.name)
					v.Set("step", "60")
					query, err := MetricQuery("demo", v)
					if err != nil {
						t.Fatal(err)
					}
					var expected []map[string]any
					if scenario != "absent" {
						value := 3
						if scenario == "zero" {
							value = 0
						}
						expected = []map[string]any{{"labels": group.labels, "value": value}}
					}
					assertions = append(assertions, map[string]any{"expr": query.Get("query"), "eval_time": "60s", "exp_samples": expected})
				}
			}
			// Authorizer counts use the same policy, keeping status groups and
			// excluding health requests. QPS must keep its fractional units.
			for _, instance := range []string{"a", "b"} {
				input = append(input, map[string]string{"series": fmt.Sprintf(`aep_gateway_authorizer_http_requests_total{route="/v1/chat/completions",status="200",instance=%q}`, instance), "values": "100 100 101 101 101"})
			}
			input = append(input,
				map[string]string{"series": `aep_gateway_authorizer_http_requests_total{route="/healthz",status="200"}`, "values": "0+100x4"},
				map[string]string{"series": `envoy_http_downstream_rq_total{http_conn_manager_prefix="http"}`, "values": "0+1x4"},
			)
			for _, tc := range []struct {
				metric, labels string
				value          float64
			}{{"auth_requests", `{status="200"}`, 3}, {"downstream_qps", "{}", 0.06666666666666667}} {
				v := window()
				v.Set("metric", tc.metric)
				query, err := InfrastructureQuery(v)
				if err != nil {
					t.Fatal(err)
				}
				assertions = append(assertions, map[string]any{"expr": query.Get("query"), "eval_time": "60s", "exp_samples": []map[string]any{{"labels": tc.labels, "value": tc.value}}})
			}
			document, err := yaml.Marshal(map[string]any{"evaluation_interval": "15s", "tests": []any{map[string]any{"interval": "15s", "input_series": input, "promql_expr_test": assertions}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "rounded.yml")
			if err := os.WriteFile(path, document, 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(promtool, "test", "rules", path).CombinedOutput(); err != nil {
				t.Fatalf("native counter evaluation failed: %v\n%s", err, output)
			}
		})
	}
}
