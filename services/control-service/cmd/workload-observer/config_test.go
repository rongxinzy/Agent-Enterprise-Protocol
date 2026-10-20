package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func observerEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AEP_WORKLOAD_DATABASE_URL_FILE", "AEP_WORKLOAD_DATABASE_URL", "AEP_WORKLOAD_DEPLOYMENT_ID", "AEP_WORKLOAD_NAMESPACE", "AEP_WORKLOAD_AUDIENCE", "AEP_WORKLOAD_FRESHNESS", "AEP_ENVIRONMENT"} {
		t.Setenv(key, "")
	}
	t.Setenv("AEP_ENVIRONMENT", "test")
	t.Setenv("AEP_WORKLOAD_DATABASE_URL", "postgres://fixture:fixture@127.0.0.1:1/fixture?sslmode=disable")
	t.Setenv("AEP_WORKLOAD_DEPLOYMENT_ID", "deployment-1")
	t.Setenv("AEP_WORKLOAD_NAMESPACE", "employees")
	t.Setenv("AEP_WORKLOAD_AUDIENCE", "model-gateway")
	t.Setenv("AEP_WORKLOAD_FRESHNESS", "1m")
}

func TestObserverConfigurationFilesAndProductionTransport(t *testing.T) {
	for _, sslMode := range []string{"disable", "prefer", "require", "verify-ca", "verify-full"} {
		t.Run(sslMode, func(t *testing.T) {
			observerEnvironment(t)
			t.Setenv("AEP_ENVIRONMENT", "production")
			t.Setenv("AEP_WORKLOAD_DATABASE_URL", "postgres://fixture:fixture@localhost/fixture?sslmode="+sslMode)
			_, err := loadConfiguration()
			if (err == nil) != (sslMode == "verify-full") {
				t.Fatal("unexpected production transport acceptance", err)
			}
		})
	}
	t.Run("mounted database source", func(t *testing.T) {
		observerEnvironment(t)
		dsn := os.Getenv("AEP_WORKLOAD_DATABASE_URL")
		path := filepath.Join(t.TempDir(), "database-url")
		if err := os.WriteFile(path, []byte(dsn+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AEP_WORKLOAD_DATABASE_URL", "")
		t.Setenv("AEP_WORKLOAD_DATABASE_URL_FILE", path)
		cfg, err := loadConfiguration()
		if err != nil || cfg.databaseURL != dsn {
			t.Fatal("mounted database URL not loaded")
		}
		t.Setenv("AEP_WORKLOAD_DATABASE_URL_FILE", filepath.Join(t.TempDir(), "missing"))
		if _, err := loadConfiguration(); err == nil {
			t.Fatal("missing mounted database source accepted")
		}
	})
}

func TestObserverConfigurationRejectsUnboundedScopeAndLifetime(t *testing.T) {
	for key, values := range map[string][]string{
		"AEP_WORKLOAD_DEPLOYMENT_ID": {"deployment with space", strings.Repeat("x", 81), "deployment\n"},
		"AEP_WORKLOAD_NAMESPACE":     {"", "UpperCase", strings.Repeat("n", 64)},
		"AEP_WORKLOAD_AUDIENCE":      {"audience\n", strings.Repeat("a", 257)},
		"AEP_WORKLOAD_FRESHNESS":     {"", "-1s", "1s", "1001ms", "3601s"},
		"AEP_ENVIRONMENT":            {"", "staging"},
	} {
		for i, value := range values {
			t.Run(key+strings.Repeat("_", i), func(t *testing.T) {
				observerEnvironment(t)
				t.Setenv(key, value)
				if _, err := loadConfiguration(); err == nil {
					t.Fatal("unbounded configuration accepted")
				}
			})
		}
	}
}

func TestObserverStartupFailsClosedWithoutDatabase(t *testing.T) {
	observerEnvironment(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := run(ctx)
	if err == nil || strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "postgres://") {
		t.Fatal("startup did not sanitize unavailable database")
	}
}

func TestObserverConfigurationRequiresExplicitTrustScope(t *testing.T) {
	for key, value := range map[string]string{
		"valid": "", "AEP_WORKLOAD_DATABASE_URL": "postgres://%invalid", "AEP_WORKLOAD_DEPLOYMENT_ID": "",
		"AEP_WORKLOAD_NAMESPACE": "foreign namespace", "AEP_WORKLOAD_AUDIENCE": "", "AEP_WORKLOAD_FRESHNESS": "1500ms",
		"AEP_ENVIRONMENT": "production", "AEP_WORKLOAD_DATABASE_URL_FILE": "missing-file",
	} {
		t.Run(key, func(t *testing.T) {
			observerEnvironment(t)
			if key != "valid" {
				t.Setenv(key, value)
			}
			_, err := loadConfiguration()
			if (err == nil) != (key == "valid") {
				t.Fatal("unexpected configuration outcome", err)
			}
			if err != nil && strings.Contains(err.Error(), "postgres://") {
				t.Fatal("database credential leaked")
			}
		})
	}
}
