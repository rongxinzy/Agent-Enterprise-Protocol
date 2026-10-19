package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// JSONObject is a JSON object column. GORM has no first-party jsonb mapping
// that also works under sqlmock, so the value round-trips through marshaled
// text; PostgreSQL coerces the text parameter on write.
type JSONObject map[string]any

func (v JSONObject) Value() (driver.Value, error) {
	if v == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (v *JSONObject) Scan(src any) error {
	return scanJSON(src, func(decoded map[string]any) { *v = JSONObject(decoded) })
}

// JSONArray is a JSON array column with the same text round-trip.
type JSONArray []any

func (v JSONArray) Value() (driver.Value, error) {
	if v == nil {
		return "[]", nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (v *JSONArray) Scan(src any) error {
	return scanJSON(src, func(decoded []any) { *v = JSONArray(decoded) })
}

func scanJSON[T any](src any, assign func(T)) error {
	var zero T
	switch value := src.(type) {
	case nil:
		assign(zero)
		return nil
	case []byte:
		if len(value) == 0 {
			assign(zero)
			return nil
		}
		var decoded T
		if err := json.Unmarshal(value, &decoded); err != nil {
			return err
		}
		assign(decoded)
		return nil
	case string:
		if value == "" {
			assign(zero)
			return nil
		}
		var decoded T
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return err
		}
		assign(decoded)
		return nil
	default:
		return fmt.Errorf("cannot scan %T into a JSON column", src)
	}
}

// Asset is one managed object in the asset catalog: governance anchor for
// ownership, location, undertakers, and capability exposure. Connection
// parameters live in spec as type-validated JSON; credentials are referenced
// by ID only.
type Asset struct {
	DeploymentID         string     `gorm:"column:deployment_id;primaryKey"`
	ID                   string     `gorm:"column:id;primaryKey"`
	TypeID               string     `gorm:"column:type_id;not null"`
	Name                 string     `gorm:"column:name;not null"`
	Description          pgtype.Text `gorm:"column:description"`
	OrgTeamID            pgtype.Text `gorm:"column:org_team_id"`
	LocationID           pgtype.Text `gorm:"column:location_id"`
	OwnerUserID          pgtype.Text `gorm:"column:owner_user_id"`
	Undertakers          JSONArray `gorm:"column:undertakers"`
	Spec                 JSONObject `gorm:"column:spec"`
	CredentialRefs       JSONObject `gorm:"column:credential_refs"`
	CapabilitiesOverride JSONObject `gorm:"column:capabilities_override"`
	Status               string     `gorm:"column:status;not null"`
	HealthState          pgtype.Text `gorm:"column:health_state"`
	HealthCheckedAt      *time.Time `gorm:"column:health_checked_at"`
	HealthDetail         pgtype.Text `gorm:"column:health_detail"`
	Source               string     `gorm:"column:source;not null"`
	CreatedAt            time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt            time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (Asset) TableName() string { return "assets" }

// AssetBinding grants usage of an asset, optionally narrowed to specific
// capabilities, to a digital employee, companion, user, or team.
type AssetBinding struct {
	DeploymentID        string    `gorm:"column:deployment_id;primaryKey"`
	ID                  string    `gorm:"column:id;primaryKey"`
	AssetID             string    `gorm:"column:asset_id;not null"`
	SubjectType         string    `gorm:"column:subject_type;not null"`
	SubjectID           string    `gorm:"column:subject_id;not null"`
	CapabilityAllowlist JSONArray `gorm:"column:capability_allowlist"`
	GrantedBy           string    `gorm:"column:granted_by;not null"`
	ExpiresAt           *time.Time `gorm:"column:expires_at"`
	CreatedAt           time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (AssetBinding) TableName() string { return "asset_bindings" }

// AssetFilters narrows a page query. Empty values are ignored.
type AssetFilters struct {
	TypeID string
	Status string
}

// UpdateAssetParams replaces only the provided fields; nil leaves the stored
// value untouched. TypeID is deliberately absent: the type is immutable.
type UpdateAssetParams struct {
	Name                 *string
	Description          *pgtype.Text
	OrgTeamID            *pgtype.Text
	LocationID           *pgtype.Text
	OwnerUserID          *pgtype.Text
	Undertakers          *JSONArray
	Spec                 *JSONObject
	CredentialRefs       *JSONObject
	CapabilitiesOverride *JSONObject
	Status               *string
}

func (s *DeploymentStore) ListAssetsPage(ctx context.Context, cursor string, fetchLimit int32, filters AssetFilters) ([]Asset, error) {
	items := make([]Asset, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	if filters.TypeID != "" {
		query = query.Where("type_id = ?", filters.TypeID)
	}
	if filters.Status != "" {
		query = query.Where("status = ?", filters.Status)
	}
	query = query.Order("id")
	if fetchLimit > 0 {
		query = query.Limit(int(fetchLimit))
	}
	err := query.Find(&items).Error
	return items, err
}

func (s *DeploymentStore) GetAsset(ctx context.Context, id string) (Asset, error) {
	var value Asset
	err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&value).Error
	return value, err
}

func (s *DeploymentStore) CreateAsset(ctx context.Context, value Asset) (Asset, error) {
	value.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&value).Error
	return value, err
}

func (s *DeploymentStore) UpdateAsset(ctx context.Context, id string, params UpdateAssetParams) (Asset, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if params.Name != nil {
		updates["name"] = *params.Name
	}
	if params.Description != nil {
		updates["description"] = *params.Description
	}
	if params.OrgTeamID != nil {
		updates["org_team_id"] = *params.OrgTeamID
	}
	if params.LocationID != nil {
		updates["location_id"] = *params.LocationID
	}
	if params.OwnerUserID != nil {
		updates["owner_user_id"] = *params.OwnerUserID
	}
	if params.Undertakers != nil {
		updates["undertakers"] = *params.Undertakers
	}
	if params.Spec != nil {
		updates["spec"] = *params.Spec
	}
	if params.CredentialRefs != nil {
		updates["credential_refs"] = *params.CredentialRefs
	}
	if params.CapabilitiesOverride != nil {
		updates["capabilities_override"] = *params.CapabilitiesOverride
	}
	if params.Status != nil {
		updates["status"] = *params.Status
	}
	result := s.db.WithContext(ctx).Model(&Asset{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).Updates(updates)
	if result.Error != nil {
		return Asset{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Asset{}, ErrNotFound
	}
	return s.GetAsset(ctx, id)
}

func (s *DeploymentStore) DeleteAsset(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&Asset{})
	return resultError(result)
}

func (s *DeploymentStore) ListAssetBindingsPage(ctx context.Context, assetID, cursor string, fetchLimit int32) ([]AssetBinding, error) {
	items := make([]AssetBinding, 0)
	query := s.db.WithContext(ctx).
		Where("deployment_id = ? AND asset_id = ?", s.deploymentID, assetID)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	query = query.Order("id")
	if fetchLimit > 0 {
		query = query.Limit(int(fetchLimit))
	}
	err := query.Find(&items).Error
	return items, err
}

func (s *DeploymentStore) CreateAssetBinding(ctx context.Context, value AssetBinding) (AssetBinding, error) {
	value.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&value).Error
	return value, err
}

func (s *DeploymentStore) GetAssetBinding(ctx context.Context, id string) (AssetBinding, error) {
	var value AssetBinding
	err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&value).Error
	return value, err
}

func (s *DeploymentStore) DeleteAssetBinding(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&AssetBinding{})
	return resultError(result)
}
