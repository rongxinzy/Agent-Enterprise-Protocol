package reconciler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
		body, _ := io.ReadAll(request.Body)
		mutex.Lock()
		applied = append(applied, appliedResource{path: request.URL.Path, query: request.URL.RawQuery, contentType: request.Header.Get("Content-Type"), auth: request.Header.Get("Authorization"), body: string(body)})
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"metadata":{"name":"applied"}}`))
	}))
	defer server.Close()

	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "service-account-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredState{EnterpriseID: "Demo Tenant", Revision: "rev-1", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: "/v1/chat", UpstreamModel: "upstream", Protocol: "openai-compatible"}}}
	document, _, err := Render(desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), desired, document); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 {
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
	if !strings.Contains(applied[0].path, "/ingresses/aep-model-gateway-demo-tenant-") || !strings.Contains(applied[1].path, "/wasmplugins/aep-ai-proxy-demo-tenant-") {
		t.Fatalf("unexpected resource paths: %#v", applied)
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
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredState{EnterpriseID: "demo", Revision: "rev-1", Routes: []Route{{ModelID: "chat", Enabled: true, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible"}}}
	document, _, err := Render(desired)
	if err != nil {
		t.Fatal(err)
	}
	err = applier.Apply(context.Background(), desired, document)
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
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	desired := DesiredState{EnterpriseID: "demo", Revision: "rev-disabled", Routes: []Route{{ModelID: "chat", Enabled: false, Endpoint: "/v1", UpstreamModel: "upstream", Protocol: "openai-compatible"}}}
	document, _, err := Render(desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), desired, document); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || !strings.HasPrefix(methods[0], "DELETE ") || !strings.Contains(methods[0], "/ingresses/") || !strings.HasPrefix(methods[1], "PATCH ") || !strings.Contains(methods[1], "/wasmplugins/") {
		t.Fatalf("requests = %#v", methods)
	}
}

func TestResourceSuffixIsStableAndKubernetesSafe(t *testing.T) {
	t.Parallel()
	first := resourceSuffix("Customer / North-East 01")
	second := resourceSuffix("Customer / North-East 01")
	if first != second || len(first) > 49 || strings.ContainsAny(first, " /_") {
		t.Fatalf("resource suffix = %q", first)
	}
}
