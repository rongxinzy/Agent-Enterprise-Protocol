package repository

import (
	"context"
	"encoding/json"
	"time"
)

// AgentProfile carries digital-employee presentation metadata for a user with
// kind='agent'. Presence is never stored here; it derives from user_sessions.
type AgentProfile struct {
	DeploymentID    string    `gorm:"column:deployment_id;primaryKey"`
	UserID          string    `gorm:"column:user_id;primaryKey"`
	DisplayTitle    string    `gorm:"column:display_title;not null"`
	Description     string    `gorm:"column:description;not null"`
	AvatarObjectKey *string   `gorm:"column:avatar_object_key"`
	HomeTeamID      string    `gorm:"column:home_team_id;not null"`
	PromptSkillID   *string   `gorm:"column:prompt_skill_id"`
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (AgentProfile) TableName() string { return "agent_profiles" }

// AgentDirectoryEntry is one row of the admin agent directory: the service
// account joined with its profile and last observed heartbeat.
type AgentDirectoryEntry struct {
	User            User
	Profile         *AgentProfile
	LastHeartbeatAt *time.Time
	Online          bool
}

type IdentitySource struct {
	DeploymentID string          `gorm:"column:deployment_id;primaryKey"`
	ID           string          `gorm:"column:id;primaryKey"`
	Kind         string          `gorm:"column:kind;not null"`
	DisplayName  string          `gorm:"column:display_name;not null"`
	Config       json.RawMessage `gorm:"column:config;type:jsonb"`
	Enabled      bool            `gorm:"column:enabled;not null"`
	CreatedAt    time.Time       `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time       `gorm:"column:updated_at;autoUpdateTime"`
}

func (IdentitySource) TableName() string { return "identity_sources" }

type IdentityMapping struct {
	DeploymentID         string     `gorm:"column:deployment_id;primaryKey"`
	SourceID             string     `gorm:"column:source_id;primaryKey"`
	ExternalSubjectType  string     `gorm:"column:external_subject_type;primaryKey"`
	ExternalID           string     `gorm:"column:external_id;primaryKey"`
	LocalSubjectID       string     `gorm:"column:local_subject_id;not null"`
	Status               string     `gorm:"column:status;not null"`
	LinkedAt             time.Time  `gorm:"column:linked_at;autoCreateTime"`
	LastSyncedAt         *time.Time `gorm:"column:last_synced_at"`
}

func (IdentityMapping) TableName() string { return "identity_mappings" }

type DataScopeRule struct {
	ID           string     `gorm:"column:id;primaryKey"`
	DeploymentID string     `gorm:"column:deployment_id;not null"`
	RuleKind     string     `gorm:"column:rule_kind;not null"`
	SubjectType  string     `gorm:"column:subject_type;not null"`
	SubjectID    string     `gorm:"column:subject_id;not null"`
	ResourceKind string     `gorm:"column:resource_kind;not null"`
	ResourceID   string     `gorm:"column:resource_id;not null"`
	Priority     int        `gorm:"column:priority;not null"`
	StartsAt     *time.Time `gorm:"column:starts_at"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	Reason       string     `gorm:"column:reason;not null"`
	CreatedBy    string     `gorm:"column:created_by;not null"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (DataScopeRule) TableName() string { return "data_scope_rules" }

func (s *DeploymentStore) UpsertAgentProfile(ctx context.Context, profile AgentProfile) error {
	profile.DeploymentID = s.deploymentID
	return s.db.WithContext(ctx).Save(&profile).Error
}

func (s *DeploymentStore) GetAgentProfile(ctx context.Context, userID string) (AgentProfile, error) {
	var profile AgentProfile
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND user_id = ?", s.deploymentID, userID).
		Take(&profile).Error
	return profile, err
}

// ListAgentDirectory joins service accounts (kind='agent') with their profile
// and the freshest active-session heartbeat for presence projection.
func (s *DeploymentStore) ListAgentDirectory(ctx context.Context) ([]AgentDirectoryEntry, error) {
	rows, err := s.db.WithContext(ctx).Raw(`
SELECT u.id, u.username, u.display_name, u.status, u.kind,
       p.display_title, p.description, p.avatar_object_key, p.home_team_id, p.prompt_skill_id,
       p.created_at, p.updated_at,
       presence.last_seen_at
FROM users u
LEFT JOIN agent_profiles p ON p.deployment_id = u.deployment_id AND p.user_id = u.id
LEFT JOIN LATERAL (
  SELECT MAX(s2.last_seen_at) AS last_seen_at
  FROM user_sessions s2
  WHERE s2.deployment_id = u.deployment_id AND s2.user_id = u.id AND s2.revoked_at IS NULL
) presence ON true
WHERE u.deployment_id = ? AND u.kind = 'agent'
ORDER BY u.id`, s.deploymentID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]AgentDirectoryEntry, 0)
	for rows.Next() {
		var entry AgentDirectoryEntry
		var displayName, profileTitle, profileDescription *string
		var avatarObjectKey, promptSkillID, homeTeamID *string
		var profileCreatedAt, profileUpdatedAt *time.Time
		if err := rows.Scan(
			&entry.User.ID, &entry.User.Username, &displayName, &entry.User.Status, &entry.User.Kind,
			&profileTitle, &profileDescription, &avatarObjectKey, &homeTeamID, &promptSkillID,
			&profileCreatedAt, &profileUpdatedAt,
			&entry.LastHeartbeatAt,
		); err != nil {
			return nil, err
		}
		entry.User.DeploymentID = s.deploymentID
		if displayName != nil {
			entry.User.DisplayName = *displayName
		}
		if homeTeamID != nil {
			entry.Profile = &AgentProfile{
				DeploymentID: s.deploymentID, UserID: entry.User.ID,
				DisplayTitle: deref(profileTitle), Description: deref(profileDescription),
				AvatarObjectKey: avatarObjectKey, HomeTeamID: *homeTeamID, PromptSkillID: promptSkillID,
				CreatedAt: derefTime(profileCreatedAt), UpdatedAt: derefTime(profileUpdatedAt),
			}
		}
		entry.Online = entry.LastHeartbeatAt != nil && time.Since(*entry.LastHeartbeatAt) < 5*time.Minute
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func (s *DeploymentStore) CreateIdentitySource(ctx context.Context, source IdentitySource) (IdentitySource, error) {
	source.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&source).Error
	return source, err
}

func (s *DeploymentStore) ListIdentitySources(ctx context.Context) ([]IdentitySource, error) {
	sources := make([]IdentitySource, 0)
	err := s.db.WithContext(ctx).
		Where("deployment_id = ?", s.deploymentID).Order("id").Find(&sources).Error
	return sources, err
}

func (s *DeploymentStore) GetIdentitySource(ctx context.Context, id string) (IdentitySource, error) {
	var source IdentitySource
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&source).Error
	return source, err
}

func (s *DeploymentStore) UpdateIdentitySource(ctx context.Context, id string, displayName *string, config json.RawMessage, enabled *bool) (IdentitySource, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if displayName != nil {
		updates["display_name"] = *displayName
	}
	if config != nil {
		updates["config"] = config
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if err := s.db.WithContext(ctx).Model(&IdentitySource{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Updates(updates).Error; err != nil {
		return IdentitySource{}, err
	}
	return s.GetIdentitySource(ctx, id)
}

func (s *DeploymentStore) DeleteIdentitySource(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Delete(&IdentitySource{}).Error
}

func (s *DeploymentStore) UpsertIdentityMapping(ctx context.Context, mapping IdentityMapping) error {
	mapping.DeploymentID = s.deploymentID
	if mapping.Status == "" {
		mapping.Status = "active"
	}
	return s.db.WithContext(ctx).Save(&mapping).Error
}

func (s *DeploymentStore) ListIdentityMappings(ctx context.Context, sourceID, subjectType string) ([]IdentityMapping, error) {
	mappings := make([]IdentityMapping, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ? AND source_id = ?", s.deploymentID, sourceID)
	if subjectType != "" {
		query = query.Where("external_subject_type = ?", subjectType)
	}
	err := query.Order("external_id").Find(&mappings).Error
	return mappings, err
}

func (s *DeploymentStore) DeleteIdentityMapping(ctx context.Context, sourceID, subjectType, externalID string) error {
	return s.db.WithContext(ctx).
		Where("deployment_id = ? AND source_id = ? AND external_subject_type = ? AND external_id = ?",
			s.deploymentID, sourceID, subjectType, externalID).
		Delete(&IdentityMapping{}).Error
}

func (s *DeploymentStore) CreateDataScopeRule(ctx context.Context, rule DataScopeRule) (DataScopeRule, error) {
	rule.DeploymentID = s.deploymentID
	err := s.db.WithContext(ctx).Create(&rule).Error
	return rule, err
}

func (s *DeploymentStore) ListDataScopeRules(ctx context.Context, fetchLimit int32) ([]DataScopeRule, error) {
	rules := make([]DataScopeRule, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID).Order("rule_kind, subject_type, subject_id, resource_kind, resource_id")
	if fetchLimit > 0 {
		query = query.Limit(int(fetchLimit))
	}
	err := query.Find(&rules).Error
	return rules, err
}

func (s *DeploymentStore) GetDataScopeRule(ctx context.Context, id string) (DataScopeRule, error) {
	var rule DataScopeRule
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&rule).Error
	return rule, err
}

func (s *DeploymentStore) DeleteDataScopeRule(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Delete(&DataScopeRule{}).Error
}

// DeleteExpiredAssignments physically removes authorization rows whose
// expiry has passed. Entitlement evaluation already filters them eagerly, so
// this sweep only reclaims space and keeps assignment listings honest.
func (s *DeploymentStore) DeleteExpiredAssignments(ctx context.Context) (skill, model, credential, scope int64, err error) {
	result := s.db.WithContext(ctx).
		Where("deployment_id = ? AND expires_at IS NOT NULL AND expires_at <= now()", s.deploymentID).
		Delete(&SkillAssignment{})
	if result.Error != nil {
		return 0, 0, 0, 0, result.Error
	}
	skill = result.RowsAffected
	result = s.db.WithContext(ctx).
		Where("deployment_id = ? AND expires_at IS NOT NULL AND expires_at <= now()", s.deploymentID).
		Delete(&ModelAssignment{})
	if result.Error != nil {
		return 0, 0, 0, 0, result.Error
	}
	model = result.RowsAffected
	result = s.db.WithContext(ctx).
		Where("deployment_id = ? AND expires_at IS NOT NULL AND expires_at <= now()", s.deploymentID).
		Delete(&CredentialAssignment{})
	if result.Error != nil {
		return 0, 0, 0, 0, result.Error
	}
	credential = result.RowsAffected
	result = s.db.WithContext(ctx).
		Where("deployment_id = ? AND expires_at IS NOT NULL AND expires_at <= now()", s.deploymentID).
		Delete(&DataScopeRule{})
	if result.Error != nil {
		return 0, 0, 0, 0, result.Error
	}
	scope = result.RowsAffected
	return skill, model, credential, scope, nil
}
