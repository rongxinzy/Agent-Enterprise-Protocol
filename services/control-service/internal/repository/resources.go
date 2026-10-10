package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StringArray []string

func (values *StringArray) Scan(source any) error {
	if source == nil {
		*values = nil
		return nil
	}
	var encoded []byte
	switch value := source.(type) {
	case []string:
		*values = append((*values)[:0], value...)
		return nil
	case string:
		encoded = []byte(value)
	case []byte:
		encoded = value
	default:
		return fmt.Errorf("scan PostgreSQL text array from %T", source)
	}
	var decoded []string
	var format int16 = pgtype.BinaryFormatCode
	if len(encoded) == 0 || encoded[0] == '{' {
		format = pgtype.TextFormatCode
	}
	if err := pgtype.NewMap().Scan(pgtype.TextArrayOID, format, encoded, &decoded); err != nil {
		return err
	}
	*values = decoded
	return nil
}

func (values StringArray) Value() (driver.Value, error) {
	encoded, err := pgtype.NewMap().Encode(pgtype.TextArrayOID, pgtype.TextFormatCode, []string(values), nil)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

type Skill struct {
	ID          string    `gorm:"column:id;primaryKey"`
	Name        string    `gorm:"column:name;not null"`
	Description string    `gorm:"column:description;not null"`
	Enabled     bool      `gorm:"column:enabled;not null"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (Skill) TableName() string { return "skills" }

type SkillVersion struct {
	SkillID     string     `gorm:"column:skill_id;primaryKey"`
	Version     string     `gorm:"column:version;primaryKey"`
	ObjectKey   string     `gorm:"column:object_key;not null"`
	SHA256      string     `gorm:"column:sha256;not null"`
	SizeBytes   int64      `gorm:"column:size_bytes;not null"`
	Published   bool       `gorm:"column:published;not null"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	PublishedAt *time.Time `gorm:"column:published_at"`
}

func (SkillVersion) TableName() string { return "skill_versions" }

type SkillAssignment struct {
	ID           string     `gorm:"column:id;primaryKey"`
	DeploymentID string     `gorm:"column:deployment_id;not null"`
	SkillID      string     `gorm:"column:skill_id;not null"`
	SubjectType  string     `gorm:"column:subject_type;not null"`
	SubjectID    string     `gorm:"column:subject_id;not null"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

func (SkillAssignment) TableName() string { return "skill_assignments" }

type Credential struct {
	DeploymentID   string    `gorm:"column:deployment_id;primaryKey"`
	ID             string    `gorm:"column:id;primaryKey"`
	Name           string    `gorm:"column:name;not null"`
	Service        string    `gorm:"column:service;not null"`
	Type           string    `gorm:"column:type;not null"`
	DeliveryMode   string    `gorm:"column:delivery_mode;not null"`
	EncryptedValue []byte    `gorm:"column:encrypted_value;not null"`
	Nonce          []byte    `gorm:"column:nonce;not null"`
	KeyID          string    `gorm:"column:key_id;not null"`
	MaskedValue    string    `gorm:"column:masked_value;not null"`
	Enabled        bool      `gorm:"column:enabled;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime"`
	RotatedAt      time.Time `gorm:"column:rotated_at;autoCreateTime"`
}

func (Credential) TableName() string { return "credentials" }

type CredentialAssignment struct {
	ID           string     `gorm:"column:id;primaryKey"`
	DeploymentID string     `gorm:"column:deployment_id;not null"`
	CredentialID string     `gorm:"column:credential_id;not null"`
	SubjectType  string     `gorm:"column:subject_type;not null"`
	SubjectID    string     `gorm:"column:subject_id;not null"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

func (CredentialAssignment) TableName() string { return "credential_assignments" }

type Model struct {
	DeploymentID           string             `gorm:"column:deployment_id;primaryKey"`
	ID                     string             `gorm:"column:id;primaryKey"`
	DisplayName            string             `gorm:"column:display_name;not null"`
	SourceType             string             `gorm:"column:source_type;not null"`
	Protocol               string             `gorm:"column:protocol;not null"`
	Endpoint               pgtype.Text        `gorm:"column:endpoint"`
	UpstreamModel          pgtype.Text        `gorm:"column:upstream_model"`
	LocalModelRef          pgtype.Text        `gorm:"column:local_model_ref"`
	CredentialID           pgtype.Text        `gorm:"column:credential_id"`
	Capabilities           StringArray        `gorm:"column:capabilities;type:text[]"`
	ReasoningCompatibility json.RawMessage    `gorm:"column:reasoning_compatibility;type:jsonb"`
	ContextWindow          pgtype.Int4        `gorm:"column:context_window"`
	IsDefault              bool               `gorm:"column:is_default;not null"`
	Enabled                bool               `gorm:"column:enabled;not null"`
	HealthStatus           string             `gorm:"column:health_status;not null;default:'unknown'"`
	HealthCheckedAt        pgtype.Timestamptz `gorm:"column:health_checked_at"`
	HealthSince            pgtype.Timestamptz `gorm:"column:health_since"`
	HealthDetail           pgtype.Text        `gorm:"column:health_detail"`
	CreatedAt              time.Time          `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt              time.Time          `gorm:"column:updated_at;autoUpdateTime"`
}

func (Model) TableName() string { return "models" }

type ModelAssignment struct {
	ID           string     `gorm:"column:id;primaryKey"`
	DeploymentID string     `gorm:"column:deployment_id;not null"`
	ModelID      string     `gorm:"column:model_id;not null"`
	SubjectType  string     `gorm:"column:subject_type;not null"`
	SubjectID    string     `gorm:"column:subject_id;not null"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

func (ModelAssignment) TableName() string { return "model_assignments" }

type UpdateSkillParams struct {
	Name        *string
	Description *string
	Enabled     *bool
}

type UpdateCredentialParams struct {
	Name         *string
	Service      *string
	DeliveryMode *string
	Enabled      *bool
}

func (s *Store) ListSkills(ctx context.Context) ([]Skill, error) {
	return s.ListSkillsPage(ctx, "", 0)
}

func (s *Store) ListSkillsPage(ctx context.Context, cursor string, fetchLimit int32) ([]Skill, error) {
	items := make([]Skill, 0)
	query := s.db.WithContext(ctx)
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

func (s *Store) ListSkillVersions(ctx context.Context, skillID string) ([]SkillVersion, error) {
	items := make([]SkillVersion, 0)
	err := s.db.WithContext(ctx).
		Where("skill_id = ?", skillID).
		Order("created_at DESC, version DESC").
		Find(&items).Error
	return items, err
}

func (s *Store) CreateSkill(ctx context.Context, skill Skill) (Skill, error) {
	err := s.db.WithContext(ctx).Create(&skill).Error
	return skill, err
}

func (s *Store) GetSkill(ctx context.Context, id string) (Skill, error) {
	var skill Skill
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&skill).Error
	return skill, err
}

func (s *Store) UpdateSkill(ctx context.Context, id string, params UpdateSkillParams) (Skill, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if params.Name != nil {
		updates["name"] = *params.Name
	}
	if params.Description != nil {
		updates["description"] = *params.Description
	}
	if params.Enabled != nil {
		updates["enabled"] = *params.Enabled
	}
	result := s.db.WithContext(ctx).Model(&Skill{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return Skill{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Skill{}, ErrNotFound
	}
	return s.GetSkill(ctx, id)
}

// SkillReferenceCounts is the usage that blocks deleting a Skill: the digital
// employee prompt binding (which the foreign key would silently SET NULL) and
// the Skill assignments (which would be cascade deleted).
type SkillReferenceCounts struct {
	PromptBindings int64
	Assignments    int64
}

// DeleteSkill removes a Skill. It locks the Skill row for the transaction so a
// concurrent assignment or agent-profile write cannot slip in between the check
// and the delete (PostgreSQL makes such writers take FOR KEY SHARE on this
// row). Unless force is set it refuses while references remain and returns
// ErrSkillInUse with the counts - the prompt Skill binding would otherwise be
// SET NULL and the assignments cascade deleted. With force it deletes anyway,
// accepting that cascade. A missing Skill returns ErrNotFound.
func (s *Store) DeleteSkill(ctx context.Context, id string, force bool) (SkillReferenceCounts, error) {
	var counts SkillReferenceCounts
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var skill Skill
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&skill).Error; err != nil {
			return err
		}
		if !force {
			if err := tx.Model(&AgentProfile{}).Where("prompt_skill_id = ?", id).Count(&counts.PromptBindings).Error; err != nil {
				return err
			}
			if err := tx.Model(&SkillAssignment{}).Where("skill_id = ?", id).Count(&counts.Assignments).Error; err != nil {
				return err
			}
			if counts.PromptBindings > 0 || counts.Assignments > 0 {
				return ErrSkillInUse
			}
		}
		return tx.Where("id = ?", id).Delete(&Skill{}).Error
	})
	return counts, err
}

// UpsertSkillVersion stores a version's package. The version number is the key,
// so re-uploading an existing version replaces its content while keeping its
// publication state - a published version stays published, a draft stays a
// draft. It returns whether the stored version is published.
func (s *Store) UpsertSkillVersion(ctx context.Context, version SkillVersion) (bool, error) {
	var published bool
	err := s.db.WithContext(ctx).Raw(`
INSERT INTO skill_versions (skill_id,version,object_key,sha256,size_bytes,published,published_at)
VALUES (?,?,?,?,?,false,NULL)
ON CONFLICT (skill_id,version) DO UPDATE SET
  object_key=EXCLUDED.object_key,sha256=EXCLUDED.sha256,size_bytes=EXCLUDED.size_bytes
RETURNING published`, version.SkillID, version.Version, version.ObjectKey, version.SHA256, version.SizeBytes).Scan(&published).Error
	return published, err
}

func (s *Store) PublishSkillVersion(ctx context.Context, skillID, version string) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&SkillVersion{}).
		Where("skill_id = ? AND version = ?", skillID, version).
		Updates(map[string]any{"published": true, "published_at": now})
	return resultError(result)
}

