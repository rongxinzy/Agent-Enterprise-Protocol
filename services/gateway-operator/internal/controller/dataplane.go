package controller

import (
	"context"
	"reflect"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/dataplane"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *Runtime) observeDataPlane(ctx context.Context, d *gateway.DeploymentConfig, hash string) {
	if r.DataPlane == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	set := func(status metav1.ConditionStatus, reason string) {
		meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: gateway.ConditionReady, Status: status, ObservedGeneration: d.Generation, Reason: reason, Message: "Independent loaded-configuration observation; provider availability is separate"})
	}
	set(metav1.ConditionUnknown, "DataPlaneObservationPending")
	before, err := r.snapshot(ctx, client.ObjectKeyFromObject(d))
	if err != nil || before.hash != hash || before.deployment.UID != d.UID || before.deployment.Generation != d.Generation || !reflect.DeepEqual(publicationReference(d), publicationReference(&before.deployment)) {
		return
	}
	desired, err := dataplane.Compile(before.resources, before.forbidden)
	if err != nil {
		set(metav1.ConditionUnknown, "DataPlaneConfigurationUnsupported")
		return
	}
	evidence, err := r.DataPlane.Observe(ctx, desired)
	if err != nil {
		return
	}
	after, err := r.snapshot(ctx, client.ObjectKeyFromObject(d))
	if err != nil || after.hash != hash || after.deployment.UID != d.UID || after.deployment.Generation != d.Generation || !reflect.DeepEqual(publicationReference(&before.deployment), publicationReference(&after.deployment)) {
		return
	}
	d.Status.DataPlane = &gateway.DataPlaneEvidence{Snapshot: hash, ObservedAt: metav1.NewTime(evidence.ObservedAt), GatewayUID: evidence.DeploymentUID, PodUIDs: evidence.PodUIDs}
	set(metav1.ConditionTrue, "EnvoyConfigurationObserved")
}

func (r *Runtime) inheritDataPlane(ctx context.Context, namespace, hash string, generation int64, items *[]metav1.Condition) {
	if hash == "" {
		return
	}
	var d gateway.DeploymentConfig
	if r.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "deployment"}, &d) != nil || d.Status.ObservedGeneration != d.Generation || !d.DeletionTimestamp.IsZero() || !d.Status.DataPlane.Fresh(hash, time.Now()) {
		return
	}
	ready := meta.FindStatusCondition(d.Status.Conditions, gateway.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != d.Generation {
		return
	}
	meta.SetStatusCondition(items, metav1.Condition{Type: gateway.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: generation, Reason: "EnvoyConfigurationObserved", Message: "Matching aggregate snapshot is loaded by the observed serving fleet"})
}

func inheritReady(items *[]metav1.Condition, previous []metav1.Condition, generation int64) {
	c := meta.FindStatusCondition(previous, gateway.ConditionReady)
	if c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == generation {
		meta.SetStatusCondition(items, *c)
	}
}

func preserveReadyTransition(items *[]metav1.Condition, previous []metav1.Condition) {
	old := meta.FindStatusCondition(previous, gateway.ConditionReady)
	current := meta.FindStatusCondition(*items, gateway.ConditionReady)
	if old != nil && current != nil && old.Status == current.Status && !old.LastTransitionTime.IsZero() {
		current.LastTransitionTime = old.LastTransitionTime
	}
}
