package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ModelAccessSource is a credential-free business snapshot for the publication
// producer, not a public API, CR or cached authorization decision. Resources are
// derived through effective roles only. LegacyDirectGrants makes the existing
// user/team grant compatibility gap explicit; it must be resolved before cutover.
type ModelAccessSource struct {
	DeploymentID       string               `json:"deploymentId"`
	SubjectID          string               `json:"subjectId"`
	ObservedAt         time.Time            `json:"observedAt"`
	Exists             bool                 `json:"exists"`
	Kind               string               `json:"kind"`
	Disabled           bool                 `json:"disabled"`
	ProfileMissing     bool                 `json:"profileMissing"`
	SubjectExpiresAt   *time.Time           `json:"subjectExpiresAt"`
	RoleIDs            []string             `json:"roleIds"`
	TeamIDs            []string             `json:"teamIds"`
	Models             []ModelAccessModel   `json:"models"`
	Sessions           []ModelAccessSession `json:"sessions"`
	Licenses           []ModelAccessLicense `json:"licenses"`
	LegacyDirectGrants bool                 `json:"legacyDirectGrants"`
}

// ExpiresAt is the last applicable role grant's deadline, or nil if any role
// grants the model indefinitely. Expiry of a redundant grant cannot revoke a
// model still allowed by another effective role.
type ModelAccessModel struct {
	ID        string     `json:"id"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// Sessions contain only identifiers and the last live refresh-token deadline.
// Token hashes, access/refresh tokens, passwords and provider fields never enter
// this source. Expired/revoked/deleted bindings need tombstones from the ledger.
type ModelAccessSession struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ModelAccessLicense preserves exact entitlement binding. An active license in
// the deployment is not interchangeable with the ID/digest in a request JWT.
// ExpiresAt uses the existing gateway's grace boundary; nil means perpetual.
type ModelAccessLicense struct {
	ID        string     `json:"id"`
	Digest    string     `json:"digest"`
	Features  []string   `json:"features"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// NextExpiry is source work due to the passage of time, which DB write triggers
// cannot signal. It is not publication freshness or a periodic cache flush.
func (s ModelAccessSource) NextExpiry() *time.Time {
	var next *time.Time
	consider := func(value *time.Time) {
		if value != nil && value.After(s.ObservedAt) && (next == nil || value.Before(*next)) {
			copy := *value
			next = &copy
		}
	}
	consider(s.SubjectExpiresAt)
	for _, model := range s.Models {
		consider(model.ExpiresAt)
	}
	for _, session := range s.Sessions {
		consider(&session.ExpiresAt)
	}
	for _, license := range s.Licenses {
		consider(license.ExpiresAt)
	}
	return next
}

// WithModelAccessSource joins a caller transaction or opens one, takes the
// existing configuration lock BEFORE any signal/stream row lock, then reads all
// source tables in one statement snapshot. The DB timestamp is taken after any
// lock wait, not at transaction start. The callback receives that same transaction
// for durable capture and exact-version acknowledgement; returning an error rolls
// both back. The callback must do no network I/O. It owns CR validation, existing
// binding tombstones, legacy-grant cutover checks and durable publication/ack.
// This method alone never acknowledges a signal or enables the new runtime.
func (s *DeploymentStore) WithModelAccessSource(ctx context.Context, userID string, capture func(*DeploymentStore, ModelAccessSource) error) error {
	if userID == "" || len(userID) > 256 || capture == nil {
		return errors.New("invalid model access source input")
	}
	return s.transaction(ctx, func(tx *DeploymentStore) error {
		// An inherited repeatable-read snapshot can predate a writer we wait for.
		// Refuse it instead of later acknowledging that writer against old allow.
		var isolation string
		if err := tx.db.Raw(`SHOW transaction_isolation`).Scan(&isolation).Error; err != nil {
			return err
		}
		if isolation != "read committed" {
			return errors.New("model access source requires read committed isolation")
		}
		if err := tx.db.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,101))`, s.deploymentID).Error; err != nil {
			return err
		}
		var payload []byte
		if err := tx.db.Raw(modelAccessSourceSQL, s.deploymentID, userID).Row().Scan(&payload); err != nil {
			return err
		}
		var source ModelAccessSource
		if err := json.Unmarshal(payload, &source); err != nil {
			return err
		}
		return capture(tx, source)
	})
}
