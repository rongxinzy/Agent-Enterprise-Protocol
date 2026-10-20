package config

import (
	"testing"
	"time"
)

func TestAccessRelayRequiresExplicitOptInAndFreshness(t *testing.T) {
	for _, test := range []struct {
		name, enabled, relay, freshness string
		invalid                         bool
	}{
		{"default", "", "", "", false},
		{"missing route relay", "true", "false", "5m", true},
		{"missing freshness", "true", "true", "", true},
		{"minimum", "true", "true", "2s", false},
		{"maximum", "true", "true", "1h", false},
		{"too short", "true", "true", "1s", true},
		{"fractional", "true", "true", "2500ms", true},
		{"too long", "true", "true", "61m", true},
		{"negative", "true", "true", "-1s", true},
		{"invalid duration", "true", "true", "tomorrow", true},
		{"invalid flag", "sometimes", "true", "5m", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("AEP_GATEWAY_ACCESS_RELAY_ENABLED", test.enabled)
			t.Setenv("AEP_GATEWAY_ACCESS_FRESHNESS", test.freshness)
			t.Setenv("AEP_GATEWAY_PUBLICATION_RELAY_ENABLED", test.relay)
			t.Setenv("AEP_GATEWAY_PUBLICATIONS_ENABLED", "true")
			cfg, err := Load()
			if (err != nil) != test.invalid {
				t.Fatalf("Load = %v", err)
			}
			if err == nil && test.name == "default" && (cfg.GatewayAccessRelayEnabled || cfg.GatewayAccessFreshness != 0) {
				t.Fatal("access delivery enabled without opt-in")
			}
		})
	}
	if err := (Config{GatewayAccessRelayEnabled: true, GatewayRelayEnabled: true, GatewayAccessFreshness: 5 * time.Minute}).Validate(); err == nil {
		t.Fatal("access option bypassed the existing publication and namespace fences")
	}
}
