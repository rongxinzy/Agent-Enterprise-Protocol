package publicationrelay

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

type workerStore struct {
	item                    *repository.PublicationDelivery
	claimErr, errorOnFinish error
	phase, code             string
	retry                   time.Duration
	finished                int
}

func (s *workerStore) ClaimDataPlanePublication(context.Context) (*repository.PublicationDelivery, error) {
	return s.item, s.claimErr
}
func (s *workerStore) FinishDataPlanePublication(_ context.Context, op, token, phase, code string, retry time.Duration) error {
	if op != s.item.OperationID || token != s.item.ClaimToken {
		panic("wrong claim acknowledged")
	}
	s.phase, s.code, s.retry = phase, code, retry
	s.finished++
	return s.errorOnFinish
}

type projectionFunc func(context.Context, repository.PublicationDelivery) error

func (f projectionFunc) Project(ctx context.Context, item repository.PublicationDelivery) error {
	return f(ctx, item)
}

func TestWorkerCompletionClassification(t *testing.T) {
	for _, test := range []struct {
		err         error
		phase, code string
	}{
		{nil, "projected", ""}, {ErrSuperseded, "superseded", ""}, {ErrSnapshot, "failed", "INVALID_OPERATOR_SNAPSHOT"},
		{apierrors.NewInvalid(schema.GroupKind{Group: "gateway.aep.zhiyuan.io", Kind: "ModelRoute"}, "bad", nil), "failed", "INVALID_OPERATOR_SNAPSHOT"},
		{ErrOwnership, "failed", "CR_OWNERSHIP_CONFLICT"}, {errors.New("API offline"), "", ""}, {context.DeadlineExceeded, "", ""},
	} {
		t.Run(test.phase+test.code, func(t *testing.T) {
			item := delivery(1, nil, nil)
			store := &workerStore{item: &item}
			w := New(store, projectionFunc(func(ctx context.Context, _ repository.PublicationDelivery) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded Kubernetes operation")
				}
				return test.err
			}))
			if err := w.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if store.phase != test.phase || store.code != test.code || store.finished != 1 {
				t.Fatalf("completion = %+v", store)
			}
			if test.phase == "" {
				if store.retry < time.Second {
					t.Fatal("transient failure not backed off")
				}
				if err := w.Step(t.Context()); err != nil {
					t.Fatal(err)
				}
				if store.retry <= time.Second {
					t.Fatal("retry backoff did not grow")
				}
			}
		})
	}
}

func TestWorkerDoesNotDropFailedOrLostClaims(t *testing.T) {
	for _, mode := range []string{"empty", "claim-error", "finish-error", "claim-lost"} {
		t.Run(mode, func(t *testing.T) {
			item := delivery(1, nil, nil)
			store := &workerStore{item: &item}
			wantErr := false
			switch mode {
			case "empty":
				store.item = nil
			case "claim-error":
				store.claimErr = errors.New("db down")
				wantErr = true
			case "finish-error":
				store.errorOnFinish = errors.New("commit uncertain")
				wantErr = true
			case "claim-lost":
				store.errorOnFinish = repository.ErrPublicationClaimLost
				wantErr = true
			}
			calls := 0
			w := New(store, projectionFunc(func(context.Context, repository.PublicationDelivery) error { calls++; return nil }))
			if err := w.Step(t.Context()); (err != nil) != wantErr {
				t.Fatalf("Step = %v", err)
			}
			if (mode == "empty" || mode == "claim-error") && calls != 0 {
				t.Fatal("unclaimed work was projected")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	New(&workerStore{}, projectionFunc(func(context.Context, repository.PublicationDelivery) error { return nil })).Run(ctx)
}
