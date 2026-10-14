package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// ModelPolicyReconciler watches ModelPolicy objects, checks the target
// ModelRoute exists, and records the verdict in the Accepted condition. It
// never touches Higress resources.
type ModelPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
	condition := metav1.Condition{
		Type:               gatewayv1alpha1.ConditionAccepted,
		Status:             status,
		ObservedGeneration: policy.Generation,
		Reason:             reason,
		Message:            message,
	}
	changed := policy.Status.ObservedGeneration != policy.Generation ||
		setCondition(&policy.Status.Conditions, condition)
	if !changed {
		return nil
	}
	policy.Status.ObservedGeneration = policy.Generation
	return r.Status().Update(ctx, policy)
}

// SetupWithManager registers the ModelPolicy watch. The pilot reconciles on
// policy changes only; a policy whose target appears later is re-validated on
// its own next change.
func (r *ModelPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.ModelPolicy{}).
		Named("modelpolicy").
		Complete(r)
}
