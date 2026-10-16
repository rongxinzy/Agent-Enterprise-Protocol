package dataplane

import (
	"encoding/json"
	"testing"
)

func TestLoadedPluginRejectsUnsafePolicyAndConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		accept bool
		mutate func(*plugin, map[string]any)
	}{
		{"explicit-fail-open-overrides-legacy", true, func(p *plugin, wasm map[string]any) {
			p.FailOpen = true
			mp(wasm["config"])["fail_open"] = false
			mp(wasm["config"])["failure_policy"] = "FAIL_OPEN"
		}},
		{"explicit-fail-closed-overrides-legacy", true, func(p *plugin, wasm map[string]any) {
			p.FailOpen = false
			mp(wasm["config"])["fail_open"] = true
			mp(wasm["config"])["failure_policy"] = "FAIL_CLOSED"
		}},
		{"fail-open-mismatch", false, func(p *plugin, wasm map[string]any) {
			p.FailOpen = false
			mp(wasm["config"])["failure_policy"] = "FAIL_OPEN"
		}},
		{"unknown-failure-policy", false, func(_ *plugin, wasm map[string]any) {
			mp(wasm["config"])["failure_policy"] = 99
		}},
		{"invalid-protobuf", false, func(_ *plugin, wasm map[string]any) { wasm["unknownProtoField"] = true }},
		{"missing-plugin-config", false, func(_ *plugin, wasm map[string]any) { delete(wasm, "config") }},
		{"wrong-configuration-type", false, func(_ *plugin, wasm map[string]any) {
			mp(wasm["config"])["configuration"] = map[string]any{"@type": "type.googleapis.com/google.protobuf.BoolValue", "value": true}
		}},
		{"malformed-private-configuration", false, func(_ *plugin, wasm map[string]any) {
			mp(mp(wasm["config"])["configuration"])["value"] = "{"
		}},
		{"extra-private-configuration-field", false, func(_ *plugin, wasm map[string]any) {
			configuration := mp(mp(wasm["config"])["configuration"])
			var fields map[string]any
			if err := json.Unmarshal([]byte(configuration["value"].(string)), &fields); err != nil {
				t.Fatal(err)
			}
			fields["unexpectedGlobalRule"] = true
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			configuration["value"] = string(raw)
		}},
		{"empty-disabled-rules", true, func(p *plugin, wasm map[string]any) {
			p.Rules = nil
			mp(mp(wasm["config"])["configuration"])["value"] = `{"_rules_":[]}`
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired, dump := fixture(t)
			filter := mp(arr(config(dump, 4), "ecds_filters")[0])["ecds_filter"]
			name := mp(filter)["name"].(string)
			tc.mutate(desired.plugins[name], firstWasm(dump))
			raw, err := json.Marshal(dump)
			if err != nil {
				t.Fatal(err)
			}
			if err := desired.Verify(raw); (err == nil) != tc.accept {
				t.Fatalf("loaded plugin policy verification = %v", err)
			}
		})
	}
}
