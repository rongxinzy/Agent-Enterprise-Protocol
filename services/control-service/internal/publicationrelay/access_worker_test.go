package publicationrelay

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

type accessWorkerStore struct {
	*workerStore
	ids                     []string
	discoverErr, captureErr error
	claimAccessErr          error
	item                    *repository.ModelAccessDelivery
	guard                   func(int) error
	checks, projected       int
	captured                []string
	cursors                 []string
	phase, code             string
	retry                   time.Duration
	finished                int
	finishErr               error
}

func requireDeadline(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded access operation")
	}
}

func (s *accessWorkerStore) PendingModelAccessSubjects(ctx context.Context, after string, limit int) ([]string, error) {
	requireDeadline(ctx)
	s.cursors = append(s.cursors, after)
	if limit != 32 {
		panic("unbounded discovery")
	}
	return s.ids, s.discoverErr
}

func (s *accessWorkerStore) CaptureModelAccessSessions(ctx context.Context, id string, freshness time.Duration) (*repository.ModelAccessCapture, error) {
	requireDeadline(ctx)
	if freshness != 5*time.Minute {
		panic("unexpected freshness")
	}
	s.captured = append(s.captured, id)
	if id == "alice" {
		return nil, s.captureErr
	}
	return nil, nil
}

func (s *accessWorkerStore) ClaimModelAccessPublication(ctx context.Context) (*repository.ModelAccessDelivery, error) {
	requireDeadline(ctx)
	return s.item, s.claimAccessErr
}

func (s *accessWorkerStore) CheckModelAccessDelivery(ctx context.Context, _ repository.ModelAccessDelivery) error {
	requireDeadline(ctx)
	s.checks++
	if s.guard != nil {
		return s.guard(s.checks)
	}
	return nil
}

func (s *accessWorkerStore) FinishModelAccessPublication(ctx context.Context, op, token, phase, code string, retry time.Duration) error {
	requireDeadline(ctx)
	if op != s.item.OperationID || token != s.item.ClaimToken {
		panic("wrong access claim completed")
	}
	s.phase, s.code, s.retry = phase, code, retry
	s.finished++
	return s.finishErr
}

type accessProjectionFunc func(context.Context, repository.ModelAccessDelivery) error

func (f accessProjectionFunc) ProjectAccess(ctx context.Context, item repository.ModelAccessDelivery) error {
	requireDeadline(ctx)
	return f(ctx, item)
}

