package gatewaysource

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestDurationWindowMatchesDefinition(t *testing.T) {
	for _, step := range []int{1, 60, 120, 300, 3600} {
		for _, metric := range []string{"first_token_duration", "service_duration"} {
			v := window()
			v.Set("step", strconv.Itoa(step))
			v.Set("metric", metric)
			q, err := MetricQuery("demo", v)
			if err != nil {
				t.Fatal(err)
			}
			d, err := DescribeMetric("prometheus", v)
			if err != nil || d.WindowSeconds != max(step, 120) || !strings.Contains(q.Get("query"), fmt.Sprintf("[%ds]", d.WindowSeconds)) || strings.Contains(q.Get("query"), "irate(") {
				t.Fatalf("lookback disagrees with query: %+v %s %v", d, q, err)
			}
		}
	}
}

// Evaluate production PromQL against native Prometheus with 2-second scrapes.
// The completion is long before the final two scrapes, so irate would be 0/0.
func TestSparseDurationWithNativePrometheus(t *testing.T) {
	promtool := os.Getenv("AEP_PROMTOOL")
	if promtool == "" {
		t.Skip("AEP_PROMTOOL is not configured")
	}
	for _, scenario := range []string{"sparse", "reset", "nonstream", "idle", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			var input []map[string]string
			for _, pod := range []string{"a", "b"} {
				for _, counter := range []string{"llm_duration_count", "llm_service_duration", "llm_stream_duration_count", "llm_first_token_duration"} {
					if scenario == "absent" || (scenario == "nonstream" && (counter == "llm_stream_duration_count" || counter == "llm_first_token_duration")) {
						continue
					}
					value := map[string]int{"llm_duration_count": 1, "llm_service_duration": 40, "llm_stream_duration_count": 1, "llm_first_token_duration": 2}[counter]
					if pod == "b" {
						value = map[string]int{"llm_duration_count": 3, "llm_service_duration": 180, "llm_stream_duration_count": 3, "llm_first_token_duration": 18}[counter]
					}
					values := fmt.Sprintf("0 0 %d+0x149", value)
					switch scenario {
					case "reset":
						values = fmt.Sprintf("%d 0 %d+0x149", value*100, value)
					case "idle":
						values = "0+0x151"
					}
					input = append(input, map[string]string{
						"series": fmt.Sprintf(`route_upstream_model_consumer_metric_%s{ai_consumer=%q,ai_model="shared-model",pod=%q}`, counter, Consumer("demo", "user-a"), pod),
						"values": values,
					})
				}
			}
			// An unrelated deployment must never contribute to the weighted mean.
			for _, counter := range []string{"llm_duration_count", "llm_service_duration", "llm_stream_duration_count", "llm_first_token_duration"} {
				input = append(input, map[string]string{"series": fmt.Sprintf(`route_upstream_model_consumer_metric_%s{ai_consumer=%q}`, counter, Consumer("other", "user-a")), "values": "0+100x151"})
			}
			var assertions []map[string]any
			for _, metric := range []string{"first_token_duration", "service_duration"} {
				for _, group := range []struct{ name, labels string }{{"none", "{}"}, {"user", fmt.Sprintf(`{ai_consumer=%q}`, Consumer("demo", "user-a"))}, {"model", `{ai_model="shared-model"}`}} {
					for _, step := range []int{60, 300} {
						v := window()
						v.Set("metric", metric)
						v.Set("step", strconv.Itoa(step))
						v.Set("groupBy", group.name)
						q, err := MetricQuery("demo", v)
						if err != nil {
							t.Fatal(err)
						}
						var expected []map[string]any
						if scenario != "absent" && (scenario != "nonstream" || metric != "first_token_duration") {
							value := float64(55)
							if metric == "first_token_duration" {
								value = 5
							}
							expected = []map[string]any{{"labels": group.labels, "value": value}}
						}
						expr := q.Get("query")
						// NaN cannot compare equal to itself in promtool's expected values.
						// A native comparison retains only NaN; count verifies it is present.
						if scenario == "idle" {
							expr = fmt.Sprintf("count((%s) != (%s))", expr, expr)
							expected = []map[string]any{{"labels": "{}", "value": 1}}
						}
						assertions = append(assertions, map[string]any{"expr": expr, "eval_time": "60s", "exp_samples": expected})
						if scenario == "sparse" && step == 60 {
							assertions = append(assertions, map[string]any{"expr": fmt.Sprintf("count((%s) != (%s))", expr, expr), "eval_time": "240s", "exp_samples": []map[string]any{{"labels": "{}", "value": 1}}})
						}
					}
				}
			}
			document, err := yaml.Marshal(map[string]any{"evaluation_interval": "2s", "fuzzy_compare": true, "tests": []any{map[string]any{"interval": "2s", "input_series": input, "promql_expr_test": assertions}}})
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "duration.yml")
			if err := os.WriteFile(file, document, 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(promtool, "test", "rules", file).CombinedOutput(); err != nil {
				t.Fatalf("native duration evaluation failed: %v\n%s", err, output)
			}
		})
	}
}
