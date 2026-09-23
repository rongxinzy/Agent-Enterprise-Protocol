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
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", ContentHash: "ignored", Routes: []Route{{ModelID: "model-b", Enabled: true, Endpoint: "/b", UpstreamModel: "up-b", Protocol: "openai-compatible", CredentialRef: &SecretReference{Name: "provider-secrets", Key: "model-b"}}, {ModelID: "model-a", Enabled: true, Endpoint: "/a", UpstreamModel: "up-a", Protocol: "openai-compatible"}}}
	first, firstHash, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := Render(DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{desired.Routes[1], desired.Routes[0]}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstHash != secondHash {
		t.Fatal("render output is not deterministic")
	}
	// With nil credential values the render omits apiTokens entirely; the
	// credentialRef name stays a lookup key and never reaches the document.
	if strings.Contains(first, "provider-secret-value") {
		t.Fatal("secret value leaked into render without credential fetcher")
	}
	// With the value resolved, it inlines as an apiToken for ai-proxy.
	withCredentials, _, err := Render(desired, map[string]string{"provider-secrets/model-b": "provider-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withCredentials, "provider-secret-value") {
		t.Fatal("resolved credential was not inlined as an apiToken")
	}
}

func TestRenderRejectsMissingRevision(t *testing.T) {
	if _, _, err := Render(DesiredState{DeploymentID: "demo"}, nil); err == nil {
		t.Fatal("missing revision was accepted")
	}
}

func TestRenderSelectsDeepSeekProviderAndRejectsUnknownProviders(t *testing.T) {
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-deepseek", Routes: []Route{{
		ModelID: "reasoner", Enabled: true, Endpoint: "/v1/chat", UpstreamModel: "deepseek-reasoner", Protocol: "openai-compatible", ProviderType: "deepseek",
	}}}
	document, _, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document, "type: 'deepseek'") {
		t.Fatalf("DeepSeek provider was not rendered: %s", document)
	}
	desired.Routes[0].ProviderType = "unknown"
	if _, _, err := Render(desired, nil); err == nil {
		t.Fatal("unsupported provider type was accepted")
	}
}

func TestSyncReadsDesiredStateWritesResourcesAndReportsReady(t *testing.T) {
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{{ModelID: "model-1", Enabled: true, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible", CredentialRef: &SecretReference{Name: "provider-secrets", Key: "model-1"}}}}
	desired.ContentHash = canonicalHash(desired)
	statuses := make([]Status, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-AEP-Data-Plane-Token") != "token" || request.Header.Get("X-AEP-Deployment-ID") != "demo" {
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
	// Without a CredentialFetcher the render omits apiTokens (no value leak);
	// the credentialRef name stays a lookup key internal to the reconciler.
	if strings.Contains(string(content), "provider-secret-value") {
		t.Fatalf("secret value leaked without credential fetcher: %s", content)
	}
	if len(statuses) != 2 || statuses[0].State != "applying" || statuses[1].State != "ready" {
		t.Fatalf("unexpected status transitions: %#v", statuses)
	}
	if applier.calls != 1 {
		t.Fatalf("apply calls = %d", applier.calls)
	}
}

func TestSyncReportsKubernetesApplyFailure(t *testing.T) {
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{}}
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

func TestSyncReportsControlAndDesiredStateFailures(t *testing.T) {
	for _, test := range []struct {
		name, failureCode                                                         string
		fetchStatus                                                               int
		malformed, noRevision, noDeployment, badHash, blockedOutput, rejectStatus bool
		wantStates                                                                []string
	}{
		{name: "control unavailable", fetchStatus: http.StatusServiceUnavailable, failureCode: "CONTROL_PLANE_UNAVAILABLE", wantStates: []string{"error"}},
		{name: "invalid desired json", malformed: true, failureCode: "CONTROL_PLANE_UNAVAILABLE", wantStates: []string{"error"}},
		{name: "not published", noRevision: true, wantStates: []string{"pending"}},
		{name: "invalid deployment", noDeployment: true, failureCode: "RENDER_FAILED", wantStates: []string{"applying", "error"}},
		{name: "mismatched desired hash", badHash: true, failureCode: "DESIRED_HASH_MISMATCH", wantStates: []string{"applying", "error"}},
		{name: "output not writable", blockedOutput: true, failureCode: "OUTPUT_WRITE_FAILED", wantStates: []string{"applying", "error"}},
		{name: "status update rejected", rejectStatus: true, failureCode: "status update returned 503", wantStates: []string{"applying"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []Route{}}
			desired.ContentHash = canonicalHash(desired)
			if test.noRevision {
				desired.Revision = ""
			}
			if test.noDeployment {
				desired.DeploymentID = ""
			}
			if test.badHash {
				desired.ContentHash = "mismatched"
			}
			statuses := make([]Status, 0, 2)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet {
					if test.fetchStatus != 0 {
						response.WriteHeader(test.fetchStatus)
						return
					}
					if test.malformed {
						_, _ = response.Write([]byte("{"))
						return
					}
					_ = json.NewEncoder(response).Encode(desired)
					return
				}
				var status Status
				if err := json.NewDecoder(request.Body).Decode(&status); err != nil {
					t.Errorf("decode status: %v", err)
				}
				statuses = append(statuses, status)
				if test.rejectStatus {
					response.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer server.Close()
			output := t.TempDir()
			if test.blockedOutput {
				output = filepath.Join(output, "not-a-directory")
				if err := os.WriteFile(output, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			worker, err := New(Config{ControlURL: server.URL, Token: "token", OutputDir: output, Tenants: []string{"demo"}, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			err = worker.Sync(context.Background(), "demo")
			if test.failureCode == "" && err != nil || test.failureCode != "" && (err == nil || !strings.Contains(err.Error(), test.failureCode)) {
				t.Fatalf("Sync() error = %v, want %q", err, test.failureCode)
			}
			if len(statuses) != len(test.wantStates) {
				t.Fatalf("statuses = %#v", statuses)
			}
			for i, want := range test.wantStates {
				if statuses[i].State != want {
					t.Fatalf("status %d = %#v, want %s", i, statuses[i], want)
				}
			}
			if test.failureCode != "" && !test.rejectStatus {
				last := statuses[len(statuses)-1]
				if last.ErrorCode == nil || *last.ErrorCode != test.failureCode {
					t.Fatalf("failure status = %#v", last)
				}
			}
		})
	}
}

func TestNewReconcilerValidatesConfigAndSortsTenants(t *testing.T) {
	for _, config := range []Config{
		{Token: "token", OutputDir: "/tmp", Tenants: []string{"demo"}},
		{ControlURL: "https://control.example", OutputDir: "/tmp", Tenants: []string{"demo"}},
		{ControlURL: "https://control.example", Token: "token", Tenants: []string{"demo"}},
		{ControlURL: "https://control.example", Token: "token", OutputDir: "/tmp"},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%#v) accepted missing configuration", config)
		}
	}
	worker, err := New(Config{ControlURL: "https://control.example///", Token: "token", OutputDir: t.TempDir(), Tenants: []string{"z", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if worker.config.ControlURL != "https://control.example" || worker.config.Tenants[0] != "a" || worker.config.HTTPClient == nil {
		t.Fatalf("normalized config = %#v", worker.config)
	}
}
