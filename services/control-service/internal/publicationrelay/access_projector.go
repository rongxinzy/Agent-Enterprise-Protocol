package publicationrelay

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

const accessBindingKey = "gateway.aep.zhiyuan.io/access-binding"

func accessMarkers(item repository.ModelAccessDelivery) repository.PublicationDelivery {
	return repository.PublicationDelivery{DataPlanePublication: repository.DataPlanePublication{
		DeploymentID: item.DeploymentID, Sequence: item.Sequence, OperationID: item.OperationID,
		Revision: item.Revision, ContentHash: item.ContentHash}}
}

func desiredAccess(item repository.ModelAccessDelivery) (gateway.ModelAccessGrantSpec, error) {
	policy, err := gatewayaccess.Decode(item.Payload, item.ContentHash)
	operation, operationErr := uuid.Parse(item.OperationID)
	if err != nil || operationErr != nil || operation.String() != item.OperationID || item.Sequence < 1 ||
		item.Revision == "" || len(item.Revision) > 200 || item.SubjectID != policy.Subject.ID ||
		item.DeploymentID != policy.DeploymentID || item.BindingKey != policy.BindingKey() ||
		item.IssuedAt.IsZero() || !item.ValidUntil.After(item.IssuedAt) || item.ValidUntil.Sub(item.IssuedAt) > time.Hour ||
		item.IssuedAt.Nanosecond() != 0 || item.ValidUntil.Nanosecond() != 0 {
		return gateway.ModelAccessGrantSpec{}, ErrSnapshot
	}
	return gateway.ModelAccessGrantSpec{
		DeploymentID: policy.DeploymentID, Subject: policy.Subject, Binding: policy.Binding,
		Publication: gateway.PublicationReference{Sequence: item.Sequence, OperationID: item.OperationID,
			Revision: item.Revision, ContentHash: item.ContentHash},
		IssuedAt: metav1.NewTime(item.IssuedAt.UTC()), ValidUntil: metav1.NewTime(item.ValidUntil.UTC()),
		Disabled: policy.Disabled, Models: policy.Models, Licenses: policy.Licenses, RoleIDs: policy.RoleIDs, TeamIDs: policy.TeamIDs,
		WorkloadPodUIDs: policy.PodUIDs,
	}, nil
}

func checkAccess(live *gateway.ModelAccessGrant, item repository.ModelAccessDelivery, spec gateway.ModelAccessGrantSpec) error {
	if err := checkMarker(live, accessMarkers(item), true); err != nil {
		return err
	}
	if live.Annotations[accessBindingKey] != item.BindingKey || live.Spec.DeploymentID != spec.DeploymentID ||
		live.Spec.Subject != spec.Subject || !apiequality.Semantic.DeepEqual(live.Spec.Binding, spec.Binding) {
		return ErrOwnership
	}
	// Both metadata and spec must describe the same prior publication. Do not
	// silently repair a corrupt object or expand grants at an existing sequence.
	p := live.Spec.Publication
	if p.Sequence < 1 || live.Annotations[sequenceKey] != strconv.FormatInt(p.Sequence, 10) ||
		live.Annotations[operationKey] != p.OperationID || live.Annotations[hashKey] != p.ContentHash ||
		live.Annotations[revisionKey] != p.Revision || live.Annotations[auditResourceKey] != p.OperationID ||
		p.Sequence == spec.Publication.Sequence && !apiequality.Semantic.DeepEqual(live.Spec, spec) {
		return ErrOwnership
	}
	return nil
}

// ProjectAccess writes only this immutable binding's CR, retaining deny
// tombstones. It never stamps the route aggregate, renders native policy, reads
// Secrets or claims serving-process adoption. The caller owns live-claim checks.
func (p *Projector) ProjectAccess(ctx context.Context, item repository.ModelAccessDelivery) error {
	spec, err := desiredAccess(item)
	if err != nil {
		return err
	}
	var deployment gateway.DeploymentConfig
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: "deployment"}, &deployment); err != nil {
		return err
	}
	if deployment.Spec.DeploymentID != item.DeploymentID || !deployment.DeletionTimestamp.IsZero() {
		return ErrOwnership
	}
	// Full binding digest includes deployment+subject+session/workload identity;
	// permissions, source versions and freshness never change the resource name.
	key := client.ObjectKey{Namespace: p.Namespace, Name: "access-" + item.BindingKey}
	var live gateway.ModelAccessGrant
	err = p.Client.Get(ctx, key, &live)
	switch {
	case apierrors.IsNotFound(err):
		live = gateway.ModelAccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}, Spec: spec}
		mark(&live, accessMarkers(item))
		live.Annotations[accessBindingKey] = item.BindingKey
		err = p.Client.Create(ctx, &live, client.FieldOwner("aep-publication-relay"))
	case err == nil:
		if err := checkAccess(&live, item, spec); err != nil {
			return err
		}
		before := live.DeepCopy()
		live.Spec = spec
		mark(&live, accessMarkers(item))
		if !apiequality.Semantic.DeepEqual(before.Spec, live.Spec) || !apiequality.Semantic.DeepEqual(before.ObjectMeta, live.ObjectMeta) {
			err = p.Client.Update(ctx, &live, client.FieldOwner("aep-publication-relay"))
		}
	}
	if err != nil {
		return err
	}
	var confirmed gateway.ModelAccessGrant
	if err := p.Client.Get(ctx, key, &confirmed); err != nil {
		return err
	}
	if err := checkAccess(&confirmed, item, spec); err != nil {
		return err
	}
	if !apiequality.Semantic.DeepEqual(confirmed.Spec, spec) || confirmed.UID != live.UID {
		return ErrOwnership
	}
	return nil
}
