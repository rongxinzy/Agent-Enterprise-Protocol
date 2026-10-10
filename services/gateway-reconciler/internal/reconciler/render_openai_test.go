package reconciler

import (
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestOpenAIModelsSharingPathHaveIsolatedProviderConfigurations(t *testing.T) {
	d := DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []Route{
		{ModelID: "alpha", Enabled: true, Endpoint: "http://alpha.example/v1", UpstreamModel: "same-upstream", CredentialRef: &SecretReference{Name: "fixture", Key: "alpha"}},
		{ModelID: "beta", Enabled: true, Endpoint: "http://beta.example/v1", UpstreamModel: "same-upstream", CredentialRef: &SecretReference{Name: "fixture", Key: "beta"}},
	}}
	_, resources, _, err := Render(d, map[string]string{"fixture/alpha": "disposable-alpha", "fixture/beta": "disposable-beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 4 {
		t.Fatalf("resources = %d", len(resources))
	}
	var plugin struct {
		Spec struct {
			MatchRules []struct {
				Ingress []string `yaml:"ingress"`
				Config  struct {
					Provider struct {
						OpenAICustomURL string            `yaml:"openaiCustomUrl"`
						ModelMapping    map[string]string `yaml:"modelMapping"`
						APITokens       []string          `yaml:"apiTokens"`
					} `yaml:"provider"`
				} `yaml:"config"`
			} `yaml:"matchRules"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(resources[2].Body), &plugin); err != nil {
		t.Fatal(err)
	}
	if len(plugin.Spec.MatchRules) != 2 {
		t.Fatal("missing per-model provider rules")
	}
	for i, route := range d.Routes {
		var ingress struct {
			Metadata struct {
				Name        string            `yaml:"name"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(resources[i].Body), &ingress); err != nil {
			t.Fatal(err)
		}
		name := openAIResourceName(d.TenantID(), route.ModelID)
		rule := plugin.Spec.MatchRules[i]
		if ingress.Metadata.Name != name || ingress.Metadata.Annotations["higress.io/exact-match-header-x-aep-model-id"] != route.ModelID || !reflect.DeepEqual(rule.Ingress, []string{name}) {
			t.Fatalf("model %s has an ambiguous route/plugin binding", route.ModelID)
		}
		p := rule.Config.Provider
		if p.OpenAICustomURL != route.Endpoint || !reflect.DeepEqual(p.ModelMapping, map[string]string{route.ModelID: route.UpstreamModel}) || !reflect.DeepEqual(p.APITokens, []string{"disposable-" + route.ModelID}) {
			t.Fatalf("model %s lost its provider configuration", route.ModelID)
		}
		if len(name) > 63 || name == openAIResourceName("other", route.ModelID) {
			t.Fatal("unsafe resource ownership")
		}
		if !strings.Contains(resources[3].Body, name) {
			t.Fatal("statistics lost a model route")
		}
	}
	d.Routes[0].Enabled = false
	_, active, _, err := Render(d, nil)
	if err != nil || len(active) != 3 || strings.Contains(active[1].Body, openAIResourceName("demo", "alpha")) {
		t.Fatal("disabled model still matches ai-proxy")
	}
}

func TestNativePluginsFollowEveryOpenAIModelIngress(t *testing.T) {
	d, p, cfg := nativeFixture()
	d.Routes = append(d.Routes, Route{ModelID: "second", Enabled: true})
	limits, err := RenderGatewayLimits(d, p, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	quota, err := RenderGatewayQuota(d, cfg, "", "disposable-admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range append(limits, quota[1]) {
		for _, id := range []string{"a", "second"} {
			if !strings.Contains(resource.Body, openAIResourceName(d.TenantID(), id)) {
				t.Fatal("native plugin lost a model ingress")
			}
		}
		if strings.Contains(resource.Body, openAIResourceName(d.TenantID(), "disabled")) {
			t.Fatal("disabled route matched")
		}
	}
}
