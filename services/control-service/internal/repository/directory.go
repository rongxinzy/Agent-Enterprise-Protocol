package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AgentProfile carries digital-employee presentation metadata for a user with
// kind='agent'. Presence is never stored here; it derives from user_sessions.
type AgentProfile struct {
	DeploymentID    string     `gorm:"column:deployment_id;primaryKey"`
	UserID          string     `gorm:"column:user_id;primaryKey"`
	DisplayTitle    string     `gorm:"column:display_title;not null"`
	Description     string     `gorm:"column:description;not null"`
	AvatarObjectKey *string    `gorm:"column:avatar_object_key"`
	HomeTeamID      string     `gorm:"column:home_team_id;not null"`
	PromptSkillID   *string    `gorm:"column:prompt_skill_id"`
	Ephemeral       bool       `gorm:"column:ephemeral;not null"`
	ExpiresAt       *time.Time `gorm:"column:expires_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime"`
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
	DeploymentID        string     `gorm:"column:deployment_id;primaryKey"`
	SourceID            string     `gorm:"column:source_id;primaryKey"`
	ExternalSubjectType string     `gorm:"column:external_subject_type;primaryKey"`
	ExternalID          string     `gorm:"column:external_id;primaryKey"`
	LocalSubjectID      string     `gorm:"column:local_subject_id;not null"`
	Status              string     `gorm:"column:status;not null"`
	LinkedAt            time.Time  `gorm:"column:linked_at;autoCreateTime"`
	LastSyncedAt        *time.Time `gorm:"column:last_synced_at"`
}

func (IdentityMapping) TableName() string { return "identity_mappings" }

