package controller

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	"reflect"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	projection "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/render"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// DeploymentConfigReconciler is the sole aggregate plugin writer.
type DeploymentConfigReconciler struct{ *Runtime }

// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=deploymentconfigs;modelroutes,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=deploymentconfigs/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=deploymentconfigs/finalizers;modelroutes/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;create;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=envoyfilters,verbs=get;list;create;patch;delete
// +kubebuilder:rbac:groups=extensions.higress.io,resources=wasmplugins,verbs=get;list;create;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *DeploymentConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var deployment gateway.DeploymentConfig
	if err := r.Get(ctx, req.NamespacedName, &deployment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !deployment.DeletionTimestamp.IsZero() {
		if err := requireNoRoutes(ctx, r.Runtime, deployment.Namespace); err != nil {
			return r.status(ctx, &deployment, false, false, "RoutesRemain", err.Error(), nil, "")
		}
		return ctrl.Result{}, r.finalize(ctx, &deployment)
	}
	if changed, err := r.ensureFinalizer(ctx, &deployment); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}
	in, err := r.snapshot(ctx, req.NamespacedName)
	if err != nil {
		return r.status(ctx, &deployment, false, false, "InputsUnavailable", err.Error(), nil, "")
	}
	_, aggregate := projection.Partition(in.resources)
	inventory, err := r.apply(ctx, &deployment, aggregate, in.hash)
	if err != nil {
		return r.status(ctx, &deployment, true, false, "ApplyFailed", err.Error(), nil, "")
	}
	return r.status(ctx, &deployment, true, true, "ResourcesApplied", "Aggregate resources applied; data-plane readiness is unobserved", inventory, in.hash)
}

func (r *DeploymentConfigReconciler) status(ctx context.Context, deployment *gateway.DeploymentConfig, accepted, applied bool, reason, message string, inventory []gateway.AppliedResource, hash string) (ctrl.Result, error) {
	base := deployment.DeepCopy()
	conditions(&deployment.Status.Conditions, deployment.Generation, accepted, applied, reason, message)
	deployment.Status.DataPlane = nil
	if applied && base.Status.ObservedGeneration == deployment.Generation && reflect.DeepEqual(base.Status.ObservedPublication, publicationReference(deployment)) && base.Status.DataPlane.Fresh(hash, time.Now()) && time.Since(base.Status.DataPlane.ObservedAt.Time) < driftInterval && r.DataPlane != nil {
		deployment.Status.DataPlane = base.Status.DataPlane
		inheritReady(&deployment.Status.Conditions, base.Status.Conditions, deployment.Generation)
	} else if applied {
		r.observeDataPlane(ctx, deployment, hash)
	}
	preserveReadyTransition(&deployment.Status.Conditions, base.Status.Conditions)
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.ObservedPublication = publicationReference(deployment)
	if applied {
		deployment.Status.AppliedResources = inventory
		deployment.Status.AppliedSnapshot = hash
	}
	if !reflect.DeepEqual(base.Status, deployment.Status) {
		if err := r.Status().Patch(ctx, deployment, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: driftInterval}, nil
}

func (r *DeploymentConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueue := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: "deployment"}}}
	})
	return ctrl.NewControllerManagedBy(mgr).For(&gateway.DeploymentConfig{}).
		Watches(&gateway.ModelRoute{}, enqueue).
		Watches(&gateway.ModelPolicy{}, enqueue).
		WatchesMetadata(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
			if obj.GetNamespace() != "aep-credentials" {
				return nil
			}
			return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "aep-system", Name: "deployment"}}}
		})).
		Named("deploymentconfig").Complete(r)
}
