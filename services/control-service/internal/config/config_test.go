package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var environmentKeys = []string{
	"AEP_ENVIRONMENT", "AEP_LOG_FORMAT", "AEP_LOG_LEVEL", "AEP_ENABLE_MOCK_FEDERATED_AUTH", "AEP_DATABASE_URL",
	"AEP_DATABASE_URL_FILE", "AEP_MINIO_ACCESS_KEY", "AEP_MINIO_ACCESS_KEY_FILE",
	"AEP_MINIO_SECRET_KEY", "AEP_MINIO_SECRET_KEY_FILE", "AEP_MINIO_SECURE",
	"AEP_SIGNING_KEY_BASE64", "AEP_SIGNING_KEY_BASE64_FILE",
	"AEP_CREDENTIAL_MASTER_KEY_BASE64", "AEP_CREDENTIAL_MASTER_KEY_BASE64_FILE",
	"AEP_LICENSE_TRUSTED_KEYS_FILE", "AEP_LICENSE_FILE", "AEP_LICENSE_DEPLOYMENT_ID", "AEP_LICENSE_CUSTOMER_ID", "AEP_LICENSE_ENTERPRISE_ID",
	"AEP_BOOTSTRAP_ADMIN_PASSWORD", "AEP_BOOTSTRAP_ADMIN_PASSWORD_FILE",
	"AEP_DATA_PLANE_RECONCILER_TOKEN", "AEP_DATA_PLANE_RECONCILER_TOKEN_FILE",
	"AEP_GATEWAY_LICENSE_STATUS_TOKEN", "AEP_GATEWAY_LICENSE_STATUS_TOKEN_FILE",
	"AEP_DEPLOYMENT_ID", "AEP_DEPLOYMENT_NAME",
	"AEP_HTTP_READ_TIMEOUT", "AEP_HTTP_MAX_HEADER_BYTES",
	"AEP_LOGIN_FAILURE_LIMIT", "AEP_LOGIN_FAILURE_WINDOW", "AEP_LOGIN_BACKOFF_BASE", "AEP_LOGIN_BACKOFF_MAX",
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	clearEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "development" || cfg.LogFormat != "text" || !cfg.EnableMockFederatedAuth || cfg.HTTPReadTimeout <= 0 || cfg.LoginFailureLimit != 5 || cfg.LoginBackoffBase != 30*time.Second || cfg.DeploymentID != "demo" || cfg.DeploymentName != "Demo Deployment" {
		t.Fatalf("unexpected development defaults: %#v", cfg)
	}
}

func TestLoadUsesExplicitDeploymentIdentity(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("AEP_DEPLOYMENT_ID", "deployment-42")
	t.Setenv("AEP_DEPLOYMENT_NAME", "Zhiyuan deployment")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeploymentID != "deployment-42" || cfg.DeploymentName != "Zhiyuan deployment" {
		t.Fatalf("deployment configuration = %q/%q", cfg.DeploymentID, cfg.DeploymentName)
	}
}

