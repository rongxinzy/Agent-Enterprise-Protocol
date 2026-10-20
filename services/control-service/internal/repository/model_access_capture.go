package repository

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

var (
	ErrAccessLegacyGrants = errors.New("legacy direct model grants require migration before access cutover")
	ErrAccessBinding      = errors.New("model access binding cannot be safely captured")
	ErrAccessExpiring     = errors.New("model access source is too close to expiry")
)

type ModelAccessCapture struct {
	SubjectID     string
	SourceVersion int64
	Publications  []ModelAccessPublication
	NextCaptureAt *time.Time
}

// PendingModelAccessSubjects is a bounded, resumable discovery query for the
// existing relay. Discovery does not claim/ack work. Callers must handle each
// subject independently so an incompatible legacy grant cannot starve others.
func (s *DeploymentStore) PendingModelAccessSubjects(ctx context.Context, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 256 {
		return nil, ErrAccessPublicationInput
	}
	ids := []string{}
	err := s.db.WithContext(ctx).Raw(`SELECT user_id FROM model_access_changes
WHERE deployment_id=? AND user_id>? AND (version>acknowledged_version OR next_capture_at<=clock_timestamp())
ORDER BY user_id LIMIT ?`, s.deploymentID, after, limit).Scan(&ids).Error
	return ids, err
}

// CaptureModelAccessSessions captures every active/prior session binding and
// acknowledges exactly that subject's source version in one DB transaction.
// It is deliberately not enabled on the default path. Kubernetes workload
// bindings need the trusted lifecycle mapping stage: encountering one refuses
// the whole capture, rather than acknowledging incomplete binding coverage.
// Freshness is an explicit reviewed bound, not a cache TTL or a cache flush.
func (s *DeploymentStore) CaptureModelAccessSessions(ctx context.Context, userID string, freshness time.Duration) (*ModelAccessCapture, error) {
	if freshness < 2*time.Second || freshness > time.Hour || freshness%time.Second != 0 {
		return nil, ErrAccessPublicationInput
	}
	var capture *ModelAccessCapture
	err := s.WithModelAccessSource(ctx, userID, func(tx *DeploymentStore, source ModelAccessSource) error {
		var change struct {
			Version, AcknowledgedVersion int64
			NextCaptureAt                *time.Time
		}
		result := tx.db.Raw(`SELECT version,acknowledged_version,next_capture_at FROM model_access_changes
WHERE deployment_id=? AND user_id=? FOR UPDATE`, s.deploymentID, userID).Scan(&change)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		due := change.NextCaptureAt != nil && !change.NextCaptureAt.After(source.ObservedAt)
		if change.Version == change.AcknowledgedVersion && !due {
			return nil
		}
		if source.LegacyDirectGrants && !source.Disabled {
			return ErrAccessLegacyGrants
		}
		if change.Version == change.AcknowledgedVersion {
			change.Version++
			if err := tx.db.Exec(`UPDATE model_access_changes SET version=? WHERE deployment_id=? AND user_id=?`, change.Version, s.deploymentID, userID).Error; err != nil {
				return err
			}
		}
		prior := []ModelAccessPublication{}
		if err := tx.db.Raw(`SELECT p.* FROM model_access_streams s JOIN model_access_publications p
ON p.deployment_id=s.deployment_id AND p.binding_key=s.binding_key AND p.sequence=s.last_sequence
WHERE s.deployment_id=? AND s.subject_id=? ORDER BY s.binding_key`, s.deploymentID, userID).Scan(&prior).Error; err != nil {
			return err
		}
		intents, err := sessionAccessIntents(source, prior, change.Version, freshness, due)
		if err != nil {
			return err
		}
		value := &ModelAccessCapture{SubjectID: userID, SourceVersion: change.Version, Publications: []ModelAccessPublication{}}
		for _, intent := range intents {
			publication, err := tx.QueueModelAccessPublication(ctx, intent)
			if err != nil {
				return err
			}
			value.Publications = append(value.Publications, publication)
			policy, err := gatewayaccess.Decode(publication.Payload, publication.ContentHash)
			if err != nil {
				return err
			}
			if !policy.Disabled && len(policy.Models) > 0 {
				// Refresh only the reviewed freshness cap, not a shorter natural
				// model/session/license deadline. At a natural deadline rederive.
				next := publication.ValidUntil
				if publication.ValidUntil.Equal(publication.IssuedAt.Add(freshness)) {
					next = publication.IssuedAt.Add(freshness / 2)
				}
				if value.NextCaptureAt == nil || next.Before(*value.NextCaptureAt) {
					value.NextCaptureAt = &next
				}
			}
		}
		ack := tx.db.Exec(`UPDATE model_access_changes SET acknowledged_version=?,next_capture_at=?
WHERE deployment_id=? AND user_id=? AND version=?`, change.Version, value.NextCaptureAt, s.deploymentID, userID, change.Version)
		if ack.Error != nil {
			return ack.Error
		}
		if ack.RowsAffected != 1 {
			return ErrAccessPublicationStale
		}
		capture = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	return capture, nil
}

func sortedAccessKeys(values map[string]gatewayaccess.Snapshot) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
