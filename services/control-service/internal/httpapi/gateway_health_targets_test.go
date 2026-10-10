package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayHealthTargetsRespectDedicatedAndSharedSourceOwnership(t *testing.T) {
	for _, test := range []struct {
		name, binding string
		count         int
	}{
		{"dedicated source admits unlabeled discovery target", "deployment-a", 2},
		{"shared source requires target label", "", 1},
		{"foreign source binding admits no targets", "deployment-b", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, token, _ := testHTTPApplication(t)
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Scope-OrgID") != "deployment-a" {
					t.Error("untrusted source scope")
				}
				_, _ = fmt.Fprint(w, `{"status":"success","data":{"activeTargets":[
				{"labels":{"job":"higress-gateway"},"health":"up","scrapeUrl":"http://private-address:15020","lastError":"private-error","lastScrape":"2026-10-08T00:00:00Z","lastScrapeDuration":0.123},
				{"labels":{"aep_deployment_id":"deployment-a"},"health":"down","lastScrapeDuration":0.456},
				{"labels":{"aep_deployment_id":"deployment-b"},"health":"foreign-secret","lastScrapeDuration":9.999}
				]}}`)
			}))
			defer source.Close()
			application.Config.GatewayPrometheusURL = source.URL
			application.Config.GatewayMetricsDeployment = test.binding
			got := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/model-gateway/health", "")
			if got.Code != http.StatusOK {
				t.Fatal(got.Body.String())
			}
			var result struct {
				Sources []struct {
					Source, State string
					Targets       []struct {
						Health   string
						Duration float64 `json:"lastScrapeDuration"`
					}
				}
			}
			if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Sources) != 2 || result.Sources[0].State != "healthy" || len(result.Sources[0].Targets) != test.count {
				t.Fatal(got.Body.String())
			}
			if strings.Contains(got.Body.String(), "foreign-secret") || strings.Contains(got.Body.String(), "9.999") || strings.Contains(got.Body.String(), "private-") || strings.Contains(got.Body.String(), "scrapeUrl") || strings.Contains(got.Body.String(), "lastError") {
				t.Fatal("target metadata leaked")
			}
			if test.count == 2 && (result.Sources[0].Targets[0].Health != "up" || result.Sources[0].Targets[0].Duration != 0.123) {
				t.Fatal("native target values changed")
			}
		})
	}
}
