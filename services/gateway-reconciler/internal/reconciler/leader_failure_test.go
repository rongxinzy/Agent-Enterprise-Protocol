package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	coretesting "k8s.io/client-go/testing"
)

func TestLeaseReadFailureNeverGrantsLeadership(t *testing.T) {
	for _, inject := range []func() error{
		func() error { return apierrors.NewUnauthorized("expired") },
		func() error {
			return apierrors.NewForbidden(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, "gateway-reconciler", errors.New("denied"))
		},
		func() error { return apierrors.NewInternalError(errors.New("unavailable")) },
		func() error { return apierrors.NewServiceUnavailable("overloaded") },
	} {
		elector, client := newFakeElector("a")
		client.Fake.PrependReactor("get", "leases", func(action coretesting.Action) (bool, runtime.Object, error) {
			return true, nil, inject()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		elector.Run(ctx)
		cancel()
		if elector.IsLeader() {
			t.Fatal("a failed lease read granted leadership")
		}
	}
}

func TestLeaseCreateRejectionNeverGrantsLeadership(t *testing.T) {
	for _, inject := range []func() error{
		func() error { return apierrors.NewUnauthorized("expired") },
		func() error {
			return apierrors.NewForbidden(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, "gateway-reconciler", errors.New("denied"))
		},
		func() error {
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, "gateway-reconciler")
		},
		func() error { return apierrors.NewInternalError(errors.New("unavailable")) },
	} {
		elector, client := newFakeElector("a")
		client.Fake.PrependReactor("create", "leases", func(action coretesting.Action) (bool, runtime.Object, error) {
			return true, nil, inject()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		elector.Run(ctx)
		cancel()
		if elector.IsLeader() {
			t.Fatal("a rejected lease create granted leadership")
		}
	}
}

func TestRunKeepsFollowerIdleAcrossSuccessfulReads(t *testing.T) {
	holder := "other"
	fresh := metav1.NewMicroTime(time.Now())
	duration := int32(15)
	elector, client := newFakeElector("a", &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "aep-system", Name: "gateway-reconciler"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &fresh},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	elector.Run(ctx)
	if elector.IsLeader() {
		t.Fatal("successful follower read granted leadership")
	}
	if writes := writeActions(client); writes != 0 {
		t.Fatalf("follower wrote another owner's lease %d times", writes)
	}
}

func TestReleaseNeverClearsAnotherOwner(t *testing.T) {
	holder := "other"
	fresh := metav1.NewMicroTime(time.Now())
	duration := int32(15)
	elector, client := newFakeElector("a", &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "aep-system", Name: "gateway-reconciler"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &fresh},
	})
	// The elector never led, so a cancel must not touch the lease at all.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	elector.Run(ctx)
	cancel()
	lease, err := client.CoordinationV1().Leases("aep-system").Get(context.Background(), "gateway-reconciler", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lease read: %v", err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "other" || elector.IsLeader() {
		t.Fatalf("release overwrote another owner: %#v", lease.Spec.HolderIdentity)
	}
}
