package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingApplier struct {
	calls int
	err   error
}

func (a *recordingApplier) Apply(_ context.Context, _ DesiredState, _ string) error {
	a.calls++
	return a.err
}

func TestRenderIsDeterministicAndDoesNotIncludeSecretValues(t *testing.T) {
	desired := DesiredState{EnterpriseID: "demo", Revision: "rev-1", ContentHash: "ignored", Routes: []Route{{ModelID: "model-b", Enabled: true, Endpoint: "/b", UpstreamModel: "up-b", Protocol: "openai-compatible", CredentialRef: &SecretReference{Name: "provider-secrets", Key: "model-b"}}, {ModelID: "model-a", Enabled: true, Endpoint: "/a", UpstreamModel: "up-a", Protocol: "openai-compatible"}}}
	first, firstHash, err := Render(desired)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := Render(DesiredState{EnterpriseID: "demo", Revision: "rev-1", Routes: []Route{desired.Routes[1], desired.Routes[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstHash != secondHash {
		t.Fatal("render output is not deterministic")
	}
	if strings.Contains(first, "provider-secret-value") || !strings.Contains(first, "provider-secrets") {
		t.Fatal("Secret value policy was not preserved")
	}
}

func TestRenderRejectsMissingRevision(t *testing.T) {
	if _, _, err := Render(DesiredState{EnterpriseID: "demo"}); err == nil {
		t.Fatal("missing revision was accepted")
	}
}

func TestSyncReadsDesiredStateWritesResourcesAndReportsReady(t *testing.T) {
	desired := DesiredState{EnterpriseID: "demo", Revision: "rev-1", Routes: []Route{{ModelID: "model-1", Enabled: true, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible", CredentialRef: &SecretReference{Name: "provider-secrets", Key: "model-1"}}}}
	desired.ContentHash = canonicalHash(desired)
	statuses := make([]Status, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-AEP-Data-Plane-Token") != "token" || request.Header.Get("X-AEP-Tenant-ID") != "demo" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodGet {
			_ = json.NewEncoder(response).Encode(desired)
			return
		}
		var status Status
		if err := json.NewDecoder(request.Body).Decode(&status); err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		statuses = append(statuses, status)
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	output := t.TempDir()
	applier := &recordingApplier{}
	worker, err := New(Config{ControlURL: server.URL, Token: "token", OutputDir: output, Tenants: []string{"demo"}, HTTPClient: server.Client(), Applier: applier})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Sync(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(output, "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "provider-secrets") || strings.Contains(string(content), "provider-secret-value") {
		t.Fatalf("unexpected rendered resource: %s", content)
	}
	if len(statuses) != 2 || statuses[0].State != "applying" || statuses[1].State != "ready" {
		t.Fatalf("unexpected status transitions: %#v", statuses)
	}
	if applier.calls != 1 {
		t.Fatalf("apply calls = %d", applier.calls)
	}
}

func TestSyncReportsKubernetesApplyFailure(t *testing.T) {
	desired := DesiredState{EnterpriseID: "demo", Revision: "rev-1", Routes: []Route{}}
	desired.ContentHash = canonicalHash(desired)
	statuses := make([]Status, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_ = json.NewEncoder(response).Encode(desired)
			return
		}
		var status Status
		_ = json.NewDecoder(request.Body).Decode(&status)
		statuses = append(statuses, status)
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	applier := &recordingApplier{err: errors.New("Kubernetes unavailable")}
	worker, err := New(Config{ControlURL: server.URL, Token: "token", OutputDir: t.TempDir(), Tenants: []string{"demo"}, HTTPClient: server.Client(), Applier: applier})
	if err != nil {
		t.Fatal(err)
	}
	err = worker.Sync(context.Background(), "demo")
	if err == nil || len(statuses) != 2 || statuses[1].State != "error" || statuses[1].ErrorCode == nil || *statuses[1].ErrorCode != "KUBERNETES_APPLY_FAILED" {
		t.Fatalf("Sync() error = %v, statuses = %#v", err, statuses)
	}
}
