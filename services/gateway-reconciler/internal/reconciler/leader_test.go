package reconciler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLeaseAPI is a minimal coordination.k8s.io/v1 leases server backed by a
// single in-memory lease. It records requests so tests can assert the
// acquire/renew/release sequence.
type fakeLeaseAPI struct {
	mutex  sync.Mutex
	lease  *lease
	get    int
	post   int
	put    int
	status int // force this status on PUT instead of 200
}

func (f *fakeLeaseAPI) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		f.mutex.Lock()
		defer f.mutex.Unlock()
		if !strings.Contains(request.URL.Path, "/leases") {
			http.NotFound(response, request)
			return
		}
		switch request.Method {
		case http.MethodGet:
			f.get++
			if f.lease == nil {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			body, _ := json.Marshal(f.lease)
			_, _ = response.Write(body)
		case http.MethodPost:
			f.post++
			var created lease
			if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
				t.Errorf("post body: %v", err)
			}
			f.lease = &created
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{}`))
		case http.MethodPut:
			f.put++
			var updated lease
			if err := json.NewDecoder(request.Body).Decode(&updated); err != nil {
				t.Errorf("put body: %v", err)
			}
			f.lease = &updated
			if f.status != 0 {
				response.WriteHeader(f.status)
				return
			}
			_, _ = response.Write([]byte(`{}`))
		default:
			http.Error(response, "method", http.StatusMethodNotAllowed)
		}
	})
}

func newTestElector(t *testing.T, api *fakeLeaseAPI, identity string) *LeaderElector {
	t.Helper()
	server := httptest.NewServer(api.handler(t))
	t.Cleanup(server.Close)
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewKubernetesApplier(): %v", err)
	}
	return NewLeaderElector(applier, "aep-system", "gateway-reconciler", identity)
}

func TestLeaderElectionAcquire(t *testing.T) {
	api := &fakeLeaseAPI{}
	elector := newTestElector(t, api, "instance-a")
	if elector.IsLeader() {
		t.Fatal("a fresh elector is not the leader")
	}
	if !strings.Contains(elector.leasePath(), "/namespaces/aep-system/leases/gateway-reconciler") {
		t.Fatalf("leasePath = %q", elector.leasePath())
	}

	if err := elector.acquireOrRenew(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !elector.IsLeader() {
		t.Fatal("creating the lease must acquire leadership")
	}
	if api.lease.Spec.HolderIdentity != "instance-a" {
		t.Fatalf("holder = %q", api.lease.Spec.HolderIdentity)
	}
}

func TestLeaderElectionFollowerRespectsValidLease(t *testing.T) {
	api := &fakeLeaseAPI{lease: &lease{
		APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Spec: leaseSpec{HolderIdentity: "instance-b", LeaseDurationSeconds: 15, RenewTime: time.Now().UTC().Format(time.RFC3339Nano)},
	}}
	elector := newTestElector(t, api, "instance-a")

	if err := elector.acquireOrRenew(context.Background()); err != nil {
		t.Fatalf("follower cycle: %v", err)
	}
	if elector.IsLeader() {
		t.Fatal("another instance's fresh lease must not be taken over")
	}
	if api.put != 0 {
		t.Fatal("a follower must not write the lease")
	}
}

func TestLeaderElectionTakesOverExpiredLease(t *testing.T) {
	stale := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	api := &fakeLeaseAPI{lease: &lease{
		APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Spec: leaseSpec{HolderIdentity: "instance-b", LeaseDurationSeconds: 15, RenewTime: stale},
	}}
	elector := newTestElector(t, api, "instance-a")

	if err := elector.acquireOrRenew(context.Background()); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	// Leadership requires a successful server-side takeover.
	if api.put != 1 || api.lease.Spec.HolderIdentity != "instance-a" || api.lease.Spec.AcquireTime == "" {
		t.Fatalf("after takeover: put=%d spec=%#v", api.put, api.lease.Spec)
	}
}

func TestLeaderElectionRenewFailureDropsLeadership(t *testing.T) {
	// Seed a lease held by us so the cycle reaches the renew PUT, which the
	// fake API rejects with 409 (another instance took over mid-cycle).
	api := &fakeLeaseAPI{status: http.StatusConflict, lease: &lease{
		APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Spec: leaseSpec{HolderIdentity: "instance-a", LeaseDurationSeconds: 15, RenewTime: time.Now().UTC().Format(time.RFC3339Nano)},
	}}
	elector := newTestElector(t, api, "instance-a")
	elector.isLeader.Store(true)

	if err := elector.acquireOrRenew(context.Background()); err == nil {
		t.Fatal("a failed renew must surface an error")
	}
}

func TestLeaderElectionRunAcquiresAndReleases(t *testing.T) {
	api := &fakeLeaseAPI{}
	elector := newTestElector(t, api, "instance-run")
	elector.renewDur = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { elector.Run(ctx); close(done) }()

	// The API stores the holder before its create response reaches the elector.
	// Wait for both the server write and the atomic leadership flag so cancel
	// exercises release of an acquired lease, not an in-flight acquisition.
	deadline := time.Now().Add(2 * time.Second)
	for {
		api.mutex.Lock()
		acquired := api.lease != nil && api.lease.Spec.HolderIdentity == "instance-run"
		api.mutex.Unlock()
		if acquired && elector.IsLeader() {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run never acquired leadership")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	// Run has finished releasing the acquired lease.
	if elector.IsLeader() {
		t.Fatal("release must clear leadership")
	}
	api.mutex.Lock()
	holder := api.lease.Spec.HolderIdentity
	api.mutex.Unlock()
	if holder != "" {
		t.Fatalf("release must clear the holder, got %q", holder)
	}
}

func TestLeaderElectionGetDecodesLease(t *testing.T) {
	api := &fakeLeaseAPI{lease: &lease{
		APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Spec: leaseSpec{HolderIdentity: "someone", RenewTime: time.Now().UTC().Format(time.RFC3339Nano)},
	}}
	elector := newTestElector(t, api, "instance-a")
	current, code, err := elector.get(context.Background())
	if err != nil || code != http.StatusOK || current.Spec.HolderIdentity != "someone" {
		t.Fatalf("get = %#v, %d, %v", current, code, err)
	}
}

// TestLeaderElectorHandlesServerManagedLeaseMetadata pins the production
// failure where the API server's managedFields array (present on any
// previously written Lease) failed decode into map[string]string and the
// elector silently never led.
func TestLeaderElectorHandlesServerManagedLeaseMetadata(t *testing.T) {
	t.Parallel()
	const realShapedLease = `{"kind":"Lease","apiVersion":"coordination.k8s.io/v1","metadata":{"name":"gateway-reconciler-leader","namespace":"aep-system","uid":"27c06c75-ce9f-4c33-af3c-798278b35040","resourceVersion":"6371722","creationTimestamp":"2026-10-01T08:29:01Z","managedFields":[{"manager":"python-urllib","operation":"Update","apiVersion":"coordination.k8s.io/v1","time":"2026-10-03T10:47:12Z","fieldsType":"FieldsV1","fieldsV1":{"f:spec":{".":{},"f:acquireTime":{},"f:holderIdentity":{},"f:leaseDurationSeconds":{},"f:renewTime":{}}}}]},"spec":{"holderIdentity":"previous-owner","leaseDurationSeconds":15,"acquireTime":"2026-10-01T08:29:01.469777Z","renewTime":"2020-01-01T00:00:00.000000Z","leaseTransitions":0}}`
	var seeded lease
	if err := json.Unmarshal([]byte(realShapedLease), &seeded); err != nil {
		t.Fatalf("real-shaped lease no longer decodes: %v", err)
	}
	api := &fakeLeaseAPI{lease: &seeded}
	elector := newTestElector(t, api, "reconciler-test")
	if err := elector.acquireOrRenew(context.Background()); err != nil {
		t.Fatalf("acquireOrRenew against a real-shaped lease failed: %v", err)
	}
	// Verify the server-side takeover, not just the local leadership flag.
	api.mutex.Lock()
	holder := api.lease.Spec.HolderIdentity
	api.mutex.Unlock()
	if holder != "reconciler-test" {
		t.Fatalf("lease holder = %q", holder)
	}
}

func TestBearerTokenRereadsProjectedTokenFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applier, err := NewKubernetesApplier(KubernetesConfig{URL: "http://localhost", TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := applier.bearerToken(); got != "first-token" {
		t.Fatalf("bearerToken() = %q", got)
	}
	if err := os.WriteFile(path, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := applier.bearerToken(); got != "rotated-token" {
		t.Fatalf("rotated bearerToken() = %q", got)
	}
	// A static token still works without a file and a broken file falls back
	// to the last known value rather than sending an empty credential.
	static, err := NewKubernetesApplier(KubernetesConfig{URL: "http://localhost", Token: "static"})
	if err != nil || static.bearerToken() != "static" {
		t.Fatalf("static token applier = %v %v", static.bearerToken(), err)
	}
}

// TestLeaseTimestampUsesMicrosecondPrecision pins the apiserver contract:
// metav1.Time rejects nanosecond-precision strings, and RFC3339Nano trims
// trailing zeros so only some timestamps fail — the two-day silent stall on
// the cicd cluster came from exactly this.
func TestLeaseTimestampUsesMicrosecondPrecision(t *testing.T) {
	t.Parallel()
	stamp := leaseTimestamp()
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", stamp); err != nil {
		t.Fatalf("leaseTimestamp() = %q, not microsecond RFC3339: %v", stamp, err)
	}
	if fraction := strings.Split(stamp, ".")[1]; len(fraction) != 7 {
		t.Fatalf("leaseTimestamp() = %q, unexpected fraction %q", stamp, fraction)
	}
}