func (s *Store) DeleteSkillVersion(ctx context.Context, skillID, version string) (string, error) {
	var item SkillVersion
	if err := s.db.WithContext(ctx).Where("skill_id = ? AND version = ?", skillID, version).Take(&item).Error; err != nil {
		return "", err
	}
	result := s.db.WithContext(ctx).Where("skill_id = ? AND version = ?", skillID, version).Delete(&SkillVersion{})
	if err := resultError(result); err != nil {
		return "", err
	}
	return item.ObjectKey, nil
}

func (s *DeploymentStore) ListSkillAssignments(ctx context.Context) ([]SkillAssignment, error) {
	items := make([]SkillAssignment, 0)
	err := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID).Order("id").Find(&items).Error
	return items, err
}

func (s *DeploymentStore) ListCredentials(ctx context.Context) ([]Credential, error) {
	return s.ListCredentialsPage(ctx, "", 0)
}

func (s *DeploymentStore) ListCredentialsPage(ctx context.Context, cursor string, fetchLimit int32) ([]Credential, error) {
	items := make([]Credential, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID)
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

func (s *DeploymentStore) CreateCredential(ctx context.Context, value Credential) (Credential, error) {
	value.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&value).Error
	return value, err
}

func (s *DeploymentStore) GetCredential(ctx context.Context, id string) (Credential, error) {
	var value Credential
	err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&value).Error
	return value, err
}

