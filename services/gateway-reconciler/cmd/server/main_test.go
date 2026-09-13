package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-reconciler/internal/reconciler"
)

func TestLoadConfigReadsMountedToken(t *testing.T) {
	t.Setenv("AEP_RECONCILER_KUBERNETES_URL", "")
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
	t.Setenv("AEP_RECONCILER_KUBERNETES_URL", "")
	t.Setenv("AEP_RECONCILER_CONTROL_URL", "http://control-service:8080")
	t.Setenv("AEP_RECONCILER_TENANTS", "enterprise")
	t.Setenv("AEP_RECONCILER_INTERVAL", "15s")
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN", "direct")
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", filepath.Join(t.TempDir(), "token"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("ambiguous token source was accepted")
	}
}

func TestLoadConfigRejectsInvalidSources(t *testing.T) {
	for _, test := range []struct {
		name, key, value, want string
	}{
		{"invalid interval", "AEP_RECONCILER_INTERVAL", "invalid", "INTERVAL"},
		{"zero interval", "AEP_RECONCILER_INTERVAL", "0s", "INTERVAL"},
		{"missing token file", "AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", "missing-file", "missing-file"},
		{"invalid Kubernetes URL", "AEP_RECONCILER_KUBERNETES_URL", "://invalid", "Kubernetes URL"},
		{"missing Kubernetes token", "AEP_RECONCILER_KUBERNETES_URL", "https://kubernetes.example", "service-account token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AEP_RECONCILER_INTERVAL", "15s")
			t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN", "control-token")
			t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", "")
			t.Setenv("AEP_RECONCILER_KUBERNETES_URL", "")
			t.Setenv("AEP_RECONCILER_KUBERNETES_TOKEN", "")
			t.Setenv("AEP_RECONCILER_KUBERNETES_TOKEN_FILE", "")
			if test.name == "missing token file" {
				t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN", "")
			}
			t.Setenv(test.key, test.value)
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadConfig() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigSetsKubernetesApplierAndDefaults(t *testing.T) {
	t.Setenv("AEP_RECONCILER_INTERVAL", "")
	t.Setenv("AEP_RECONCILER_TENANTS", " first , , second ")
	t.Setenv("AEP_RECONCILER_ADDRESS", "")
	t.Setenv("AEP_RECONCILER_OUTPUT_DIR", "")
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN", "control-token")
	t.Setenv("AEP_DATA_PLANE_RECONCILER_TOKEN_FILE", "")
	t.Setenv("AEP_RECONCILER_KUBERNETES_URL", "https://kubernetes.example")
	t.Setenv("AEP_RECONCILER_KUBERNETES_TOKEN", "kubernetes-token")
	t.Setenv("AEP_RECONCILER_KUBERNETES_TOKEN_FILE", "")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.interval != 15*time.Second || config.address != ":8091" || config.worker.OutputDir != "/var/lib/aep-reconciler" || len(config.worker.Tenants) != 2 || config.worker.Tenants[1] != "second" || config.worker.Applier == nil {
		t.Fatalf("unexpected default config: %#v", config)
	}
}

func TestRunTenantStopsAfterPendingStatus(t *testing.T) {
	observed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_ = json.NewEncoder(response).Encode(reconciler.DesiredState{DeploymentID: "demo"})
			return
		}
		var status reconciler.Status
		if err := json.NewDecoder(request.Body).Decode(&status); err != nil || status.State != "pending" {
			http.Error(response, "unexpected status", http.StatusBadRequest)
			return
		}
		observed <- struct{}{}
	}))
	defer server.Close()
	worker, err := reconciler.New(reconciler.Config{ControlURL: server.URL, Token: "token", OutputDir: t.TempDir(), Tenants: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runTenant(ctx, worker, "demo", time.Hour)
		close(done)
	}()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not publish pending status")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not stop when canceled")
	}
}
