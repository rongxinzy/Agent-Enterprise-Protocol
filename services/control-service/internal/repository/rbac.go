package repository

import (
	"context"
	"time"
)

func (s *Store) ListPermissions(ctx context.Context) ([]Permission, error) {
	items := make([]Permission, 0)
	err := s.db.WithContext(ctx).Order("id").Find(&items).Error
	return items, err
}

// UserPermissions returns the effective permissions from enabled roles. The
// deployment and user predicates keep the result scoped to one installation.
func (s *DeploymentStore) UserPermissions(ctx context.Context, userID string) ([]string, error) {
	var rows []struct {
		PermissionID string `gorm:"column:permission_id"`
	}
	err := s.db.WithContext(ctx).
		Table("role_permissions AS rp").
		Select("DISTINCT rp.permission_id").
		Joins("JOIN user_role_bindings AS urb ON urb.deployment_id = rp.deployment_id AND urb.role_id = rp.role_id").
		Joins("JOIN roles AS r ON r.deployment_id = rp.deployment_id AND r.id = rp.role_id").
		Where("rp.deployment_id = ? AND urb.user_id = ? AND r.enabled = ?", s.deploymentID, userID, true).
		Order("rp.permission_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	permissions := make([]string, 0, len(rows))
	for _, row := range rows {
		permissions = append(permissions, row.PermissionID)
	}
	return permissions, nil
}

func (s *DeploymentStore) ListRoles(ctx context.Context) ([]RoleRecord, error) {
	roles := make([]Role, 0)
	if err := s.db.WithContext(ctx).
		Where("deployment_id = ?", s.deploymentID).
		Order("id").Find(&roles).Error; err != nil {
		return nil, err
	}
	roleIDs := make([]string, 0, len(roles))
	for _, role := range roles {
		roleIDs = append(roleIDs, role.ID)
	}
	permissionsByRole := make(map[string][]string, len(roleIDs))
	if len(roleIDs) > 0 {
		bindings := make([]RolePermission, 0)
		if err := s.db.WithContext(ctx).
			Where("deployment_id = ? AND role_id IN ?", s.deploymentID, roleIDs).
			Order("role_id, permission_id").Find(&bindings).Error; err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			permissionsByRole[binding.RoleID] = append(permissionsByRole[binding.RoleID], binding.PermissionID)
		}
	}
	items := make([]RoleRecord, 0, len(roles))
	for _, role := range roles {
		permissions := permissionsByRole[role.ID]
		if permissions == nil {
			permissions = make([]string, 0)
		}
		items = append(items, RoleRecord{Role: role, Permissions: permissions})
	}
	return items, nil
}

func (s *DeploymentStore) CreateRole(ctx context.Context, role Role, permissions []string) error {
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		role.DeploymentID = tx.deploymentID
		role.Enabled = true
		if err := tx.db.Create(&role).Error; err != nil {
			return err
		}
		permissions = uniqueStrings(permissions)
		if len(permissions) > 0 {
			var count int64
			if err := tx.db.Model(&Permission{}).Where("id IN ?", permissions).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(permissions)) {
				return ErrUnknownPermission
			}
		}
		bindings := make([]RolePermission, 0, len(permissions))
		for _, permissionID := range permissions {
			bindings = append(bindings, RolePermission{DeploymentID: tx.deploymentID, RoleID: role.ID, PermissionID: permissionID})
		}
		if len(bindings) > 0 {
			return tx.db.Create(&bindings).Error
		}
		return nil
	})
}

func (s *DeploymentStore) GetRoleRecord(ctx context.Context, id string) (RoleRecord, error) {
	var role Role
	if err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&role).Error; err != nil {
		return RoleRecord{}, err
	}
	roles, err := s.ListRoles(ctx)
	if err != nil {
		return RoleRecord{}, err
	}
	for _, item := range roles {
		if item.ID == id {
			return item, nil
		}
	}
	return RoleRecord{Role: role, Permissions: []string{}}, nil
}

func (s *DeploymentStore) UpdateRole(ctx context.Context, id string, name, description *string, enabled *bool, permissions *[]string) (RoleRecord, error) {
	err := s.transaction(ctx, func(tx *DeploymentStore) error {
		updates := map[string]any{"updated_at": time.Now().UTC()}
		if name != nil {
			updates["name"] = *name
		}
		if description != nil {
			updates["description"] = *description
		}
		if enabled != nil {
			updates["enabled"] = *enabled
		}
		result := tx.db.WithContext(ctx).Model(&Role{}).Where("deployment_id = ? AND id = ?", tx.deploymentID, id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if permissions == nil {
			return nil
		}
		items := uniqueStrings(*permissions)
		if len(items) > 0 {
			var count int64
			if err := tx.db.Model(&Permission{}).Where("id IN ?", items).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(items)) {
				return ErrUnknownPermission
			}
		}
		if err := tx.db.Where("deployment_id = ? AND role_id = ?", tx.deploymentID, id).Delete(&RolePermission{}).Error; err != nil {
			return err
		}
		bindings := make([]RolePermission, 0, len(items))
		for _, permissionID := range items {
			bindings = append(bindings, RolePermission{DeploymentID: tx.deploymentID, RoleID: id, PermissionID: permissionID})
		}
		if len(bindings) > 0 {
			return tx.db.Create(&bindings).Error
		}
		return nil
	})
	if err != nil {
		return RoleRecord{}, err
	}
	return s.GetRoleRecord(ctx, id)
}

