package dataplane

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func asMap(t *testing.T, m proto.Message) map[string]any {
	t.Helper()
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		t.Fatal("fixture decode")
	}
	return out
}

func fixture(t *testing.T) (*Desired, map[string]any) {
	t.Helper()
	input := gatewayrender.Input{Deployment: "test", Revision: "one", Routes: []gatewayrender.Route{
		{ModelID: "chat", Enabled: true, Protocol: "openai-compatible", Endpoint: "https://provider.example/v1", UpstreamModel: "upstream-chat", CredentialRef: &gatewayrender.SecretReference{Name: "provider", Key: "key"}},
		{ModelID: "claude", Enabled: true, Protocol: "anthropic", Endpoint: "https://anthropic.example/v1", UpstreamModel: "upstream-claude", CredentialRef: &gatewayrender.SecretReference{Name: "provider", Key: "key"}},
	}}
	_, resources, _, err := gatewayrender.Render(input, map[string]string{"provider/key": "disposable-fixture-value"})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := Compile(resources, []string{"removed-route"})
	if err != nil {
		t.Fatal(err)
	}
	routes, clusters, filters, ecds := []any{}, []any{}, []any{}, []any{}
	for name, i := range desired.ingresses {
		for _, kind := range []string{"path", "prefix"} {
			match := map[string]any{"case_sensitive": true, kind: i.Path}
			if kind == "prefix" {
				match[kind] = i.Path + "/"
			}
			if i.Model != "" {
				match["headers"] = []any{map[string]any{"name": "x-aep-model-id", "string_match": map[string]any{"exact": i.Model}}}
			}
			r := asMap(t, i.Patch)
			r["name"] = name
			r["match"] = match
			routes = append(routes, r)
		}
		c := asMap(t, i.Cluster)
		c["@type"] = "type.googleapis.com/envoy.config.cluster.v3.Cluster"
		clusters = append(clusters, map[string]any{"cluster": c})
	}
	for name, p := range desired.plugins {
		config, _ := json.Marshal(map[string]any{"_rules_": p.Rules})
		filters = append(filters, map[string]any{"name": name, "config_discovery": map[string]any{"config_source": map[string]any{"ads": map[string]any{}}}})
		ecds = append(ecds, map[string]any{"ecds_filter": map[string]any{"name": name, "typed_config": map[string]any{
			"@type":  "type.googleapis.com/envoy.extensions.filters.http.wasm.v3.Wasm",
			"config": map[string]any{"fail_open": p.FailOpen, "vm_config": map[string]any{"runtime": "envoy.wasm.runtime.v8", "code": map[string]any{"local": map[string]any{"filename": "/var/lib/istio/data/cache/" + p.Digest + ".wasm"}}}, "configuration": map[string]any{"@type": "type.googleapis.com/google.protobuf.StringValue", "value": string(config)}},
		}}})
	}
	listener := map[string]any{"address": map[string]any{"socket_address": map[string]any{"address": "0.0.0.0", "port_value": 80}}, "filter_chains": []any{map[string]any{"filters": []any{map[string]any{"typed_config": map[string]any{
		"@type":         "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
		"scoped_routes": map[string]any{"name": "scoped-route", "scope_key_builder": map[string]any{"fragments": []any{map[string]any{"local_port_value_extractor": map[string]any{}}, map[string]any{"host_value_extractor": map[string]any{}}}}, "scoped_rds": map[string]any{"scoped_rds_config_source": map[string]any{"ads": map[string]any{}}}},
		"http_filters":  filters,
	}}}}}}
	dump := map[string]any{"configs": []any{
		map[string]any{"@type": "type.googleapis.com/envoy.admin.v3.ListenersConfigDump", "dynamic_listeners": []any{map[string]any{"active_state": map[string]any{"listener": listener}}}},
		map[string]any{"@type": "type.googleapis.com/envoy.admin.v3.ScopedRoutesConfigDump", "dynamic_scoped_route_configs": []any{map[string]any{"name": "scoped-route", "scoped_route_configs": []any{map[string]any{"route_configuration_name": "routes", "key": map[string]any{"fragments": []any{map[string]any{"string_key": "80"}, map[string]any{"string_key": "*"}}}}}}}},
		map[string]any{"@type": "type.googleapis.com/envoy.admin.v3.RoutesConfigDump", "dynamic_route_configs": []any{map[string]any{"route_config": map[string]any{"name": "routes", "virtual_hosts": []any{map[string]any{"domains": []any{"*"}, "routes": routes}}}}}},
		map[string]any{"@type": "type.googleapis.com/envoy.admin.v3.ClustersConfigDump", "dynamic_active_clusters": clusters},
		map[string]any{"@type": "type.googleapis.com/envoy.admin.v3.EcdsConfigDump", "ecds_filters": ecds},
	}}
	return desired, dump
}

