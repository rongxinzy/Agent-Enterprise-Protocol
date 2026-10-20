package access

import (
	"context"
	"errors"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestManagedRouteLifecycle(t *testing.T) {
	g := fixture()
	c := newClient(t, g)
	checkRouteLifecycle(t, c, g)
}

func TestRoutesRemainDisabledByDefault(t *testing.T) {
	g := fixture()
	c := newClient(t, g)
	r := &Reconciler{Client: c, Reader: c, Options: options()}
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(g), g); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(g, routeFinalizer) {
		t.Fatal("default projection installed an ingress cleanup dependency")
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "higress-system", Name: g.Name}, bindingRoute()); !apierrors.IsNotFound(err) {
		t.Fatalf("default projection wrote an ingress route: %v", err)
	}
	config := authConfig()
	if err := c.Get(ctx, client.ObjectKeyFromObject(g), config); err != nil {
		t.Fatal(err)
	}
	headers, _, _ := unstructured.NestedMap(config.Object, "spec", "response", "success", "headers")
	if headers["x-aep-access-binding"] != nil || headers["x-aep-model-id"] != nil {
		t.Fatal("default native response enabled ingress handoff")
	}
}

func checkRouteLifecycle(t *testing.T, c client.Client, g *gateway.ModelAccessGrant) {
	t.Helper()
	ctx := context.Background()
	o := options()
	o.IngressHost = "shared.access.test"
	r := &Reconciler{Client: c, Reader: c, Options: o}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
	key := client.ObjectKey{Namespace: "higress-system", Name: g.Name}
	reconcile := func() {
		t.Helper()
		for range 2 { // First pass installs the source finalizer before any route.
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
		}
	}
	route := func() *unstructured.Unstructured {
		t.Helper()
		o := bindingRoute()
		if err := c.Get(ctx, key, o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	mutate := func(change func(*gateway.ModelAccessGrant)) {
		t.Helper()
		if err := c.Get(ctx, req.NamespacedName, g); err != nil {
			t.Fatal(err)
		}
		g.Spec.Publication.Sequence++
		change(g)
		markFixture(g)
		if err := c.Update(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	absent := func() {
		t.Helper()
		if err := c.Get(ctx, key, bindingRoute()); !apierrors.IsNotFound(err) {
			t.Fatalf("route was not reclaimed: %v", err)
		}
	}
	reconcile()
	first := route()
	if !routeOwned(first, g) || len(first.GetOwnerReferences()) != 0 {
		t.Fatal("missing UID ownership or invalid cross-namespace owner reference")
	}
	config := authConfig()
	if err := c.Get(ctx, req.NamespacedName, config); err != nil {
		t.Fatal(err)
	}
	headers, _, _ := unstructured.NestedMap(config.Object, "spec", "response", "success", "headers")
	if headers["x-aep-model-id"] == nil || headers["x-aep-access-binding"] == nil {
		t.Fatal("route handoff lacks authenticated model/binding overwrite")
	}
	mutate(func(g *gateway.ModelAccessGrant) {
		g.Spec.ValidUntil = metav1.NewTime(g.Spec.ValidUntil.Add(time.Minute))
	})
	reconcile()
	if route().GetResourceVersion() != first.GetResourceVersion() {
		t.Fatal("renewal unnecessarily rewrote a stable session route")
	}
	if err := c.Delete(ctx, route()); err != nil {
		t.Fatal(err)
	}
	reconcile()
	drift := route()
	drift.Object["spec"].(map[string]any)["configPatches"] = []any{}
	if err := c.Update(ctx, drift); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if patches, _, _ := unstructured.NestedSlice(route().Object, "spec", "configPatches"); len(patches) != 1 {
		t.Fatal("owned route drift was not repaired")
	}
	mutate(func(g *gateway.ModelAccessGrant) { g.Spec.Disabled = true; g.Spec.Models = []string{} })
	reconcile()
	absent()
	mutate(func(g *gateway.ModelAccessGrant) { g.Spec.Disabled = false; g.Spec.Models = []string{"model-a"} })
	reconcile()
	_ = route()
	mutate(func(g *gateway.ModelAccessGrant) {
		g.Spec.IssuedAt = metav1.NewTime(time.Now().Add(-2 * time.Minute))
		g.Spec.ValidUntil = metav1.NewTime(time.Now().Add(-time.Minute))
	})
	reconcile()
	absent()
	mutate(func(g *gateway.ModelAccessGrant) {
		g.Spec.IssuedAt = metav1.NewTime(time.Now().UTC().Truncate(time.Second))
		g.Spec.ValidUntil = metav1.NewTime(g.Spec.IssuedAt.Add(5 * time.Minute))
	})
	reconcile()
	_ = route()
	if err := c.Get(ctx, req.NamespacedName, g); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(g, routeFinalizer) {
		t.Fatal("cross-namespace cleanup is not fenced")
	}
	if err := c.Delete(ctx, g); err != nil {
		t.Fatal(err)
	}
	reconcile()
	absent()
	if err := c.Get(ctx, req.NamespacedName, &gateway.ModelAccessGrant{}); !apierrors.IsNotFound(err) {
		t.Fatalf("finalized grant remains: %v", err)
	}
}

func TestRouteOwnershipAndLateWrite(t *testing.T) {
	ctx := context.Background()
	g := fixture()
	c := newClient(t, g)
	o := options()
	o.IngressHost = "shared.access.test"
	r := &Reconciler{Client: c, Reader: c, Options: o}
	foreign := renderBindingRoute(g, o.IngressHost)
	foreign.SetLabels(map[string]string{routeLabel: "true", routeOwner: "someone-else"})
	if err := c.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.writeBindingRoute(ctx, g); !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign route overwritten: %v", err)
	}
	if _, err := r.removeBindingRoute(ctx, g); !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign route deleted: %v", err)
	}
	old := g.DeepCopy()
	g.Spec.Publication.Sequence++
	markFixture(g)
	if err := c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := r.writeBindingRoute(ctx, old); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("superseded publication attempted a write: %v", err)
	}
	if _, err := r.removeBindingRoute(ctx, old); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("superseded cleanup attempted a delete: %v", err)
	}
}

