package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var environmentKeys = []string{
	"AEP_ENVIRONMENT", "AEP_LOG_FORMAT", "AEP_LOG_LEVEL", "AEP_DATABASE_URL",
	"AEP_DATABASE_URL_FILE", "AEP_MINIO_ACCESS_KEY", "AEP_MINIO_ACCESS_KEY_FILE",
	"AEP_MINIO_SECRET_KEY", "AEP_MINIO_SECRET_KEY_FILE", "AEP_MINIO_SECURE",
	"AEP_SIGNING_KEY_BASE64", "AEP_SIGNING_KEY_BASE64_FILE",
	"AEP_CREDENTIAL_MASTER_KEY_BASE64", "AEP_CREDENTIAL_MASTER_KEY_BASE64_FILE",
	"AEP_BOOTSTRAP_ADMIN_PASSWORD", "AEP_BOOTSTRAP_ADMIN_PASSWORD_FILE",
	"AEP_HTTP_READ_TIMEOUT", "AEP_HTTP_MAX_HEADER_BYTES",
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	clearEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "development" || cfg.LogFormat != "text" || cfg.HTTPReadTimeout <= 0 {
		t.Fatalf("unexpected development defaults: %#v", cfg)
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

func TestLoadProductionGuardrails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T)
		match  string
	}{
		{name: "ephemeral signing key", mutate: func(t *testing.T) { t.Setenv("AEP_SIGNING_KEY_BASE64", "") }, match: "SIGNING_KEY"},
		{name: "development database", mutate: func(t *testing.T) { t.Setenv("AEP_DATABASE_URL", defaultDatabaseURL) }, match: "DATABASE_URL"},
		{name: "development object credentials", mutate: func(t *testing.T) { t.Setenv("AEP_MINIO_SECRET_KEY", "minioadmin") }, match: "MinIO"},
		{name: "development administrator password", mutate: func(t *testing.T) { t.Setenv("AEP_BOOTSTRAP_ADMIN_PASSWORD", defaultAdminPassword) }, match: "administrator password"},
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
}
