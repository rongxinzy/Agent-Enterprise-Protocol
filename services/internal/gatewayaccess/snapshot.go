// Package gatewayaccess defines immutable publication bytes for derived access.
// It is a compiler/transport contract, not an authentication or cache runtime.
package gatewayaccess

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

var ErrSnapshot = errors.New("invalid effective access snapshot")

var (
	modelID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	digest  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Snapshot excludes publication markers and freshness timestamps: those are
// immutable ledger columns, added to the CR by the existing relay. A changed
// source version/observation time alone cannot alter canonical policy bytes.
// Retired records a terminal binding tombstone; its identifier must never be
// reused to reactivate an old signed session. It is not a new grant condition.
type Snapshot struct {
	Schema              string                                  `json:"schema"`
	DeploymentID        string                                  `json:"deploymentId"`
	Subject             gateway.ModelAccessSubject              `json:"subject"`
	Binding             gateway.ModelAccessBinding              `json:"binding"`
	Disabled            bool                                    `json:"disabled"`
	Retired             bool                                    `json:"retired"`
	Models              []string                                `json:"models"`
	PodUIDs             []string                                `json:"workloadPodUids,omitempty"`
	RoleIDs             []string                                `json:"roleIds,omitempty"`
	TeamIDs             []string                                `json:"teamIds,omitempty"`
	Licenses            []gateway.ModelAccessLicense            `json:"licenses,omitempty"`
	WorkloadEntitlement *gateway.ModelAccessWorkloadEntitlement `json:"workloadEntitlement,omitempty"`
}

func bounded(value string, max int) bool { return len(value) > 0 && len(value) <= max }

func canonicalIDs(ids []string, maxCount, maxLength int) bool {
	if len(ids) > maxCount || !slices.IsSorted(ids) {
		return false
	}
	for i, id := range ids {
		if !bounded(id, maxLength) || i > 0 && id == ids[i-1] {
			return false
		}
	}
	return true
}

func (s Snapshot) valid() bool {
	if s.Schema != "aep-model-access/v1" || !bounded(s.DeploymentID, 80) || !bounded(s.Subject.ID, 256) ||
		(s.Subject.Kind != "human" && s.Subject.Kind != "agent") || s.Models == nil ||
		!ValidWorkloadPods(s.Binding.Kind, s.PodUIDs) || !canonicalIDs(s.PodUIDs, 512, 128) ||
		!canonicalIDs(s.Models, 512, 40) || !canonicalIDs(s.RoleIDs, 256, 256) || !canonicalIDs(s.TeamIDs, 256, 256) ||
		len(s.Licenses) > 64 || s.Disabled && len(s.Models) != 0 || s.Retired && !s.Disabled {
		return false
	}
	for _, model := range s.Models {
		if !modelID.MatchString(model) {
			return false
		}
	}
	for i, license := range s.Licenses {
		if !bounded(license.ID, 256) || !digest.MatchString(license.Digest) || i > 0 && license.ID <= s.Licenses[i-1].ID ||
			license.ValidUntil != nil && license.ValidUntil.IsZero() {
			return false
		}
	}
	if _, valid := SelectedWorkloadLicense(s.Binding.Kind, s.WorkloadEntitlement, s.Licenses); !valid {
		return false
	}
	switch s.Binding.Kind {
	case "session":
		return bounded(s.Binding.SessionID, 256) && s.Binding.Workload == nil
	case "kubernetes":
		w := s.Binding.Workload
		return s.Subject.Kind == "agent" && s.Binding.SessionID == "" && w != nil &&
			bounded(w.Namespace, 63) && bounded(w.ServiceAccountName, 253) &&
			bounded(string(w.ServiceAccountUID), 128) && bounded(string(w.EmployeeUID), 128) && bounded(w.Audience, 256)
	default:
		return false
	}
}

// Canonical validates sets and schema before hashing; callers cannot silently
// truncate oversized authorization or publish malformed binding identifiers.
func (s Snapshot) Canonical() ([]byte, string, error) {
	if !s.valid() {
		return nil, "", ErrSnapshot
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, "", ErrSnapshot
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}

func Decode(payload []byte, contentHash string) (Snapshot, error) {
	if len(payload) > 1048576 {
		return Snapshot{}, ErrSnapshot
	}
	var s Snapshot
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return Snapshot{}, ErrSnapshot
	}
	canonical, hash, err := s.Canonical()
	if err != nil || hash != contentHash || !bytes.Equal(canonical, payload) {
		return Snapshot{}, ErrSnapshot
	}
	return s, nil
}

// BindingKey hashes the full immutable identity tuple, independently of grants,
// credentials, source versions and time. Canonical must succeed before delivery.
func (s Snapshot) BindingKey() string {
	value, _ := json.Marshal(struct {
		DeploymentID string                     `json:"deploymentId"`
		Subject      gateway.ModelAccessSubject `json:"subject"`
		Binding      gateway.ModelAccessBinding `json:"binding"`
	}{s.DeploymentID, s.Subject, s.Binding})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