func arr(m map[string]any, key string) []any               { return m[key].([]any) }
func mp(v any) map[string]any                              { return v.(map[string]any) }
func config(dump map[string]any, index int) map[string]any { return mp(arr(dump, "configs")[index]) }
func routeList(dump map[string]any) map[string]any {
	return mp(arr(mp(mp(arr(config(dump, 2), "dynamic_route_configs")[0])["route_config"]), "virtual_hosts")[0])
}
func firstRoute(dump map[string]any) map[string]any { return mp(arr(routeList(dump), "routes")[0]) }
func hcm(dump map[string]any) map[string]any {
	listener := mp(mp(mp(arr(config(dump, 0), "dynamic_listeners")[0])["active_state"])["listener"])
	return mp(mp(arr(mp(arr(listener, "filter_chains")[0]), "filters")[0])["typed_config"])
}
func firstWasm(dump map[string]any) map[string]any {
	return mp(mp(mp(arr(config(dump, 4), "ecds_filters")[0])["ecds_filter"])["typed_config"])
}

func TestLoadedConfigurationRequiresActiveCompleteMatchingSnapshot(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Desired, map[string]any)
	}{
		{"matching", func(*Desired, map[string]any) {}},
		{"no-active-listener", func(_ *Desired, d map[string]any) { config(d, 0)["dynamic_listeners"] = []any{} }},
		{"warming-only", func(_ *Desired, d map[string]any) {
			l := mp(arr(config(d, 0), "dynamic_listeners")[0])
			l["warming_state"] = l["active_state"]
			delete(l, "active_state")
		}},
		{"detached-rds", func(_ *Desired, d map[string]any) { hcm(d)["scoped_routes"] = map[string]any{"name": "absent"} }},
		{"wrong-scope-port", func(_ *Desired, d map[string]any) {
			e := mp(arr(config(d, 1), "dynamic_scoped_route_configs")[0])
			s := mp(arr(e, "scoped_route_configs")[0])
			mp(arr(mp(s["key"]), "fragments")[0])["string_key"] = "443"
		}},
		{"unsupported-builder", func(_ *Desired, d map[string]any) {
			mp(hcm(d)["scoped_routes"])["scope_key_builder"] = map[string]any{}
		}},
		{"wrong-route-path", func(_ *Desired, d map[string]any) { mp(firstRoute(d)["match"])["path"] = "/wrong" }},
		{"case-insensitive", func(_ *Desired, d map[string]any) { mp(firstRoute(d)["match"])["case_sensitive"] = false }},
		{"runtime-match", func(_ *Desired, d map[string]any) { mp(firstRoute(d)["match"])["runtime_fraction"] = map[string]any{} }},
		{"missing-route", func(_ *Desired, d map[string]any) { r := routeList(d); r["routes"] = arr(r, "routes")[1:] }},
		{"wrong-cluster", func(_ *Desired, d map[string]any) { mp(firstRoute(d)["route"])["cluster"] = "other" }},
		{"wrong-host", func(_ *Desired, d map[string]any) { mp(firstRoute(d)["route"])["host_rewrite_literal"] = "other" }},
		{"unintended-rewrite", func(_ *Desired, d map[string]any) { mp(firstRoute(d)["route"])["prefix_rewrite"] = "/steal" }},
		{"wrong-endpoint", func(_ *Desired, d map[string]any) {
			c := mp(mp(arr(config(d, 3), "dynamic_active_clusters")[0])["cluster"])
			c["load_assignment"] = map[string]any{"cluster_name": "other"}
		}},
		{"tls-missing", func(_ *Desired, d map[string]any) {
			c := mp(mp(arr(config(d, 3), "dynamic_active_clusters")[0])["cluster"])
			delete(c, "transport_socket")
		}},
		{"cluster-warming-only", func(_ *Desired, d map[string]any) {
			c := config(d, 3)
			c["dynamic_warming_clusters"] = c["dynamic_active_clusters"]
			delete(c, "dynamic_active_clusters")
		}},
		{"detached-ecds", func(_ *Desired, d map[string]any) { hcm(d)["http_filters"] = []any{} }},
		{"disabled-filter", func(_ *Desired, d map[string]any) { mp(arr(hcm(d), "http_filters")[0])["disabled"] = true }},
		{"missing-plugin", func(_ *Desired, d map[string]any) { config(d, 4)["ecds_filters"] = []any{} }},
		{"fallback-rbac", func(_ *Desired, d map[string]any) {
			firstWasm(d)["@type"] = "type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC"
		}},
		{"artifact-mismatch", func(_ *Desired, d map[string]any) {
			c := mp(firstWasm(d)["config"])
			mp(mp(mp(c["vm_config"])["code"])["local"])["filename"] = "/wrong.wasm"
		}},
		{"artifact-parent-directory", func(_ *Desired, d map[string]any) {
			c := mp(firstWasm(d)["config"])
			local := mp(mp(mp(c["vm_config"])["code"])["local"])
			local["filename"] = "/cache/../" + strings.TrimPrefix(local["filename"].(string), "/var/lib/istio/data/cache/")
		}},
		{"stale-secret-or-rules", func(_ *Desired, d map[string]any) {
			mp(mp(firstWasm(d)["config"])["configuration"])["value"] = `{"_rules_":[{"provider":{"apiTokens":["old-disposable"]}}]}`
		}},
		{"removed-route-still-loaded", func(_ *Desired, d map[string]any) { firstRoute(d)["name"] = "removed-route" }},
		{"untrusted-domain", func(_ *Desired, d map[string]any) { routeList(d)["domains"] = []any{"other.example"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired, dump := fixture(t)
			tc.mutate(desired, dump)
			raw, _ := json.Marshal(dump)
			err := desired.Verify(raw)
			if (err == nil) != (tc.name == "matching") {
				t.Fatalf("verification result = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "disposable") {
				t.Fatal("configuration leaked")
			}
		})
	}
}

// This fixed mock-only wire snapshot is deliberately not reconstructed from
// Compile during the test. Renderer/Compile changes must continue to match it
// or require explicit fixture review; the live Kind check remains separate.
func TestRendererMatchesPinnedSyntheticHigressWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/higress-2.2.4-synthetic-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	desired, _ := fixture(t)
	if err := desired.Verify(raw); err != nil {
		t.Fatalf("renderer no longer matches pinned wire fixture: %v", err)
	}
	for _, route := range desired.ingresses {
		route.Path = "/different-expected-path"
	}
	if desired.Verify(raw) == nil {
		t.Fatal("fixed wire fixture followed changed expected route paths")
	}
}

func TestVerificationRejectsMalformedAndOversizedInput(t *testing.T) {
	d, _ := fixture(t)
	for _, raw := range [][]byte{nil, []byte("{"), []byte(`{"configs":[]}`), make([]byte, MaxDumpBytes+1)} {
		if d.Verify(raw) == nil {
			t.Fatal("invalid dump accepted")
		}
	}
	for _, resource := range []gatewayrender.RenderedResource{{Body: "{"}, {Body: `{"kind":"Secret","metadata":{"name":"bad","namespace":"higress-system"}}`}} {
		if _, err := Compile([]gatewayrender.RenderedResource{resource}, nil); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
