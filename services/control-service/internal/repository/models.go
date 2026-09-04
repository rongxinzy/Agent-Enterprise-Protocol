package repository

import "time"

type Deployment struct {
	ID        string    `gorm:"column:id;primaryKey"`
	Name      string    `gorm:"column:name"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (Deployment) TableName() string { return "deployments" }

type User struct {
	ID                    string    `gorm:"column:id;primaryKey"`
	DeploymentID          string    `gorm:"column:deployment_id;not null"`
	Username              string    `gorm:"column:username;not null"`
	DisplayName           string    `gorm:"column:display_name;not null"`
	Email                 *string   `gorm:"column:email"`
	PasswordHash          string    `gorm:"column:password_hash;not null"`
	Status                string    `gorm:"column:status;not null"`
	RequirePasswordChange bool      `gorm:"column:require_password_change;not null"`
	IsAdmin               bool      `gorm:"column:is_admin;not null"`
	CreatedAt             time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt             time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (User) TableName() string { return "users" }

type Permission struct {
	ID          string `gorm:"column:id;primaryKey"`
	Description string `gorm:"column:description;not null"`
}

func (Permission) TableName() string { return "permissions" }

type Role struct {
	DeploymentID string    `gorm:"column:deployment_id;primaryKey"`
	ID           string    `gorm:"column:id;primaryKey"`
	Name         string    `gorm:"column:name;not null"`
	Description  string    `gorm:"column:description;not null"`
	BuiltIn      bool      `gorm:"column:built_in;not null"`
	Enabled      bool      `gorm:"column:enabled;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (Role) TableName() string { return "roles" }

type RolePermission struct {
	DeploymentID string `gorm:"column:deployment_id;primaryKey"`
	RoleID       string `gorm:"column:role_id;primaryKey"`
	PermissionID string `gorm:"column:permission_id;primaryKey"`
}

func (RolePermission) TableName() string { return "role_permissions" }

type Team struct {
	DeploymentID string    `gorm:"column:deployment_id;primaryKey"`
	ID           string    `gorm:"column:id;primaryKey"`
	Name         string    `gorm:"column:name;not null"`
	Description  string    `gorm:"column:description;not null"`
	BuiltIn      bool      `gorm:"column:built_in;not null"`
	Enabled      bool      `gorm:"column:enabled;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (Team) TableName() string { return "teams" }

type UserRoleBinding struct {
	DeploymentID string    `gorm:"column:deployment_id;primaryKey"`
	UserID       string    `gorm:"column:user_id;primaryKey"`
	RoleID       string    `gorm:"column:role_id;primaryKey"`
	IsPrimary    bool      `gorm:"column:is_primary;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (UserRoleBinding) TableName() string { return "user_role_bindings" }

type UserTeamBinding struct {
	DeploymentID string    `gorm:"column:deployment_id;primaryKey"`
	UserID       string    `gorm:"column:user_id;primaryKey"`
	TeamID       string    `gorm:"column:team_id;primaryKey"`
	IsPrimary    bool      `gorm:"column:is_primary;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (UserTeamBinding) TableName() string { return "user_team_bindings" }

type UserRecord struct {
	User
	RoleIDs []string
	TeamIDs []string
}

type RoleRecord struct {
	Role
	Permissions []string
}

type TeamRecord struct {
	Team
	MemberCount int64
}
