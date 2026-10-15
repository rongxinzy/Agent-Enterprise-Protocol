package repository

import (
	"context"
	"errors"
)

var (
	ErrPublicationNotLatest         = errors.New("publication is not latest selected intent")
	ErrPublicationReferencesChanged = errors.New("publication reference withdrawal pending")
)

// ReplayDataPlanePublication copies approved intent, never catalog/rule drafts.
// It uses the same deployment publication -> native lock order as capture and
// invalidation. An idempotent retry must return before all mutable-state reads.
func (s *DeploymentStore) ReplayDataPlanePublication(ctx context.Context, sourceID, keyHash, requestHash, actorID string) (DataPlanePublication, error) {
	var result DataPlanePublication
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		if err := tx.db.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "aep:publication:"+s.deploymentID).Error; err != nil {
			return err
		}
		err := tx.db.Where("deployment_id = ? AND idempotency_key_hash = ?", s.deploymentID, keyHash).Take(&result).Error
		if err == nil {
			if result.RequestHash != requestHash {
				return ErrPublicationKeyConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := tx.db.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,101))`, s.deploymentID).Error; err != nil {
			return err
		}
		source, err := tx.GetDataPlanePublication(ctx, sourceID)
		if err != nil {
			return err
		}
		latest, err := tx.LatestDataPlanePublication(ctx)
		if err != nil {
			return err
		}
		if latest.OperationID != source.OperationID || latest.Sequence != source.Sequence {
			return ErrPublicationNotLatest
		}
		var pending bool
		if err := tx.db.Raw(`SELECT EXISTS(SELECT 1 FROM gateway_reference_invalidations WHERE deployment_id=? AND version>acknowledged_version)`, s.deploymentID).Scan(&pending).Error; err != nil {
			return err
		}
		if pending {
			return ErrPublicationReferencesChanged
		}
		var snapshot PublicationSnapshot
		if err := tx.db.Where("deployment_id = ? AND revision = ? AND content_hash = ?", s.deploymentID, source.Revision, source.ContentHash).Take(&snapshot).Error; err != nil {
			return err
		}
		result, err = tx.insertPublication(ctx, keyHash, requestHash, actorID, snapshot)
		return err
	})
	if err != nil {
		return DataPlanePublication{}, err
	}
	return result, nil
}
