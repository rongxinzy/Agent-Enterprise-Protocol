package workloadidentity

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Setup installs ordinary controller-runtime watches/workqueue/leader lifecycle.
// Informers schedule reconciles; only uncached APIReader reads can renew trust.
func Setup(mgr ctrl.Manager, registry Registry, deployment, namespace, audience string, freshness time.Duration) error {
	observer, err := New(mgr.GetAPIReader(), registry, deployment, namespace, audience, freshness)
	if err != nil {
		return err
	}
	employee := &unstructured.Unstructured{}
	employee.SetGroupVersionKind(employeeGVK)
	return ctrl.NewControllerManagedBy(mgr).Named("employee-workload-identity").For(employee).
		Watches(&corev1.ServiceAccount{}, handler.EnqueueRequestsFromMapFunc(accountOwnerRequest(namespace))).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(observer.podOwnerRequest(namespace))).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1, ReconciliationTimeout: 20 * time.Second}).Complete(observer)
}

func accountOwnerRequest(namespace string) handler.MapFunc {
	return func(_ context.Context, object client.Object) []reconcile.Request {
		if object.GetNamespace() != namespace {
			return nil
		}
		owner := metav1.GetControllerOf(object)
		if owner == nil || owner.Kind != employeeGVK.Kind || owner.APIVersion != employeeGVK.GroupVersion().String() {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: owner.Name}}}
	}
}
