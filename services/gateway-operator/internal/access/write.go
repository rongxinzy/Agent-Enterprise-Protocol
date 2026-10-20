package access

import (
	"context"
	"strconv"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func owned(object client.Object, grant *gateway.ModelAccessGrant) bool {
	owner := metav1.GetControllerOf(object)
	return owner != nil && owner.UID == grant.UID && owner.Name == grant.Name &&
		owner.APIVersion == gateway.GroupVersion.String() && owner.Kind == "ModelAccessGrant" &&
		object.GetLabels()[PolicyLabel] == "true" && object.GetDeletionTimestamp().IsZero()
}

func sequence(object client.Object) (int64, error) {
	value, err := strconv.ParseInt(object.GetAnnotations()[gateway.PublicationSequence], 10, 64)
	if err != nil || value < 1 {
		return 0, ErrOwnership
	}
	return value, nil
}

func (r *Reconciler) writePolicy(ctx context.Context, grant *gateway.ModelAccessGrant, desired *corev1.ConfigMap) error {
	if err := r.current(ctx, grant); err != nil {
		return err
	}
	var live corev1.ConfigMap
	err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), &live)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		seq, err := sequence(&live)
		if err != nil || !owned(&live, grant) || seq > grant.Spec.Publication.Sequence {
			return ErrOwnership
		}
		if seq == grant.Spec.Publication.Sequence {
			if len(live.BinaryData) != 0 || !apiequality.Semantic.DeepEqual(live.Data, desired.Data) || !sameMarkers(&live, desired) {
				return ErrOwnership
			}
		} else {
			live.Data, live.BinaryData = desired.Data, nil
			mergeAnnotations(&live, desired)
			if err := r.current(ctx, grant); err != nil {
				return err
			}
			if err := r.Update(ctx, &live); err != nil {
				return err
			}
		}
	}
	var stored corev1.ConfigMap
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), &stored); err != nil {
		return err
	}
	if !owned(&stored, grant) || len(stored.BinaryData) != 0 || !sameMarkers(&stored, desired) || !apiequality.Semantic.DeepEqual(stored.Data, desired.Data) {
		return ErrOwnership
	}
	return nil
}

func sameMarkers(a, b client.Object) bool {
	for _, key := range []string{gateway.PublicationSequence, gateway.PublicationOperationID,
		gateway.PublicationRevision, gateway.PublicationContentHash, SemanticHash} {
		if a.GetAnnotations()[key] != b.GetAnnotations()[key] {
			return false
		}
	}
	return true
}

func mergeAnnotations(target, desired client.Object) {
	annotations := target.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for key, value := range desired.GetAnnotations() {
		annotations[key] = value
	}
	target.SetAnnotations(annotations)
}

func (r *Reconciler) writeConfig(ctx context.Context, grant *gateway.ModelAccessGrant, desired *unstructured.Unstructured) error {
	if err := r.current(ctx, grant); err != nil {
		return err
	}
	// A later freshness write fences an older semantic write, including after
	// lease handoff. ResourceVersion separately fences concurrent object updates.
	var policy corev1.ConfigMap
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), &policy); err != nil {
		return err
	}
	if !owned(&policy, grant) || !sameMarkers(&policy, desired) {
		return ErrSuperseded
	}
	live := authConfig()
	err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		seq, err := sequence(live)
		if err != nil || !owned(live, grant) || seq > grant.Spec.Publication.Sequence {
			return ErrOwnership
		}
		if !apiequality.Semantic.DeepEqual(live.Object["spec"], desired.Object["spec"]) ||
			live.GetAnnotations()[SemanticHash] != desired.GetAnnotations()[SemanticHash] {
			live.Object["spec"] = desired.Object["spec"]
			mergeAnnotations(live, desired)
			if err := r.current(ctx, grant); err != nil {
				return err
			}
			if err := r.Update(ctx, live); err != nil {
				return err
			}
		}
		// No metadata/status stamp on identical semantic configuration: even
		// metadata events can reconstruct Authorino evaluator caches.
	}
	stored := authConfig()
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(desired), stored); err != nil {
		return err
	}
	if !owned(stored, grant) || stored.GetAnnotations()[SemanticHash] != desired.GetAnnotations()[SemanticHash] ||
		!apiequality.Semantic.DeepEqual(stored.Object["spec"], desired.Object["spec"]) {
		return ErrOwnership
	}
	return nil
}
