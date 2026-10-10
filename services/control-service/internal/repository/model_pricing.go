package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/modelpricing"
	"gorm.io/gorm/clause"
)

var ErrModelPricingVersion = errors.New("model pricing version conflict")

type ModelPricing struct {
	DeploymentID string          `gorm:"primaryKey" json:"-"`
	ModelID      string          `gorm:"primaryKey" json:"modelId"`
	Version      int64           `json:"version"`
	Pricing      json.RawMessage `gorm:"type:jsonb" json:"pricing"`
	UpdatedAt    *time.Time      `gorm:"autoUpdateTime" json:"updatedAt"`
}

func (ModelPricing) TableName() string { return "model_pricing" }

func (s *DeploymentStore) GetModelPricing(ctx context.Context, modelID string) (ModelPricing, error) {
	var record ModelPricing
	err := s.db.WithContext(ctx).Where("deployment_id=? AND model_id=?", s.deploymentID, modelID).Take(&record).Error
	if errors.Is(err, ErrNotFound) {
		return ModelPricing{ModelID: modelID}, nil
	}
	return record, err
}

func (s *DeploymentStore) PutModelPricing(ctx context.Context, modelID string, pricing json.RawMessage, expectedVersion int64) (ModelPricing, error) {
	var result ModelPricing
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		// Serialize edits on the owning model row, including first writes and deletion.
		var model Model
		if err := tx.db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("deployment_id=? AND id=?", tx.deploymentID, modelID).Take(&model).Error; err != nil {
			return err
		}
		record, err := tx.GetModelPricing(ctx, modelID)
		if err != nil {
			return err
		}
		if record.Version != expectedVersion || record.Version >= modelpricing.MaxVersion {
			return ErrModelPricingVersion
		}
		now := time.Now().UTC()
		record.DeploymentID = tx.deploymentID
		record.ModelID = modelID
		record.Version++
		record.Pricing = pricing
		record.UpdatedAt = &now
		if expectedVersion == 0 {
			err = tx.db.Create(&record).Error
		} else {
			err = tx.db.Model(&ModelPricing{}).Where("deployment_id=? AND model_id=?", tx.deploymentID, modelID).
				Updates(map[string]any{"version": record.Version, "pricing": record.Pricing, "updated_at": now}).Error
		}
		result = record
		return err
	})
	return result, err
}
