package workloadidentity

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func podFixture(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "employees", UID: types.UID(name), OwnerReferences: []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "worker-rs", UID: "rs-uid", Controller: ptr.To(true)}}},
		Spec: corev1.PodSpec{ServiceAccountName: "worker-model"}}
}

func addPodOwnership(t *testing.T, o *Observer) client.Client {
	t.Helper()
	c := o.reader.(client.Client)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "de-worker", Namespace: "employees", UID: "deployment-uid",
		OwnerReferences: accountFixture().OwnerReferences}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "worker-model"}}}}
	r := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "worker-rs", Namespace: "employees", UID: "rs-uid", OwnerReferences: []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "de-worker", UID: d.UID, Controller: ptr.To(true)}}}}
	for _, object := range []client.Object{d, r} {
		if err := c.Create(t.Context(), object); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestObserverLivePodReplacementAndNoPodRecovery(t *testing.T) {
	o, registry := observerFixture(t, employeeFixture(), accountFixture())
	c := addPodOwnership(t, o)
	a, b, other := podFixture("pod-a"), podFixture("pod-b"), podFixture("pod-other")
	other.Spec.ServiceAccountName = "unrelated-account"
	for _, object := range []client.Object{b, a, other} {
		if err := c.Create(t.Context(), object); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want []string) {
		t.Helper()
		if _, err := o.Reconcile(t.Context(), request()); err != nil {
			t.Fatal(err)
		}
		got := registry.registered[len(registry.registered)-1].PodUIDs
		if !slices.Equal(got, want) || len(registry.retired) != 0 {
			t.Fatal("wrong live evidence or permanently retired employee", got, registry.retired)
		}
	}
	check([]string{"pod-a", "pod-b"})
	if err := c.Delete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	check([]string{"pod-b"})
	replacement := podFixture("pod-c")
	if err := c.Create(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	check([]string{"pod-b", "pod-c"})
	for _, object := range []client.Object{b, replacement} {
		if err := c.Delete(t.Context(), object); err != nil {
			t.Fatal(err)
		}
	}
	check(nil)
	if err := c.Create(t.Context(), podFixture("pod-d")); err != nil {
		t.Fatal(err)
	}
	check([]string{"pod-d"})
}

func TestObserverIgnoresForeignTerminalAndTerminatingPods(t *testing.T) {
	o, registry := observerFixture(t, employeeFixture(), accountFixture())
	c := addPodOwnership(t, o)
	for _, name := range []string{"wrong-owner", "succeeded", "failed", "terminating", "uncontrolled"} {
		p := podFixture(name)
		switch name {
		case "wrong-owner":
			p.OwnerReferences[0].UID = "replacement-rs-uid"
		case "succeeded":
			p.Status.Phase = corev1.PodSucceeded
		case "failed":
			p.Status.Phase = corev1.PodFailed
		case "uncontrolled":
			p.OwnerReferences = nil
		case "terminating":
			p.Finalizers = []string{"fixture"}
		}
		if err := c.Create(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		if name == "terminating" {
			if err := c.Delete(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := o.Reconcile(t.Context(), request()); err != nil {
		t.Fatal(err)
	}
	if len(registry.registered) != 1 || len(registry.registered[0].PodUIDs) != 0 {
		t.Fatal("nonlive/foreign Pod became positive evidence")
	}
}

func TestPodEventMapsOnlyItsTrustedAccountOwner(t *testing.T) {
	o, _ := observerFixture(t, employeeFixture(), accountFixture())
	mapper := o.podOwnerRequest("employees")
	p := podFixture("pod-a")
	requests := mapper(t.Context(), p)
	if len(requests) != 1 || requests[0] != request() {
		t.Fatal("Pod event lost employee mapping", requests)
	}
	p.Namespace = "foreign"
	if len(mapper(t.Context(), p)) != 0 {
		t.Fatal("cross-namespace Pod mapped")
	}
	p.Namespace, p.Spec.ServiceAccountName = "employees", "unknown"
	if len(mapper(t.Context(), p)) != 0 {
		t.Fatal("unknown account mapped")
	}
}
