package main

import "testing"

func TestAccessOptions(t *testing.T) {
	for _, enabled := range []string{"", "false"} {
		value, err := accessOptions(func(string) string { return enabled })
		if err != nil || value != nil {
			t.Fatalf("default path changed: %#v %v", value, err)
		}
	}
	good := map[string]string{"AEP_GATEWAY_ACCESS_OPERATOR_ENABLED": "true", "AEP_GATEWAY_ACCESS_REFRESH_SECONDS": "5",
		"AEP_GATEWAY_ACCESS_ISSUER_URL": "https://control.example.test", "AEP_GATEWAY_ACCESS_POLICY_BASE_URL": "http://policies.aep-system.svc:8080"}
	value, err := accessOptions(func(key string) string { return good[key] })
	if err != nil || value == nil || !value.RequireEntitlement {
		t.Fatalf("unsafe enabled defaults: %#v %v", value, err)
	}
	for key, bad := range map[string]string{"AEP_GATEWAY_ACCESS_OPERATOR_ENABLED": "maybe", "AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT": "maybe",
		"AEP_GATEWAY_ACCESS_REFRESH_SECONDS": "0", "AEP_GATEWAY_ACCESS_ISSUER_URL": "file:///tmp/issuer", "AEP_GATEWAY_ACCESS_POLICY_BASE_URL": "http://example.test?token=secret",
		"AEP_GATEWAY_ACCESS_INGRESS_HOST": "*.access.test"} {
		input := map[string]string{}
		for key, value := range good {
			input[key] = value
		}
		input[key] = bad
		if _, err := accessOptions(func(key string) string { return input[key] }); err == nil {
			t.Fatalf("bad option accepted: %s", key)
		}
	}
}
