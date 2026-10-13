package reconciler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	coretesting "k8s.io/client-go/testing"
)

// newFakeElector builds an elector over a fake clientset with test-sized
// timing (the production defaults are 15s/10s/2s; the client-go validation
// rule is leaseDuration > renewDeadline > retryPeriod*1.2).
func newFakeElector(identity string, objects ...runtime.Object) (*LeaderElector, *fake.Clientset) {
	client := fake.NewClientset(objects...)
	applier := &KubernetesApplier{client: client}
	elector := NewLeaderElector(applier, "aep-system", "gateway-reconciler", identity)
	elector.leaseDuration = 300 * time.Millisecond
	elector.renewDeadline = 200 * time.Millisecond
	elector.retryPeriod = 50 * time.Millisecond
	return elector, client
}

func currentLease(t *testing.T, client *fake.Clientset) *coordinationv1.Lease {
	t.Helper()
	lease, err := client.CoordinationV1().Leases("aep-system").Get(context.Background(), "gateway-reconciler", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lease read: %v", err)
	}
	return lease
}

func writeActions(client *fake.Clientset) int {
	count := 0
	for _, action := range client.Actions() {
		if verb := action.GetVerb(); verb == "create" || verb == "update" {
			count++
		}
	}
	return count
}

func TestLeaderElectionAcquire(t *testing.T) {
	elector, client := newFakeElector("instance-a")
	if elector.IsLeader() {
		t.Fatal("a fresh elector is not the leader")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- elector.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !elector.IsLeader() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}
	lease := currentLease(t, client)
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "instance-a" {
		t.Fatalf("holder = %v", lease.Spec.HolderIdentity)
	}
	if lease.Spec.AcquireTime == nil {
		t.Fatal("acquired lease carries no acquireTime")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func TestLeaderElectionFollowerRespectsValidLease(t *testing.T) {
	holder := "instance-b"
	fresh := metav1.NewMicroTime(time.Now())
	duration := int32(15)
	elector, client := newFakeElector("instance-a", &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "aep-system", Name: "gateway-reconciler"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &fresh},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := elector.Run(ctx); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if elector.IsLeader() {
		t.Fatal("another instance's fresh lease must not be taken over")
	}
	if writes := writeActions(client); writes != 0 {
		t.Fatalf("a follower must not write the lease, wrote %d times", writes)
	}
}

func TestLeaderElectionTakesOverExpiredLease(t *testing.T) {
	// client-go does not trust the record's RenewTime (clock skew): a lease is
	// expired for takeover when the local observer sees it unchanged for its
	// leaseDurationSeconds. The seeded record's one-second duration makes the
	// takeover observable quickly; production keeps 15s.
	holder := "instance-b"
	stale := metav1.NewMicroTime(time.Now().Add(-2 * time.Minute))
	duration := int32(1)
	elector, client := newFakeElector("instance-a", &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "aep-system", Name: "gateway-reconciler"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &stale},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- elector.Run(ctx) }()
	deadline := time.Now().Add(4 * time.Second)
	for !elector.IsLeader() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run never took over the expired lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	lease := currentLease(t, client)
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "instance-a" {
		t.Fatalf("after takeover holder = %v", lease.Spec.HolderIdentity)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func TestLeaderElectionRenewFailureDropsLeadership(t *testing.T) {
	elector, client := newFakeElector("instance-a")
	// Reactors must be registered before Run starts — mutating the chain
	// mid-flight races with in-flight calls. The conflict gate opens only once
	// this instance leads. Sustained conflicts are safe here: the lost-lease
	// path never releases, so there is no release retry to deadlock.
	var conflicting atomic.Bool
	client.PrependReactor("update", "leases", func(action coretesting.Action) (bool, runtime.Object, error) {
		if !conflicting.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, "gateway-reconciler", errors.New("conflict"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- elector.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !elector.IsLeader() {
		if time.Now().After(deadline) {
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}

	conflicting.Store(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("losing the lease must end the election with an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a sustained renew conflict must end the election")
	}
	if elector.IsLeader() {
		t.Fatal("a sustained renew conflict must drop leadership")
	}
}

func TestLeaderElectionRunAcquiresAndReleases(t *testing.T) {
	elector, client := newFakeElector("instance-run")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- elector.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		lease, err := client.CoordinationV1().Leases("aep-system").Get(context.Background(), "gateway-reconciler", metav1.GetOptions{})
		acquired := err == nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == "instance-run"
		if acquired && elector.IsLeader() {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if elector.IsLeader() {
		t.Fatal("release must clear leadership")
	}
	lease := currentLease(t, client)
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" {
		t.Fatalf("release must clear the holder, got %q", *lease.Spec.HolderIdentity)
	}
}

// TestLeadershipLossEndsElectionRun pins the P1 regression from the
// adversarial cloud-native review of the client-go migration: client-go's
// elector never re-enters an election after a lost lease, so the election run
// must end with an error and the lease-scoped work context must be cancelled —
// the process then exits for Kubernetes to restart it instead of idling as a
// ready-but-passive replica forever.
func TestLeadershipLossEndsElectionRun(t *testing.T) {
	elector, client := newFakeElector("instance-a")
	workCtxCh := make(chan context.Context, 1)
	var workCancelled atomic.Bool
	elector.StartWork = func(ctx context.Context) {
		workCtxCh <- ctx
		go func() {
			<-ctx.Done()
			workCancelled.Store(true)
		}()
	}
	var conflicting atomic.Bool
	client.PrependReactor("update", "leases", func(action coretesting.Action) (bool, runtime.Object, error) {
		if !conflicting.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, "gateway-reconciler", errors.New("conflict"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- elector.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !elector.IsLeader() {
		if time.Now().After(deadline) {
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var workCtx context.Context
	select {
	case workCtx = <-workCtxCh:
	case <-time.After(2 * time.Second):
		t.Fatal("StartWork did not receive the lease-scoped context")
	}
	if workCtx == nil {
		t.Fatal("StartWork did not receive the lease-scoped context")
	}

	conflicting.Store(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("losing the lease must end the election with an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the election run did not end after sustained renew conflicts")
	}
	if ctx.Err() != nil {
		t.Fatal("the election ended the caller's context; the process context must stay live")
	}
	if elector.IsLeader() {
		t.Fatal("leadership flag still set after the election ended")
	}
	if !workCancelled.Load() {
		t.Fatal("lease-scoped work context was not cancelled when leadership stopped")
	}
}

// TestFormerLeaderReleaseSkipsSuccessor pins the second P1 from the
// adversarial review (adapted from pr271-cloudnative-review-test.go): an
// instance that WAS leader gets taken over during its pause, and its release
// on shutdown must not clear the successor's holderIdentity.
func TestFormerLeaderReleaseSkipsSuccessor(t *testing.T) {
	elector, client := newFakeElector("review-a")
	ctx, cancel := context.WithCancel(context.Background())
	// Mutate the remote lease to a successor after cancellation, so the
	// release path reads a lease that changed hands during our pause.
	client.PrependReactor("get", "leases", func(action coretesting.Action) (bool, runtime.Object, error) {
		if ctx.Err() == nil {
			return false, nil, nil
		}
		resource := action.GetResource()
		obj, err := client.Tracker().Get(resource, "aep-system", "gateway-reconciler")
		if err != nil {
			return true, nil, errors.New("review lease missing")
		}
		lease := obj.(*coordinationv1.Lease).DeepCopy()
		holder := "review-b"
		lease.Spec.HolderIdentity = &holder
		if err := client.Tracker().Update(resource, lease, "aep-system"); err != nil {
			return true, nil, err
		}
		return true, lease, nil
	})
	done := make(chan error, 1)
	go func() { done <- elector.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !elector.IsLeader() {
		if time.Now().After(deadline) {
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean shutdown Run = %v", err)
	}
	// Read the tracker directly to avoid the injection masking an overwrite.
	obj, err := client.Tracker().Get(coordinationv1.SchemeGroupVersion.WithResource("leases"), "aep-system", "gateway-reconciler")
	if err != nil {
		t.Fatal(err)
	}
	lease := obj.(*coordinationv1.Lease)
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "review-b" {
		t.Fatalf("former leader cleared the successor's lease: %#v", lease.Spec.HolderIdentity)
	}
}
