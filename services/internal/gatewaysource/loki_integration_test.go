package gatewaysource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in test executes the actual query templates in Loki's native
// engine. It writes only disposable metadata fixtures to a loopback service.
func TestNativeLokiQueries(t *testing.T) {
	endpoint := os.Getenv("AEP_NATIVE_LOKI_TEST_URL")
	if endpoint == "" {
		t.Skip("set AEP_NATIVE_LOKI_TEST_URL for native Loki integration")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Scheme != "http" {
		t.Fatal("native Loki fixture requires a loopback HTTP endpoint")
	}
	tenant := fmt.Sprintf("native-fixture-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)
	line := map[string]any{"request_id": "request-a", "model_id": "model-a", "user_id": "user-a", "team_ids": "|" + encodeID("team-a") + "|" + encodeID("team-b") + "|", "role_ids": "|" + encodeID("role-a") + "|", "status": 503, "input_token": 3, "output_token": 2, "llm_service_duration": 17, "llm_first_token_duration": 5, "prompt": "must-not-return", "credential": "must-not-return"}
	streams := make([]map[string]any, 0)
	for _, dimension := range []string{"base", "team", "role"} {
		copy := make(map[string]any, len(line))
		for key, value := range line {
			copy[key] = value
		}
		if dimension == "team" {
			copy["team_id"] = encodeID("team-a")
		}
		if dimension == "role" {
			copy["role_id"] = encodeID("role-a")
		}
		raw, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, map[string]any{"stream": map[string]string{"aep_source": "gateway", "aep_deployment_id": tenant, "aep_dimension": dimension}, "values": [][2]string{{fmt.Sprint(now.Add(-5 * time.Second).UnixNano()), string(raw)}}})
	}
	body, err := json.Marshal(map[string]any{"streams": streams})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/loki/api/v1/push", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 204 {
		diagnostic, _ := io.ReadAll(response.Body)
		t.Fatal(response.StatusCode, string(diagnostic))
	}
	values := url.Values{"start": {now.Add(-time.Minute).Format(time.RFC3339Nano)}, "end": {now.Format(time.RFC3339Nano)}, "step": {"60"}}
	logQuery, err := LogQuery(tenant, "request-a", values)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Fetch(context.Background(), endpoint, "", tenant, "/loki/api/v1/query_range", http.MethodGet, logQuery)
	if err != nil {
		t.Fatal("native request LogQL rejected", err)
	}
	var logs struct {
		Data struct {
			Result []struct {
				Values [][2]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &logs); err != nil || len(logs.Data.Result) != 1 || len(logs.Data.Result[0].Values) != 1 {
		t.Fatal("native request lookup", err, string(data))
	}
	var safe map[string]string
	if err := json.Unmarshal([]byte(logs.Data.Result[0].Values[0][1]), &safe); err != nil || safe["request_id"] != "request-a" || safe["status"] != "503" || strings.Contains(logs.Data.Result[0].Values[0][1], "must-not-return") {
		t.Fatal("native line_format did not render safe JSON", err, logs.Data.Result[0].Values[0][1])
	}
	for _, group := range []string{"none", "model", "route", "user", "team", "role"} {
		for metric, want := range map[string]string{"calls": "1", "failures": "1", "input_tokens": "3", "output_tokens": "2", "service_duration": "17", "first_token_duration": "5"} {
			values.Set("groupBy", group)
			values.Set("metric", metric)
			values.Set("teamId", "team-b") // non-primary membership filter
			query, err := LogMetricQuery(tenant, values)
			if err != nil {
				t.Fatal(err)
			}
			data, err := Fetch(context.Background(), endpoint, "", tenant, "/loki/api/v1/query_range", http.MethodGet, query)
			if err != nil {
				t.Fatalf("native %s/%s LogQL rejected: %v", group, metric, err)
			}
			var metrics struct {
				Data struct {
					Result []struct {
						Values [][2]json.RawMessage `json:"values"`
					} `json:"result"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &metrics); err != nil || len(metrics.Data.Result) != 1 || len(metrics.Data.Result[0].Values) == 0 {
				t.Fatal(group, metric, err, string(data))
			}
			points := metrics.Data.Result[0].Values
			var got string
			if err := json.Unmarshal(points[len(points)-1][1], &got); err != nil || got != want {
				t.Fatal(group, metric, "native fixture value", got, "want", want, err)
			}
		}
	}
}
