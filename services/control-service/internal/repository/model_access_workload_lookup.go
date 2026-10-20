package repository

import "context"

// ModelAccessWorkloadReference identifies an exact current registry row for a
// trusted lifecycle observer. Expired observations remain discoverable so a live
// owner can renew via CAS; retired history must never be offered for revival.
type ModelAccessWorkloadReference struct {
	UserID     string
	BindingKey string
}

func (s *DeploymentStore) ModelAccessWorkloadRegistrations(ctx context.Context, namespace, employeeName string) ([]ModelAccessWorkloadReference, error) {
	if namespace == "" || employeeName == "" {
		return nil, ErrAccessBinding
	}
	rows := []ModelAccessWorkloadReference{}
	err := s.db.WithContext(ctx).Model(&modelAccessWorkload{}).Select("user_id,binding_key").
		Where("deployment_id=? AND namespace=? AND employee_name=? AND retired_at IS NULL", s.deploymentID, namespace, employeeName).
		Order("binding_key").Find(&rows).Error
	return rows, err
}
