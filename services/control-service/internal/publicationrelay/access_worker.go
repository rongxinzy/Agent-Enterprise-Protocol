package publicationrelay

import (
	"context"
	"errors"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/workqueue"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

type AccessStore interface {
	PendingModelAccessSubjects(context.Context, string, int) ([]string, error)
	CaptureModelAccessBindings(context.Context, string, time.Duration) (*repository.ModelAccessCapture, error)
	ClaimModelAccessPublication(context.Context) (*repository.ModelAccessDelivery, error)
	CheckModelAccessDelivery(context.Context, repository.ModelAccessDelivery) error
	FinishModelAccessPublication(context.Context, string, string, string, string, time.Duration) error
}

type AccessProjection interface {
	ProjectAccess(context.Context, repository.ModelAccessDelivery) error
}

// AccessRelay is driven by the existing single-consumer publication worker.
// Native delaying workqueue isolates incompatible subjects; durable DB intent
// remains authoritative across replicas, restarts and transient failures.
type AccessRelay struct {
	store     AccessStore
	projector AccessProjection
	freshness time.Duration
	queue     workqueue.TypedRateLimitingInterface[string]
	backoff   workqueue.TypedRateLimiter[string]
	cursor    string
}

func NewAccessRelay(store AccessStore, projector AccessProjection, freshness time.Duration) (*AccessRelay, error) {
	if freshness < 2*time.Second || freshness > time.Hour || freshness%time.Second != 0 {
		return nil, repository.ErrAccessPublicationInput
	}
	return &AccessRelay{store: store, projector: projector, freshness: freshness,
		queue:   workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Second, time.Minute)),
		backoff: workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Second, time.Minute)}, nil
}

func (a *AccessRelay) Close() { a.queue.ShutDown() }

func (a *AccessRelay) capture(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	ids, discoveryErr := a.store.PendingModelAccessSubjects(queryCtx, a.cursor, 32)
	cancel()
	if discoveryErr == nil {
		if len(ids) == 32 {
			a.cursor = ids[len(ids)-1]
		} else {
			a.cursor = ""
		}
		for _, id := range ids {
			// Add must not bypass an existing AddRateLimited delay.
			if a.queue.NumRequeues(id) == 0 {
				a.queue.Add(id)
			}
		}
	}
	if a.queue.Len() == 0 {
		return discoveryErr
	}
	id, shutdown := a.queue.Get() // Only the owning Worker consumes this queue.
	if shutdown {
		return discoveryErr
	}
	defer a.queue.Done(id)
	captureCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	_, captureErr := a.store.CaptureModelAccessBindings(captureCtx, id, a.freshness)
	stop()
	if captureErr != nil {
		a.queue.AddRateLimited(id)
		// No raw error, subject identifier, token or policy payload in logs.
		slog.Warn("model access source capture deferred; durable change retained")
	} else {
		a.queue.Forget(id)
	}
	return errors.Join(discoveryErr, captureErr)
}

func (a *AccessRelay) project(ctx context.Context, item repository.ModelAccessDelivery) error {
	if err := a.store.CheckModelAccessDelivery(ctx, item); err != nil {
		return err
	}
	if err := a.projector.ProjectAccess(ctx, item); err != nil {
		return err
	}
	return a.store.CheckModelAccessDelivery(ctx, item)
}

func (a *AccessRelay) deliver(ctx context.Context) error {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	item, err := a.store.ClaimModelAccessPublication(claimCtx)
	cancel()
	if err != nil || item == nil {
		return err
	}
	projectCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	err = a.project(projectCtx, *item)
	stop()
	if errors.Is(err, repository.ErrPublicationClaimLost) {
		a.backoff.Forget(item.OperationID)
		return err // A newer intent/reclaimed lease owns completion now.
	}
	phase, code := "projected", ""
	retry := time.Duration(0)
	switch {
	case err == nil:
	case errors.Is(err, ErrSnapshot), apierrors.IsInvalid(err):
		phase, code = "failed", "INVALID_ACCESS_SNAPSHOT"
	case errors.Is(err, ErrOwnership), errors.Is(err, ErrSuperseded):
		// A newer legitimate intent loses this lease. If it is still current,
		// a higher foreign CR sequence is an ownership failure, not success.
		phase, code = "failed", "ACCESS_CR_OWNERSHIP_CONFLICT"
	default:
		phase = ""
		retry = a.backoff.When(item.OperationID)
		slog.Warn("model access projection will retry", "operation_id", item.OperationID,
			"reason", apierrors.ReasonForError(err), "retry_after", retry)
	}
	finishCtx, finishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer finishCancel()
	finishErr := a.store.FinishModelAccessPublication(finishCtx, item.OperationID, item.ClaimToken, phase, code, retry)
	if phase != "" || errors.Is(finishErr, repository.ErrPublicationClaimLost) {
		a.backoff.Forget(item.OperationID)
	}
	if phase == "failed" {
		slog.Warn("model access projection rejected", "operation_id", item.OperationID, "code", code)
	}
	return finishErr
}
