package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/dataplane"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReadinessHeartbeatDoesNotCauseStatusReconcileLoop(t *testing.T) {
	scheme := runtime.NewScheme()
	if clientgoscheme.AddToScheme(scheme) != nil || gateway.AddToScheme(scheme) != nil {
		t.Fatal("scheme")
	}
	refs := func(kind string, uid types.UID) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, UID: uid, Controller: ptr.To(true)}}
	}
	d := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "aep-system", UID: "aep", Generation: 1}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "test"}}
	labels := map[string]string{"app": "gateway"}
	objects := []client.Object{d,
		&apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "higress-system", UID: "gateway", Generation: 1}, Spec: apps.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels}}, Status: apps.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}},
		&apps.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "higress-system", UID: "rs", Labels: labels, OwnerReferences: refs("Deployment", "gateway")}},
		&core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "higress-system", UID: "pod", Labels: labels, OwnerReferences: refs("ReplicaSet", "rs")}, Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}}},
		&core.Service{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "higress-system", UID: "service"}, Spec: core.ServiceSpec{Selector: labels, Ports: []core.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(80)}}}},
		&discovery.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "endpoints", Namespace: "higress-system", Labels: map[string]string{discovery.LabelServiceName: "gateway"}, OwnerReferences: refs("Service", "service")}, Endpoints: []discovery.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discovery.EndpointConditions{Ready: ptr.To(true)}, TargetRef: &core.ObjectReference{Kind: "Pod", Name: "pod", Namespace: "higress-system", UID: "pod"}}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(d).WithObjects(objects...).Build()
	dumps := 0
	fleet := &dataplane.Fleet{Reader: c, Deployment: client.ObjectKey{Namespace: "higress-system", Name: "gateway"}, Dump: func(context.Context, core.Pod) ([]byte, error) {
		dumps++
		return []byte(`{"configs":[{"@type":"type.googleapis.com/envoy.admin.v3.ListenersConfigDump","dynamic_listeners":[{"active_state":{"listener":{"address":{"socket_address":{"port_value":80}},"filter_chains":[{"filters":[{"typed_config":{"@type":"type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager","rds":{"route_config_name":"empty"}}}]}]}}}]}]}`), nil
	}}
	rt := &Runtime{Client: c, Reader: c, DataPlane: fleet}
	in, err := rt.snapshot(t.Context(), client.ObjectKeyFromObject(d))
	if err != nil {
		t.Fatal(err)
	}
	r := &DeploymentConfigReconciler{Runtime: rt}
	if _, err := r.status(t.Context(), d, true, true, "ResourcesApplied", "Applied", nil, in.hash); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(d.Status.Conditions, gateway.ConditionReady) || d.Status.DataPlane == nil || dumps != 1 {
		t.Fatal("fresh full-fleet evidence missing")
	}
	rv := d.ResourceVersion
	at := d.Status.DataPlane.ObservedAt
	if _, err := r.status(t.Context(), d, true, true, "ResourcesApplied", "Applied", nil, in.hash); err != nil {
		t.Fatal(err)
	}
	if dumps != 1 || d.ResourceVersion != rv || !d.Status.DataPlane.ObservedAt.Equal(&at) {
		t.Fatal("unchanged status triggered a repeated observation/write")
	}
	old := d.Status.Conditions
	inherited := []metav1.Condition{}
	conditions(&inherited, 2, true, true, "Applied", "Applied")
	rt.inheritDataPlane(t.Context(), d.Namespace, in.hash, 2, &inherited)
	if !meta.IsStatusConditionTrue(inherited, gateway.ConditionReady) {
		t.Fatal("matching evidence not inherited")
	}
	conditions(&inherited, 2, true, true, "Applied", "Applied")
	rt.inheritDataPlane(t.Context(), d.Namespace, strings.Repeat("a", 64), 2, &inherited)
	if meta.IsStatusConditionTrue(inherited, gateway.ConditionReady) {
		t.Fatal("different snapshot inherited readiness")
	}
	d.Status.DataPlane.ObservedAt = metav1.NewTime(time.Now().Add(-time.Minute))
	if c.Status().Update(t.Context(), d) != nil {
		t.Fatal("expire heartbeat")
	}
	if _, err := r.status(t.Context(), d, true, true, "ResourcesApplied", "Applied", nil, in.hash); err != nil {
		t.Fatal(err)
	}
	if dumps != 2 || !meta.FindStatusCondition(d.Status.Conditions, gateway.ConditionReady).LastTransitionTime.Equal(&meta.FindStatusCondition(old, gateway.ConditionReady).LastTransitionTime) {
		t.Fatal("heartbeat changed transition time or did not refresh")
	}
	rt.DataPlane = nil
	if _, err := r.status(t.Context(), d, true, true, "ResourcesApplied", "Applied", nil, in.hash); err != nil {
		t.Fatal(err)
	}
	if d.Status.DataPlane != nil || meta.IsStatusConditionTrue(d.Status.Conditions, gateway.ConditionReady) {
		t.Fatal("disabled observer retained readiness")
	}
}
