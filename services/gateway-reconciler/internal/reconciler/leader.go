package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// LeaderElector uses a Kubernetes Lease (coordination.k8s.io/v1) to ensure
// only one reconciler instance runs the reconcile loop at a time. Followers
// stay idle but healthy (the readiness endpoint keeps responding), so a
// pod failure triggers a fast failover without split-brain applies.
type LeaderElector struct {
	applier   *KubernetesApplier
	baseURL   string
	client    *http.Client
	namespace string
	leaseName string
	identity  string
	leaseDur  time.Duration
	renewDur  time.Duration
	isLeader  atomic.Bool
}

// NewLeaderElector creates a Lease-based elector reusing the applier's HTTP
// client and credentials (same ServiceAccount, same token).
func NewLeaderElector(applier *KubernetesApplier, namespace, leaseName, identity string) *LeaderElector {
	return &LeaderElector{
		applier:   applier,
		baseURL:   applier.baseURL,
		client:    applier.client,
		namespace: namespace,
		leaseName: leaseName,
		identity:  identity,
		leaseDur:  15 * time.Second,
		renewDur:  5 * time.Second,
	}
}

// Run blocks until the context is cancelled, continuously acquiring or
// renewing the lease. Call IsLeader() from the reconcile loop to gate work.
func (l *LeaderElector) Run(ctx context.Context) {
	ticker := time.NewTicker(l.renewDur)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.release()
			return
		case <-ticker.C:
			wasLeader := l.IsLeader()
			if err := l.acquireOrRenew(ctx); err != nil {
				if wasLeader || os.Getenv("AEP_LEADER_ELECTION_DEBUG") != "" {
					slog.Warn("leader election: lost lease", "error", err)
				}
			} else if !wasLeader && l.IsLeader() {
				slog.Info("leader election: acquired lease", "identity", l.identity)
			}
		}
	}
}

// IsLeader reports whether this instance holds the lease.
func (l *LeaderElector) IsLeader() bool { return l.isLeader.Load() }

type leaseSpec struct {
	HolderIdentity       string `json:"holderIdentity"`
	LeaseDurationSeconds int    `json:"leaseDurationSeconds"`
	AcquireTime          string `json:"acquireTime,omitempty"`
	RenewTime            string `json:"renewTime"`
}

// leaseMetadata keeps only the fields a renew PUT needs. The API server adds
// complex metadata (managedFields, ownerReferences — arrays/objects) to Lease
// responses after any write; decoding into map[string]string dies on those,
// so unknown fields must be ignored rather than typed.
type leaseMetadata struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ResourceVersion string `json:"resourceVersion"`
}

type lease struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Metadata   leaseMetadata `json:"metadata"`
	Spec       leaseSpec     `json:"spec"`
}

func (l *LeaderElector) leasePath() string {
	return fmt.Sprintf("/apis/coordination.k8s.io/v1/namespaces/%s/leases/%s", l.namespace, l.leaseName)
}

func (l *LeaderElector) acquireOrRenew(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			l.isLeader.Store(false)
		}
	}()
	now := leaseTimestamp()

	// Read the current lease (if any).
	current, code, err := l.get(ctx)
	if err != nil && code != http.StatusNotFound {
		return fmt.Errorf("read lease: %w", err)
	}

	if code == http.StatusNotFound {
		// Create the lease.
		body, _ := json.Marshal(lease{
			APIVersion: "coordination.k8s.io/v1",
			Kind:       "Lease",
			Metadata:   leaseMetadata{Name: l.leaseName, Namespace: l.namespace},
			Spec: leaseSpec{
				HolderIdentity:       l.identity,
				LeaseDurationSeconds: int(l.leaseDur.Seconds()),
				AcquireTime:          now,
				RenewTime:            now,
			},
		})
		_, code, err := l.request(ctx, http.MethodPost,
			fmt.Sprintf("/apis/coordination.k8s.io/v1/namespaces/%s/leases", l.namespace), body)
		if err != nil || code != http.StatusCreated {
			return fmt.Errorf("create lease: status=%d error=%v", code, err)
		}
		l.isLeader.Store(true)
		return nil
	}

	// Check if the lease is expired or held by us.
	if current.Spec.HolderIdentity != l.identity {
		renewTime, err := time.Parse(time.RFC3339Nano, current.Spec.RenewTime)
		if err == nil && time.Since(renewTime) < l.leaseDur {
			// Someone else holds a valid lease.
			l.isLeader.Store(false)
			return nil
		}
		// Lease expired — take over.
		slog.Info("leader election: lease expired, taking over", "previous", current.Spec.HolderIdentity)
	}

	// Renew (or take over) the lease.
	current.Spec.HolderIdentity = l.identity
	current.Spec.RenewTime = now
	if current.Spec.AcquireTime == "" {
		current.Spec.AcquireTime = now
	}
	body, _ := json.Marshal(current)
	_, code, err = l.request(ctx, http.MethodPut, l.leasePath(), body)
	if err != nil || code != http.StatusOK {
		return fmt.Errorf("renew lease: status=%d error=%v", code, err)
	}
	l.isLeader.Store(true)
	return nil
}

// leaseTimestamp renders microsecond-precision RFC3339: the coordination API
// rejects metav1.Time strings with nanosecond digits (RFC3339Nano emits up to
// nine and trims trailing zeros, so some timestamps parse and most do not).
func leaseTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}

func (l *LeaderElector) release() {
	if !l.isLeader.Swap(false) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Best-effort: clear the holder so the follower can acquire immediately.
	current, code, err := l.get(ctx)
	if err != nil || code != http.StatusOK || current.Spec.HolderIdentity != l.identity {
		return
	}
	current.Spec.HolderIdentity = ""
	body, _ := json.Marshal(current)
	_, _, _ = l.request(ctx, http.MethodPut, l.leasePath(), body)
}

func (l *LeaderElector) get(ctx context.Context) (*lease, int, error) {
	body, code, err := l.request(ctx, http.MethodGet, l.leasePath(), nil)
	if err != nil {
		return nil, code, err
	}
	if code != http.StatusOK {
		return nil, code, fmt.Errorf("lease request returned %d", code)
	}
	var result lease
	if err := json.NewDecoder(strings.NewReader(string(body))).Decode(&result); err != nil {
		return nil, code, fmt.Errorf("decode lease: %w", err)
	}
	return &result, code, nil
}

func (l *LeaderElector) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var reader *strings.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, l.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+l.applier.bearerToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// Read the whole body: a single Read may legally return a partial chunk.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if os.Getenv("AEP_LEADER_ELECTION_DEBUG") != "" {
			slog.Warn("leader election: api error body", "status", resp.StatusCode, "body", string(body))
		}
	}
	return body, resp.StatusCode, nil
}
