package reconciler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func TestKubernetesApplierUsesServerSideApplyForHigressResources(t *testing.T) {
	t.Parallel()
	type appliedResource struct {
		path        string
		query       string
		contentType string
		auth        string
		body        string
	}
	var mutex sync.Mutex
	applied := make([]appliedResource, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(request.Body)
		mutex.Lock()
		applied = append(applied, appliedResource{path: request.URL.Path, query: request.URL.RawQuery, contentType: request.Header.Get("Content-Type"), auth: request.Header.Get("Authorization"), body: string(body)})
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"metadata":{"name":"applied"}}`))
	}))
	defer server.Close()

	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "service-account-token"})
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredState{DeploymentID: "Demo Tenant", Revision: "rev-1", Routes: []gatewayrender.Route{{ModelID: "chat", Enabled: true, Endpoint: "/v1/chat", UpstreamModel: "upstream", Protocol: "openai-compatible"}}}
	_, resources, _, err := gatewayrender.Render(renderInput(desired), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), desired, resources); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 3 {
		t.Fatalf("apply requests = %d", len(applied))
	}
	for _, request := range applied {
		if request.contentType != "application/apply-patch+yaml" || request.auth != "Bearer service-account-token" {
			t.Fatalf("unexpected headers: %#v", request)
		}
		if !strings.Contains(request.query, "fieldManager=aep-gateway-reconciler") || !strings.Contains(request.query, "force=true") {
			t.Fatalf("unexpected apply query: %s", request.query)
		}
		if strings.Contains(request.body, "service-account-token") {
			t.Fatal("service account token leaked into resource body")
		}
	}
	if applied[0].path != gatewayrender.IngressAPIPath(gatewayrender.OpenAIResourceName("Demo Tenant", "chat")) || !strings.Contains(applied[1].path, "/wasmplugins/aep-ai-proxy-demo-tenant-") || !strings.Contains(applied[2].path, "/wasmplugins/aep-ai-statistics-demo-tenant-") {
		t.Fatalf("unexpected resource paths: %#v", applied)
	}
}

func TestKubernetesApplierValidatesConfigurationAndResources(t *testing.T) {
	for _, config := range []KubernetesConfig{
		{URL: "https://kubernetes.example"},
		{Token: "token"},
		{URL: "ftp://kubernetes.example", Token: "token"},
		{URL: "://broken", Token: "token"},
	} {
		if _, err := NewKubernetesApplier(config); err == nil {
			t.Fatalf("accepted invalid Kubernetes config: %#v", config)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing-ca")
	if _, err := NewKubernetesApplier(KubernetesConfig{URL: "https://kubernetes.example", Token: "token", CAFile: missing}); err == nil || !strings.Contains(err.Error(), "read Kubernetes CA") {
		t.Fatalf("missing CA error = %v", err)
	}
	ca := filepath.Join(t.TempDir(), "invalid-ca")
	if err := os.WriteFile(ca, []byte("not a PEM certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKubernetesApplier(KubernetesConfig{URL: "https://kubernetes.example", Token: "token", CAFile: ca}); err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Fatalf("invalid CA error = %v", err)
	}
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: "http://localhost", Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), DesiredState{DeploymentID: "demo"}, nil); err == nil {
		t.Fatal("Apply accepted a render without the WasmPlugin")
	}
}

func TestKubernetesApplierHandlesDeletedAndRejectedIngress(t *testing.T) {
	for _, test := range []struct {
		name         string
		deleteStatus int
		wantError    bool
	}{
		{"already absent", http.StatusNotFound, false},
		{"delete rejected", http.StatusForbidden, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			methods := make([]string, 0, 2)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				methods = append(methods, request.Method)
				if request.Method == http.MethodDelete {
					response.WriteHeader(test.deleteStatus)
				}
			}))
			defer server.Close()
			applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token"})
			if err != nil {
				t.Fatal(err)
			}
			desired := DesiredState{DeploymentID: "demo", Revision: "rev-1"}
			_, resources, _, err := gatewayrender.Render(renderInput(desired), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = applier.Apply(context.Background(), desired, resources)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "403") || len(methods) != 3 {
					t.Fatalf("Apply() = %v, methods = %v", err, methods)
				}
			} else if err != nil || len(methods) != 3 || methods[0] != http.MethodPatch || methods[1] != http.MethodPatch || methods[2] != http.MethodDelete {
				t.Fatalf("Apply() = %v, methods = %v", err, methods)
			}
		})
	}
}

func TestKubernetesApplierStopsAfterPartialFailure(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(response, "admission rejected resource", http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{{ModelID: "chat", Enabled: true, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible"}}}
	_, resources, _, err := gatewayrender.Render(renderInput(desired), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = applier.Apply(context.Background(), desired, resources)
	if err == nil || !strings.Contains(err.Error(), "422") || requests != 1 {
		t.Fatalf("Apply() error = %v, requests = %d", err, requests)
	}
}

func TestKubernetesApplierDeletesIngressWhenAllRoutesAreDisabled(t *testing.T) {
	t.Parallel()
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method+" "+request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-disabled", Routes: []gatewayrender.Route{{ModelID: "chat", Enabled: false, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible"}}}
	_, resources, _, err := gatewayrender.Render(renderInput(desired), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), desired, resources); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 5 || methods[0] != "DELETE "+gatewayrender.EnvoyFilterAPIPath(gatewayrender.OpenAIResourceName("demo", "chat")) || methods[1] != "DELETE "+gatewayrender.IngressAPIPath(gatewayrender.OpenAIResourceName("demo", "chat")) || !strings.HasPrefix(methods[2], "PATCH ") || !strings.Contains(methods[2], "/wasmplugins/") || !strings.Contains(methods[3], "/wasmplugins/aep-ai-statistics-") || methods[4] != "DELETE "+gatewayrender.OpenAIIngressAPIPath(gatewayrender.ResourceSuffix("demo")) {
		t.Fatalf("requests = %#v", methods)
	}
}

func TestKubernetesApplierRemovesAbsoluteUpstreamOnRelativeTransition(t *testing.T) {
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operations = append(operations, r.Method+" "+r.URL.Path)
	}))
	defer server.Close()
	a, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://api.example/v1", "/v1"} {
		operations = nil
		d := DesiredState{DeploymentID: "demo", Revision: "rev", Routes: []gatewayrender.Route{{ModelID: "chat", Enabled: true, Endpoint: endpoint}}}
		_, resources, _, err := gatewayrender.Render(renderInput(d), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Apply(context.Background(), d, resources); err != nil {
			t.Fatal(err)
		}
		path := gatewayrender.EnvoyFilterAPIPath(gatewayrender.OpenAIResourceName("demo", "chat"))
		want := "PATCH " + path
		forbidden := "DELETE " + path
		if endpoint == "/v1" {
			want, forbidden = forbidden, want
		}
		seen := strings.Join(operations, "\n")
		if !strings.Contains(seen, want) || strings.Contains(seen, forbidden) || strings.Contains(seen, "DELETE "+gatewayrender.IngressAPIPath(gatewayrender.OpenAIResourceName("demo", "chat"))) {
			t.Fatalf("unexpected lifecycle: %v", operations)
		}
	}
}

func TestKubernetesApplierDeletesAnthropicPairWhenRouteDisabled(t *testing.T) {
	t.Parallel()
	operations := make([]string, 0, 8)
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		operations = append(operations, request.Method+" "+request.URL.Path)
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	disabled := gatewayrender.Route{ModelID: "bench-anthropic", Enabled: false, Endpoint: "https://open.bigmodel.cn/api/anthropic", UpstreamModel: "upstream", Protocol: "anthropic", CredentialRef: &gatewayrender.SecretReference{Name: "aep-credential-bigmodel", Key: "api-key"}}
	desired := DesiredState{DeploymentID: "demo", Revision: "rev-2", Routes: []gatewayrender.Route{disabled}}
	_, resources, _, err := gatewayrender.Render(renderInput(desired), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), desired, resources); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	name := gatewayrender.AnthropicResourceName("bench-anthropic")
	seen := make(map[string]bool, len(operations))
	for _, operation := range operations {
		seen[operation] = true
	}
	// Disabled anthropic route: its Ingress+EnvoyFilter pair is deleted (plus
	// the idle openai ingress per the no-enabled-openai rule); the WasmPlugin
	// is still applied.
	for _, path := range []string{
		"DELETE " + gatewayrender.IngressAPIPath(name),
		"DELETE " + gatewayrender.EnvoyFilterAPIPath(name),
		"DELETE " + gatewayrender.OpenAIIngressAPIPath(gatewayrender.ResourceSuffix("demo")),
		"PATCH " + gatewayrender.WasmPluginAPIPath(gatewayrender.ResourceSuffix("demo")),
		"PATCH " + gatewayrender.AIStatisticsAPIPath(gatewayrender.ResourceSuffix("demo")),
	} {
		if !seen[path] {
			t.Fatalf("missing %q in %#v", path, operations)
		}
	}
	if len(operations) != 5 {
		t.Fatalf("operations = %#v", operations)
	}
}

func TestBearerTokenFlowsFromStaticValueAndTokenFile(t *testing.T) {
	t.Parallel()
	// client-go's cached file token source reads the token file at client
	// construction and re-reads it at a one-minute cadence tuned to the
	// kubelet's projected-token refresh; the production tokens rotate hourly.
	// The assertions here pin the wiring at the wire boundary: the initial
	// file content wins, a fresh client on the rotated file sees the new
	// value, and a static token works without a file.
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		tokens = append(tokens, request.Header.Get("Authorization"))
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"provider","namespace":"higress-system"},"data":{"api-key":"eA=="}}`))
	}))
	defer server.Close()

	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applier.ReadSecret(context.Background(), "provider", "api-key"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.ReadSecret(context.Background(), "provider", "api-key"); err != nil {
		t.Fatal(err)
	}
	static, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "static"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := static.ReadSecret(context.Background(), "provider", "api-key"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Bearer first-token", "Bearer rotated-token", "Bearer static"}
	if strings.Join(tokens, ",") != strings.Join(want, ",") {
		t.Fatalf("authorization headers = %v", tokens)
	}
}

func TestKubernetesApplierReadSecretThroughTypedClient(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/namespaces/higress-system/secrets/good":
			_, _ = response.Write([]byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"good","namespace":"higress-system"},"data":{"api-key":"c2VjcmV0LXZhbHVl"}}`))
		case "/api/v1/namespaces/higress-system/secrets/missing-key":
			_, _ = response.Write([]byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"missing-key","namespace":"higress-system"},"data":{"other":"eA=="}}`))
		case "/api/v1/namespaces/higress-system/secrets/bad-base64":
			_, _ = response.Write([]byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"bad-base64","namespace":"higress-system"},"data":{"api-key":"!!not-base64!!"}}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := applier.ReadSecret(context.Background(), "good", "api-key")
	if err != nil || value != "secret-value" {
		t.Fatalf("ReadSecret(good) = %q, %v", value, err)
	}
	if _, err := applier.ReadSecret(context.Background(), "missing-key", "api-key"); err == nil || !strings.Contains(err.Error(), "has no key api-key") {
		t.Fatalf("ReadSecret(missing-key) = %v", err)
	}
	// The typed client decodes data fields itself: corrupt base64 fails the
	// whole read instead of surfacing a per-key decode step.
	if _, err := applier.ReadSecret(context.Background(), "bad-base64", "api-key"); err == nil {
		t.Fatal("ReadSecret(bad-base64) succeeded")
	}
	if _, err := applier.ReadSecret(context.Background(), "absent", "api-key"); err == nil || !strings.Contains(err.Error(), "returned 404") {
		t.Fatalf("ReadSecret(absent) = %v", err)
	}
}

