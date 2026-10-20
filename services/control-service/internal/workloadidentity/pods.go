package workloadidentity

import (
	"context"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func controllerIs(object client.Object, apiVersion, kind, name string, uid string) bool {
	owner := metav1.GetControllerOf(object)
	return uid != "" && owner != nil && owner.APIVersion == apiVersion && owner.Kind == kind && owner.Name == name && string(owner.UID) == uid
}

// Read the real CR -> Deployment -> ReplicaSet -> Pod ownership chain, never
// employee-supplied labels/status or a Pod name alone. All reads use APIReader.
func (o *Observer) livePods(ctx context.Context, employee *unstructured.Unstructured, sa *corev1.ServiceAccount) ([]string, error) {
	deployment := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: employee.GetNamespace(), Name: "de-" + employee.GetName()}
	if err := o.reader.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return []string{}, nil
		}
		return nil, errUnverified
	}
	if !controllerIs(deployment, employeeGVK.GroupVersion().String(), employeeGVK.Kind, employee.GetName(), string(employee.GetUID())) ||
		deployment.UID == "" || !deployment.DeletionTimestamp.IsZero() || deployment.Spec.Template.Spec.ServiceAccountName != sa.Name {
		return nil, errUnverified
	}
	var pods corev1.PodList
	if err := o.reader.List(ctx, &pods, client.InNamespace(employee.GetNamespace())); err != nil {
		return nil, errUnverified
	}
	active := []string{}
	sets := map[string]*appsv1.ReplicaSet{}
	for _, pod := range pods.Items {
		if pod.Spec.ServiceAccountName != sa.Name || pod.UID == "" || !pod.DeletionTimestamp.IsZero() ||
			pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Kind != "ReplicaSet" {
			continue
		}
		set, found := sets[owner.Name]
		if !found {
			set = &appsv1.ReplicaSet{}
			if err := o.reader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: owner.Name}, set); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, errUnverified
			}
			sets[owner.Name] = set
		}
		if set.UID != owner.UID || !set.DeletionTimestamp.IsZero() ||
			!controllerIs(set, appsv1.SchemeGroupVersion.String(), "Deployment", deployment.Name, string(deployment.UID)) {
			continue
		}
		active = append(active, string(pod.UID))
		if len(active) > 512 {
			return nil, errUnverified
		}
	}
	// Fence identity/owner changes during the live list. Later deletion is bounded
	// by native watches and the observation/publication deadline, not an atomic DB/K8s transaction.
	var latest appsv1.Deployment
	if err := o.reader.Get(ctx, key, &latest); err != nil || latest.UID != deployment.UID ||
		latest.ResourceVersion != deployment.ResourceVersion || !latest.DeletionTimestamp.IsZero() {
		return nil, errUnverified
	}
	var current corev1.ServiceAccount
	if err := o.reader.Get(ctx, client.ObjectKeyFromObject(sa), &current); err != nil || current.UID != sa.UID ||
		current.ResourceVersion != sa.ResourceVersion || !current.DeletionTimestamp.IsZero() {
		return nil, errUnverified
	}
	slices.Sort(active)
	return active, nil
}

func (o *Observer) podOwnerRequest(namespace string) handler.MapFunc {
	return func(ctx context.Context, object client.Object) []reconcile.Request {
		pod, ok := object.(*corev1.Pod)
		if !ok || pod.Namespace != namespace || pod.Spec.ServiceAccountName == "" {
			return nil
		}
		var sa corev1.ServiceAccount
		if err := o.reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: pod.Spec.ServiceAccountName}, &sa); err != nil {
			return nil
		}
		return accountOwnerRequest(namespace)(ctx, &sa)
	}
}
