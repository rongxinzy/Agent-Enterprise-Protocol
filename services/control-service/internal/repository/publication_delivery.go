package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrPublicationClaimLost = errors.New("publication delivery claim lost")

// PublicationDelivery contains only immutable references, never Secret values.
// HistoricalRoutes retains distinct definitions in publication order, including models
// removed from the current snapshot, so a newer delivery fences late creates.
type PublicationDelivery struct {
	DataPlanePublication
	ClaimToken       string
	Payload          json.RawMessage
	HistoricalRoutes json.RawMessage
}

func (s *DeploymentStore) ClaimDataPlanePublication(ctx context.Context) (*PublicationDelivery, error) {
	var delivery *PublicationDelivery
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		var locked bool
		if err := tx.db.Raw(`SELECT pg_try_advisory_xact_lock(hashtextextended(?, 0))`, "aep:publication:"+s.deploymentID).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		var latest DataPlanePublication
		err := tx.db.Where("deployment_id = ?", s.deploymentID).Order("sequence DESC").Take(&latest).Error
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		// A newer full snapshot replaces older undelivered intents, even when
		// the older worker is still running. Its claim cannot acknowledge later.
		if err := tx.db.Exec(`UPDATE data_plane_publications SET phase='superseded', updated_at=now()
WHERE deployment_id=? AND sequence<? AND phase IN ('queued','projecting')`, s.deploymentID, latest.Sequence).Error; err != nil {
			return err
		}
		if err := tx.db.Exec(`UPDATE data_plane_publication_outbox o SET completed_at=now(),claim_token=NULL,lease_until=NULL
FROM data_plane_publications p WHERE o.deployment_id=p.deployment_id AND o.operation_id=p.operation_id
AND p.deployment_id=? AND p.phase='superseded' AND o.completed_at IS NULL`, s.deploymentID).Error; err != nil {
			return err
		}
		if latest.Phase != "queued" && latest.Phase != "projecting" {
			return nil
		}
		token := uuid.NewString()
		result := tx.db.Exec(`UPDATE data_plane_publication_outbox SET claim_token=?,lease_until=now()+interval '30 seconds',attempts=attempts+1
WHERE deployment_id=? AND operation_id=? AND completed_at IS NULL AND next_attempt_at<=now()
AND (lease_until IS NULL OR lease_until<=now())`, token, s.deploymentID, latest.OperationID)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if err := tx.db.Exec(`UPDATE data_plane_publications SET phase='projecting',updated_at=now()
WHERE deployment_id=? AND operation_id=?`, s.deploymentID, latest.OperationID).Error; err != nil {
			return err
		}
		item := &PublicationDelivery{DataPlanePublication: latest, ClaimToken: token}
		var snapshot struct{ Payload json.RawMessage }
		if err := tx.db.Raw(`SELECT payload FROM data_plane_publication_snapshots WHERE deployment_id=? AND revision=?`, s.deploymentID, latest.Revision).Scan(&snapshot).Error; err != nil {
			return err
		}
		item.Payload = snapshot.Payload
		var history struct{ HistoricalRoutes json.RawMessage }
		// Keep older valid definitions even if a newer snapshot was invalid:
		// otherwise deletion could omit a tombstone for an already created CR.
		// PostgreSQL deduplicates immutable definitions; no mutable catalog reads
		// or credential resolution happen here.
		if err := tx.db.Raw(`SELECT COALESCE(jsonb_agg(route ORDER BY sequence,route->>'modelId'),'[]'::jsonb) AS historical_routes FROM (
 SELECT DISTINCT ON (route) route,p.sequence
 FROM data_plane_publications p JOIN data_plane_publication_snapshots s USING (deployment_id,revision,content_hash)
 CROSS JOIN LATERAL jsonb_array_elements(s.payload->'routes') AS route
 WHERE p.deployment_id=? AND p.sequence<=? ORDER BY route,p.sequence DESC
) history`, s.deploymentID, latest.Sequence).Scan(&history).Error; err != nil {
			return err
		}
		item.HistoricalRoutes = history.HistoricalRoutes
		item.Phase = "projecting"
		delivery = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return delivery, nil
}

// FinishDataPlanePublication atomically acknowledges only the unexpired owner
// of the latest intent. A crash after CR writes leaves a recoverable claim.
// Empty phase schedules a transient retry; terminal rows remain queryable.
func (s *DeploymentStore) FinishDataPlanePublication(ctx context.Context, operationID, token, phase, code string, retryAfter time.Duration) error {
	if phase != "" && phase != "projected" && phase != "failed" && phase != "superseded" {
		return errors.New("invalid publication completion phase")
	}
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		result := tx.db.Exec(`UPDATE data_plane_publication_outbox o SET claim_token=NULL,lease_until=NULL,
 next_attempt_at=now()+(? * interval '1 millisecond'),completed_at=CASE WHEN ?='' THEN NULL ELSE now() END
FROM data_plane_publications p WHERE o.deployment_id=p.deployment_id AND o.operation_id=p.operation_id
AND o.deployment_id=? AND o.operation_id=? AND o.claim_token=? AND o.lease_until>now()
AND o.completed_at IS NULL AND p.phase='projecting'
AND NOT EXISTS (SELECT 1 FROM data_plane_publications newer WHERE newer.deployment_id=p.deployment_id AND newer.sequence>p.sequence)`,
			retryAfter.Milliseconds(), phase, s.deploymentID, operationID, token)
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
		return tx.db.Exec(`UPDATE data_plane_publications SET phase=?,error_code=?,updated_at=now() WHERE deployment_id=? AND operation_id=?`, phase, errorCode, s.deploymentID, operationID).Error
	})
}
