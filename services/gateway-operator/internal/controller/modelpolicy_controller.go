package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// ModelPolicyReconciler validates the target and reports aggregate application
// status. DeploymentConfig remains the sole writer of shared policy plugins.
type ModelPolicyReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Runtime *Runtime
}

// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelroutes,verbs=get;list;watch

func (r *ModelPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var policy gatewayv1alpha1.ModelPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if r.Runtime != nil {
		return r.reconcileApplied(ctx, &policy)
	}
	var route gatewayv1alpha1.ModelRoute
	err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: policy.Spec.TargetRef.Name}, &route)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("ModelPolicy target not found", "target", policy.Spec.TargetRef.Name)
			return ctrl.Result{}, r.writeAccepted(ctx, &policy, metav1.ConditionFalse, gatewayv1alpha1.ReasonTargetNotFound, "Target ModelRoute does not exist")
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.writeAccepted(ctx, &policy, metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, "Spec accepted")
}

func (r *ModelPolicyReconciler) writeAccepted(ctx context.Context, policy *gatewayv1alpha1.ModelPolicy, status metav1.ConditionStatus, reason, message string) error {
	// See ModelRouteReconciler.writeAccepted: meta.SetStatusCondition owns
	// LastTransitionTime and keeps the condition's ObservedGeneration in step
	// with policy.Generation.
	condition := metav1.Condition{
		Type:               gatewayv1alpha1.ConditionAccepted,
		Status:             status,
		ObservedGeneration: policy.Generation,
		Reason:             reason,
		Message:            message,
	}
	stored := meta.FindStatusCondition(policy.Status.Conditions, gatewayv1alpha1.ConditionAccepted)
	unchanged := stored != nil &&
		stored.Status == condition.Status &&
		stored.Reason == condition.Reason &&
		stored.Message == condition.Message &&
		stored.ObservedGeneration == condition.ObservedGeneration &&
		policy.Status.ObservedGeneration == policy.Generation
	if unchanged {
		return nil
	}
	meta.SetStatusCondition(&policy.Status.Conditions, condition)
	policy.Status.ObservedGeneration = policy.Generation
	return r.Status().Update(ctx, policy)
}

// policiesForRoute maps a ModelRoute event to every ModelPolicy in the same
// namespace that targets it, so target create/update/delete re-validates the
// dependent policies instead of leaving a stale verdict.
func (r *ModelPolicyReconciler) policiesForRoute(ctx context.Context, obj client.Object) []ctrl.Request {
	var policies gatewayv1alpha1.ModelPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(obj.GetNamespace()), client.MatchingFields{"spec.targetRef.name": obj.GetName()}); err != nil {
		log.FromContext(ctx).Error(err, "list policies for route", "route", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}
	requests := make([]ctrl.Request, 0, len(policies.Items))
	for index := range policies.Items {
		requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[index])})
	}
	return requests
}

// SetupWithManager registers the ModelPolicy watch and a secondary watch on
// ModelRoute so a policy is re-validated when its target appears, changes, or
// disappears. The targetRef.name field index backs the reverse lookup.
func (r *ModelPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &gatewayv1alpha1.ModelPolicy{}, "spec.targetRef.name", func(obj client.Object) []string {
		return []string{obj.(*gatewayv1alpha1.ModelPolicy).Spec.TargetRef.Name}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.ModelPolicy{}).
		Watches(&gatewayv1alpha1.ModelRoute{}, handler.EnqueueRequestsFromMapFunc(r.policiesForRoute)).
		Watches(&gatewayv1alpha1.DeploymentConfig{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
			var policies gatewayv1alpha1.ModelPolicyList
			if r.List(ctx, &policies, client.InNamespace(obj.GetNamespace())) != nil {
				return nil
			}
			out := make([]ctrl.Request, 0, len(policies.Items))
			for i := range policies.Items {
				out = append(out, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
			}
			return out
		})).
		Named("modelpolicy").
		Complete(r)
}
