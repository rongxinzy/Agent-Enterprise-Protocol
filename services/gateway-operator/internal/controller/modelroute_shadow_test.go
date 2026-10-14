package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// The shadow render (migration phase 1) must never perturb the reconcile it
// rides on: render failures are logged, not propagated, and the Accepted
// condition is still written.
func TestShadowRenderNeverFailsReconcile(t *testing.T) {
	t.Run("render error is swallowed", func(t *testing.T) {
		// Two anthropic routes whose slug sanitization collides make the
		// shared render fail; the reconcile must still succeed.
		first := routeFixture("slug-first")
		first.Spec.Protocol = "anthropic"
		first.Spec.ModelID = "Bench GLM"
		second := routeFixture("slug-second")
		second.Spec.Protocol = "anthropic"
		second.Spec.ModelID = "bench-glm"
		k8sClient := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(first, second).WithStatusSubresource(&gatewayv1alpha1.ModelRoute{}).Build()
		reconciler := &ModelRouteReconciler{Client: k8sClient, Scheme: controllerTestScheme, ShadowDeployment: "shadow-demo"}
		if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "slug-first", Namespace: "default"}}); err != nil {
			t.Fatalf("shadow render failure must not fail the reconcile: %v", err)
		}
		var stored gatewayv1alpha1.ModelRoute
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "slug-first", Namespace: "default"}, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.Status.Conditions) != 1 || stored.Status.Conditions[0].Reason != gatewayv1alpha1.ReasonAccepted {
			t.Fatalf("conditions = %#v", stored.Status.Conditions)
		}
	})

	t.Run("happy path renders and reconciles", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(routeFixture("route-a")).WithStatusSubresource(&gatewayv1alpha1.ModelRoute{}).Build()
		reconciler := &ModelRouteReconciler{Client: k8sClient, Scheme: controllerTestScheme, ShadowDeployment: "shadow-demo"}
		if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "route-a", Namespace: "default"}}); err != nil {
			t.Fatal(err)
		}
	})
}
