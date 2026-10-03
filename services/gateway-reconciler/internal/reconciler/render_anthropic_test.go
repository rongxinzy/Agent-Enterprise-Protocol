package reconciler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func anthropicRoute(modelID, endpoint string) Route {
	return Route{ModelID: modelID, Enabled: true, Endpoint: endpoint, UpstreamModel: "upstream", Protocol: "anthropic", CredentialRef: &SecretReference{Name: "aep-credential-bigmodel", Key: "api-key"}}
}

func TestRenderAnthropicPassthrough(t *testing.T) {
	t.Parallel()
	route := anthropicRoute("bench-anthropic", "https://open.bigmodel.cn/api/anthropic")
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{route}}
	document, resources, digest, err := Render(desired, map[string]string{"aep-credential-bigmodel/api-key": "bigmodel-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 3 {
		t.Fatalf("resources = %#v", resources)
	}
	if resources[0].Kind != ResourceAnthropicIngress || resources[1].Kind != ResourceEnvoyFilter || resources[2].Kind != ResourceWasmPlugin {
		t.Fatalf("resource order = %#v", resources)
	}
	if resources[0].APIPath != ingressAPIPath(anthropicResourceName("bench-anthropic")) || resources[1].APIPath != envoyFilterAPIPath(anthropicResourceName("bench-anthropic")) {
		t.Fatalf("anthropic API paths = %q %q", resources[0].APIPath, resources[1].APIPath)
	}

	ingress := resources[0].Body
	if !strings.Contains(ingress, "name: 'aep-anthropic-"+resourceSuffix("bench-anthropic")+"'") || !strings.Contains(ingress, "path: '/bench-anthropic'") {
		t.Fatalf("anthropic ingress = %s", ingress)
	}

	filter := resources[1].Body
	for _, expected := range []string{
		"kind: EnvoyFilter",
		"applyTo: CLUSTER",
		"type: STRICT_DNS",
			"dns_lookup_family: V4_ONLY",
		"address: 'open.bigmodel.cn'",
		"port_value: 443",
		"sni: 'open.bigmodel.cn'",
		"route:\n              name: 'aep-anthropic-" + resourceSuffix("bench-anthropic") + "'",
		"cluster: 'aep-anthropic-" + resourceSuffix("bench-anthropic") + "'",
		"host_rewrite_literal: 'open.bigmodel.cn'",
		"regex: '^/bench-anthropic/(.*)$'",
		"substitution: '/api/anthropic/\\1'",
		"key: x-api-key\n                value: 'bigmodel-secret'",
		"key: authorization\n                value: 'Bearer bigmodel-secret'",
	} {
		if !strings.Contains(filter, expected) {
			t.Fatalf("envoy filter missing %q:\n%s", expected, filter)
		}
	}

	// The WasmPlugin keeps its idle shape and never references the anthropic
	// route; ai-proxy only engages the tenant's openai ingresses.
	if !strings.Contains(resources[2].Body, "matchRules: []") {
		t.Fatalf("idle wasm plugin = %s", resources[2].Body)
	}

	// The document joins all three bodies in resource order and the digest is
	// stable across renders.
	if strings.Count(document, "---\n") != 2 {
		t.Fatalf("document separator count wrong: %s", document)
	}
	again, _, againDigest, err := Render(desired, map[string]string{"aep-credential-bigmodel/api-key": "bigmodel-secret"})
	if err != nil || again != document || againDigest != digest {
		t.Fatal("anthropic render is not deterministic")
	}
}

func TestRenderAnthropicEndpointVariations(t *testing.T) {
	t.Parallel()
	// No endpoint path: strip the slug only. http scheme: no TLS block, port 80.
	route := anthropicRoute("bench-anthropic", "http://internal-gateway.svc:8000")
	_, resources, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{route}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	filter := resources[1].Body
	if !strings.Contains(filter, "port_value: 8000") || strings.Contains(filter, "sni:") || strings.Contains(filter, "transport_socket") {
		t.Fatalf("http endpoint filter = %s", filter)
	}
	if !strings.Contains(filter, "substitution: '/\\1'") {
		t.Fatalf("slug-only substitution missing: %s", filter)
	}
	// Without a resolved credential the header block is omitted entirely; the
	// upstream then answers 401 itself.
	if strings.Contains(filter, "x-api-key") || strings.Contains(filter, "authorization") {
		t.Fatalf("unresolved credential must not render headers: %s", filter)
	}

	// Trailing slashes on the endpoint path normalize away.
	route = anthropicRoute("bench-anthropic", "https://open.bigmodel.cn/api/anthropic/")
	_, resources, _, err = Render(DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{route}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resources[1].Body, "substitution: '/api/anthropic/\\1'") {
		t.Fatalf("trailing-slash substitution = %s", resources[1].Body)
	}
}