func TestLoadRejectsInvalidTypedValues(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "AEP_MINIO_SECURE", value: "sometimes"},
		{key: "AEP_HTTP_READ_TIMEOUT", value: "forever"},
		{key: "AEP_HTTP_MAX_HEADER_BYTES", value: "0"},
		{key: "AEP_LOGIN_FAILURE_LIMIT", value: "0"},
		{key: "AEP_LOGIN_FAILURE_WINDOW", value: "forever"},
	} {
		t.Run(test.key, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadRejectsInvertedLoginBackoff(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("AEP_LOGIN_BACKOFF_BASE", "2m")
	t.Setenv("AEP_LOGIN_BACKOFF_MAX", "1m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AEP_LOGIN_BACKOFF_MAX") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadProductionGuardrails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T)
		match  string
	}{
		{name: "ephemeral signing key", mutate: func(t *testing.T) { t.Setenv("AEP_SIGNING_KEY_BASE64", "") }, match: "SIGNING_KEY"},
		{name: "mock federated authentication", mutate: func(t *testing.T) { t.Setenv("AEP_ENABLE_MOCK_FEDERATED_AUTH", "true") }, match: "MOCK_FEDERATED_AUTH"},
		{name: "development database", mutate: func(t *testing.T) { t.Setenv("AEP_DATABASE_URL", defaultDatabaseURL) }, match: "DATABASE_URL"},
		{name: "development object credentials", mutate: func(t *testing.T) { t.Setenv("AEP_MINIO_SECRET_KEY", "minioadmin") }, match: "MinIO"},
		{name: "development administrator password", mutate: func(t *testing.T) { t.Setenv("AEP_BOOTSTRAP_ADMIN_PASSWORD", defaultAdminPassword) }, match: "administrator password"},
		{name: "missing gateway License status token", mutate: func(t *testing.T) { t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "") }, match: "GATEWAY_LICENSE_STATUS_TOKEN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validProductionEnvironment(t)
			test.mutate(t)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
	validProductionEnvironment(t)
	if _, err := Load(); err != nil {
		t.Fatalf("valid production configuration failed: %v", err)
	}
}

func TestLoadReadsFileSecretsAndRejectsAmbiguousSources(t *testing.T) {
	clearEnvironment(t)
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte("postgres://file-user:file-pass@db.internal/aep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEP_DATABASE_URL_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://file-user:file-pass@db.internal/aep" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	t.Setenv("AEP_DATABASE_URL", "postgres://direct@db.internal/aep")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadReadsDataPlaneTokenFromSecretFile(t *testing.T) {
	clearEnvironment(t)
	path := filepath.Join(t.TempDir(), "data-plane-token")
	if err := os.WriteFile(path, []byte("reconciler-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataPlaneReconcilerToken != "reconciler-token" {
		t.Fatalf("DataPlaneReconcilerToken = %q", cfg.DataPlaneReconcilerToken)
	}
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN", "direct")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Load() error = %v", err)
	}
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range environmentKeys {
		t.Setenv(key, "")
	}
}

func validProductionEnvironment(t *testing.T) {
	t.Helper()
	clearEnvironment(t)
	t.Setenv("AEP_ENVIRONMENT", "production")
	t.Setenv("AEP_DATABASE_URL", "postgres://aep:secret@postgres.internal/aep")
	t.Setenv("AEP_MINIO_ACCESS_KEY", "production-access")
	t.Setenv("AEP_MINIO_SECRET_KEY", "production-secret")
	t.Setenv("AEP_SIGNING_KEY_BASE64", "production-signing-seed")
	t.Setenv("AEP_BOOTSTRAP_ADMIN_PASSWORD", "production-admin-password")
	keyFile := filepath.Join(t.TempDir(), "license-public-keys.json")
	if err := os.WriteFile(keyFile, []byte(`{"license-prod-1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEP_LICENSE_TRUSTED_KEYS_FILE", keyFile)
	t.Setenv("AEP_LICENSE_DEPLOYMENT_ID", "deployment-enterprise-001")
	t.Setenv("AEP_LICENSE_CUSTOMER_ID", "customer-enterprise-001")
	t.Setenv("AEP_LICENSE_ENTERPRISE_ID", "enterprise")
	t.Setenv("AEP_LICENSE_FILE", filepath.Join(t.TempDir(), "license.zylic"))
	t.Setenv("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "production-gateway-license-status-token")
}

func validConfig() Config {
	return Config{
		Environment: "development", LogFormat: "text", LogLevel: "info",
		DatabaseURL: "postgres://aep:secret@postgres.internal/aep", Issuer: "https://control.internal",
		MinioEndpoint: "minio.internal:9000", MinioBucket: "skills",
		DeploymentID: "deployment-a", DeploymentName: "Deployment A",
		LoginBackoffBase: time.Second, LoginBackoffMax: time.Minute,
	}
}

func TestValidateRejectsInvalidRuntimeFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		match  string
	}{
		{name: "environment", mutate: func(cfg *Config) { cfg.Environment = "staging" }, match: "AEP_ENVIRONMENT"},
		{name: "log format", mutate: func(cfg *Config) { cfg.LogFormat = "yaml" }, match: "AEP_LOG_FORMAT"},
		{name: "log level", mutate: func(cfg *Config) { cfg.LogLevel = "trace" }, match: "AEP_LOG_LEVEL"},
		{name: "database URL", mutate: func(cfg *Config) { cfg.DatabaseURL = "postgres-without-host" }, match: "AEP_DATABASE_URL"},
		{name: "database scheme", mutate: func(cfg *Config) { cfg.DatabaseURL = "https://postgres.internal/aep" }, match: "must use one of these schemes"},
		{name: "issuer", mutate: func(cfg *Config) { cfg.Issuer = "://invalid" }, match: "AEP_ISSUER"},
		{name: "model gateway", mutate: func(cfg *Config) { cfg.ModelGatewayBaseURL = "ftp://gateway.internal" }, match: "AEP_MODEL_GATEWAY_BASE_URL"},
		{name: "MinIO endpoint", mutate: func(cfg *Config) { cfg.MinioEndpoint = " " }, match: "AEP_MINIO_ENDPOINT"},
		{name: "deployment identity", mutate: func(cfg *Config) { cfg.DeploymentID = "" }, match: "AEP_DEPLOYMENT_ID"},
		{name: "login backoff", mutate: func(cfg *Config) { cfg.LoginBackoffMax = 0 }, match: "AEP_LOGIN_BACKOFF_MAX"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate() error = %v, want detail %q", err, test.match)
			}
		})
	}
}

func TestValidateRejectsMissingProductionLicenseBindings(t *testing.T) {
	validProduction := validConfig()
	validProduction.Environment = "production"
	validProduction.SigningKeyBase64 = "signing-key"
	validProduction.MinioAccessKey = "access"
	validProduction.MinioSecretKey = "secret"
	validProduction.BootstrapAdminPassword = "secure-admin-password"
	validProduction.LicenseTrustedKeys = map[string]string{"license-prod": "public-key"}
	validProduction.LicenseDeploymentID = "deployment-a"
	validProduction.LicenseCustomerID = "customer-a"
	validProduction.LicenseFile = "license.zylic"
	validProduction.GatewayLicenseStatusToken = "gateway-token"

	tests := []struct {
		name   string
		mutate func(*Config)
		match  string
	}{
		{name: "trusted keys", mutate: func(cfg *Config) { cfg.LicenseTrustedKeys = nil }, match: "AEP_LICENSE_TRUSTED_KEYS_FILE"},
		{name: "deployment", mutate: func(cfg *Config) { cfg.LicenseDeploymentID = " " }, match: "AEP_LICENSE_DEPLOYMENT_ID"},
		{name: "customer", mutate: func(cfg *Config) { cfg.LicenseCustomerID = "" }, match: "AEP_LICENSE_CUSTOMER_ID"},
		{name: "license file", mutate: func(cfg *Config) { cfg.LicenseFile = "" }, match: "AEP_LICENSE_FILE"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProduction
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate() error = %v, want detail %q", err, test.match)
			}
		})
	}
}
