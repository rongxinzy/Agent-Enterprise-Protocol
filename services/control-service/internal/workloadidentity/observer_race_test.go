package workloadidentity

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type interruptedReader struct {
	client.Reader
	accountFailure       bool
	employeeReads        int
	changeOnConfirmation bool
}

func (r *interruptedReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, ok := object.(*corev1.ServiceAccount); ok && r.accountFailure {
		r.accountFailure = false
		return errors.New("fixture API outage")
	}
	if err := r.Reader.Get(ctx, key, object, options...); err != nil {
		return err
	}
	if e, ok := object.(*unstructured.Unstructured); ok {
		r.employeeReads++
		if r.changeOnConfirmation && r.employeeReads == 2 {
			e.SetResourceVersion("new-resource-version")
		}
	}
	return nil
}

func TestObserverRetriesTransientFailureWithoutDestroyingBinding(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	r.current = []Registration{{AgentID: "agent-1", BindingKey: "old-binding"}}
	o.reader = &interruptedReader{Reader: o.reader, accountFailure: true}
	if _, err := o.Reconcile(t.Context(), request()); err == nil || len(r.registered) != 0 || len(r.retired) != 0 {
		t.Fatal("outage renewed or destroyed identity", err)
	}
	if _, err := o.Reconcile(t.Context(), request()); err != nil || len(r.registered) != 1 || r.expected[0] != "old-binding" {
		t.Fatal("positive live evidence could not recover", err)
	}
}

func TestObserverRejectsChangedCRBetweenLiveReads(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	o.reader = &interruptedReader{Reader: o.reader, changeOnConfirmation: true}
	if _, err := o.Reconcile(t.Context(), request()); err == nil || len(r.registered) != 0 {
		t.Fatal("observation race renewed stale identity", err)
	}
}

func TestAccountWatchMapsOnlyEmployeeControllerInTrustedNamespace(t *testing.T) {
	sa := accountFixture()
	mapper := accountOwnerRequest("employees")
	requests := mapper(t.Context(), sa)
	if len(requests) != 1 || requests[0] != request() {
		t.Fatal("owned SA changes do not requeue employee", requests)
	}
	for _, mutate := range []func(*corev1.ServiceAccount){
		func(sa *corev1.ServiceAccount) { sa.Namespace = "foreign" },
		func(sa *corev1.ServiceAccount) { sa.OwnerReferences = nil },
		func(sa *corev1.ServiceAccount) {
			sa.OwnerReferences = []metav1.OwnerReference{{Name: "worker", Kind: "Deployment"}}
		},
	} {
		copy := sa.DeepCopy()
		mutate(copy)
		if len(mapper(t.Context(), copy)) != 0 {
			t.Fatal("foreign SA event crossed observation scope")
		}
	}
}
