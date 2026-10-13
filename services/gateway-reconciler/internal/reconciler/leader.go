package reconciler

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// LeaderElector uses a Kubernetes Lease (coordination.k8s.io/v1) to ensure
// only one reconciler instance runs the reconcile loop at a time. Followers
// stay idle but healthy (the readiness endpoint keeps responding), so a
// pod failure triggers a fast failover without split-brain applies.
type LeaderElector struct {
	client    kubernetes.Interface
	namespace string
	leaseName string
	identity  string
	// The timing maps the previous hand-rolled loop: the 15s lease duration is
	// unchanged; the 5s renew ticker becomes a 10s renew deadline (the longest
	// gap without a successful renew before stepping down) with a 2s retry
	// cadence, the client-go decomposition of that contract.
	leaseDuration time.Duration
	renewDeadline time.Duration
	retryPeriod   time.Duration
	isLeader      atomic.Bool
}

// NewLeaderElector creates a Lease-based elector reusing the applier's
// Kubernetes client (same ServiceAccount, same token).
func NewLeaderElector(applier *KubernetesApplier, namespace, leaseName, identity string) *LeaderElector {
	return &LeaderElector{
		client:        applier.client,
		namespace:     namespace,
		leaseName:     leaseName,
		identity:      identity,
		leaseDuration: 15 * time.Second,
		renewDeadline: 10 * time.Second,
		retryPeriod:   2 * time.Second,
	}
}

// IsLeader reports whether this instance holds the lease.
func (l *LeaderElector) IsLeader() bool { return l.isLeader.Load() }

// Run blocks until the context is cancelled, continuously acquiring or
// renewing the lease. Call IsLeader() from the reconcile loop to gate work.
func (l *LeaderElector) Run(ctx context.Context) {
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Namespace: l.namespace, Name: l.leaseName},
			Client:     l.client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: l.identity},
		},
		LeaseDuration: l.leaseDuration,
		RenewDeadline: l.renewDeadline,
		RetryPeriod:   l.retryPeriod,
		// Replaces the hand-rolled best-effort holder clear: client-go only
		// writes the release when this instance actually led, so it can never
		// clear another owner's lease.
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(_ context.Context) {
				l.isLeader.Store(true)
				slog.Info("leader election: acquired lease", "identity", l.identity)
			},
			// client-go retries renewals until the renew deadline, so this
			// fires only for a sustained loss, not one transient failure.
			OnStoppedLeading: func() {
				l.isLeader.Store(false)
				slog.Warn("leader election: lost lease", "identity", l.identity)
			},
		},
	})
	if err != nil {
		// Only reachable when the timing constants contradict each other.
		slog.Error("leader election: invalid configuration", "error", err)
		return
	}
	elector.Run(ctx)
}
