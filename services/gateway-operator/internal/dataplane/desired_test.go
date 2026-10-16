package dataplane

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	"sigs.k8s.io/yaml"
)

func TestCompileRejectsUnsupportedRendererInputs(t *testing.T) {
	cases := []struct {
		name, kind string
		mutate     func(map[string]any)
	}{
		{"foreign-namespace", "Ingress", func(o map[string]any) { mp(o["metadata"])["namespace"] = "other" }},
		{"unnamed-resource", "Ingress", func(o map[string]any) { delete(mp(o["metadata"]), "name") }},
		{"missing-ingress-rule", "Ingress", func(o map[string]any) { mp(o["spec"])["rules"] = []any{} }},
		{"unsupported-path-type", "Ingress", func(o map[string]any) {
			rule := mp(arr(mp(o["spec"]), "rules")[0])
			mp(arr(mp(rule["http"]), "paths")[0])["pathType"] = "Exact"
		}},
		{"untrusted-backend", "Ingress", func(o map[string]any) {
			rule := mp(arr(mp(o["spec"]), "rules")[0])
			path := mp(arr(mp(rule["http"]), "paths")[0])
			mp(mp(path["backend"])["service"])["name"] = "other-service"
		}},
		{"mutable-plugin-artifact", "WasmPlugin", func(o map[string]any) { mp(o["spec"])["url"] = "oci://example.invalid/plugin:latest" }},
		{"globally-enabled-plugin", "WasmPlugin", func(o map[string]any) { mp(o["spec"])["defaultConfigDisable"] = false }},
		{"disabled-plugin-match", "WasmPlugin", func(o map[string]any) {
			mp(arr(mp(o["spec"]), "matchRules")[0])["configDisable"] = true
		}},
		{"unscoped-plugin-match", "WasmPlugin", func(o map[string]any) {
			mp(arr(mp(o["spec"]), "matchRules")[0])["ingress"] = []any{}
		}},
		{"orphan-envoy-filter", "EnvoyFilter", func(o map[string]any) { mp(o["metadata"])["name"] = "unrelated-route" }},
		{"missing-envoy-patch", "EnvoyFilter", func(o map[string]any) {
			spec := mp(o["spec"])
			spec["configPatches"] = arr(spec, "configPatches")[:1]
		}},
		{"invalid-cluster-protobuf", "EnvoyFilter", func(o map[string]any) {
			patch := mp(arr(mp(o["spec"]), "configPatches")[0])
			mp(mp(patch["patch"])["value"])["unknownProtoField"] = true
		}},
		{"unrelated-cluster", "EnvoyFilter", func(o map[string]any) {
			patch := mp(arr(mp(o["spec"]), "configPatches")[0])
			mp(mp(patch["patch"])["value"])["name"] = "unrelated-cluster"
		}},
		{"invalid-route-protobuf", "EnvoyFilter", func(o map[string]any) {
			patch := mp(arr(mp(o["spec"]), "configPatches")[1])
			mp(mp(patch["patch"])["value"])["unknownProtoField"] = true
		}},
		{"unsupported-patch-operation", "EnvoyFilter", func(o map[string]any) {
			patch := mp(arr(mp(o["spec"]), "configPatches")[1])
			mp(patch["patch"])["operation"] = "REMOVE"
		}},
		{"missing-route-patch", "EnvoyFilter", func(o map[string]any) {
			spec := mp(o["spec"])
			patches := arr(spec, "configPatches")
			spec["configPatches"] = []any{patches[0], patches[0]}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resources, _, err := gatewayrender.Render(gatewayrender.Input{
				Deployment: "test", Revision: "one",
				Routes: []gatewayrender.Route{{ModelID: "chat", Enabled: true, Protocol: "openai-compatible", Endpoint: "https://provider.example/v1"}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Compile(resources, nil); err != nil {
				t.Fatalf("valid renderer fixture rejected: %v", err)
			}
			changed := false
			for i := range resources {
				var object map[string]any
				if err := yaml.Unmarshal([]byte(resources[i].Body), &object); err != nil {
					t.Fatal(err)
				}
				if object["kind"] != tc.kind {
					continue
				}
				tc.mutate(object)
				raw, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				resources[i].Body = string(raw)
				changed = true
				break
			}
			if !changed {
				t.Fatalf("fixture contains no %s", tc.kind)
			}
			if desired, err := Compile(resources, nil); !errors.Is(err, errUnsupported) || desired != nil {
				t.Fatalf("unsupported input produced usable verification state: %v", err)
			}
		})
	}
}
