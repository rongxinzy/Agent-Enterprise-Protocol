package repository

import (
	"context"
	"encoding/json"
)

// AdminAuditEntry is one administrative write operation to persist. Changes is
// an already-masked {before, after} document; masking is the caller's concern.
type AdminAuditEntry struct {
	ActorUserID  string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	Reason       string
	Changes      map[string]any
}

// WithTransaction runs operation inside a transaction and hands it a store
// bound to that transaction, so a resource change and its audit row commit
// together.
func (s *DeploymentStore) WithTransaction(ctx context.Context, operation func(*DeploymentStore) error) error {
	return s.transaction(ctx, operation)
}

// RecordAdminAudit appends an administrative audit row through this store's
// connection. Called on a store returned by WithTransaction it joins that
// transaction; called directly it is a standalone write.
func (s *DeploymentStore) RecordAdminAudit(ctx context.Context, entry AdminAuditEntry) error {
	var changes any
	if entry.Changes != nil {
		raw, err := json.Marshal(entry.Changes)
		if err != nil {
			return err
		}
		changes = raw
	}
	return s.db.WithContext(ctx).Exec(
		`INSERT INTO admin_audit_events (deployment_id,actor_user_id,action,resource_type,resource_id,result,reason,payload) VALUES (?,?,?,?,?,?,?,?)`,
		s.deploymentID, optionalText(entry.ActorUserID), entry.Action, entry.ResourceType, optionalText(entry.ResourceID), entry.Result, optionalText(entry.Reason), changes).Error
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
