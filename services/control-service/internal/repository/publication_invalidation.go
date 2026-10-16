package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

type invalidationSnapshot struct {
	Revision      string                 `json:"revision"`
	Routes        []gatewayrender.Route  `json:"routes"`
	GatewayLimits *[]gatewaypolicy.Limit `json:"gatewayLimits,omitempty"`
}

func invalidationDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// QueueReferenceInvalidations changes only previously selected intent. It never
// publishes an unrelated catalog/rule draft or resolves a credential. The signal
// acknowledgement and replacement snapshot/audit/outbox commit atomically.
func (s *DeploymentStore) QueueReferenceInvalidations(ctx context.Context) error {
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		var locked bool
		if err := tx.db.Raw(`SELECT pg_try_advisory_xact_lock(hashtextextended(?,0))`, "aep:publication:"+s.deploymentID).Scan(&locked).Error; err != nil || !locked {
			return err
		}
		if err := tx.db.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,101))`, s.deploymentID).Error; err != nil {
			return err
		}
		var signal struct{ Version int64 }
		if err := tx.db.Raw(`SELECT version FROM gateway_reference_invalidations WHERE deployment_id=? AND version>acknowledged_version`, s.deploymentID).Scan(&signal).Error; err != nil {
			return err
		}
		if signal.Version == 0 {
			return nil
		}
		var latest struct {
			Payload  json.RawMessage
			Sequence int64
		}
		if err := tx.db.Raw(`SELECT s.payload,p.sequence FROM data_plane_publications p JOIN data_plane_publication_snapshots s USING(deployment_id,revision,content_hash) WHERE p.deployment_id=? ORDER BY p.sequence DESC LIMIT 1`, s.deploymentID).Scan(&latest).Error; err != nil {
			return err
		}
		if len(latest.Payload) > 0 {
			var state invalidationSnapshot
			if err := json.Unmarshal(latest.Payload, &state); err != nil {
				return errors.New("invalid selected publication")
			}
			var ids []string
			if err := tx.db.Model(&Model{}).Where("deployment_id = ?", s.deploymentID).Pluck("id", &ids).Error; err != nil {
				return err
			}
			var removed []string
			if err := tx.db.Table("gateway_model_removals").Where("deployment_id=? AND last_publication_sequence>=?", s.deploymentID, latest.Sequence).Pluck("model_id", &removed).Error; err != nil {
				return err
			}
			var fences []GatewayLimit
			if state.GatewayLimits != nil {
				if err := tx.db.Where("deployment_id=? AND reference_invalidated=true", s.deploymentID).Find(&fences).Error; err != nil {
					return err
				}
			}
			if invalidateSelected(&state, ids, removed, fences) {
				state.Revision = "gateway-reference-"
				preimage, err := json.Marshal(state)
				if err != nil {
					return err
				}
				state.Revision += invalidationDigest(preimage)
				payload, err := json.Marshal(state)
				if err != nil {
					return err
				}
				value, err := tx.insertPublication(ctx, invalidationDigest([]byte(uuid.NewString())), invalidationDigest([]byte("reference-removal")), "", PublicationSnapshot{Revision: state.Revision, ContentHash: invalidationDigest(payload), Payload: payload})
				if err != nil {
					return err
				}
				if err := tx.RecordAdminAudit(ctx, AdminAuditEntry{Action: "invalidate", ResourceType: "data_plane_publication", ResourceID: value.OperationID, Result: "success", Reason: "gateway_reference_removed"}); err != nil {
					return err
				}
			}
		}
		return tx.db.Exec(`UPDATE gateway_reference_invalidations SET acknowledged_version=? WHERE deployment_id=? AND version=?`, signal.Version, s.deploymentID, signal.Version).Error
	})
}

func invalidateSelected(state *invalidationSnapshot, ids, removed []string, fences []GatewayLimit) bool {
	models := make(map[string]bool, len(ids))
	for _, id := range ids {
		models[id] = true
	}
	for _, id := range removed {
		delete(models, id)
	}
	changed := false
	for i := range state.Routes {
		if state.Routes[i].Enabled && !models[state.Routes[i].ModelID] {
			state.Routes[i].Enabled = false
			changed = true
		}
	}
	if state.GatewayLimits == nil {
		return changed
	}
	byID := make(map[string]GatewayLimit, len(fences))
	for _, fence := range fences {
		byID[fence.ID] = fence
	}
	for i, item := range *state.GatewayLimits {
		if fence, ok := byID[item.ID]; ok && fence.Version >= item.Version && (item.Configuration.Enabled || fence.Version > item.Version) {
			item.Configuration.Enabled = false
			item.Version = fence.Version
			item.UpdatedAt = fence.UpdatedAt
			(*state.GatewayLimits)[i] = item
			changed = true
		}
	}
	return changed
}
