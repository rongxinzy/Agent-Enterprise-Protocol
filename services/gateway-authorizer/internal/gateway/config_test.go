package gateway

import (
	"strings"
	"testing"
)

var gatewayEnvironmentKeys = []string{
	"AEP_ENVIRONMENT", "AEP_LOG_FORMAT", "AEP_LOG_LEVEL",
	"AEP_GATEWAY_UPSTREAM_URL", "AEP_GATEWAY_JWKS_URL", "AEP_GATEWAY_ISSUER",
	"AEP_GATEWAY_JWKS_TTL", "AEP_GATEWAY_JWKS_TIMEOUT", "AEP_GATEWAY_REQUEST_LIMIT",
	"AEP_GATEWAY_UPSTREAM_HEADER_TIMEOUT",
	"AEP_GATEWAY_HTTP_READ_TIMEOUT", "AEP_GATEWAY_HTTP_WRITE_TIMEOUT",
	"AEP_GATEWAY_HTTP_MAX_HEADER_BYTES",
}

func TestLoadConfigDefaultsAndProductionLogging(t *testing.T) {
	clearGatewayEnvironment(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFormat != "text" || cfg.RequestLimit <= 0 || cfg.HTTPWriteTimeout != 0 || cfg.UpstreamHeaderTimeout <= 0 {
		t.Fatalf("unexpected gateway defaults: %#v", cfg)
	}
	t.Setenv("AEP_ENVIRONMENT", "production")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFormat != "json" {
		t.Fatalf("production LogFormat = %q", cfg.LogFormat)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "AEP_GATEWAY_UPSTREAM_URL", value: "/relative"},
		{key: "AEP_GATEWAY_JWKS_TTL", value: "0s"},
		{key: "AEP_GATEWAY_REQUEST_LIMIT", value: "-1"},
		{key: "AEP_GATEWAY_HTTP_MAX_HEADER_BYTES", value: "many"},
	} {
		t.Run(test.key, func(t *testing.T) {
			clearGatewayEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("LoadConfig() error = %v", err)
			}
		})
	}
}

func clearGatewayEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range gatewayEnvironmentKeys {
		t.Setenv(key, "")
	}
}