func accessWorker(t *testing.T, s *accessWorkerStore, failure error) *AccessRelay {
	t.Helper()
	a, err := NewAccessRelay(s, accessProjectionFunc(func(context.Context, repository.ModelAccessDelivery) error {
		s.projected++
		return failure
	}), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestAccessWorkerRejectsStaleClaimsBeforeAndAfterWrite(t *testing.T) {
	for _, at := range []int{1, 2} {
		item := accessDelivery(t, 1, "alice", "one", false)
		s := &accessWorkerStore{item: &item, guard: func(n int) error {
			if n == at {
				return repository.ErrPublicationClaimLost
			}
			return nil
		}}
		a := accessWorker(t, s, nil)
		if err := a.deliver(t.Context()); !errors.Is(err, repository.ErrPublicationClaimLost) || s.finished != 0 || s.projected != at-1 {
			t.Fatalf("stale claim at %d: err=%v writes=%d finishes=%d", at, err, s.projected, s.finished)
		}
	}
}

func TestAccessWorkerDeliveryClassification(t *testing.T) {
	for _, test := range []struct {
		err         error
		phase, code string
	}{
		{nil, "projected", ""},
		{ErrSnapshot, "failed", "INVALID_ACCESS_SNAPSHOT"},
		{apierrors.NewInvalid(schema.GroupKind{Kind: "ModelAccessGrant"}, "invalid", nil), "failed", "INVALID_ACCESS_SNAPSHOT"},
		{ErrOwnership, "failed", "ACCESS_CR_OWNERSHIP_CONFLICT"},
		{ErrSuperseded, "failed", "ACCESS_CR_OWNERSHIP_CONFLICT"},
		{context.DeadlineExceeded, "", ""},
		{errors.New("connection lost"), "", ""},
	} {
		t.Run(test.phase+test.code, func(t *testing.T) {
			item := accessDelivery(t, 1, "alice", "one", false)
			s := &accessWorkerStore{item: &item}
			a := accessWorker(t, s, test.err)
			if err := a.deliver(t.Context()); err != nil || s.finished != 1 || s.phase != test.phase || s.code != test.code {
				t.Fatalf("completion phase=%s code=%s err=%v", s.phase, s.code, err)
			}
			if test.err == nil && s.checks != 2 {
				t.Fatal("successful projection missing post-write lease check")
			}
			if test.phase == "" {
				before := s.retry
				if err := a.deliver(t.Context()); err != nil || s.retry <= before || before < time.Second {
					t.Fatal("transient retry did not back off")
				}
			}
		})
	}
	for _, mode := range []string{"claim error", "no work", "finish error"} {
		item := accessDelivery(t, 1, "alice", "one", false)
		s := &accessWorkerStore{item: &item}
		switch mode {
		case "claim error":
			s.claimAccessErr = context.DeadlineExceeded
		case "no work":
			s.item = nil
		case "finish error":
			s.finishErr = context.DeadlineExceeded
		}
		if err := accessWorker(t, s, nil).deliver(t.Context()); (err != nil) != (mode != "no work") {
			t.Fatalf("%s = %v", mode, err)
		}
		if mode != "finish error" && s.projected != 0 {
			t.Fatal("unclaimed work projected")
		}
	}
}

func TestAccessWorkerCaptureFailureDoesNotStarveOtherSubjectsOrDelivery(t *testing.T) {
	item := accessDelivery(t, 1, "bob", "one", false)
	s := &accessWorkerStore{workerStore: &workerStore{claimErr: errors.New("route DB error")},
		ids: []string{"alice", "bob"}, item: &item, captureErr: repository.ErrAccessLegacyGrants}
	a := accessWorker(t, s, nil)
	w := New(s, projectionFunc(func(context.Context, repository.PublicationDelivery) error {
		t.Fatal("unclaimed route projection")
		return nil
	}))
	w.Access = a
	if err := w.Step(t.Context()); !errors.Is(err, repository.ErrAccessLegacyGrants) || s.finished != 1 {
		t.Fatal("source/route failure starved independent queued delivery")
	}
	if err := w.Step(t.Context()); err == nil || !slices.Equal(s.captured, []string{"alice", "bob"}) {
		t.Fatal("incompatible Alice starved Bob")
	}
	if a.queue.NumRequeues("alice") != 1 {
		t.Fatal("source error was dropped instead of native delayed retry")
	}
	// Discovery failure must not discard already queued work.
	a.queue.Add("carol")
	s.discoverErr = context.DeadlineExceeded
	if err := a.capture(t.Context()); !errors.Is(err, context.DeadlineExceeded) || s.captured[len(s.captured)-1] != "carol" {
		t.Fatal("discovery outage starved existing queue")
	}
}

func TestAccessWorkerDiscoveryCursorAndOptionValidation(t *testing.T) {
	s := &accessWorkerStore{}
	for i := range 32 {
		s.ids = append(s.ids, string(rune('a'+i)))
	}
	a := accessWorker(t, s, nil)
	if err := a.capture(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.ids = nil
	if err := a.capture(t.Context()); err != nil || s.cursors[1] != string(rune('a'+31)) || a.cursor != "" {
		t.Fatal("discovery did not resume/reset pagination")
	}
	for _, duration := range []time.Duration{0, time.Second, 2500 * time.Millisecond, 2 * time.Hour} {
		if _, err := NewAccessRelay(s, nil, duration); err == nil {
			t.Fatal("unsafe implicit/invalid freshness accepted")
		}
	}
}
