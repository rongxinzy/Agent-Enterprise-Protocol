package repository

import (
	"context"
	"time"
)

type CreateUserParams struct {
	ID                    string
	Username              string
	DisplayName           string
	Email                 *string
	PasswordHash          string
	RequirePasswordChange bool
	IsAdmin               bool
	RoleIDs               []string
	TeamIDs               []string
}

type UpdateUserParams struct {
	DisplayName *string
	Email       *string
	Status      *string
}

func (s *DeploymentStore) GetUser(ctx context.Context, id string) (User, error) {
	var user User
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Take(&user).Error
	return user, err
}

func (s *DeploymentStore) GetUserByUsername(ctx context.Context, username string) (User, error) {
	var user User
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND username = ?", s.deploymentID, username).
		Take(&user).Error
	return user, err
}

func (s *DeploymentStore) GetUserRecord(ctx context.Context, id string) (UserRecord, error) {
	user, err := s.GetUser(ctx, id)
	if err != nil {
		return UserRecord{}, err
	}
	roleIDs, err := s.UserRoleIDs(ctx, id)
	if err != nil {
		return UserRecord{}, err
	}
	teamIDs, err := s.UserTeamIDs(ctx, id)
	if err != nil {
		return UserRecord{}, err
	}
	return UserRecord{User: user, RoleIDs: roleIDs, TeamIDs: teamIDs}, nil
}

func (s *DeploymentStore) ListUsers(ctx context.Context, cursor string, limit int32) ([]UserRecord, error) {
	query := s.db.WithContext(ctx).
		Where("deployment_id = ?", s.deploymentID).
		Order("id").Limit(int(limit))
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	var users []User
	if err := query.Find(&users).Error; err != nil {
		return nil, err
	}
	userIDs := make([]string, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	roleIDsByUser, err := s.roleIDsByUser(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	teamIDsByUser, err := s.teamIDsByUser(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	items := make([]UserRecord, 0, len(users))
	for _, user := range users {
		roleIDs := roleIDsByUser[user.ID]
		if roleIDs == nil {
			roleIDs = make([]string, 0)
		}
		teamIDs := teamIDsByUser[user.ID]
		if teamIDs == nil {
			teamIDs = make([]string, 0)
		}
		items = append(items, UserRecord{
			User: user, RoleIDs: roleIDs, TeamIDs: teamIDs,
		})
	}
	return items, nil
}

func (s *DeploymentStore) CreateUser(ctx context.Context, params CreateUserParams) (UserRecord, error) {
	var result UserRecord
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		user := User{
			ID: params.ID, DeploymentID: tx.deploymentID, Username: params.Username,
			DisplayName: params.DisplayName, Email: params.Email, PasswordHash: params.PasswordHash,
			Status: "active", RequirePasswordChange: params.RequirePasswordChange, IsAdmin: params.IsAdmin,
		}
		if err := tx.db.Create(&user).Error; err != nil {
			return err
		}
		roleIDs := uniqueStrings(params.RoleIDs)
		for index, roleID := range roleIDs {
			binding := UserRoleBinding{DeploymentID: tx.deploymentID, UserID: user.ID, RoleID: roleID, IsPrimary: index == 0}
			if err := tx.db.Create(&binding).Error; err != nil {
				return err
			}
		}
		teamIDs := uniqueStrings(params.TeamIDs)
		for index, teamID := range teamIDs {
			binding := UserTeamBinding{DeploymentID: tx.deploymentID, UserID: user.ID, TeamID: teamID, IsPrimary: index == 0}
			if err := tx.db.Create(&binding).Error; err != nil {
				return err
			}
		}
		result = UserRecord{User: user, RoleIDs: roleIDs, TeamIDs: teamIDs}
		return nil
	})
	return result, err
}

func (s *DeploymentStore) UpdateUser(ctx context.Context, id string, params UpdateUserParams) (UserRecord, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if params.DisplayName != nil {
		updates["display_name"] = *params.DisplayName
	}
	if params.Email != nil {
		updates["email"] = *params.Email
	}
	if params.Status != nil {
		updates["status"] = *params.Status
	}
	result := s.db.WithContext(ctx).Model(&User{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Updates(updates)
	if result.Error != nil {
		return UserRecord{}, result.Error
	}
	if result.RowsAffected == 0 {
		return UserRecord{}, ErrNotFound
	}
	return s.GetUserRecord(ctx, id)
}

func (s *DeploymentStore) UpdatePassword(ctx context.Context, id, passwordHash string, requirePasswordChange bool) error {
	result := s.db.WithContext(ctx).Model(&User{}).
		Where("deployment_id = ? AND id = ?", s.deploymentID, id).
		Updates(map[string]any{
			"password_hash": passwordHash, "require_password_change": requirePasswordChange,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DeploymentStore) UserRoleIDs(ctx context.Context, userID string) ([]string, error) {
	ids := make([]string, 0)
	err := s.db.WithContext(ctx).Model(&UserRoleBinding{}).
		Where("deployment_id = ? AND user_id = ?", s.deploymentID, userID).
		Order("role_id").Pluck("role_id", &ids).Error
	return ids, err
}

func (s *DeploymentStore) UserTeamIDs(ctx context.Context, userID string) ([]string, error) {
	ids := make([]string, 0)
	err := s.db.WithContext(ctx).Model(&UserTeamBinding{}).
		Where("deployment_id = ? AND user_id = ?", s.deploymentID, userID).
		Order("team_id").Pluck("team_id", &ids).Error
	return ids, err
}

func (s *DeploymentStore) roleIDsByUser(ctx context.Context, userIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	bindings := make([]UserRoleBinding, 0)
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND user_id IN ?", s.deploymentID, userIDs).
		Order("user_id, role_id").Find(&bindings).Error
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		result[binding.UserID] = append(result[binding.UserID], binding.RoleID)
	}
	return result, nil
}

func (s *DeploymentStore) teamIDsByUser(ctx context.Context, userIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	bindings := make([]UserTeamBinding, 0)
	err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND user_id IN ?", s.deploymentID, userIDs).
		Order("user_id, team_id").Find(&bindings).Error
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		result[binding.UserID] = append(result[binding.UserID], binding.TeamID)
	}
	return result, nil
}
