// Package controller holds the gateway operator's watch-only pilot
// controllers. They validate shallowly and write the Accepted condition on
// their own CRs; they write nothing else — no Higress resources, no external
// calls, no credential reads (pilot stage of the hybrid architecture).
// With ShadowDeployment set, the ModelRoute controller additionally
// re-renders the deployment scope from CRs in memory and logs the result at
// debug level (migration phase 1 shadow: render only, never apply).
package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	operatorrender "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/render"
)

// ModelRouteReconciler watches ModelRoute objects and records shallow
// validation in the Accepted condition. It never touches Higress resources.
type ModelRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// ShadowDeployment enables the migration phase 1 shadow render: every
	// accepted reconcile re-renders the deployment scope from CRs and logs
	// the rendered kinds/paths at debug level. Nothing is applied; the
	// DB-driven reconciler remains the only writer. Empty disables it.
	ShadowDeployment string
}

// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelpolicies,verbs=get;list;watch

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
	if err := r.writeAccepted(ctx, &route, metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, "Spec accepted"); err != nil {
		return ctrl.Result{}, err
	}
	r.shadowRender(ctx, route.Namespace)
	return ctrl.Result{}, nil
}

// shadowRender re-renders the whole deployment scope from CRs and logs the
// rendered kinds/paths at debug level (migration phase 1: render only, never
// apply). Failures are logged, never propagated — the shadow must not perturb
// the Accepted-condition path.
func (r *ModelRouteReconciler) shadowRender(ctx context.Context, namespace string) {
	if r.ShadowDeployment == "" {
		return
	}
	logger := log.FromContext(ctx)
	var routes gatewayv1alpha1.ModelRouteList
	if err := r.List(ctx, &routes, client.InNamespace(namespace)); err != nil {
		logger.Error(err, "shadow render: list ModelRoutes failed")
		return
	}
	var policies gatewayv1alpha1.ModelPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(namespace)); err != nil {
		logger.Error(err, "shadow render: list ModelPolicies failed")
		return
	}
	resources, err := operatorrender.RenderDeployment(ctx, operatorrender.DeploymentInput{
		Deployment: r.ShadowDeployment,
		Revision:   "cr-shadow",
		Routes:     routes.Items,
		Policies:   policies.Items,
	}, nil)
	if err != nil {
		logger.Error(err, "shadow render failed")
		return
	}
	rendered := make([]string, 0, len(resources))
	for _, resource := range resources {
		rendered = append(rendered, string(resource.Kind)+" "+resource.APIPath)
	}
	logger.V(1).Info("shadow render completed", "deployment", r.ShadowDeployment, "resources", rendered)
}

func (r *ModelRouteReconciler) writeAccepted(ctx context.Context, route *gatewayv1alpha1.ModelRoute, status metav1.ConditionStatus, reason, message string) error {
	// meta.SetStatusCondition owns LastTransitionTime and always refreshes
	// ObservedGeneration/Reason/Message on the stored condition, so the
	// condition can never lag route.Generation behind a stale entry.
	condition := metav1.Condition{
		Type:               gatewayv1alpha1.ConditionAccepted,
		Status:             status,
		ObservedGeneration: route.Generation,
		Reason:             reason,
		Message:            message,
	}
	stored := meta.FindStatusCondition(route.Status.Conditions, gatewayv1alpha1.ConditionAccepted)
	unchanged := stored != nil &&
		stored.Status == condition.Status &&
		stored.Reason == condition.Reason &&
		stored.Message == condition.Message &&
		stored.ObservedGeneration == condition.ObservedGeneration &&
		route.Status.ObservedGeneration == route.Generation
	if unchanged {
		return nil
	}
	meta.SetStatusCondition(&route.Status.Conditions, condition)
	route.Status.ObservedGeneration = route.Generation
	return r.Status().Update(ctx, route)
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
