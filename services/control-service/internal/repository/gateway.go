package repository

import "context"

func (s *DeploymentStore) GatewayMemberships(ctx context.Context, userID string) ([]string, []string, error) {
	roles, teams := make([]string, 0), make([]string, 0)
	err := s.db.WithContext(ctx).Table("user_role_bindings AS b").Joins("JOIN roles AS r ON r.deployment_id=b.deployment_id AND r.id=b.role_id AND r.enabled=true").Where("b.deployment_id=? AND b.user_id=?", s.deploymentID, userID).Order("b.role_id").Pluck("b.role_id", &roles).Error
	if err != nil {
		return nil, nil, err
	}
	err = s.db.WithContext(ctx).Table("user_team_bindings AS b").Joins("JOIN teams AS t ON t.deployment_id=b.deployment_id AND t.id=b.team_id AND t.enabled=true").Where("b.deployment_id=? AND b.user_id=?", s.deploymentID, userID).Order("b.team_id").Pluck("b.team_id", &teams).Error
	return roles, teams, err
}
