package repository

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var accessErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,99}$`)

// ClaimModelAccessPublication claims one current binding, not the deployment's
// global latest operation. SKIP LOCKED lets other bindings progress while a
// stream is busy; claims share the stream lock used by intent capture.
func (s *DeploymentStore) ClaimModelAccessPublication(ctx context.Context) (*ModelAccessDelivery, error) {
	var delivery *ModelAccessDelivery
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		var binding struct{ BindingKey string }
		result := tx.db.Raw(`SELECT s.binding_key FROM model_access_streams s
JOIN model_access_publications p ON p.deployment_id=s.deployment_id AND p.binding_key=s.binding_key AND p.sequence=s.last_sequence
JOIN model_access_publication_outbox o ON o.deployment_id=p.deployment_id AND o.operation_id=p.operation_id
WHERE s.deployment_id=? AND p.phase IN ('queued','projecting') AND o.completed_at IS NULL
AND o.next_attempt_at<=clock_timestamp() AND (o.lease_until IS NULL OR o.lease_until<=clock_timestamp())
ORDER BY o.next_attempt_at,p.created_at,s.binding_key LIMIT 1 FOR UPDATE OF s SKIP LOCKED`, s.deploymentID).Scan(&binding)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		value, err := tx.LatestModelAccessPublication(ctx, binding.BindingKey)
		if err != nil {
			return err
		}
		var valid bool
		if err := tx.db.Raw(`SELECT ?::timestamptz>clock_timestamp()`, value.ValidUntil).Scan(&valid).Error; err != nil {
			return err
		}
		if !valid {
			if err := tx.db.Exec(`UPDATE model_access_publications SET phase='failed',error_code='ACCESS_SNAPSHOT_EXPIRED',updated_at=now() WHERE deployment_id=? AND operation_id=?`, s.deploymentID, value.OperationID).Error; err != nil {
				return err
			}
			return tx.db.Exec(`UPDATE model_access_publication_outbox SET completed_at=now(),claim_token=NULL,lease_until=NULL WHERE deployment_id=? AND operation_id=?`, s.deploymentID, value.OperationID).Error
		}
		token := uuid.NewString()
		if err := tx.db.Exec(`UPDATE model_access_publication_outbox SET claim_token=?,lease_until=clock_timestamp()+interval '30 seconds',attempts=attempts+1 WHERE deployment_id=? AND operation_id=?`, token, s.deploymentID, value.OperationID).Error; err != nil {
			return err
		}
		if err := tx.db.Exec(`UPDATE model_access_publications SET phase='projecting',updated_at=now() WHERE deployment_id=? AND operation_id=?`, s.deploymentID, value.OperationID).Error; err != nil {
			return err
		}
		value.Phase = "projecting"
		delivery = &ModelAccessDelivery{ModelAccessPublication: value, ClaimToken: token}
		return nil
	})
	return delivery, err
}

// FinishModelAccessPublication acknowledges only a live claim on current intent.
// Projection means CR delivery, not all-serving-process adoption. Empty phase
// retries with bounded delay; malformed/ownership inputs can fail terminally.
func (s *DeploymentStore) FinishModelAccessPublication(ctx context.Context, operationID, token, phase, code string, retryAfter time.Duration) error {
	if _, err := uuid.Parse(operationID); err != nil {
		return ErrPublicationClaimLost
	}
	if _, err := uuid.Parse(token); err != nil {
		return ErrPublicationClaimLost
	}
	if (phase != "" && phase != "projected" && phase != "failed") ||
		(phase == "failed" && !accessErrorCodePattern.MatchString(code)) ||
		(phase != "failed" && code != "") || retryAfter < 0 || retryAfter > time.Hour {
		return errors.New("invalid model access delivery completion")
	}
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		var binding struct{ BindingKey string }
		result := tx.db.Raw(`SELECT s.binding_key FROM model_access_streams s JOIN model_access_publications p
ON p.deployment_id=s.deployment_id AND p.binding_key=s.binding_key AND p.sequence=s.last_sequence
WHERE p.deployment_id=? AND p.operation_id=? FOR UPDATE OF s`, s.deploymentID, operationID).Scan(&binding)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrPublicationClaimLost
		}
		result = tx.db.Exec(`UPDATE model_access_publication_outbox o SET claim_token=NULL,lease_until=NULL,
next_attempt_at=clock_timestamp()+(? * interval '1 millisecond'),completed_at=CASE WHEN ?='' THEN NULL ELSE now() END
FROM model_access_publications p WHERE p.deployment_id=o.deployment_id AND p.operation_id=o.operation_id
AND o.deployment_id=? AND o.operation_id=? AND o.claim_token=? AND o.lease_until>clock_timestamp()
AND o.completed_at IS NULL AND p.phase='projecting' AND p.valid_until>clock_timestamp()`, retryAfter.Milliseconds(), phase, s.deploymentID, operationID, token)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPublicationClaimLost
		}
		var errorCode any
		if phase == "failed" {
			errorCode = code
		}
		if phase == "" {
			phase = "queued"
		}
		return tx.db.Exec(`UPDATE model_access_publications SET phase=?,error_code=?,updated_at=now() WHERE deployment_id=? AND operation_id=?`, phase, errorCode, s.deploymentID, operationID).Error
	})
}

func (s *DeploymentStore) IsLatestModelAccessPublication(ctx context.Context, value ModelAccessPublication) (bool, error) {
	if value.DeploymentID != s.deploymentID {
		return false, nil
	}
	var current bool
	err := s.db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM model_access_publications p JOIN model_access_streams s
ON s.deployment_id=p.deployment_id AND s.binding_key=p.binding_key AND s.last_sequence=p.sequence
WHERE p.deployment_id=? AND p.binding_key=? AND p.operation_id=? AND p.sequence=? AND p.phase='projected' AND p.valid_until>clock_timestamp())`, s.deploymentID, value.BindingKey, value.OperationID, value.Sequence).Scan(&current).Error
	return current, err
}
