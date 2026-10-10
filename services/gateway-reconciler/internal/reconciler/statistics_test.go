package reconciler

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestStatisticsOnlyMatchesEnabledManagedIngresses(t *testing.T) {
	t.Parallel()
	openAI := Route{ModelID: "chat", Enabled: true, Endpoint: "/v1", Protocol: "openai-compatible"}
	active := anthropicRoute("active", "https://provider.example/v1")
	disabled := anthropicRoute("disabled", "https://provider.example/v1")
	disabled.Enabled = false
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{disabled, active, openAI}}
	_, resources, _, err := Render(desired, map[string]string{"aep-credential-bigmodel/api-key": "fixture-provider-secret"})
	if err != nil {
		t.Fatal(err)
	}
	resource := resources[len(resources)-1]
	if resource.Kind != ResourceAIStatistics || resource.APIPath != aiStatisticsAPIPath(resourceSuffix("demo")) {
		t.Fatalf("statistics resource identity = %s %s", resource.Kind, resource.APIPath)
	}
	var plugin struct {
		Spec struct {
			URL                  string `yaml:"url"`
			FailStrategy         string `yaml:"failStrategy"`
			Priority             int    `yaml:"priority"`
			DefaultConfigDisable bool   `yaml:"defaultConfigDisable"`
			MatchRules           []struct {
				Ingress       []string               `yaml:"ingress"`
				ConfigDisable bool                   `yaml:"configDisable"`
				Config        map[string]interface{} `yaml:"config"`
			} `yaml:"matchRules"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(resource.Body), &plugin); err != nil {
		t.Fatal(err)
	}
	if !plugin.Spec.DefaultConfigDisable || plugin.Spec.FailStrategy != "FAIL_OPEN" || plugin.Spec.Priority != 200 || !strings.Contains(plugin.Spec.URL, "@sha256:") {
		t.Fatalf("unsafe statistics defaults: %#v", plugin.Spec)
	}
	if len(plugin.Spec.MatchRules) != 1 {
		t.Fatalf("match rules = %#v", plugin.Spec.MatchRules)
	}
	rule := plugin.Spec.MatchRules[0]
	want := []string{openAIResourceName("demo", "chat"), anthropicResourceName("active")}
	if rule.ConfigDisable || len(rule.Ingress) != len(want) || rule.Ingress[0] != want[0] || rule.Ingress[1] != want[1] {
		t.Fatalf("statistics must only match enabled managed ingresses: %v", rule.Ingress)
	}
	if rule.Config["use_default_attributes"] != false || rule.Config["use_default_response_attributes"] != true || rule.Config["attributes"] != nil {
		t.Fatalf("statistics must not capture prompt/answer attributes: %#v", rule.Config)
	}
	if strings.Contains(resource.Body, "fixture-provider-secret") || strings.Contains(resource.Body, anthropicResourceName("disabled")) {
		t.Fatal("statistics contains provider material or a disabled route")
	}
}

func TestStatisticsStopsMatchingWhenAllRoutesAreDisabled(t *testing.T) {
	t.Parallel()
	_, resources, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev-disabled"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource := resources[len(resources)-1]
	if resource.Kind != ResourceAIStatistics || !strings.Contains(resource.Body, "defaultConfigDisable: true\n  matchRules: []") {
		t.Fatalf("idle statistics could affect other gateway routes: %s", resource.Body)
	}
}
