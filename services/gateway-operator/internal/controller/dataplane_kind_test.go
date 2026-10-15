package controller

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/dataplane"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Explicit opt-in read-only integration check. Never prints dumps, render
// inputs or low-level errors. Shared automated suites use mock providers.
func TestLocalKindLoadedConfiguration(t *testing.T) {
	path := os.Getenv("AEP_DATAPLANE_KUBECONFIG")
	if path == "" {
		t.Skip("set AEP_DATAPLANE_KUBECONFIG for isolated Kind acceptance")
	}
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal("isolated cluster configuration unavailable")
	}
	config.Timeout = 5 * time.Second
	scheme := runtime.NewScheme()
	if clientgoscheme.AddToScheme(scheme) != nil || gateway.AddToScheme(scheme) != nil {
		t.Fatal("scheme initialization failed")
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal("isolated cluster client unavailable")
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal("isolated cluster transport unavailable")
	}
	rt := &Runtime{Client: c, Reader: c, CredentialNamespace: "aep-credentials", NativeSecretNamespace: "higress-system"}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	in, err := rt.snapshot(ctx, client.ObjectKey{Namespace: "aep-system", Name: "deployment"})
	if err != nil {
		t.Fatal("isolated cluster desired snapshot unavailable")
	}
	desired, err := dataplane.Compile(in.resources, in.forbidden)
	if err != nil {
		t.Fatal("isolated cluster desired snapshot unsupported")
	}
	fleet := &dataplane.Fleet{Reader: c, Deployment: client.ObjectKey{Namespace: "higress-system", Name: "higress-gateway"}, Dump: dataplane.PodDump(config, kube)}
	evidence, err := fleet.Observe(ctx, desired)
	if err != nil {
		t.Fatalf("isolated cluster observation: %v", err)
	}
	if len(evidence.PodUIDs) == 0 {
		t.Fatal("no serving replicas observed")
	}
	t.Logf("verified loaded configuration in %d serving gateway replicas", len(evidence.PodUIDs))
	// Reuse real pod/API reads, but fail one required transport. A successful
	// other replica must never be reported as full-fleet evidence.
	goodDump := fleet.Dump
	refused := evidence.PodUIDs[len(evidence.PodUIDs)-1]
	fleet.Dump = func(ctx context.Context, p core.Pod) ([]byte, error) {
		if p.UID == refused {
			return nil, errors.New("simulated single-replica transport failure")
		}
		return goodDump(ctx, p)
	}
	if e, err := fleet.Observe(ctx, desired); err == nil || !e.ObservedAt.IsZero() {
		t.Fatal("partial fleet was accepted")
	}
	fleet.Dump = goodDump
	// Alter only an expected artifact digest in memory, leaving the running
	// cluster untouched. Its actual ECDS code must fail the comparison.
	altered := append([]gatewayrender.RenderedResource(nil), in.resources...)
	tested := false
	for i := range altered {
		var obj struct {
			Kind string
			Spec struct {
				URL        string
				MatchRules []any
			}
		}
		if yaml.Unmarshal([]byte(altered[i].Body), &obj) != nil || obj.Kind != "WasmPlugin" || len(obj.Spec.MatchRules) == 0 {
			continue
		}
		parts := strings.Split(obj.Spec.URL, "@sha256:")
		if len(parts) != 2 {
			continue
		}
		altered[i].Body = strings.ReplaceAll(altered[i].Body, parts[1], strings.Repeat("0", 64))
		tested = true
		break
	}
	if tested {
		wrong, err := dataplane.Compile(altered, in.forbidden)
		if err != nil {
			t.Fatal("negative artifact fixture unsupported")
		}
		if e, err := fleet.Observe(ctx, wrong); err == nil || !e.ObservedAt.IsZero() {
			t.Fatal("loaded artifact mismatch was accepted")
		}
		t.Log("rejected mismatched actual ECDS artifact and partial serving fleet")
	}
}
