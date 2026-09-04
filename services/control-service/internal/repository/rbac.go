package repository

import "context"

func (s *Store) ListPermissions(ctx context.Context) ([]Permission, error) {
	items := make([]Permission, 0)
	err := s.db.WithContext(ctx).Order("id").Find(&items).Error
	return items, err
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
