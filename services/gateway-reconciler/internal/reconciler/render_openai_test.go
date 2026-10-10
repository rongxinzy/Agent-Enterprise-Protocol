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
	if len(resources) != 6 {
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
	if err := yaml.Unmarshal([]byte(resources[4].Body), &plugin); err != nil {
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
		if err := yaml.Unmarshal([]byte(resources[2*i].Body), &ingress); err != nil {
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
		if !strings.Contains(resources[5].Body, name) {
			t.Fatal("statistics lost a model route")
		}
		filter := resources[2*i+1]
		for _, expected := range []string{"address: '" + route.ModelID + ".example'", "cluster: '" + name + "'", "host_rewrite_literal: '" + route.ModelID + ".example'", "name: '" + name + "'"} {
			if filter.Kind != ResourceEnvoyFilter || !strings.Contains(filter.Body, expected) {
				t.Fatalf("model %s did not select its own upstream: missing %s", route.ModelID, expected)
			}
		}
		if strings.Contains(filter.Body, "disposable-") || strings.Contains(filter.Body, "request_headers_to_add") {
			t.Fatal("OpenAI credentials must remain in ai-proxy only")
		}
	}
	d.Routes[0].Enabled = false
	_, active, _, err := Render(d, nil)
	if err != nil || len(active) != 4 || strings.Contains(active[2].Body, openAIResourceName("demo", "alpha")) {
		t.Fatal("disabled model still matches ai-proxy")
	}
}

func TestOpenAIUpstreamTransportAndClientPaths(t *testing.T) {
	for _, test := range []struct {
		endpoint, host, authority, port string
		tls                             bool
	}{
		{"http://internal.example:8000/api/v1", "internal.example", "internal.example:8000", "8000", false},
		{"https://api.example/api/v1/", "api.example", "api.example", "443", true},
		{"https://api.example:8443", "api.example", "api.example:8443", "8443", true},
		{"http://internal.example", "internal.example", "internal.example", "80", false},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			d := DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: test.endpoint, UpstreamModel: "upstream"}}}
			_, resources, _, err := Render(d, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(resources) != 4 || !strings.Contains(resources[0].Body, "path: '/v1'") {
				t.Fatal("client path changed to provider path")
			}
			filter := resources[1].Body
			for _, expected := range []string{"type: STRICT_DNS", "address: '" + test.host + "'", "port_value: " + test.port, "host_rewrite_literal: '" + test.authority + "'"} {
				if !strings.Contains(filter, expected) {
					t.Fatalf("missing upstream configuration %s", expected)
				}
			}
			if strings.Contains(filter, "regex_rewrite") {
				t.Fatal("upstream filter overrides ai-proxy path transformation")
			}
			if strings.Contains(filter, "transport_socket:") != test.tls {
				t.Fatal("wrong transport protocol")
			}
			if test.tls {
				for _, expected := range []string{"sni: '" + test.host + "'", "filename: /etc/ssl/certs/ca-certificates.crt", "san_type: DNS", "exact: '" + test.host + "'"} {
					if !strings.Contains(filter, expected) {
						t.Fatalf("missing TLS validation %s", expected)
					}
				}
			}
		})
	}
	_, relative, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: "/v1"}}}, nil)
	if err != nil || len(relative) != 3 || relative[1].Kind != ResourceWasmPlugin {
		t.Fatal("relative backend compatibility lost")
	}
}

func TestOpenAIRejectsUnsafeAbsoluteEndpoints(t *testing.T) {
	for _, endpoint := range []string{"//api.example/v1", "ftp://api.example/v1", "api.example/v1", "https://user:password@api.example/v1", "https://api.example/v1?key=value", "https://api.example/v1#fragment", "https://api.example:0/v1", "https://api.example:65536/v1", "https://api.example:invalid/v1"} {
		if _, _, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: endpoint}}}, nil); err == nil {
			t.Fatalf("accepted unsafe endpoint %s", endpoint)
		}
	}
	_, _, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: "ftp://user:disposable-password@api.example/v1"}}}, nil)
	if err == nil || strings.Contains(err.Error(), "disposable-password") {
		t.Fatal("endpoint validation leaked credentials")
	}
}

func TestOpenAIHTTPSIPAddressValidatesIPSAN(t *testing.T) {
	_, resources, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: "https://192.0.2.1:8443/v1"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resources[1].Body, "san_type: IP_ADDRESS") || !strings.Contains(resources[1].Body, "exact: '192.0.2.1'") {
		t.Fatal("IP endpoint lacks IP certificate identity validation")
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