func TestRenderAnthropicMultipleModelsAndErrors(t *testing.T) {
	t.Parallel()
	first := anthropicRoute("alpha-anthropic", "https://open.bigmodel.cn/api/anthropic")
	second := anthropicRoute("beta-anthropic", "https://api.anthropic.com")
	_, resources, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{second, first}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 5 { // ingress+filter per model, then the wasm plugin
		t.Fatalf("multi-model resources = %#v", resources)
	}
	if !strings.Contains(resources[0].Body, "path: '/alpha-anthropic'") || !strings.Contains(resources[2].Body, "path: '/beta-anthropic'") {
		t.Fatalf("models not rendered in sorted order: %s %s", resources[0].Body, resources[2].Body)
	}

	for name, invalid := range map[string]Route{
		"provider type":  func() Route { r := first; r.ProviderType = "openai"; return r }(),
		"relative":       func() Route { r := first; r.Endpoint = "/v1"; return r }(),
		"missing scheme": func() Route { r := first; r.Endpoint = "open.bigmodel.cn"; return r }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{invalid}}, nil); err == nil {
				t.Fatal("invalid anthropic route was accepted")
			}
		})
	}

	// Two models whose sanitized slugs collide cannot share path prefixes.
	collideA := anthropicRoute("Bench GLM", "https://open.bigmodel.cn/api/anthropic")
	collideB := anthropicRoute("bench-glm", "https://api.anthropic.com")
	if _, _, _, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{collideA, collideB}}, nil); err == nil {
		t.Fatal("slug collision was accepted")
	}
}

func TestRenderMixedProtocolsKeepOpenAIShape(t *testing.T) {
	t.Parallel()
	openai := Route{ModelID: "chat", Enabled: true, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible"}
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{openai, anthropicRoute("bench-anthropic", "https://open.bigmodel.cn/api/anthropic")}}
	document, resources, _, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resources[0].Kind != ResourceOpenAIIngress || resources[1].Kind != ResourceAnthropicIngress || resources[2].Kind != ResourceEnvoyFilter || resources[3].Kind != ResourceWasmPlugin {
		t.Fatalf("pinned resource order = %#v", resources)
	}
	if !strings.Contains(resources[0].Body, "path: '/v1'") || !strings.Contains(document, "matchRules:\n    - config:") {
		t.Fatalf("openai rendering drifted: %s", document)
	}
}

func TestAnthropicSlugAndRewriteHelpers(t *testing.T) {
	t.Parallel()
	if got := anthropicSlug("Bench_GLM/01"); got != "bench-glm-01" {
		t.Fatalf("anthropicSlug = %q", got)
	}
	if got := anthropicSlug("  "); got != "model" {
		t.Fatalf("empty slug fallback = %q", got)
	}
	if got := anthropicSlug(strings.Repeat("x", 50)); len(got) != 30 {
		t.Fatalf("slug length = %d", len(got))
	}
	if got := anthropicRewrite(""); got != "/\\1" {
		t.Fatalf("root rewrite = %q", got)
	}
	if got := anthropicRewrite("/api/anthropic/"); got != "/api/anthropic/\\1" {
		t.Fatalf("path rewrite = %q", got)
	}
	name := anthropicResourceName("bench-anthropic")
	if !strings.HasPrefix(name, "aep-anthropic-") || len(name) > 63 {
		t.Fatalf("resource name = %q", name)
	}
}

func TestKubernetesApplierDeletesAnthropicPairWhenRouteDisabled(t *testing.T) {
	t.Parallel()
	operations := make([]string, 0, 8)
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		operations = append(operations, request.Method+" "+request.URL.Path)
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	disabled := anthropicRoute("bench-anthropic", "https://open.bigmodel.cn/api/anthropic")
	disabled.Enabled = false
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-2", Routes: []Route{disabled}}
	_, resources, _, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), desired, resources); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	name := anthropicResourceName("bench-anthropic")
	seen := make(map[string]bool, len(operations))
	for _, operation := range operations {
		seen[operation] = true
	}
	// Disabled anthropic route: its Ingress+EnvoyFilter pair is deleted (plus
	// the idle openai ingress per the no-enabled-openai rule); the WasmPlugin
	// is still applied.
	for _, path := range []string{
		"DELETE " + ingressAPIPath(name),
		"DELETE " + envoyFilterAPIPath(name),
		"DELETE " + openAIIngressAPIPath(resourceSuffix("demo")),
		"PATCH " + wasmPluginAPIPath(resourceSuffix("demo")),
	} {
		if !seen[path] {
			t.Fatalf("missing %q in %#v", path, operations)
		}
	}
	if len(operations) != 4 {
		t.Fatalf("operations = %#v", operations)
	}
}
