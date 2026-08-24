package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigReadsMountedToken(t *testing.T) {
	t.Setenv("AEP_RECONCILER_CONTROL_URL", "http://control-service:8080")
	t.Setenv("AEP_RECONCILER_TENANTS", "enterprise")
	t.Setenv("AEP_RECONCILER_INTERVAL", "15s")
	path := filepath.Join(t.TempDir(), "reconciler-token")
	if err := os.WriteFile(path, []byte("mounted-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", path)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.worker.Token != "mounted-token" {
		t.Fatalf("Token = %q", config.worker.Token)
	}
}

func TestLoadConfigRejectsAmbiguousToken(t *testing.T) {
	t.Setenv("AEP_RECONCILER_CONTROL_URL", "http://control-service:8080")
	t.Setenv("AEP_RECONCILER_TENANTS", "enterprise")
	t.Setenv("AEP_RECONCILER_INTERVAL", "15s")
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN", "direct")
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", filepath.Join(t.TempDir(), "token"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("ambiguous token source was accepted")
	}
}
