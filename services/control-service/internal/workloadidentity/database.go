package workloadidentity

import (
	"context"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// DatabaseRegistry uses the existing transaction, account-kind validation and
// immutable lifecycle fences. The observer cannot grant roles/models or purge
// history. No public registration endpoint accepts caller-supplied CR claims.
type DatabaseRegistry struct{ Store *repository.DeploymentStore }

func (r DatabaseRegistry) Registrations(ctx context.Context, namespace, name string) ([]Registration, error) {
	rows, err := r.Store.ModelAccessWorkloadRegistrations(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	result := make([]Registration, 0, len(rows))
	for _, row := range rows {
		result = append(result, Registration{AgentID: row.UserID, BindingKey: row.BindingKey})
	}
	return result, nil
}

func (r DatabaseRegistry) Register(ctx context.Context, agentID, expected string, w repository.ObservedModelWorkload, validFor time.Duration) (string, error) {
	return r.Store.RegisterModelAccessWorkload(ctx, agentID, expected, w, validFor)
}

func (r DatabaseRegistry) Retire(ctx context.Context, agentID, key string) error {
	return r.Store.RetireModelAccessWorkload(ctx, agentID, key)
}
