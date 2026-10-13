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
	done := make(chan struct{})
	go func() { elector.Run(ctx); close(done) }()
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
	<-done
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
	elector.Run(ctx)
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
	done := make(chan struct{})
	go func() { elector.Run(ctx); close(done) }()
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
	<-done
}

func TestLeaderElectionRenewFailureDropsLeadership(t *testing.T) {
	elector, client := newFakeElector("instance-a")
	// Reactors must be registered before Run starts — mutating the chain
	// mid-flight races with in-flight calls. The conflict gate opens only once
	// this instance leads, and the budget lets the release update land after
	// the renew deadline drops leadership (client-go retries a conflicting
	// release until it lands).
	var conflicting atomic.Bool
	var conflicts atomic.Int32
	client.Fake.PrependReactor("update", "leases", func(action coretesting.Action) (bool, runtime.Object, error) {
		if !conflicting.Load() || conflicts.Load() >= 6 {
			return false, nil, nil
		}
		conflicts.Add(1)
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, "gateway-reconciler", errors.New("conflict"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { elector.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !elector.IsLeader() {
		if time.Now().After(deadline) {
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}

	conflicting.Store(true)
	lost := time.Now().Add(2 * time.Second)
	for elector.IsLeader() {
		if time.Now().After(lost) {
			t.Fatal("a sustained renew conflict must drop leadership")
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-done
	cancel()
}

func TestLeaderElectionRunAcquiresAndReleases(t *testing.T) {
	elector, client := newFakeElector("instance-run")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { elector.Run(ctx); close(done) }()

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
	case <-done:
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
