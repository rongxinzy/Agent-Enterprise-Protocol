package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// Regression for the Accepted condition lagging metadata.generation: the
// status writer must refresh the condition's observedGeneration even when
// Status/Reason/Message are unchanged, and stay idempotent afterwards (no
// LastTransitionTime churn, no further writes).
func TestAcceptedConditionTracksGeneration(t *testing.T) {
	newRequest := func(name string) ctrl.Request {
		return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
	}

	t.Run("ModelRoute", func(t *testing.T) {
		route := routeFixture("route-gen")
		route.Generation = 2
		route.Status.Conditions = []metav1.Condition{{
			Type:               gatewayv1alpha1.ConditionAccepted,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			Reason:             gatewayv1alpha1.ReasonAccepted,
			Message:            "Spec accepted",
			LastTransitionTime: metav1.Now(),
		}}
		route.Status.ObservedGeneration = 2
		k8sClient := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(route).WithStatusSubresource(&gatewayv1alpha1.ModelRoute{}).Build()
		reconciler := &ModelRouteReconciler{Client: k8sClient, Scheme: controllerTestScheme}

		var firstTransition metav1.Time
		for reconcile := 0; reconcile < 3; reconcile++ {
			if _, err := reconciler.Reconcile(context.Background(), newRequest("route-gen")); err != nil {
				t.Fatal(err)
			}
			var stored gatewayv1alpha1.ModelRoute
			if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "route-gen", Namespace: "default"}, &stored); err != nil {
				t.Fatal(err)
			}
			if len(stored.Status.Conditions) != 1 {
				t.Fatalf("conditions = %#v", stored.Status.Conditions)
			}
			condition := stored.Status.Conditions[0]
			if condition.ObservedGeneration != stored.Generation || stored.Status.ObservedGeneration != stored.Generation {
				t.Fatalf("reconcile %d: metadata.generation=%d status.observedGeneration=%d condition.observedGeneration=%d",
					reconcile+1, stored.Generation, stored.Status.ObservedGeneration, condition.ObservedGeneration)
			}
			if reconcile == 0 {
				firstTransition = condition.LastTransitionTime
			} else if !condition.LastTransitionTime.Equal(&firstTransition) {
				t.Fatalf("reconcile %d rewrote LastTransitionTime: %s -> %s", reconcile+1, firstTransition, condition.LastTransitionTime)
			}
		}
	})

	t.Run("ModelPolicy", func(t *testing.T) {
		policy := &gatewayv1alpha1.ModelPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-gen", Namespace: "default", Generation: 2},
			Spec: gatewayv1alpha1.ModelPolicySpec{
				TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-gen"},
			},
			Status: gatewayv1alpha1.ModelPolicyStatus{
				ObservedGeneration: 2,
				Conditions: []metav1.Condition{{
					Type:               gatewayv1alpha1.ConditionAccepted,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 1,
					Reason:             gatewayv1alpha1.ReasonAccepted,
					Message:            "Spec accepted",
					LastTransitionTime: metav1.Now(),
				}},
			},
		}
		route := routeFixture("route-gen")
		k8sClient := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(policy, route).WithStatusSubresource(&gatewayv1alpha1.ModelPolicy{}).Build()
		reconciler := &ModelPolicyReconciler{Client: k8sClient, Scheme: controllerTestScheme}

		var firstTransition metav1.Time
		for reconcile := 0; reconcile < 3; reconcile++ {
			if _, err := reconciler.Reconcile(context.Background(), newRequest("policy-gen")); err != nil {
				t.Fatal(err)
			}
			var stored gatewayv1alpha1.ModelPolicy
			if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "policy-gen", Namespace: "default"}, &stored); err != nil {
				t.Fatal(err)
			}
			if len(stored.Status.Conditions) != 1 {
				t.Fatalf("conditions = %#v", stored.Status.Conditions)
			}
			condition := stored.Status.Conditions[0]
			if condition.ObservedGeneration != stored.Generation || stored.Status.ObservedGeneration != stored.Generation {
				t.Fatalf("reconcile %d: metadata.generation=%d status.observedGeneration=%d condition.observedGeneration=%d",
					reconcile+1, stored.Generation, stored.Status.ObservedGeneration, condition.ObservedGeneration)
			}
			if reconcile == 0 {
				firstTransition = condition.LastTransitionTime
			} else if !condition.LastTransitionTime.Equal(&firstTransition) {
				t.Fatalf("reconcile %d rewrote LastTransitionTime: %s -> %s", reconcile+1, firstTransition, condition.LastTransitionTime)
			}
		}
	})
}

// The reverse-lookup index behind the ModelRoute watch must map a route event
// to exactly the policies whose targetRef.name points at it.
func TestPoliciesForRouteIndex(t *testing.T) {
	matching := &gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-hit", Namespace: "default"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-x"},
		},
	}
	other := &gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-miss", Namespace: "default"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-y"},
		},
	}
	otherNamespace := &gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-other-ns", Namespace: "staging"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-x"},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(controllerTestScheme).
		WithObjects(matching, other, otherNamespace).
		WithIndex(&gatewayv1alpha1.ModelPolicy{}, "spec.targetRef.name", func(obj client.Object) []string {
			return []string{obj.(*gatewayv1alpha1.ModelPolicy).Spec.TargetRef.Name}
		}).
		Build()
	reconciler := &ModelPolicyReconciler{Client: k8sClient, Scheme: controllerTestScheme}

	route := routeFixture("route-x")
	requests := reconciler.policiesForRoute(context.Background(), route)
	if len(requests) != 1 || requests[0].Name != "policy-hit" || requests[0].Namespace != "default" {
		t.Fatalf("policiesForRoute = %#v", requests)
	}
}
