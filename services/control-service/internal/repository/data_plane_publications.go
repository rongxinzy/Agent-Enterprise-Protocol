package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

var (
	ErrPublicationKeyConflict      = errors.New("publication key is bound to another request")
	ErrPublicationRevisionConflict = errors.New("publication revision is bound to another snapshot")
)

// PublicationSnapshot is the normalized revision+routes produced by the HTTP
// layer's shared catalog derivation. Payload never contains resolved secrets.
type PublicationSnapshot struct {
	DeploymentID string `gorm:"primaryKey"`
	Revision     string `gorm:"primaryKey"`
	ContentHash  string
	Payload      json.RawMessage `gorm:"type:jsonb"`
}

func (PublicationSnapshot) TableName() string { return "data_plane_publication_snapshots" }

type DataPlanePublication struct {
	DeploymentID         string `gorm:"primaryKey"`
	OperationID          string `gorm:"primaryKey;type:uuid"`
	Sequence             int64  `gorm:"autoIncrement"`
	IdempotencyKeyHash   string
	RequestHash          string
	Revision             string
	ContentHash          string
	Phase                string
	ErrorCode            *string
	CreatedAt            time.Time `gorm:"autoCreateTime:false;default:now()"`
	UpdatedAt            time.Time `gorm:"autoUpdateTime:false;default:now()"`
	IdempotencyExpiresAt time.Time `gorm:"default:(now() + interval '24 hours')"`
}

func (DataPlanePublication) TableName() string { return "data_plane_publications" }

type publicationOutbox struct {
	DeploymentID string `gorm:"primaryKey"`
	OperationID  string `gorm:"primaryKey;type:uuid"`
}

func (publicationOutbox) TableName() string { return "data_plane_publication_outbox" }

// QueueDataPlanePublication commits the first snapshot, operation, audit and
// outbox together. The short deployment-scoped advisory transaction lock orders
// concurrent publication requests (including requests with different keys).
// Derivation reads the catalog once, through this transaction, only after the
// replay lookup. No Kubernetes/network calls are allowed in derive.
func (s *DeploymentStore) QueueDataPlanePublication(ctx context.Context, keyHash, requestHash, actorID string, derive func([]Model) (PublicationSnapshot, error)) (DataPlanePublication, error) {
	var result DataPlanePublication
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		if err := tx.db.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, "aep:publication:"+s.deploymentID).Error; err != nil {
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
		models, err := tx.ListModels(ctx)
		if err != nil {
			return err
		}
		snapshot, err := derive(models)
		if err != nil {
			return err
		}
		snapshot.DeploymentID = s.deploymentID
		if err := tx.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&snapshot).Error; err != nil {
			return err
		}
		var existing PublicationSnapshot
		if err := tx.db.Select("content_hash").Where("deployment_id = ? AND revision = ?", s.deploymentID, snapshot.Revision).Take(&existing).Error; err != nil {
			return err
		}
		if existing.ContentHash != snapshot.ContentHash {
			return ErrPublicationRevisionConflict
		}
		result = DataPlanePublication{
			DeploymentID: s.deploymentID, OperationID: uuid.NewString(),
			IdempotencyKeyHash: keyHash, RequestHash: requestHash,
			Revision: snapshot.Revision, ContentHash: snapshot.ContentHash, Phase: "queued",
		}
		if err := tx.db.Create(&result).Error; err != nil {
			return err
		}
		if err := tx.RecordAdminAudit(ctx, AdminAuditEntry{
			ActorUserID: actorID, Action: "publish", ResourceType: "data_plane_publication", ResourceID: result.OperationID, Result: "success",
			Changes: map[string]any{"after": map[string]any{"revision": result.Revision}},
		}); err != nil {
			return err
		}
		return tx.db.Create(&publicationOutbox{DeploymentID: s.deploymentID, OperationID: result.OperationID}).Error
	})
	if err != nil {
		return DataPlanePublication{}, err
	}
	return result, nil
}

func (s *DeploymentStore) GetDataPlanePublication(ctx context.Context, operationID string) (DataPlanePublication, error) {
	var result DataPlanePublication
	err := s.db.WithContext(ctx).Where("deployment_id = ? AND operation_id = ?", s.deploymentID, operationID).Take(&result).Error
	return result, err
}
