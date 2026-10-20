package access

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPodReplacementChangesOnlyUncachedPolicy(t *testing.T) {
	g, o := workloadFixture(), workloadOptions()
	g.Spec.WorkloadPodUIDs = []string{"pod-old", "pod-peer"}
	c := newClient(t, g)
	r := &Reconciler{Client: c, Reader: c, Options: o}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	first := authConfig()
	if err := c.Get(t.Context(), req.NamespacedName, first); err != nil {
		t.Fatal(err)
	}
	for _, pods := range [][]string{{"pod-peer", "pod-replacement"}, nil} {
		if err := c.Get(t.Context(), req.NamespacedName, g); err != nil {
			t.Fatal(err)
		}
		g.Spec.WorkloadPodUIDs = pods
		g.Spec.Publication.Sequence++
		markFixture(g)
		if err := c.Update(t.Context(), g); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		current := authConfig()
		if err := c.Get(t.Context(), req.NamespacedName, current); err != nil {
			t.Fatal(err)
		}
		if first.GetResourceVersion() != current.GetResourceVersion() || !reflect.DeepEqual(first.Object["spec"], current.Object["spec"]) {
			t.Fatal("Pod lifecycle rebuilt a native authentication/decision cache")
		}
		var policy corev1.ConfigMap
		if err := c.Get(t.Context(), req.NamespacedName, &policy); err != nil {
			t.Fatal(err)
		}
		data := policy.Data[g.Name+".rego"]
		if !strings.Contains(data, "workload_pod_uids[_] == kubernetes.pod.uid") || strings.Contains(data, `"pod-old"`) {
			t.Fatal("removed Pod UID survived the current policy")
		}
		if pods == nil && !strings.Contains(data, "workload_pod_uids = []") {
			t.Fatal("missing evidence is not an explicit empty allow-set")
		}
	}
}

func TestNativeWorkloadPodEvidenceRejectsMalformedUIDs(t *testing.T) {
	for _, pods := range [][]string{{""}, {"a", "a"}, {"a\n"}, {"a/b"}, {strings.Repeat("a", 129)}, make([]string, 513)} {
		g := workloadFixture()
		g.Spec.WorkloadPodUIDs = pods
		if _, _, err := Render(g, workloadOptions()); err == nil {
			t.Fatal("invalid evidence rendered", pods)
		}
	}
	g := fixture()
	g.Spec.WorkloadPodUIDs = []string{"pod"}
	if _, _, err := Render(g, options()); err == nil {
		t.Fatal("session grant accepted Pod-only evidence")
	}
}
