package access

import (
	"context"
	"errors"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// Reconciler is opt-in. Applied means native resource read-back, never serving
// adoption. A separate, independently discovered fleet observer is still needed.
type Reconciler struct {
	client.Client
	Reader  client.Reader
	Options Options
}

// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelaccessgrants;deploymentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.aep.zhiyuan.io,resources=modelaccessgrants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=authorino.kuadrant.io,resources=authconfigs,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var grant gateway.ModelAccessGrant
	if err := r.Reader.Get(ctx, req.NamespacedName, &grant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !grant.DeletionTimestamp.IsZero() {
		// Same-namespace owner references collect native resources. Missing files
		// cannot renew the last compiled absolute deadline during GC propagation.
		return ctrl.Result{}, nil
	}
	config, policy, err := Render(&grant, r.Options)
	if err != nil {
		return ctrl.Result{}, r.status(ctx, &grant, false, false, "InvalidAccessGrant")
	}
	var deployment gateway.DeploymentConfig
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: grant.Namespace, Name: "deployment"}, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, r.status(ctx, &grant, false, false, "DeploymentUnavailable")
		}
		return ctrl.Result{}, err
	}
	if deployment.Spec.DeploymentID != grant.Spec.DeploymentID || !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.status(ctx, &grant, false, false, "DeploymentMismatch")
	}
	// Write new bounds before semantic policy. A partial rollout with different
	// semantic hashes denies rather than letting new freshness bless old access.
	if err = r.writePolicy(ctx, &grant, policy); err == nil {
		err = r.writeConfig(ctx, &grant, config)
	}
	if err == nil {
		err = r.current(ctx, &grant)
	}
	if errors.Is(err, ErrSuperseded) {
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}
	if err != nil {
		return ctrl.Result{}, errors.Join(err, r.status(ctx, &grant, true, false, "NativeProjectionFailed"))
	}
	remaining := time.Until(grant.Spec.ValidUntil.Time)
	if remaining <= 0 {
		return ctrl.Result{}, r.status(ctx, &grant, true, true, "PublicationExpired")
	}
	return ctrl.Result{RequeueAfter: remaining}, r.status(ctx, &grant, true, true, "NativeObservationRequired")
}

func (r *Reconciler) current(ctx context.Context, expected *gateway.ModelAccessGrant) error {
	var current gateway.ModelAccessGrant
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(expected), &current); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrSuperseded
		}
		return err
	}
	if current.UID != expected.UID || !current.DeletionTimestamp.IsZero() ||
		!apiequality.Semantic.DeepEqual(current.Spec, expected.Spec) ||
		!apiequality.Semantic.DeepEqual(current.Annotations, expected.Annotations) ||
		!apiequality.Semantic.DeepEqual(current.Labels, expected.Labels) {
		return ErrSuperseded
	}
	return nil
}

func (r *Reconciler) status(ctx context.Context, grant *gateway.ModelAccessGrant, accepted, applied bool, reason string) error {
	if err := r.current(ctx, grant); err != nil {
		if errors.Is(err, ErrSuperseded) {
			return nil
		}
		return err
	}
	before := grant.DeepCopy()
	state := func(value bool) metav1.ConditionStatus {
		if value {
			return metav1.ConditionTrue
		}
		return metav1.ConditionFalse
	}
	for name, value := range map[string]bool{"Accepted": accepted, "Applied": applied, "Ready": false} {
		meta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{Type: name, Status: state(value),
			ObservedGeneration: grant.Generation, Reason: reason, Message: "Native projection does not establish serving-process adoption"})
	}
	grant.Status.ObservedGeneration = grant.Generation
	p := grant.Spec.Publication
	grant.Status.ObservedPublication = &p
	grant.Status.Replicas = nil
	if apiequality.Semantic.DeepEqual(before.Status, grant.Status) {
		return nil
	}
	return r.Status().Update(ctx, grant)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Options.Validate() != nil {
		return ErrInput
	}
	return ctrl.NewControllerManagedBy(mgr).For(&gateway.ModelAccessGrant{}).
		Owns(&corev1.ConfigMap{}).Owns(authConfig()).
		Watches(&gateway.DeploymentConfig{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []ctrl.Request {
			var grants gateway.ModelAccessGrantList
			if err := r.List(ctx, &grants, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]ctrl.Request, 0, len(grants.Items))
			for i := range grants.Items {
				requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&grants.Items[i])})
			}
			return requests
		})).Named("modelaccessgrant").Complete(r)
}
