package publicationrelay

import (
	"context"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	ObservedRevision   *string   `json:"observedRevision"`
	Reason             string    `json:"reason"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

func UnknownConditions(at time.Time, reason string) []Condition {
	return []Condition{
		{Type: "Accepted", Status: "Unknown", Reason: reason, LastTransitionTime: at},
		{Type: "Applied", Status: "Unknown", Reason: reason, LastTransitionTime: at},
		{Type: "Ready", Status: "Unknown", Reason: "DataPlaneNotObserved", LastTransitionTime: at},
	}
}

// Observer has read-only Kubernetes access. It never infers effectiveness from
// writes, the legacy ready flag, or a condition for another revision/generation.
type Observer struct {
	Reader    client.Reader
	Namespace string
}

func publicationRef(item repository.PublicationDelivery) gateway.PublicationReference {
	return gateway.PublicationReference{Sequence: item.Sequence, OperationID: item.OperationID, Revision: item.Revision, ContentHash: item.ContentHash}
}

func matchingObject(obj client.Object, ref *gateway.PublicationReference, generation int64, item repository.PublicationDelivery) bool {
	if obj.GetUID() == "" || generation < 1 || generation != obj.GetGeneration() || ref == nil || *ref != publicationRef(item) || checkMarker(obj, item, true) != nil {
		return false
	}
	for key, value := range markers(item) {
		if obj.GetAnnotations()[key] != value {
			return false
		}
	}
	return true
}

type observedObject struct {
	object     client.Object
	ref        *gateway.PublicationReference
	generation int64
	conditions []metav1.Condition
	hash       string
}

func (o *Observer) Observe(ctx context.Context, item repository.PublicationDelivery) ([]Condition, error) {
	unknown := UnknownConditions(item.CreatedAt, "RevisionNotObserved")
	desired, err := desiredRoutes(item)
	if err != nil {
		return nil, err
	}
	var deployment gateway.DeploymentConfig
	key := client.ObjectKey{Namespace: o.Namespace, Name: "deployment"}
	if err := o.Reader.Get(ctx, key, &deployment); err != nil {
		return nil, err
	}
	if deployment.Spec.DeploymentID != item.DeploymentID {
		return unknown, nil
	}
	var routes gateway.ModelRouteList
	if err := o.Reader.List(ctx, &routes, client.InNamespace(o.Namespace)); err != nil {
		return nil, err
	}
	if len(routes.Items) != len(desired) {
		return unknown, nil
	}
	indexed := indexRoutes(&routes)
	objects := []observedObject{{&deployment, deployment.Status.ObservedPublication, deployment.Status.ObservedGeneration, deployment.Status.Conditions, deployment.Status.AppliedSnapshot}}
	for id, spec := range desired {
		route := indexed["model-"+id]
		if route == nil || !reflect.DeepEqual(route.Spec, spec) {
			return unknown, nil
		}
		objects = append(objects, observedObject{route, route.Status.ObservedPublication, route.Status.ObservedGeneration, route.Status.Conditions, route.Status.AppliedSnapshot})
	}
	policies, err := desiredPolicies(item)
	if err != nil {
		return nil, err
	}
	if policies != nil {
		var live gateway.ModelPolicyList
		if err := o.Reader.List(ctx, &live, client.InNamespace(o.Namespace)); err != nil {
			return nil, err
		}
		if verifyPolicies(&live, policies, item) != nil {
			return unknown, nil
		}
		for i := range live.Items {
			policy := &live.Items[i]
			objects = append(objects, observedObject{policy, policy.Status.ObservedPublication, policy.Status.ObservedGeneration, policy.Status.Conditions, policy.Status.AppliedSnapshot})
		}
	}
	// Bracket the atomic route List with direct aggregate reads. Status/spec
	// changes during the observation invalidate this response instead of mixing
	// evidence from different aggregate snapshots.
	var confirmed gateway.DeploymentConfig
	if err := o.Reader.Get(ctx, key, &confirmed); err != nil {
		return nil, err
	}
	if confirmed.UID != deployment.UID || confirmed.ResourceVersion != deployment.ResourceVersion {
		return unknown, nil
	}
	for index, kind := range []string{gateway.ConditionAccepted, gateway.ConditionApplied} {
		status, at := "True", item.CreatedAt
		for _, observed := range objects {
			condition := meta.FindStatusCondition(observed.conditions, kind)
			if !matchingObject(observed.object, observed.ref, observed.generation, item) || condition == nil || condition.ObservedGeneration != observed.object.GetGeneration() || condition.LastTransitionTime.IsZero() || (condition.Status != metav1.ConditionTrue && condition.Status != metav1.ConditionFalse) {
				status = "Unknown"
				break
			}
			if condition.Status == metav1.ConditionFalse {
				status = "False"
			}
			if kind == gateway.ConditionApplied && condition.Status == metav1.ConditionTrue && (deployment.Status.AppliedSnapshot == "" || observed.hash != deployment.Status.AppliedSnapshot) {
				status = "Unknown"
				break
			}
			if condition.LastTransitionTime.After(at) {
				at = condition.LastTransitionTime.Time
			}
		}
		if status != "Unknown" {
			revision := item.Revision
			reason := "OperatorConfirmed"
			if status == "False" {
				reason = "OperatorRejected"
			}
			unknown[index] = Condition{Type: kind, Status: status, ObservedRevision: &revision, Reason: reason, LastTransitionTime: at}
		}
	}
	return unknown, nil
}
