package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

type GatewayLimit struct {
	DeploymentID  string `gorm:"primaryKey"`
	ID            string `gorm:"primaryKey"`
	Version       int64
	Configuration json.RawMessage `gorm:"type:jsonb"`
	Deleted       bool
	UpdatedAt     time.Time
}

func (s *DeploymentStore) GatewayLimits(ctx context.Context, includeDeleted bool) ([]gatewaypolicy.Limit, error) {
	records := make([]GatewayLimit, 0)
	query := s.db.WithContext(ctx).Where("deployment_id=?", s.deploymentID).Order("id")
	if !includeDeleted {
		query = query.Where("deleted=false")
	}
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]gatewaypolicy.Limit, 0, len(records))
	for _, record := range records {
		var cfg gatewaypolicy.Configuration
		if err := json.Unmarshal(record.Configuration, &cfg); err != nil {
			return nil, err
		}
		items = append(items, gatewaypolicy.Limit{ID: record.ID, Version: record.Version, Configuration: cfg, UpdatedAt: record.UpdatedAt})
	}
	return items, nil
}