func TestRouteCleanupWaitsForDeletion(t *testing.T) {
	ctx := context.Background()
	g := fixture()
	g.Finalizers = []string{routeFinalizer}
	c := newClient(t, g)
	o := options()
	o.IngressHost = "shared.access.test"
	r := &Reconciler{Client: c, Reader: c, Options: o}
	route := renderBindingRoute(g, o.IngressHost)
	route.SetFinalizers([]string{"test.example/hold"})
	if err := c.Create(ctx, route); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(g), g); err != nil {
		t.Fatal(err)
	}
	if done, err := r.finalizeRoute(ctx, g); done || err != nil {
		t.Fatalf("finalized before route deletion: %v %v", done, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(g), g); err != nil || !controllerutil.ContainsFinalizer(g, routeFinalizer) {
		t.Fatalf("source fence lost while native deletion is pending: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
		t.Fatal(err)
	}
	route.SetFinalizers(nil)
	if err := c.Update(ctx, route); err != nil {
		t.Fatal(err)
	}
	if done, err := r.finalizeRoute(ctx, g); !done || err != nil {
		t.Fatalf("route cleanup did not release source: %v %v", done, err)
	}
}

func TestForeignRouteRetainsSourceFence(t *testing.T) {
	ctx := context.Background()
	g := fixture()
	g.Finalizers = []string{routeFinalizer}
	c := newClient(t, g)
	r := &Reconciler{Client: c, Reader: c, Options: options()}
	route := renderBindingRoute(g, "shared.access.test")
	route.SetLabels(map[string]string{routeLabel: "true", routeOwner: "foreign-uid"})
	if err := c.Create(ctx, route); err != nil {
		t.Fatal(err)
	}
	if done, err := r.finalizeRoute(ctx, g); done || !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign resource released source fence: %v %v", done, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(g), g); err != nil || !controllerutil.ContainsFinalizer(g, routeFinalizer) {
		t.Fatalf("ownership failure lost source fence: %v", err)
	}
}