func TestKubernetesApplierPinsJSONWireFormat(t *testing.T) {
	t.Parallel()
	// client-go's generated typed clients prefer protobuf for built-in
	// resources (the leader-election Lease included) unless the content type
	// is pinned; JSON-only API doubles and every real API server both speak
	// JSON, so the pin keeps the wire format explicit.
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: "http://localhost", Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if applier.config.ContentType != "application/json" {
		t.Fatalf("wire content type = %q", applier.config.ContentType)
	}
}

func TestBearerTokenRunningInstanceSurvivesFileRotation(t *testing.T) {
	t.Parallel()
	// The review asked for a RUNNING instance crossing an in-place projected
	// token rotation. client-go's cached file source re-reads at a one-minute
	// cadence (kubelet-tuned), so the immediate successor request still carries
	// the previous token — Kubernetes bound tokens stay valid across the
	// overlap window, which is exactly why the cadence is safe. What must never
	// happen is an empty credential or a wedged client after the swap.
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		auth := request.Header.Get("Authorization")
		if auth == "" || auth == "Bearer " {
			t.Errorf("request carried an empty credential")
		}
		tokens = append(tokens, auth)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"provider","namespace":"higress-system"},"data":{"api-key":"eA=="}}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applier.ReadSecret(context.Background(), "provider", "api-key"); err != nil {
		t.Fatal(err)
	}
	// Rotate the projected token in place mid-flight; the running instance
	// keeps serving with the cached token until the one-minute source refresh.
	if err := os.WriteFile(path, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applier.ReadSecret(context.Background(), "provider", "api-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := applier.ReadSecret(context.Background(), "provider", "api-key"); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 3 || tokens[0] != "Bearer first-token" || tokens[1] != "Bearer first-token" || tokens[2] != "Bearer first-token" {
		t.Fatalf("authorization headers across rotation = %v", tokens)
	}
}
