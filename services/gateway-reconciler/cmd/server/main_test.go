package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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
	readiness := newReadinessState([]string{"demo"})
	done := make(chan struct{})
	go func() {
		runTenant(ctx, worker, "demo", time.Hour, readiness)
		close(done)
	}()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not publish pending status")
	}
	waitForReadiness(t, readiness, true)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not stop when canceled")
	}
}

func TestRunTenantReportsControlPlaneFailureAndStops(t *testing.T) {
	observed := make(chan reconciler.Status, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var status reconciler.Status
		if err := json.NewDecoder(request.Body).Decode(&status); err != nil {
			t.Errorf("decode failure status: %v", err)
			return
		}
		observed <- status
	}))
	defer server.Close()
	worker, err := reconciler.New(reconciler.Config{ControlURL: server.URL, Token: "test-token", OutputDir: t.TempDir(), Tenants: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readiness := newReadinessState([]string{"demo"})
	readiness.record("demo", nil)
	done := make(chan struct{})
	go func() {
		runTenant(ctx, worker, "demo", time.Hour, readiness)
		close(done)
	}()
	select {
	case status := <-observed:
		if status.State != "error" || status.ErrorCode == nil || *status.ErrorCode != "CONTROL_PLANE_UNAVAILABLE" {
			t.Fatalf("unexpected failure status: %#v", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reconciler did not report control-plane failure")
	}
	waitForReadiness(t, readiness, false)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciler did not stop during backoff")
	}
}

func TestReadinessRequiresEveryTenantAndTracksLatestResult(t *testing.T) {
	readiness := newReadinessState([]string{"first", "second"})
	if readiness.ready() {
		t.Fatal("readiness was true before the first synchronization")
	}
	readiness.record("first", nil)
	if readiness.ready() {
		t.Fatal("readiness was true before every tenant synchronized")
	}
	readiness.record("second", nil)
	if !readiness.ready() {
		t.Fatal("readiness remained false after every tenant synchronized")
	}
	readiness.record("first", errors.New("control plane unavailable"))
	if readiness.ready() {
		t.Fatal("readiness remained true after a tenant synchronization failed")
	}
	readiness.record("first", nil)
	if !readiness.ready() {
		t.Fatal("readiness did not recover after the failed tenant synchronized")
	}
}

func waitForReadiness(t *testing.T, readiness *readinessState, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readiness.ready() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("readiness = %v, want %v", readiness.ready(), want)
}

func TestServeHealthRespondsAndShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readiness := newReadinessState([]string{"demo"})
	done := make(chan struct{})
	go func() {
		serveHealth(ctx, address, readiness)
		close(done)
	}()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := client.Get("http://" + address + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health listener did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{"/livez", http.StatusOK, `{"status":"ok"}`},
		{"/readyz", http.StatusServiceUnavailable, `{"status":"not_ready"}`},
		{"/missing", http.StatusNotFound, "404 page not found\n"},
	} {
		response, err := client.Get("http://" + address + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != test.status || string(body) != test.body {
			t.Fatalf("%s: status = %d, body = %q, error = %v", test.path, response.StatusCode, body, err)
		}
	}
	readiness.record("demo", nil)
	response, err := client.Get("http://" + address + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("ready status = %d, body = %q, error = %v", response.StatusCode, body, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("health listener did not stop after cancellation")
	}
}
