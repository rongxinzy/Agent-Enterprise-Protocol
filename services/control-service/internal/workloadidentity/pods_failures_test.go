package workloadidentity

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type interruptedPods struct {
	client.Reader
	failure         string
	deploymentReads int
	accountReads    int
}

func (r *interruptedPods) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if r.failure == "list" {
		r.failure = ""
		return errors.New("private API details")
	}
	return r.Reader.List(ctx, list, opts...)
}

func (r *interruptedPods) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if _, ok := object.(*appsv1.ReplicaSet); ok && r.failure == "replicaset" {
		r.failure = ""
		return errors.New("private API details")
	}
	if err := r.Reader.Get(ctx, key, object, opts...); err != nil {
		return err
	}
	switch object.(type) {
	case *appsv1.Deployment:
		r.deploymentReads++
		if r.failure == "deployment race" && r.deploymentReads == 2 {
			object.SetUID("replacement-deployment")
		}
	case *corev1.ServiceAccount:
		r.accountReads++
		if r.failure == "account race" && r.accountReads == 2 {
			object.SetResourceVersion("changed-version")
		}
	}
	return nil
}

func TestPodEvidenceFailureCannotRenewOrRetireAndCanRecover(t *testing.T) {
	for _, failure := range []string{"list", "replicaset", "deployment race", "account race"} {
		t.Run(failure, func(t *testing.T) {
			o, registry := observerFixture(t, employeeFixture(), accountFixture())
			c := addPodOwnership(t, o)
			if err := c.Create(t.Context(), podFixture("pod-a")); err != nil {
				t.Fatal(err)
			}
			registry.current = []Registration{{AgentID: "agent-1", BindingKey: "existing-binding"}}
			o.reader = &interruptedPods{Reader: c, failure: failure}
			if _, err := o.Reconcile(t.Context(), request()); err == nil || strings.Contains(err.Error(), "private") ||
				len(registry.registered) != 0 || len(registry.retired) != 0 {
				t.Fatal("untrusted Pod observation renewed/retired identity or leaked details", err)
			}
			if _, err := o.Reconcile(t.Context(), request()); err != nil || len(registry.registered) != 1 ||
				registry.expected[0] != "existing-binding" || len(registry.registered[0].PodUIDs) != 1 {
				t.Fatal("fresh evidence could not recover existing employee binding", err)
			}
		})
	}
}

func TestForeignDeploymentCannotSupplyLivePodEvidence(t *testing.T) {
	o, registry := observerFixture(t, employeeFixture(), accountFixture())
	c := addPodOwnership(t, o)
	if err := c.Create(t.Context(), podFixture("pod-a")); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "employees", Name: "de-worker"}, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.OwnerReferences[0].UID = "other-employee"
	if err := c.Update(t.Context(), &deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Reconcile(t.Context(), request()); err == nil || len(registry.registered) != 0 || len(registry.retired) != 0 {
		t.Fatal("foreign Deployment was trusted or permanently retired current employee", err)
	}
}
