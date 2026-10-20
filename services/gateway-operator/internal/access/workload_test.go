package access

import (
	"context"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func workloadFixture() *gateway.ModelAccessGrant {
	g := fixture()
	g.Spec.Subject = gateway.ModelAccessSubject{Kind: "agent", ID: "employee-agent"}
	g.Spec.Binding = gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
		Namespace: "employees", ServiceAccountName: "employee-a", ServiceAccountUID: "sa-a",
		EmployeeUID: "employee-cr-a", Audience: "zhiyuan-model-access",
	}}
	g.Name = "access-" + bindingKey(g.Spec)
	markFixture(g)
	return g
}

func workloadOptions() Options {
	o := options()
	o.WorkloadAudience = "zhiyuan-model-access"
	return o
}

func TestWorkloadOptInDoesNotReplaceHumanJWTAuthentication(t *testing.T) {
	config, _, err := Render(fixture(), workloadOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, jwt, _ := unstructured.NestedMap(config.Object, "spec", "authentication", "session", "jwt")
	_, review, _ := unstructured.NestedMap(config.Object, "spec", "authentication", "workload", "kubernetesTokenReview")
	if !jwt || review {
		t.Fatal("workload opt-in replaced human JWT authentication")
	}
}

func TestWorkloadRejectsUntrustedOrIncompleteBindings(t *testing.T) {
	for name, mutate := range map[string]func(*gateway.ModelAccessGrant, *Options){
		"default off":          func(_ *gateway.ModelAccessGrant, o *Options) { o.WorkloadAudience = "" },
		"entitlement required": func(_ *gateway.ModelAccessGrant, o *Options) { o.RequireEntitlement = true },
		"human":                func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Subject.Kind = "human" },
		"session":              func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.SessionID = "owner-session" },
		"missing tuple":        func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload = nil },
		"audience":             func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload.Audience = "other" },
		"namespace":            func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload.Namespace = "../ns" },
		"account":              func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload.ServiceAccountName = "a/b" },
		"missing sa uid":       func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload.ServiceAccountUID = "" },
		"missing employee uid": func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload.EmployeeUID = "" },
		"control uid":          func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Binding.Workload.ServiceAccountUID = "sa\n" },
		"disabled with models": func(g *gateway.ModelAccessGrant, _ *Options) { g.Spec.Disabled = true },
	} {
		t.Run(name, func(t *testing.T) {
			g, o := workloadFixture(), workloadOptions()
			mutate(g, &o)
			g.Name = "access-" + bindingKey(g.Spec)
			markFixture(g)
			if _, _, err := Render(g, o); err == nil {
				t.Fatal("unsafe workload grant rendered")
			}
		})
	}
	for _, audience := range []string{"a b", "a\n", strings.Repeat("a", 257)} {
		o := workloadOptions()
		o.WorkloadAudience = audience
		if o.Validate() == nil {
			t.Fatal("invalid audience accepted")
		}
	}
}

func TestWorkloadRenewalPreservesNativeCacheAndIdentityChangesInvalidateIt(t *testing.T) {
	g, o := workloadFixture(), workloadOptions()
	first, _, err := Render(g, o)
	if err != nil {
		t.Fatal(err)
	}
	auth, found, err := unstructured.NestedMap(first.Object, "spec", "authentication", "workload", "kubernetesTokenReview")
	if err != nil || !found || len(auth["audiences"].([]any)) != 1 {
		t.Fatalf("missing native review: %#v %v", auth, err)
	}
	g.Spec.Publication.Sequence++
	g.Spec.ValidUntil.Time = g.Spec.ValidUntil.Add(time.Minute)
	markFixture(g)
	renewed, _, err := Render(g, o)
	if err != nil || renewed.GetAnnotations()[SemanticHash] != first.GetAnnotations()[SemanticHash] {
		t.Fatalf("renewal invalidates cache: %v", err)
	}
	for name, mutate := range map[string]func(*gateway.ModelAccessGrant){
		"roles":               func(g *gateway.ModelAccessGrant) { g.Spec.RoleIDs = []string{"another-role"} },
		"model removal":       func(g *gateway.ModelAccessGrant) { g.Spec.Models = []string{"model-b"} },
		"employee recreation": func(g *gateway.ModelAccessGrant) { g.Spec.Binding.Workload.EmployeeUID = "employee-cr-b" },
		"sa recreation":       func(g *gateway.ModelAccessGrant) { g.Spec.Binding.Workload.ServiceAccountUID = "sa-b" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := g.DeepCopy()
			mutate(changed)
			changed.Name = "access-" + bindingKey(changed.Spec)
			markFixture(changed)
			config, _, err := Render(changed, o)
			if err != nil || config.GetAnnotations()[SemanticHash] == first.GetAnnotations()[SemanticHash] {
				t.Fatalf("changed identity or policy reused cache: %v", err)
			}
		})
	}
}

func TestWorkloadControllerUsesExistingPublicationFences(t *testing.T) {
	g := workloadFixture()
	c := newClient(t, g)
	r := &Reconciler{Client: c, Reader: c, Options: workloadOptions()}
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	first := authConfig()
	if err := c.Get(ctx, req.NamespacedName, first); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, g); err != nil {
		t.Fatal(err)
	}
	g.Spec.Publication.Sequence++
	g.Spec.ValidUntil.Time = g.Spec.ValidUntil.Add(time.Minute)
	markFixture(g)
	if err := c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	renewed := authConfig()
	if err := c.Get(ctx, req.NamespacedName, renewed); err != nil {
		t.Fatal(err)
	}
	if first.GetResourceVersion() != renewed.GetResourceVersion() {
		t.Fatal("renewal rebuilt AuthConfig")
	}
}
