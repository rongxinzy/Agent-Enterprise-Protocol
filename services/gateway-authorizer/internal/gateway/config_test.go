package gateway

import (
	"os"
	"path/filepath"
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
	"AEP_GATEWAY_REQUIRE_ENTITLEMENT",
	"AEP_GATEWAY_LICENSE_STATUS_URL", "AEP_GATEWAY_LICENSE_STATUS_TOKEN", "AEP_GATEWAY_LICENSE_STATUS_TOKEN_FILE", "AEP_GATEWAY_LICENSE_STATUS_TTL",
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
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_URL", "http://control.internal/licenses")
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "gateway-secret")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFormat != "json" || !cfg.RequireEntitlement {
		t.Fatalf("unexpected production defaults: %#v", cfg)
	}
}

func TestLoadConfigProductionEntitlementGuardrails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T)
		match  string
	}{
		{name: "explicitly disabled", mutate: func(t *testing.T) { t.Setenv("AEP_GATEWAY_REQUIRE_ENTITLEMENT", "false") }, match: "must be true"},
		{name: "missing status URL", mutate: func(t *testing.T) { t.Setenv("AEP_GATEWAY_LICENSE_STATUS_URL", "") }, match: "LICENSE_STATUS_URL"},
		{name: "missing status token", mutate: func(t *testing.T) { t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "") }, match: "LICENSE_STATUS_TOKEN"},
		{name: "excessive cache TTL", mutate: func(t *testing.T) { t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TTL", "16s") }, match: "LICENSE_STATUS_TTL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validProductionGatewayEnvironment(t)
			test.mutate(t)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("LoadConfig() error = %v", err)
			}
		})
	}
}

func TestLoadConfigReadsLicenseStatusTokenFile(t *testing.T) {
	clearGatewayEnvironment(t)
	path := filepath.Join(t.TempDir(), "gateway-license-status-token")
	if err := os.WriteFile(path, []byte("gateway-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN_FILE", path)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseStatusToken != "gateway-secret" {
		t.Fatalf("LicenseStatusToken = %q", cfg.LicenseStatusToken)
	}
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "direct-secret")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("LoadConfig() error = %v", err)
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
		{key: "AEP_GATEWAY_REQUIRE_ENTITLEMENT", value: "sometimes"},
		{key: "AEP_GATEWAY_LICENSE_STATUS_TTL", value: "0s"},
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

func validProductionGatewayEnvironment(t *testing.T) {
	t.Helper()
	clearGatewayEnvironment(t)
	t.Setenv("AEP_ENVIRONMENT", "production")
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_URL", "http://control.internal/licenses")
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "gateway-secret")
}
