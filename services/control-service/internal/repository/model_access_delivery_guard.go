package repository

import (
	"context"

	"github.com/google/uuid"
)

// CheckModelAccessDelivery checks the live lease and exact immutable intent.
// Call immediately before projection and after read-back. Never hold a database
// transaction across Kubernetes I/O: resourceVersion and admission fence writes.
func (s *DeploymentStore) CheckModelAccessDelivery(ctx context.Context, value ModelAccessDelivery) error {
	if value.DeploymentID != s.deploymentID {
		return ErrPublicationClaimLost
	}
	if _, err := uuid.Parse(value.ClaimToken); err != nil {
		return ErrPublicationClaimLost
	}
	var current bool
	err := s.db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM model_access_publications p
JOIN model_access_streams s ON s.deployment_id=p.deployment_id AND s.binding_key=p.binding_key AND s.last_sequence=p.sequence
JOIN model_access_publication_outbox o ON o.deployment_id=p.deployment_id AND o.operation_id=p.operation_id
WHERE p.deployment_id=? AND p.binding_key=? AND p.subject_id=? AND p.operation_id=? AND p.sequence=?
AND p.source_version=? AND p.revision=? AND p.content_hash=? AND p.issued_at=? AND p.valid_until=? AND p.phase='projecting'
AND o.claim_token=? AND o.completed_at IS NULL AND o.lease_until>clock_timestamp()
AND p.issued_at<=clock_timestamp() AND p.valid_until>clock_timestamp())`,
		s.deploymentID, value.BindingKey, value.SubjectID, value.OperationID, value.Sequence,
		value.SourceVersion, value.Revision, value.ContentHash, value.IssuedAt, value.ValidUntil, value.ClaimToken).Scan(&current).Error
	if err != nil {
		return err
	}
	if !current {
		return ErrPublicationClaimLost
	}
	return nil
}
