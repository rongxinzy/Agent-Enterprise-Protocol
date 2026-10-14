package repository

import (
	"context"
	"time"
)

// AgentComputeAssignment is one agent's stored compute tier override; the
// absence of a row means the deployment default tier applies.
type AgentComputeAssignment struct {
	DeploymentID string    `gorm:"column:deployment_id;primaryKey"`
	AgentID      string    `gorm:"column:agent_id;primaryKey"`
	Tier         string    `gorm:"column:tier;not null"`
	UpdatedBy    *string   `gorm:"column:updated_by"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null"`
}

func (AgentComputeAssignment) TableName() string { return "agent_compute_assignments" }

// ComputeAssignmentOverride carries the stored override fields needed to
// resolve one agent's effective tier.
type ComputeAssignmentOverride struct {
	Tier      string
	UpdatedBy *string
	UpdatedAt time.Time
}

// ListComputeAssignmentOverrides returns the stored overrides for the given
// agents, keyed by agent id; agents without a row are simply absent.
func (s *DeploymentStore) ListComputeAssignmentOverrides(ctx context.Context, agentIDs []string) (map[string]ComputeAssignmentOverride, error) {
	overrides := make(map[string]ComputeAssignmentOverride, len(agentIDs))
	if len(agentIDs) == 0 {
		return overrides, nil
	}
	var rows []AgentComputeAssignment
	if err := s.db.WithContext(ctx).
		Where("deployment_id = ? AND agent_id IN ?", s.deploymentID, agentIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		overrides[row.AgentID] = ComputeAssignmentOverride{Tier: row.Tier, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt}
	}
	return overrides, nil
}

// UpsertComputeAssignment stores one agent's tier override, replacing any
// previous row for the same agent.
func (s *DeploymentStore) UpsertComputeAssignment(ctx context.Context, assignment AgentComputeAssignment) error {
	return s.db.WithContext(ctx).Exec(`
INSERT INTO agent_compute_assignments (deployment_id, agent_id, tier, updated_by, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (deployment_id, agent_id) DO UPDATE SET tier = EXCLUDED.tier, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
		assignment.DeploymentID, assignment.AgentID, assignment.Tier, assignment.UpdatedBy, assignment.UpdatedAt).Error
}

// DeleteComputeAssignment clears one agent's tier override so the deployment
// default tier applies again; a missing row is not an error.
func (s *DeploymentStore) DeleteComputeAssignment(ctx context.Context, agentID string) error {
	return s.db.WithContext(ctx).
		Exec(`DELETE FROM agent_compute_assignments WHERE deployment_id = ? AND agent_id = ?`, s.deploymentID, agentID).Error
}
