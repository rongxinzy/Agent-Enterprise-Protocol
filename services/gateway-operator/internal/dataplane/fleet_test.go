package dataplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func owner(kind string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, UID: uid, Controller: ptr.To(true)}}
}

func fleetFixture(t *testing.T) (*Fleet, *Desired, client.Client, []byte) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{apps.AddToScheme, core.AddToScheme, discovery.AddToScheme} {
		if add(scheme) != nil {
			t.Fatal("scheme")
		}
	}
	labels := map[string]string{"app": "gateway"}
	d := &apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "higress-system", UID: "deployment", Generation: 1}, Spec: apps.DeploymentSpec{Replicas: ptr.To(int32(2)), Selector: &metav1.LabelSelector{MatchLabels: labels}}, Status: apps.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}}
	s := &apps.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "gateway-rs", Namespace: d.Namespace, UID: "replicaset", Labels: labels, OwnerReferences: owner("Deployment", d.UID)}}
	service := &core.Service{ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: d.Namespace, UID: "service"}, Spec: core.ServiceSpec{Selector: labels, Ports: []core.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(80)}}}}
	objects := []client.Object{d, s, service}
	e := &discovery.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "endpoints", Namespace: d.Namespace, Labels: map[string]string{discovery.LabelServiceName: d.Name}, OwnerReferences: owner("Service", service.UID)}}
	for _, name := range []string{"one", "two"} {
		p := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.Namespace, UID: types.UID(name), Labels: labels, OwnerReferences: owner("ReplicaSet", s.UID)}, Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}}}
		objects = append(objects, p)
		e.Endpoints = append(e.Endpoints, discovery.Endpoint{Addresses: []string{"10.0.0.1"}, Conditions: discovery.EndpointConditions{Ready: ptr.To(true)}, TargetRef: &core.ObjectReference{Kind: "Pod", Namespace: d.Namespace, Name: name, UID: p.UID}})
	}
	objects = append(objects, e)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&apps.Deployment{}, &core.Pod{}).WithObjects(objects...).Build()
	desired, dump := fixture(t)
	raw, _ := json.Marshal(dump)
	f := &Fleet{Reader: c, Deployment: client.ObjectKeyFromObject(d), Dump: func(context.Context, core.Pod) ([]byte, error) { return raw, nil }}
	return f, desired, c, raw
}

func TestFullFleetObservationRejectsPartialAndChangingEvidence(t *testing.T) {
	for _, scenario := range []string{"matching-two-replicas", "one-replica-stale", "transport-unavailable", "rolling", "pod-not-ready", "pod-deleting", "foreign-endpoint", "missing-endpoint", "endpoint-draining", "service-changed", "pod-replaced", "deployment-changed", "unknown-owner", "no-dump"} {
		t.Run(scenario, func(t *testing.T) {
			f, d, c, raw := fleetFixture(t)
			ctx := t.Context()
			dumped := 0
			f.Dump = func(ctx context.Context, p core.Pod) ([]byte, error) {
				dumped++
				switch scenario {
				case "one-replica-stale":
					if p.Name == "two" {
						return []byte(`{"configs":[]}`), nil
					}
				case "transport-unavailable":
					return nil, errors.New("sensitive transport response")
				case "service-changed":
					if dumped == 1 {
						var s core.Service
						_ = c.Get(ctx, f.Deployment, &s)
						s.Spec.Selector = map[string]string{"app": "other"}
						if c.Update(ctx, &s) != nil {
							t.Fatal("service update")
						}
					}
				case "pod-replaced":
					if dumped == 1 {
						var live core.Pod
						key := client.ObjectKeyFromObject(&p)
						_ = c.Get(ctx, key, &live)
						if c.Delete(ctx, &live) != nil {
							t.Fatal("delete")
						}
						p.ResourceVersion = ""
						p.UID = "replacement"
						if c.Create(ctx, &p) != nil {
							t.Fatal("create")
						}
					}
				case "deployment-changed":
					if dumped == 1 {
						var live apps.Deployment
						_ = c.Get(ctx, f.Deployment, &live)
						live.Generation++
						if c.Update(ctx, &live) != nil {
							t.Fatal("deployment update")
						}
					}
				}
				return raw, nil
			}
			switch scenario {
			case "rolling":
				var live apps.Deployment
				_ = c.Get(ctx, f.Deployment, &live)
				live.Status.UpdatedReplicas = 1
				if c.Status().Update(ctx, &live) != nil {
					t.Fatal("status")
				}
			case "pod-not-ready", "pod-deleting", "unknown-owner":
				var p core.Pod
				_ = c.Get(ctx, client.ObjectKey{Namespace: f.Deployment.Namespace, Name: "one"}, &p)
				switch scenario {
				case "pod-not-ready":
					p.Status.Conditions[0].Status = core.ConditionFalse
					if c.Status().Update(ctx, &p) != nil {
						t.Fatal("pod status")
					}
				case "unknown-owner":
					p.OwnerReferences = owner("ReplicaSet", "foreign")
					if c.Update(ctx, &p) != nil {
						t.Fatal("owner")
					}
				default:
					p.Finalizers = []string{"fixture"}
					if c.Update(ctx, &p) != nil || c.Delete(ctx, &p) != nil {
						t.Fatal("terminating pod")
					}
				}
			case "foreign-endpoint", "missing-endpoint", "endpoint-draining":
				var e discovery.EndpointSlice
				_ = c.Get(ctx, client.ObjectKey{Namespace: f.Deployment.Namespace, Name: "endpoints"}, &e)
				switch scenario {
				case "foreign-endpoint":
					e.Endpoints[0].TargetRef.UID = "foreign"
				case "missing-endpoint":
					e.Endpoints = e.Endpoints[:1]
				default:
					e.Endpoints[0].Conditions.Terminating = ptr.To(true)
				}
				if c.Update(ctx, &e) != nil {
					t.Fatal("endpoint update")
				}
			case "no-dump":
				f.Dump = nil
			}
			evidence, err := f.Observe(ctx, d)
			if scenario == "matching-two-replicas" {
				if err != nil || dumped != 2 || len(evidence.PodUIDs) != 2 || evidence.ObservedAt.IsZero() {
					t.Fatalf("complete fleet missing: %v", err)
				}
			} else if err == nil || !evidence.ObservedAt.IsZero() {
				t.Fatal("partial or stale evidence accepted")
			}
		})
	}
}
