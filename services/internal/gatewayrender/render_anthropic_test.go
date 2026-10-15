package gatewayrender

import (
	"regexp"
	"strings"
	"testing"
)

func anthropicRoute(modelID, endpoint string) Route {
	return Route{ModelID: modelID, Enabled: true, Endpoint: endpoint, UpstreamModel: "upstream", Protocol: "anthropic", CredentialRef: &SecretReference{Name: "aep-credential-bigmodel", Key: "api-key"}}
}

func TestRenderAnthropicPassthrough(t *testing.T) {
	t.Parallel()
	route := anthropicRoute("bench-anthropic", "https://open.bigmodel.cn/api/anthropic")
	desired := Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{route}}
	document, resources, digest, err := Render(desired, map[string]string{"aep-credential-bigmodel/api-key": "bigmodel-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 4 {
		t.Fatalf("resources = %#v", resources)
	}
	if resources[0].Kind != ResourceAnthropicIngress || resources[1].Kind != ResourceEnvoyFilter || resources[2].Kind != ResourceWasmPlugin || resources[3].Kind != ResourceAIStatistics {
		t.Fatalf("resource order = %#v", resources)
	}
	if resources[0].APIPath != IngressAPIPath(AnthropicResourceName("bench-anthropic")) || resources[1].APIPath != EnvoyFilterAPIPath(AnthropicResourceName("bench-anthropic")) {
		t.Fatalf("anthropic API paths = %q %q", resources[0].APIPath, resources[1].APIPath)
	}

	ingress := resources[0].Body
	if !strings.Contains(ingress, "name: 'aep-anthropic-"+ResourceSuffix("bench-anthropic")+"'") || !strings.Contains(ingress, "path: '/bench-anthropic'") {
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
		"route:\n              name: 'aep-anthropic-" + ResourceSuffix("bench-anthropic") + "'",
		"cluster: 'aep-anthropic-" + ResourceSuffix("bench-anthropic") + "'",
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

	// The document joins all four bodies in resource order and the digest is
	// stable across renders.
	if strings.Count(document, "---\n") != 3 {
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
	_, resources, _, err := Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{route}}, nil)
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
	_, resources, _, err = Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{route}}, nil)
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
	_, resources, _, err := Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{second, first}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 6 { // ingress+filter per model, then both wasm plugins
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
			if _, _, _, err := Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{invalid}}, nil); err == nil {
				t.Fatal("invalid anthropic route was accepted")
			}
		})
	}

	// Two models whose sanitized slugs collide cannot share path prefixes.
	collideA := anthropicRoute("Bench GLM", "https://open.bigmodel.cn/api/anthropic")
	collideB := anthropicRoute("bench-glm", "https://api.anthropic.com")
	if _, _, _, err := Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{collideA, collideB}}, nil); err == nil {
		t.Fatal("slug collision was accepted")
	}
}

func TestRenderMixedProtocolsKeepOpenAIShape(t *testing.T) {
	t.Parallel()
	openai := Route{ModelID: "chat", Enabled: true, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible"}
	desired := Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{openai, anthropicRoute("bench-anthropic", "https://open.bigmodel.cn/api/anthropic")}}
	document, resources, _, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resources[0].Kind != ResourceOpenAIIngress || resources[1].Kind != ResourceAnthropicIngress || resources[2].Kind != ResourceEnvoyFilter || resources[3].Kind != ResourceWasmPlugin || resources[4].Kind != ResourceAIStatistics {
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
	if got := anthropicSlug(strings.Repeat("x", 50)); len(got) != 40 {
		t.Fatalf("slug length = %d", len(got))
	}
	// Regression: ids longer than the old 30-char cap must route verbatim.
	long := "bench-qwen3-8-flash-next-llama-cpp-l20-2"
	if got := anthropicSlug(long); got != long {
		t.Fatalf("long id slug = %q, want verbatim %q", got, long)
	}
	if got := anthropicRewrite(""); got != "/\\1" {
		t.Fatalf("root rewrite = %q", got)
	}
	if got := anthropicRewrite("/api/anthropic/"); got != "/api/anthropic/\\1" {
		t.Fatalf("path rewrite = %q", got)
	}
	name := AnthropicResourceName("bench-anthropic")
	if !strings.HasPrefix(name, "aep-anthropic-") || len(name) > 63 {
		t.Fatalf("resource name = %q", name)
	}
}

func TestRenderAnthropicVersionedEndpointPaths(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		endpoint     string
		pattern      string
		substitution string
		paths        map[string]string
	}{
		{
			endpoint: "http://provider.example/v1", pattern: "^/bench-anthropic/(?:v1/)?(.*)$", substitution: "/v1/\\1",
			paths: map[string]string{
				"/bench-anthropic/messages":                 "/v1/messages",
				"/bench-anthropic/v1/messages":              "/v1/messages",
				"/bench-anthropic/v1/messages/count_tokens": "/v1/messages/count_tokens",
			},
		},
		{
			endpoint: "https://provider.example/api/anthropic/v1/", pattern: "^/bench-anthropic/(?:v1/)?(.*)$", substitution: "/api/anthropic/v1/\\1",
			paths: map[string]string{"/bench-anthropic/v1/messages": "/api/anthropic/v1/messages"},
		},
		{
			endpoint: "https://provider.example/api/anthropic", pattern: "^/bench-anthropic/(.*)$", substitution: "/api/anthropic/\\1",
			paths: map[string]string{"/bench-anthropic/v1/messages": "/api/anthropic/v1/messages"},
		},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			_, resources, _, err := Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{anthropicRoute("bench-anthropic", test.endpoint)}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			filter := resources[1].Body
			if !strings.Contains(filter, "regex: "+yamlScalar(test.pattern)) || !strings.Contains(filter, "substitution: "+yamlScalar(test.substitution)) {
				t.Fatalf("unexpected path rewrite for %q", test.endpoint)
			}
			pattern := regexp.MustCompile(test.pattern)
			for input, want := range test.paths {
				matches := pattern.FindStringSubmatch(input)
				if len(matches) != 2 {
					t.Fatalf("rewrite pattern did not match %q", input)
				}
				if got := strings.Replace(test.substitution, `\1`, matches[1], 1); got != want {
					t.Errorf("rewrite %q = %q, want %q", input, got, want)
				}
			}
		})
	}
}