func (s *DeploymentStore) HasCredential(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&Credential{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).Count(&count).Error
	return count == 1, err
}

func (s *DeploymentStore) UpdateCredential(ctx context.Context, id string, params UpdateCredentialParams) (Credential, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if params.Name != nil {
		updates["name"] = *params.Name
	}
	if params.Service != nil {
		updates["service"] = *params.Service
	}
	if params.DeliveryMode != nil {
		updates["delivery_mode"] = *params.DeliveryMode
	}
	if params.Enabled != nil {
		updates["enabled"] = *params.Enabled
	}
	result := s.db.WithContext(ctx).Model(&Credential{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).Updates(updates)
	if result.Error != nil {
		return Credential{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Credential{}, ErrNotFound
	}
	return s.GetCredential(ctx, id)
}

func (s *DeploymentStore) RotateCredential(ctx context.Context, id string, encryptedValue, nonce []byte, keyID, maskedValue string) (Credential, error) {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Credential{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Updates(map[string]any{
			"encrypted_value": encryptedValue, "nonce": nonce, "key_id": keyID,
			"masked_value": maskedValue, "rotated_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return Credential{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Credential{}, ErrNotFound
	}
	return s.GetCredential(ctx, id)
}

func (s *DeploymentStore) DeleteCredential(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&Credential{})
	return resultError(result)
}

func (s *DeploymentStore) ListCredentialAssignments(ctx context.Context) ([]CredentialAssignment, error) {
	items := make([]CredentialAssignment, 0)
	err := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID).Order("created_at, id").Find(&items).Error
	return items, err
}

func (s *DeploymentStore) CreateCredentialAssignment(ctx context.Context, value CredentialAssignment) (CredentialAssignment, error) {
	value.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&value).Error
	return value, err
}

func (s *DeploymentStore) DeleteCredentialAssignment(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&CredentialAssignment{})
	return resultError(result)
}

func (s *DeploymentStore) ListModels(ctx context.Context) ([]Model, error) {
	return s.ListModelsPage(ctx, "", 0)
}

func (s *DeploymentStore) ListModelsPage(ctx context.Context, cursor string, fetchLimit int32) ([]Model, error) {
	items := make([]Model, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID)
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

func (s *DeploymentStore) ListEnabledModelsByIDs(ctx context.Context, ids []string) ([]Model, error) {
	items := make([]Model, 0)
	if len(ids) == 0 {
		return items, nil
	}
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND enabled = ? AND id IN ?", s.deploymentID, true, ids).
		Order("is_default DESC, id").Find(&items).Error
	return items, err
}

func (s *DeploymentStore) GetModel(ctx context.Context, id string) (Model, error) {
	var value Model
	err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&value).Error
	return value, err
}

func (s *DeploymentStore) HasModel(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&Model{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).Count(&count).Error
	return count == 1, err
}

func (s *DeploymentStore) DeleteModel(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&Model{})
	return resultError(result)
}

func (s *DeploymentStore) ListModelAssignments(ctx context.Context) ([]ModelAssignment, error) {
	items := make([]ModelAssignment, 0)
	err := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID).Order("created_at, id").Find(&items).Error
	return items, err
}

func (s *DeploymentStore) CreateModelAssignment(ctx context.Context, value ModelAssignment) (ModelAssignment, error) {
	value.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&value).Error
	return value, err
}

func (s *DeploymentStore) DeleteModelAssignment(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&ModelAssignment{})
	return resultError(result)
}
