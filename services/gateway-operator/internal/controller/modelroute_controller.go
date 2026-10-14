// Package controller holds the gateway operator's watch-only pilot
// controllers. They validate shallowly and write the Accepted condition on
// their own CRs; they write nothing else — no Higress resources, no external
// calls, no credential reads (pilot stage of the hybrid architecture).
package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// ModelRouteReconciler watches ModelRoute objects and records shallow
// validation in the Accepted condition. It never touches Higress resources.
type ModelRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelroutes/status,verbs=get;update;patch

func (r *ModelRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var route gatewayv1alpha1.ModelRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if err := validateModelRoute(&route); err != nil {
		logger.Info("ModelRoute rejected", "reason", err.Error())
		return ctrl.Result{}, r.writeAccepted(ctx, &route, metav1.ConditionFalse, gatewayv1alpha1.ReasonInvalidSpec, err.Error())
	}
	return ctrl.Result{}, r.writeAccepted(ctx, &route, metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, "Spec accepted")
}

func (r *ModelRouteReconciler) writeAccepted(ctx context.Context, route *gatewayv1alpha1.ModelRoute, status metav1.ConditionStatus, reason, message string) error {
	condition := metav1.Condition{
		Type:               gatewayv1alpha1.ConditionAccepted,
		Status:             status,
		ObservedGeneration: route.Generation,
		Reason:             reason,
		Message:            message,
	}
	changed := route.Status.ObservedGeneration != route.Generation ||
		setCondition(&route.Status.Conditions, condition)
	if !changed {
		return nil
	}
	route.Status.ObservedGeneration = route.Generation
	return r.Status().Update(ctx, route)
}

// setCondition upserts a condition, reporting whether it changed anything.
func setCondition(conditions *[]metav1.Condition, condition metav1.Condition) bool {
	for index := range *conditions {
		current := &(*conditions)[index]
		if current.Type != condition.Type {
			continue
		}
		if current.Status == condition.Status && current.Reason == condition.Reason && current.Message == condition.Message {
			return false
		}
		condition.LastTransitionTime = metav1.Now()
		*current = condition
		return true
	}
	condition.LastTransitionTime = metav1.Now()
	*conditions = append(*conditions, condition)
	return true
}

// validateModelRoute is the shallow semantic check beyond the CRD schema:
// the endpoint must parse as an absolute http(s) URL without credentials,
// query, or fragment.
func validateModelRoute(route *gatewayv1alpha1.ModelRoute) error {
	endpoint := strings.TrimSpace(route.Spec.Upstream.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("upstream.endpoint must be an absolute http(s) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("upstream.endpoint must not contain credentials, query, or fragment")
	}
	if route.Spec.CredentialRef != nil && (strings.TrimSpace(route.Spec.CredentialRef.Name) == "" || strings.TrimSpace(route.Spec.CredentialRef.Key) == "") {
		return fmt.Errorf("credentialRef name and key must be non-empty")
	}
	return nil
}

// SetupWithManager registers the ModelRoute watch.
func (r *ModelRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.ModelRoute{}).
		Named("modelroute").
		Complete(r)
}
