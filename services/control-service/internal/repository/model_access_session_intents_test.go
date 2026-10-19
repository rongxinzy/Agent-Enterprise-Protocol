package repository

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func TestSessionIntentsCanonicalOrderAndTimeBounds(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second).Add(100 * time.Millisecond)
	source := ModelAccessSource{DeploymentID: "deployment", SubjectID: "alice", Kind: "human", Exists: true, ObservedAt: now,
		RoleIDs: []string{"z-role", "a-role"}, TeamIDs: []string{"z-team", "a-team"},
		Models:   []ModelAccessModel{{ID: "z-chat"}, {ID: "a-chat"}},
		Licenses: []ModelAccessLicense{{ID: "z-license", Digest: "sha256:" + strings.Repeat("a", 64)}, {ID: "a-license", Digest: "sha256:" + strings.Repeat("b", 64)}},
		Sessions: []ModelAccessSession{{ID: "session", ExpiresAt: now.Add(time.Hour)}}}
	values, err := sessionAccessIntents(source, nil, 1, time.Minute, false)
	if err != nil || len(values) != 1 {
		t.Fatal(err)
	}
	policy, err := gatewayaccess.Decode(values[0].Snapshot.Payload, values[0].Snapshot.ContentHash)
	if err != nil || !slices.Equal(policy.Models, []string{"a-chat", "z-chat"}) || !slices.Equal(policy.RoleIDs, []string{"a-role", "z-role"}) || policy.Licenses[0].ID != "a-license" {
		t.Fatal("source DB collation/permutation leaked into canonical policy bytes")
	}
	if source.RoleIDs[0] != "z-role" || source.TeamIDs[0] != "z-team" {
		t.Fatal("canonicalization mutated the source snapshot")
	}
	if !values[0].IssuedAt.Equal(now.Truncate(time.Second)) || values[0].ValidUntil.Sub(values[0].IssuedAt) != time.Minute {
		t.Fatal("CR time precision or publication cap changed")
	}
	soon := now.Add(20 * time.Millisecond)
	source.SubjectExpiresAt = &soon
	if _, err := sessionAccessIntents(source, nil, 1, time.Minute, false); !errors.Is(err, ErrAccessExpiring) {
		t.Fatal("subsecond security deadline was rounded up")
	}
}

func TestAccessCaptureRejectsInvalidFreshness(t *testing.T) {
	// Validation must fail before dereferencing the store or reading any source.
	var store *DeploymentStore
	for _, freshness := range []time.Duration{0, time.Second, 2500 * time.Millisecond, 2 * time.Hour} {
		if _, err := store.CaptureModelAccessSessions(t.Context(), "alice", freshness); !errors.Is(err, ErrAccessPublicationInput) {
			t.Fatal("invalid freshness accepted")
		}
	}
}
