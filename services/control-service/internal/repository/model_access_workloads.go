package repository

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrAccessWorkloadConflict = errors.New("workload identity lifecycle conflict")

// ObservedModelWorkload is credential-free evidence supplied by a TRUSTED
// lifecycle writer after reading the live employee and its controller-owned SA.
// It is not client input, a TokenReview result or a model permission grant.
// The writer must explicitly resolve the employee's AEP agent ID, never owner.
type ObservedModelWorkload struct {
	EmployeeName string                      `json:"employeeName"`
	Identity     gateway.ModelAccessWorkload `json:"identity"`
	PodUIDs      []string                    `json:"podUids"`
	// DB-derived source output; registration computes it from validFor and the
	// database clock, never from a caller-supplied timestamp in this field.
	VerifiedUntil time.Time `json:"verifiedUntil"`
}

type modelAccessWorkload struct {
	DeploymentID, BindingKey, UserID                string
	Namespace, EmployeeName, EmployeeUID            string
	ServiceAccountName, ServiceAccountUID, Audience string
	VerifiedAt, VerifiedUntil                       time.Time
	RetiredAt                                       *time.Time
	PodUIDs                                         json.RawMessage `gorm:"column:pod_uids;type:jsonb"`
}

func (modelAccessWorkload) TableName() string { return "model_access_workloads" }

func validObservedWorkload(w ObservedModelWorkload) bool {
	i := w.Identity
	bounded := func(s string, max int) bool {
		return s != "" && len(s) <= max && strings.IndexFunc(s, unicode.IsSpace) == -1 && strings.IndexFunc(s, unicode.IsControl) == -1
	}
	return len(validation.IsDNS1123Label(i.Namespace)) == 0 &&
		gatewayaccess.ValidWorkloadPods("kubernetes", w.PodUIDs) &&
		len(validation.IsDNS1123Subdomain(i.ServiceAccountName)) == 0 && len(validation.IsDNS1123Subdomain(w.EmployeeName)) == 0 &&
		bounded(string(i.EmployeeUID), 128) && bounded(string(i.ServiceAccountUID), 128) && bounded(i.Audience, 256)
}

// RegisterModelAccessWorkload atomically retires the previous identity and
// registers its replacement, guarded by the caller's expected current binding.
// Empty expected means first registration, not an unconditional upsert. Passing
// the same current binding updates mutable live-Pod evidence and freshness;
// an unchanged set does not queue a new policy. No tokens or requested model
// IDs enter this storage surface. There is no public endpoint.
func (s *DeploymentStore) RegisterModelAccessWorkload(ctx context.Context, agentID, expected string, w ObservedModelWorkload, validFor time.Duration) (string, error) {
	if agentID == "" || len(agentID) > 256 || !validObservedWorkload(w) || validFor < 2*time.Second || validFor > time.Hour {
		return "", ErrAccessBinding
	}
	policy := gatewayaccess.Snapshot{DeploymentID: s.deploymentID, Subject: gateway.ModelAccessSubject{Kind: "agent", ID: agentID},
		Binding: gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &w.Identity}}
	key := policy.BindingKey()
	pods := slices.Clone(w.PodUIDs)
	slices.Sort(pods)
	if pods == nil {
		pods = []string{}
	}
	podData, err := json.Marshal(pods)
	if err != nil {
		return "", ErrAccessBinding
	}
	err = s.transaction(ctx, func(tx *DeploymentStore) error {
		if err := tx.lockWorkload(ctx); err != nil {
			return err
		}
		// Serialize cross-deployment claims of the same immutable employee UID.
		if err := tx.db.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,104))`, string(w.Identity.EmployeeUID)).Error; err != nil {
			return err
		}
		var agent bool
		if err := tx.db.Raw(`SELECT EXISTS(SELECT 1 FROM users u JOIN agent_profiles p ON p.deployment_id=u.deployment_id AND p.user_id=u.id
WHERE u.deployment_id=? AND u.id=? AND u.kind='agent')`, s.deploymentID, agentID).Scan(&agent).Error; err != nil {
			return err
		}
		if !agent {
			return ErrAccessBinding
		}
		var foreign bool
		if err := tx.db.Raw(`SELECT EXISTS(SELECT 1 FROM model_access_workloads WHERE employee_uid=?
AND (deployment_id<>? OR user_id<>? OR namespace<>? OR employee_name<>?))`,
			string(w.Identity.EmployeeUID), s.deploymentID, agentID, w.Identity.Namespace, w.EmployeeName).Scan(&foreign).Error; err != nil {
			return err
		}
		if foreign {
			return ErrAccessWorkloadConflict
		}
		var current modelAccessWorkload
		result := tx.db.Where("deployment_id=? AND user_id=? AND retired_at IS NULL", s.deploymentID, agentID).Take(&current)
		if result.Error != nil && !errors.Is(result.Error, ErrNotFound) {
			return result.Error
		}
		if current.BindingKey != expected {
			return ErrAccessWorkloadConflict
		}
		var now time.Time
		if err := tx.db.Raw(`SELECT clock_timestamp()`).Scan(&now).Error; err != nil {
			return err
		}
		if current.BindingKey == key {
			return tx.db.Model(&current).Where("deployment_id=? AND binding_key=?", s.deploymentID, key).
				Updates(map[string]any{"verified_at": now, "verified_until": now.Add(validFor), "pod_uids": string(podData)}).Error
		}
		if current.BindingKey != "" {
			if err := tx.db.Model(&current).Where("deployment_id=? AND binding_key=?", s.deploymentID, current.BindingKey).Update("retired_at", now).Error; err != nil {
				return err
			}
		}
		row := modelAccessWorkload{DeploymentID: s.deploymentID, BindingKey: key, UserID: agentID, Namespace: w.Identity.Namespace,
			EmployeeName: w.EmployeeName, EmployeeUID: string(w.Identity.EmployeeUID), ServiceAccountName: w.Identity.ServiceAccountName,
			ServiceAccountUID: string(w.Identity.ServiceAccountUID), Audience: w.Identity.Audience, VerifiedAt: now, VerifiedUntil: now.Add(validFor), PodUIDs: podData}
		return tx.db.Create(&row).Error
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		err = ErrAccessWorkloadConflict
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (s *DeploymentStore) lockWorkload(ctx context.Context) error {
	// An inherited repeatable-read snapshot can predate a writer we wait for,
	// including a retired cross-deployment identity. Refuse stale snapshots.
	var isolation string
	if err := s.db.WithContext(ctx).Raw(`SHOW transaction_isolation`).Scan(&isolation).Error; err != nil {
		return err
	}
	if isolation != "read committed" {
		return ErrAccessBinding
	}
	return s.db.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,101))`, s.deploymentID).Error
}

// RetireModelAccessWorkload is idempotent for the exact retired binding. A late
// deletion notification cannot retire a replacement identity. History stays.
func (s *DeploymentStore) RetireModelAccessWorkload(ctx context.Context, agentID, expected string) error {
	if agentID == "" || expected == "" {
		return ErrAccessBinding
	}
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		if err := tx.lockWorkload(ctx); err != nil {
			return err
		}
		var row modelAccessWorkload
		if err := tx.db.Where("deployment_id=? AND user_id=? AND binding_key=?", s.deploymentID, agentID, expected).Take(&row).Error; err != nil {
			return err
		}
		if row.RetiredAt != nil {
			return nil
		}
		return tx.db.Model(&row).Where("deployment_id=? AND binding_key=?", s.deploymentID, expected).Update("retired_at", time.Now().UTC()).Error
	})
}
