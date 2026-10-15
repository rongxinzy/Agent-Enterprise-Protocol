package publicationrelay

import (
	"context"
	"errors"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

type Store interface {
	ClaimDataPlanePublication(context.Context) (*repository.PublicationDelivery, error)
	FinishDataPlanePublication(context.Context, string, string, string, string, time.Duration) error
}

type Projection interface {
	Project(context.Context, repository.PublicationDelivery) error
}

type Worker struct {
	Store     Store
	Projector Projection
	Backoff   workqueue.TypedRateLimiter[string]
}

func New(store Store, projector Projection) *Worker {
	return &Worker{Store: store, Projector: projector, Backoff: workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Second, time.Minute)}
}

// InClusterClient deliberately has no kubeconfig/env fallback. Enabling the
// worker cannot accidentally target a developer's current external cluster.
func InClusterClient() (client.Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, errors.New("publication relay requires in-cluster Kubernetes configuration")
	}
	cfg.Timeout = 5 * time.Second
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

func (w *Worker) Run(ctx context.Context) {
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := w.Step(ctx); err != nil && ctx.Err() == nil {
			// SQL and API error strings may carry payloads. Keep them out of logs.
			slog.Warn("publication relay cycle could not be confirmed; durable intent retained")
		}
	}, time.Second)
}

func (w *Worker) Step(ctx context.Context) error {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	item, err := w.Store.ClaimDataPlanePublication(claimCtx)
	cancel()
	if err != nil || item == nil {
		return err
	}
	projectCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	err = w.Projector.Project(projectCtx, *item)
	stop()
	phase, code := "projected", ""
	retry := time.Duration(0)
	switch {
	case err == nil:
	case errors.Is(err, ErrSuperseded):
		phase = "superseded"
	case errors.Is(err, ErrSnapshot), apierrors.IsInvalid(err):
		phase, code = "failed", "INVALID_OPERATOR_SNAPSHOT"
	case errors.Is(err, ErrOwnership):
		phase, code = "failed", "CR_OWNERSHIP_CONFLICT"
	default:
		phase = ""
		retry = w.Backoff.When(item.OperationID)
		slog.Warn("publication relay will retry", "operation_id", item.OperationID, "reason", apierrors.ReasonForError(err), "retry_after", retry)
	}
	finishCtx, finishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer finishCancel()
	finishErr := w.Store.FinishDataPlanePublication(finishCtx, item.OperationID, item.ClaimToken, phase, code, retry)
	if phase != "" || errors.Is(finishErr, repository.ErrPublicationClaimLost) {
		w.Backoff.Forget(item.OperationID)
	}
	if finishErr != nil {
		return finishErr
	}
	if phase == "failed" {
		slog.Warn("publication relay rejected intent", "operation_id", item.OperationID, "code", code)
	}
	return nil
}
