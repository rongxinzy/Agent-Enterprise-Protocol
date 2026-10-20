package access

import (
	"context"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Native gateway objects are in another namespace, so cross-namespace owner
// references would be invalid. A source finalizer fences deletion until the
// exact UID-owned route is gone. No publication/spec fields are patched here.
func (r *Reconciler) ensureRouteFinalizer(ctx context.Context, g *gateway.ModelAccessGrant) (bool, error) {
	if r.Options.IngressHost == "" || controllerutil.ContainsFinalizer(g, routeFinalizer) {
		return false, nil
	}
	if err := r.current(ctx, g); err != nil {
		return false, err
	}
	base := g.DeepCopy()
	controllerutil.AddFinalizer(g, routeFinalizer)
	return true, r.Patch(ctx, g, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func routeOwned(o client.Object, g *gateway.ModelAccessGrant) bool {
	return o.GetNamespace() == "higress-system" && o.GetName() == g.Name &&
		o.GetLabels()[routeLabel] == "true" && o.GetLabels()[routeOwner] == string(g.UID) &&
		o.GetAnnotations()[BindingKey] == bindingKey(g.Spec) && len(o.GetOwnerReferences()) == 0
}

func (r *Reconciler) removeBindingRoute(ctx context.Context, g *gateway.ModelAccessGrant) (bool, error) {
	// An older revocation/expiry must not reclaim a renewed publication's route.
	// Deleting grants are fenced instead by their finalizer and target UID/RV.
	if g.DeletionTimestamp.IsZero() {
		if err := r.current(ctx, g); err != nil {
			return false, err
		}
	}
	live := bindingRoute()
	key := client.ObjectKey{Namespace: "higress-system", Name: g.Name}
	if err := r.Reader.Get(ctx, key, live); err != nil {
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	if !routeOwned(live, g) {
		return false, ErrOwnership
	}
	uid, rv := live.GetUID(), live.GetResourceVersion()
	if err := r.Delete(ctx, live, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if err := r.Reader.Get(ctx, key, bindingRoute()); err != nil {
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	return false, nil // Finalizers may delay deletion; do not claim cleanup yet.
}

func (r *Reconciler) finalizeRoute(ctx context.Context, g *gateway.ModelAccessGrant) (bool, error) {
	if !controllerutil.ContainsFinalizer(g, routeFinalizer) {
		return true, nil
	}
	gone, err := r.removeBindingRoute(ctx, g)
	if err != nil || !gone {
		return false, err
	}
	base := g.DeepCopy()
	controllerutil.RemoveFinalizer(g, routeFinalizer)
	return true, r.Patch(ctx, g, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *Reconciler) writeBindingRoute(ctx context.Context, g *gateway.ModelAccessGrant) error {
	if r.Options.IngressHost == "" || g.Spec.Disabled || len(g.Spec.Models) == 0 || !time.Now().Before(g.Spec.ValidUntil.Time) {
		if !controllerutil.ContainsFinalizer(g, routeFinalizer) {
			return nil
		}
		gone, err := r.removeBindingRoute(ctx, g)
		if err == nil && !gone {
			return ErrRoutePending
		}
		return err
	}
	if err := r.current(ctx, g); err != nil {
		return err
	}
	desired := renderBindingRoute(g, r.Options.IngressHost)
	live := bindingRoute()
	err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
	if apierrors.IsNotFound(err) {
		if err = r.Create(ctx, desired); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if !routeOwned(live, g) || !live.GetDeletionTimestamp().IsZero() {
			return ErrOwnership
		}
		if !apiequality.Semantic.DeepEqual(live.Object["spec"], desired.Object["spec"]) {
			base := live.DeepCopy()
			live.Object["spec"] = desired.Object["spec"]
			if err := r.current(ctx, g); err != nil {
				return err
			}
			if err := r.Patch(ctx, live, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				return err
			}
		}
	}
	stored := bindingRoute()
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), stored); err != nil {
		return err
	}
	if !routeOwned(stored, g) || !apiequality.Semantic.DeepEqual(stored.Object["spec"], desired.Object["spec"]) {
		return ErrOwnership
	}
	return r.current(ctx, g)
}

func routeSource(object client.Object) []client.ObjectKey {
	if object.GetNamespace() != "higress-system" || object.GetLabels()[routeLabel] != "true" ||
		object.GetLabels()[routeOwner] == "" || object.GetName() != "access-"+object.GetAnnotations()[BindingKey] {
		return nil
	}
	return []client.ObjectKey{{Namespace: "aep-system", Name: object.GetName()}}
}
