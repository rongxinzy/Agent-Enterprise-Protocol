package reconciler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaseReadFailureNeverPanicsOrRetainsLeadership(t *testing.T) {
	for _, status := range []int{401, 403, 500, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Error("wrote after a failed lease read")
			}
			w.WriteHeader(status)
		}))
		applier, err := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "disposable", HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		elector := NewLeaderElector(applier, "aep-system", "gateway-reconciler-leader", "a")
		elector.isLeader.Store(true)
		if err := elector.acquireOrRenew(context.Background()); err == nil || !strings.Contains(err.Error(), "lease") || elector.IsLeader() {
			t.Fatalf("status %d did not fail closed: %v", status, err)
		}
		server.Close()
	}
}

func TestLeaseCreateRejectionNeverGrantsLeadership(t *testing.T) {
	for _, status := range []int{401, 403, 409, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
			} else {
				w.WriteHeader(status)
			}
		}))
		applier, _ := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "disposable", HTTPClient: server.Client()})
		elector := NewLeaderElector(applier, "aep-system", "gateway-reconciler-leader", "a")
		if err := elector.acquireOrRenew(context.Background()); err == nil || elector.IsLeader() {
			t.Fatalf("create %d granted leadership", status)
		}
		server.Close()
	}
}

func TestRunKeepsFollowerIdleAcrossSuccessfulReads(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("follower wrote another owner's lease")
		}
		_, _ = w.Write([]byte(`{"spec":{"holderIdentity":"other","renewTime":"` + leaseTimestamp() + `"}}`))
		reads.Add(1)
	}))
	defer server.Close()
	applier, _ := NewKubernetesApplier(KubernetesConfig{URL: server.URL, Token: "disposable", HTTPClient: server.Client()})
	elector := NewLeaderElector(applier, "aep-system", "gateway-reconciler-leader", "a")
	elector.renewDur = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { elector.Run(ctx); close(done) }()
	for reads.Load() < 5 && ctx.Err() == nil {
		if elector.IsLeader() {
			t.Error("successful follower read granted leadership")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if elector.IsLeader() || reads.Load() < 5 {
		t.Fatal("follower did not stay idle")
	}
}

func TestReleaseNeverClearsAnotherOwner(t *testing.T) {
	api := &fakeLeaseAPI{lease: &lease{Spec: leaseSpec{HolderIdentity: "other", RenewTime: leaseTimestamp()}}}
	elector := newTestElector(t, api, "a")
	elector.isLeader.Store(true)
	elector.release()
	if api.put != 0 || api.lease.Spec.HolderIdentity != "other" || elector.IsLeader() {
		t.Fatal("release overwrote another owner")
	}
}
