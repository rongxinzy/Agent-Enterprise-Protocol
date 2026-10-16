package controller

import (
	"context"
	"errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	projection "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/render"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func conditions(items *[]metav1.Condition, generation int64, accepted, applied bool, reason, message string) {
	a, b := metav1.ConditionFalse, metav1.ConditionFalse
	if accepted {
		a = metav1.ConditionTrue
	}
	if applied {
		b = metav1.ConditionTrue
	}
	meta.SetStatusCondition(items, metav1.Condition{Type: gateway.ConditionAccepted, Status: a, ObservedGeneration: generation, Reason: reason, Message: message})
	meta.SetStatusCondition(items, metav1.Condition{Type: gateway.ConditionApplied, Status: b, ObservedGeneration: generation, Reason: reason, Message: message})
	meta.SetStatusCondition(items, metav1.Condition{Type: gateway.ConditionReady, Status: metav1.ConditionUnknown, ObservedGeneration: generation, Reason: "DataPlaneNotObserved", Message: "Independent Higress/Envoy effectiveness observation is not available"})
}

func (r *ModelRouteReconciler) appliedStatus(ctx context.Context, route *gateway.ModelRoute, accepted, applied bool, reason, message string, inventory []gateway.AppliedResource, hash string) (ctrl.Result, error) {
	base := route.DeepCopy()
	conditions(&route.Status.Conditions, route.Generation, accepted, applied, reason, message)
	route.Status.ObservedGeneration = route.Generation
	route.Status.ObservedPublication = publicationReference(route)
	route.Status.AppliedSnapshot = hash
	if applied {
		route.Status.AppliedResources = inventory
	}
	if !reflect.DeepEqual(base.Status, route.Status) {
		if err := r.Status().Patch(ctx, route, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: driftInterval}, nil
}

func (r *ModelRouteReconciler) reconcileApplied(ctx context.Context, route *gateway.ModelRoute) (ctrl.Result, error) {
	if !route.DeletionTimestamp.IsZero() {
		var deployment gateway.DeploymentConfig
		err := r.Get(ctx, client.ObjectKey{Namespace: route.Namespace, Name: "deployment"}, &deployment)
		if err == nil && deployment.DeletionTimestamp.IsZero() {
			in, snapshotErr := r.Runtime.snapshot(ctx, client.ObjectKeyFromObject(&deployment))
			if snapshotErr != nil {
				if err := r.Runtime.prune(ctx, route.UID, nil); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: driftInterval}, nil
			}
			if deployment.Status.AppliedSnapshot != in.hash || !meta.IsStatusConditionTrue(deployment.Status.Conditions, gateway.ConditionApplied) {
				return ctrl.Result{RequeueAfter: driftInterval}, nil
			}
		} else if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.Runtime.finalize(ctx, route)
	}
	if err := validateModelRoute(route); err != nil {
		if err := r.Runtime.prune(ctx, route.UID, nil); err != nil {
			return ctrl.Result{}, err
		}
		return r.appliedStatus(ctx, route, false, false, "InvalidSpec", "Route spec is invalid", nil, "")
	}
	if changed, err := r.Runtime.ensureFinalizer(ctx, route); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}
	in, err := r.Runtime.snapshot(ctx, client.ObjectKey{Namespace: route.Namespace, Name: "deployment"})
	if err != nil {
		if cleanupErr := r.Runtime.prune(ctx, route.UID, nil); cleanupErr != nil {
			return ctrl.Result{}, cleanupErr
		}
		return r.appliedStatus(ctx, route, true, false, "InputsUnavailable", err.Error(), nil, "")
	}
	// Route admission follows the matching aggregate. A new route must never
	// expose an ingress before its provider and native policies are installed.
	if in.deployment.Status.AppliedSnapshot != in.hash || !meta.IsStatusConditionTrue(in.deployment.Status.Conditions, gateway.ConditionApplied) {
		if err := r.Runtime.prune(ctx, route.UID, nil); err != nil {
			return ctrl.Result{}, err
		}
		return r.appliedStatus(ctx, route, true, false, "AggregatePending", "Matching aggregate plugins have not been applied", nil, "")
	}
	strategy, err := gatewayrender.StrategyFor(projection.RouteFromCR(*route))
	if err != nil {
		return r.appliedStatus(ctx, route, false, false, "RenderFailed", "Route rendering failed", nil, "")
	}
	name := gatewayrender.OpenAIResourceName(in.deployment.Spec.DeploymentID, route.Spec.ModelID)
	if strategy.Bucket() == gatewayrender.BucketAnthropic {
		name = gatewayrender.AnthropicResourceName(route.Spec.ModelID)
	}
	allRoutes, _ := projection.Partition(in.resources)
	routeResources := make([]gatewayrender.RenderedResource, 0, 2)
	for _, resource := range allRoutes {
		if resource.APIPath == gatewayrender.IngressAPIPath(name) || resource.APIPath == gatewayrender.EnvoyFilterAPIPath(name) {
			routeResources = append(routeResources, resource)
		}
	}
	inventory, err := r.Runtime.apply(ctx, route, routeResources, in.hash)
	if err != nil {
		return r.appliedStatus(ctx, route, true, false, "ApplyFailed", err.Error(), nil, "")
	}
	return r.appliedStatus(ctx, route, true, true, "ResourcesApplied", "Route resources applied; data-plane readiness is unobserved", inventory, in.hash)
}

func (r *ModelPolicyReconciler) reconcileApplied(ctx context.Context, policy *gateway.ModelPolicy) (ctrl.Result, error) {
	if !policy.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := policy.DeepCopy()
	accepted, applied, reason, message := true, false, "AggregatePending", "Matching native policy plugins have not been applied"
	in, err := r.Runtime.snapshot(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: "deployment"})
	if err != nil {
		accepted, reason, message = false, "InputsUnavailable", err.Error()
	} else if in.deployment.Status.AppliedSnapshot == in.hash && meta.IsStatusConditionTrue(in.deployment.Status.Conditions, gateway.ConditionApplied) {
		applied, reason, message = true, "ResourcesApplied", "Native policy resources applied; data-plane readiness is unobserved"
	}
	conditions(&policy.Status.Conditions, policy.Generation, accepted, applied, reason, message)
	policy.Status.ObservedGeneration = policy.Generation
	if !reflect.DeepEqual(base.Status, policy.Status) {
		if err := r.Status().Patch(ctx, policy, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: driftInterval}, nil
}

func requireNoRoutes(ctx context.Context, r *Runtime, namespace string) error {
	var routes gateway.ModelRouteList
	if err := r.List(ctx, &routes, client.InNamespace(namespace)); err != nil {
		return errors.New("route inventory unavailable")
	}
	if len(routes.Items) > 0 {
		return errors.New("delete ModelRoutes before deleting the deployment")
	}
	return nil
}