func (s *DeploymentStore) DeleteRole(ctx context.Context, id string) error {
	var role Role
	if err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&role).Error; err != nil {
		return err
	}
	if role.BuiltIn {
		return ErrBuiltInResource
	}
	return resultError(s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&Role{}))
}

func (s *DeploymentStore) ListTeams(ctx context.Context) ([]TeamRecord, error) {
	type row struct {
		Team
		MemberCount int64 `gorm:"column:member_count"`
	}
	rows := make([]row, 0)
	err := s.db.WithContext(ctx).Model(&Team{}).
		Select("teams.*, COUNT(user_team_bindings.user_id) AS member_count").
		Joins("LEFT JOIN user_team_bindings ON user_team_bindings.deployment_id = teams.deployment_id AND user_team_bindings.team_id = teams.id").
		Where("teams.deployment_id = ?", s.deploymentID).
		Group("teams.deployment_id, teams.id").Order("teams.id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]TeamRecord, 0, len(rows))
	for _, item := range rows {
		items = append(items, TeamRecord{Team: item.Team, MemberCount: item.MemberCount})
	}
	return items, nil
}

func (s *DeploymentStore) CreateTeam(ctx context.Context, team Team) error {
	team.DeploymentID = s.deploymentID
	team.Enabled = true
	return s.db.WithContext(ctx).Create(&team).Error
}

func (s *DeploymentStore) GetTeamRecord(ctx context.Context, id string) (TeamRecord, error) {
	teams, err := s.ListTeams(ctx)
	if err != nil {
		return TeamRecord{}, err
	}
	for _, item := range teams {
		if item.ID == id {
			return item, nil
		}
	}
	return TeamRecord{}, ErrNotFound
}

func (s *DeploymentStore) UpdateTeam(ctx context.Context, id string, name, description *string, enabled *bool) (TeamRecord, error) {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if name != nil {
		updates["name"] = *name
	}
	if description != nil {
		updates["description"] = *description
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	result := s.db.WithContext(ctx).Model(&Team{}).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Updates(updates)
	if result.Error != nil {
		return TeamRecord{}, result.Error
	}
	if result.RowsAffected == 0 {
		return TeamRecord{}, ErrNotFound
	}
	return s.GetTeamRecord(ctx, id)
}

func (s *DeploymentStore) DeleteTeam(ctx context.Context, id string) error {
	var team Team
	if err := s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Take(&team).Error; err != nil {
		return err
	}
	if team.BuiltIn {
		return ErrBuiltInResource
	}
	return resultError(s.db.WithContext(ctx).Where("deployment_id = ? AND id = ?", s.deploymentID, id).Delete(&Team{}))
}

func (s *DeploymentStore) ReplaceUserRBAC(ctx context.Context, userID string, roleIDs, teamIDs []string) error {
	roleIDs = uniqueStrings(roleIDs)
	teamIDs = uniqueStrings(teamIDs)
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		var userCount int64
		if err := tx.db.Model(&User{}).
			Where("deployment_id = ? AND id = ?", tx.deploymentID, userID).
			Count(&userCount).Error; err != nil {
			return err
		}
		if userCount != 1 {
			return ErrNotFound
		}
		var roleCount int64
		if err := tx.db.Model(&Role{}).
			Where("deployment_id = ? AND id IN ?", tx.deploymentID, roleIDs).
			Count(&roleCount).Error; err != nil {
			return err
		}
		if roleCount != int64(len(roleIDs)) {
			return ErrUnknownRole
		}
		var teamCount int64
		if err := tx.db.Model(&Team{}).
			Where("deployment_id = ? AND id IN ?", tx.deploymentID, teamIDs).
			Count(&teamCount).Error; err != nil {
			return err
		}
		if teamCount != int64(len(teamIDs)) {
			return ErrUnknownTeam
		}
		if err := tx.db.Where("deployment_id = ? AND user_id = ?", tx.deploymentID, userID).
			Delete(&UserRoleBinding{}).Error; err != nil {
			return err
		}
		if err := tx.db.Where("deployment_id = ? AND user_id = ?", tx.deploymentID, userID).
			Delete(&UserTeamBinding{}).Error; err != nil {
			return err
		}
		roleBindings := make([]UserRoleBinding, 0, len(roleIDs))
		for index, roleID := range roleIDs {
			roleBindings = append(roleBindings, UserRoleBinding{DeploymentID: tx.deploymentID, UserID: userID, RoleID: roleID, IsPrimary: index == 0})
		}
		if err := tx.db.Create(&roleBindings).Error; err != nil {
			return err
		}
		teamBindings := make([]UserTeamBinding, 0, len(teamIDs))
		for index, teamID := range teamIDs {
			teamBindings = append(teamBindings, UserTeamBinding{DeploymentID: tx.deploymentID, UserID: userID, TeamID: teamID, IsPrimary: index == 0})
		}
		return tx.db.Create(&teamBindings).Error
	})
}
