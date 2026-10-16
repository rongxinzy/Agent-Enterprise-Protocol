package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publisherEnvironment(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "AEP_CREDENTIAL_PUBLISHER_DATABASE_URL_FILE", "AEP_CREDENTIAL_PUBLISHER_DEPLOYMENT_ID", "AEP_CREDENTIAL_PUBLISHER_INTERVAL", "AEP_CREDENTIAL_MASTER_KEY_FILE", "AEP_ENVIRONMENT"} {
		t.Setenv(key, "")
	}
	t.Setenv("AEP_ENVIRONMENT", "test")
	t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "postgres://fixture:fixture@127.0.0.1:1/fixture?sslmode=disable")
	t.Setenv("AEP_CREDENTIAL_PUBLISHER_DEPLOYMENT_ID", "deployment-fixture")
	keyring := filepath.Join(t.TempDir(), "keyring")
	if err := os.WriteFile(keyring, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEP_CREDENTIAL_MASTER_KEY_FILE", keyring)
	return keyring
}

func TestConfigurationRejectsUnsafeOrAmbiguousInputs(t *testing.T) {
	for _, name := range []string{"defaults", "custom-interval", "file", "ambiguous", "missing-file", "invalid-url", "missing-deployment", "missing-keyring", "invalid-interval", "short-interval", "long-interval", "production-insecure", "production-fallback", "production-verified"} {
		t.Run(name, func(t *testing.T) {
			publisherEnvironment(t)
			expectError := true
			expectInterval := 5 * time.Second
			switch name {
			case "defaults":
				expectError = false
			case "custom-interval":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_INTERVAL", "30s")
				expectInterval = 30 * time.Second
				expectError = false
			case "file":
				path := filepath.Join(t.TempDir(), "database-url")
				if err := os.WriteFile(path, []byte(os.Getenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "")
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL_FILE", path)
				expectError = false
			case "ambiguous":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL_FILE", "irrelevant")
			case "missing-file":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "")
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL_FILE", "missing")
			case "invalid-url":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "postgres://%invalid")
			case "missing-deployment":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DEPLOYMENT_ID", "")
			case "missing-keyring":
				t.Setenv("AEP_CREDENTIAL_MASTER_KEY_FILE", "")
			case "invalid-interval":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_INTERVAL", "bad")
			case "short-interval":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_INTERVAL", "1ms")
			case "long-interval":
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_INTERVAL", "2h")
			case "production-insecure":
				t.Setenv("AEP_ENVIRONMENT", "production")
			case "production-fallback":
				t.Setenv("AEP_ENVIRONMENT", "production")
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "postgres://fixture:fixture@localhost/fixture?sslmode=prefer")
			case "production-verified":
				t.Setenv("AEP_ENVIRONMENT", "production")
				t.Setenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL", "postgres://fixture:fixture@localhost/fixture?sslmode=verify-full")
				expectError = false
			}
			cfg, err := loadConfiguration()
			if (err != nil) != expectError {
				t.Fatalf("configuration error = %v; expected error %v", err, expectError)
			}
			if !expectError && cfg.interval != expectInterval {
				t.Fatalf("retry interval = %v; want %v", cfg.interval, expectInterval)
			}
			if err != nil && strings.Contains(err.Error(), "postgres://") {
				t.Fatal("configuration error leaked database credentials")
			}
		})
	}
}

func TestStartupAndCancellation(t *testing.T) {
	publisherEnvironment(t)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- name: fixture
  cluster: {server: "http://127.0.0.1:1"}
contexts:
- name: fixture
  context: {cluster: fixture, user: fixture}
current-context: fixture
users:
- name: fixture
  user: {}
`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStartupErrorsAreRedacted(t *testing.T) {
	publisherEnvironment(t)
	t.Setenv("AEP_CREDENTIAL_MASTER_KEY_FILE", "missing-sensitive-keyring-path")
	if err := run(context.Background()); err == nil || strings.Contains(err.Error(), "missing-sensitive") {
		t.Fatal("keyring failure was not safely reported")
	}
	t.Setenv("AEP_CREDENTIAL_PUBLISHER_INTERVAL", "invalid")
	if err := run(context.Background()); err == nil {
		t.Fatal("invalid configuration was accepted")
	}
}
