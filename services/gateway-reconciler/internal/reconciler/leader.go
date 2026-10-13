package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// LeaderElector uses a Kubernetes Lease (coordination.k8s.io/v1) to ensure
// only one reconciler instance runs the reconcile loop at a time. Followers
// stay idle but healthy (the readiness endpoint keeps responding), so a
// pod failure triggers a fast failover without split-brain applies.
//
// Lifecycle contract (client-go's elector never re-enters an election after
// losing a lease): work started through StartWork runs on the lease-scoped
// context and stops with it, and Run returns a non-nil error when leadership
// ends on its own so the caller can terminate the process for Kubernetes to
// restart — a passive-but-ready forever-follower is a silent outage.
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
	// StartWork drives the reconcile workers with the lease-scoped context;
	// client-go cancels it before leadership stops, so no in-flight reconcile
	// survives a lost lease. Set before Run.
	StartWork func(ctx context.Context)
	isLeader  atomic.Bool
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

// Run blocks until ctx is cancelled or leadership ends. A nil return means the
// caller's context ended the election (clean shutdown; the lease is released
// with a fresh holder guard); a non-nil return means the election ended on its
// own and the caller must terminate the process so Kubernetes restarts it —
// client-go never re-elects in-process, and idling would leave a permanently
// passive replica that still reports ready.
func (l *LeaderElector) Run(ctx context.Context) error {
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Namespace: l.namespace, Name: l.leaseName},
			Client:     l.client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: l.identity},
		},
		LeaseDuration: l.leaseDuration,
		RenewDeadline: l.renewDeadline,
		RetryPeriod:   l.retryPeriod,
		// The release is ours, not client-go's: its tryRelease trusts the
		// locally observed record and, after a pause or partition, can clear a
		// successor's holder identity. The guarded release below re-reads the
		// Lease instead (reproduced in TestFormerLeaderReleaseSkipsSuccessor).
		ReleaseOnCancel: false,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(workCtx context.Context) {
				l.isLeader.Store(true)
				slog.Info("leader election: acquired lease", "identity", l.identity)
				if l.StartWork != nil {
					l.StartWork(workCtx)
				}
			},
			// client-go retries renewals until the renew deadline, so this
			// fires only for a sustained loss, not one transient failure.
			OnStoppedLeading: func() {
				if l.isLeader.Swap(false) {
					slog.Warn("leader election: lost lease", "identity", l.identity)
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("leader election: invalid configuration: %w", err)
	}
	elector.Run(ctx)
	if ctx.Err() != nil {
		// Clean shutdown: release the lease with a holder guard so a follower
		// takes over immediately instead of after natural expiry.
		l.guardedRelease()
		return nil
	}
	return errors.New("leader election ended without a shutdown request; leadership was lost")
}

// guardedRelease clears our holder identity so a follower takes over
// immediately, but only when a fresh read still names us: after a pause or a
// partition the remote lease may already belong to a successor, and clearing
// it would split the election. A resourceVersion conflict means exactly that
// race happened, so the release is abandoned instead of retried.
func (l *LeaderElector) guardedRelease() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lease, err := l.client.CoordinationV1().Leases(l.namespace).Get(ctx, l.leaseName, metav1.GetOptions{})
	if err != nil {
		slog.Debug("leader election: release skipped, lease unreadable", "identity", l.identity, "error", err)
		return
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.identity {
		return
	}
	cleared := lease.DeepCopy()
	cleared.Spec.HolderIdentity = nil
	if _, err := l.client.CoordinationV1().Leases(l.namespace).Update(ctx, cleared, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			slog.Debug("leader election: release abandoned, lease changed hands", "identity", l.identity)
			return
		}
		slog.Warn("leader election: release failed", "identity", l.identity, "error", err)
	}
}