type DataScopeRule struct {
	DeploymentID string     `gorm:"column:deployment_id;primaryKey"`
	ID           string     `gorm:"column:id;primaryKey"`
	RuleKind     string     `gorm:"column:rule_kind;not null"`
	SubjectType  string     `gorm:"column:subject_type;not null"`
	SubjectID    string     `gorm:"column:subject_id;not null"`
	ResourceKind string     `gorm:"column:resource_kind;not null"`
	ResourceID   string     `gorm:"column:resource_id;not null"`
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
// and the freshest active-session heartbeat for presence projection. Pages by
// user id after the optional cursor.
func (s *DeploymentStore) ListAgentDirectory(ctx context.Context, cursor string, fetchLimit int32, includeEphemeral bool) ([]AgentDirectoryEntry, error) {
	rows, err := s.db.WithContext(ctx).Raw(`
SELECT u.id, u.username, u.display_name, u.status, u.kind,
       p.display_title, p.description, p.avatar_object_key, p.home_team_id, p.prompt_skill_id,
       p.ephemeral, p.expires_at, p.created_at, p.updated_at,
       presence.last_seen_at
FROM users u
LEFT JOIN agent_profiles p ON p.deployment_id = u.deployment_id AND p.user_id = u.id
LEFT JOIN LATERAL (
  SELECT MAX(s2.last_seen_at) AS last_seen_at
  FROM user_sessions s2
  WHERE s2.deployment_id = u.deployment_id AND s2.user_id = u.id AND s2.revoked_at IS NULL
) presence ON true
WHERE u.deployment_id = ? AND u.kind = 'agent'
  AND (? = '' OR u.id > ?)
  AND (? OR COALESCE(p.ephemeral, false) = false)
ORDER BY u.id
LIMIT ?`, s.deploymentID, cursor, cursor, includeEphemeral, fetchLimit).Rows()
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
		var ephemeral *bool
		var expiresAt *time.Time
		if err := rows.Scan(
			&entry.User.ID, &entry.User.Username, &displayName, &entry.User.Status, &entry.User.Kind,
			&profileTitle, &profileDescription, &avatarObjectKey, &homeTeamID, &promptSkillID,
			&ephemeral, &expiresAt, &profileCreatedAt, &profileUpdatedAt,
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
				Ephemeral: derefBool(ephemeral), ExpiresAt: expiresAt,
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

func (s *DeploymentStore) ListIdentitySources(ctx context.Context, cursor string, fetchLimit int32) ([]IdentitySource, error) {
	sources := make([]IdentitySource, 0)
	query := s.db.WithContext(ctx).
		Where("deployment_id = ?", s.deploymentID)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	err := query.Order("id").Limit(int(fetchLimit)).Find(&sources).Error
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

// UpsertIdentityMapping inserts a mapping or retargets an existing one. The
// update path only touches the mutable columns so a fresh struct cannot zero
// out linked_at on a rebind.
func (s *DeploymentStore) UpsertIdentityMapping(ctx context.Context, mapping IdentityMapping) error {
	mapping.DeploymentID = s.deploymentID
	if mapping.Status == "" {
		mapping.Status = "active"
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "deployment_id"}, {Name: "source_id"},
			{Name: "external_subject_type"}, {Name: "external_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"local_subject_id", "status", "last_synced_at"}),
	}).Create(&mapping).Error
}

// ListIdentityMappings pages by (external_subject_type, external_id) after the
// optional "subjectType/externalId" cursor.
func (s *DeploymentStore) ListIdentityMappings(ctx context.Context, sourceID, subjectType, cursorSubjectType, cursorExternalID string, fetchLimit int32) ([]IdentityMapping, error) {
	mappings := make([]IdentityMapping, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ? AND source_id = ?", s.deploymentID, sourceID)
	if subjectType != "" {
		query = query.Where("external_subject_type = ?", subjectType)
	}
	if cursorExternalID != "" {
		query = query.Where("(external_subject_type, external_id) > (?, ?)", cursorSubjectType, cursorExternalID)
	}
	err := query.Order("external_subject_type, external_id").Limit(int(fetchLimit)).Find(&mappings).Error
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

func (s *DeploymentStore) ListDataScopeRules(ctx context.Context, cursor string, fetchLimit int32) ([]DataScopeRule, error) {
	rules := make([]DataScopeRule, 0)
	query := s.db.WithContext(ctx).Where("deployment_id = ?", s.deploymentID)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	err := query.Order("id").Limit(int(fetchLimit)).Find(&rules).Error
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

func derefBool(v *bool) bool { return v != nil && *v }

// GetUserByID loads one user of the deployment by id.
func (s *DeploymentStore) GetUserByID(ctx context.Context, userID string) (User, error) {
	var user User
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND id = ?", s.deploymentID, userID).
		Take(&user).Error
	return user, err
}

// ErrAgentHasSessions blocks deleting a digital employee with live sessions.
var ErrAgentHasSessions = errors.New("agent still has active sessions")

// DeleteAgent removes a digital employee account and everything bound to it:
// role/team bindings, subject assignments, data-scope rules, the profile, and
// the user row (cascading sessions and tokens). Live, non-revoked sessions
// refuse the deletion with ErrAgentHasSessions; revoke them first.
func (s *DeploymentStore) DeleteAgent(ctx context.Context, userID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var activeSessions int64
		if err := tx.Raw(`SELECT count(*) FROM user_sessions
WHERE deployment_id = ? AND user_id = ? AND revoked_at IS NULL`, s.deploymentID, userID).
			Scan(&activeSessions).Error; err != nil {
			return err
		}
		if activeSessions > 0 {
			return ErrAgentHasSessions
		}
		stmts := []string{
			`DELETE FROM user_role_bindings WHERE deployment_id = ? AND user_id = ?`,
			`DELETE FROM user_team_bindings WHERE deployment_id = ? AND user_id = ?`,
			`DELETE FROM model_assignments WHERE deployment_id = ? AND subject_type = 'user' AND subject_id = ?`,
			`DELETE FROM skill_assignments WHERE deployment_id = ? AND subject_type = 'user' AND subject_id = ?`,
			`DELETE FROM credential_assignments WHERE deployment_id = ? AND subject_type = 'user' AND subject_id = ?`,
			`DELETE FROM data_scope_rules WHERE deployment_id = ? AND subject_type = 'user' AND subject_id = ?`,
			`DELETE FROM agent_profiles WHERE deployment_id = ? AND user_id = ?`,
			`DELETE FROM users WHERE deployment_id = ? AND id = ?`,
		}
		for _, stmt := range stmts {
			if err := tx.Exec(stmt, s.deploymentID, userID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
