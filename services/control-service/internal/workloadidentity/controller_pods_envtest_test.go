package workloadidentity

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// API-server watches, not manual Reconcile calls, must publish Pod rollouts
// without changing the employee CR or SA. Envtest does not run kubelet/apps
// controllers: create their real owner-linked API objects explicitly here.
func verifyControllerPodWatches(t *testing.T, c client.Client, employee *unstructured.Unstructured, sa *corev1.ServiceAccount, events <-chan lifecycleEvent) {
	t.Helper()
	ctx := t.Context()
	labels := map[string]string{"app": "worker"}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
		ServiceAccountName: sa.Name, Containers: []corev1.Container{{Name: "runtime", Image: "fixture:unused"}},
	}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "de-" + employee.GetName(), Namespace: sa.Namespace,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: employeeGVK.GroupVersion().String(), Kind: employeeGVK.Kind,
			Name: employee.GetName(), UID: employee.GetUID(), Controller: ptr.To(true)}}},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template}}
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	newPod := func(name string) *corev1.Pod {
		t.Helper()
		set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name + "-rs", Namespace: sa.Namespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name,
				UID: deployment.UID, Controller: ptr.To(true)}}}, Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template}}
		if err := c.Create(ctx, set); err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sa.Namespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: set.Name,
				UID: set.UID, Controller: ptr.To(true)}}}, Spec: *template.Spec.DeepCopy()}
		if err := c.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		return pod
	}
	waitPods := func(want ...string) {
		t.Helper()
		slices.Sort(want)
		nextLifecycleEvent(t, events, func(event lifecycleEvent) bool {
			return event.workload.EmployeeName == employee.GetName() && slices.Equal(event.workload.PodUIDs, want)
		})
	}
	first := newPod("worker-first")
	waitPods(string(first.UID))
	second := newPod("worker-rollout")
	waitPods(string(first.UID), string(second.UID))
	if err := c.Delete(ctx, first, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	waitPods(string(second.UID))
	if err := c.Delete(ctx, second, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	waitPods()
	recovered := newPod("worker-recovered")
	waitPods(string(recovered.UID))
	var latest corev1.ServiceAccount
	if err := c.Get(ctx, client.ObjectKeyFromObject(sa), &latest); err != nil || latest.ResourceVersion != sa.ResourceVersion {
		t.Fatal("Pod lifecycle required an SA update", err)
	}
	before := employee.GetResourceVersion()
	if err := c.Get(ctx, client.ObjectKeyFromObject(employee), employee); err != nil || employee.GetResourceVersion() != before {
		t.Fatal("Pod lifecycle required an employee update", err)
	}
}
