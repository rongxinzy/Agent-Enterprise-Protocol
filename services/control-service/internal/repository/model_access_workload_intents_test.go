package repository

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func workloadSourceFixture() ModelAccessSource {
	now := time.Now().UTC().Truncate(time.Second)
	w := workloadObservation()
	w.VerifiedUntil = now.Add(30 * time.Second)
	return ModelAccessSource{DeploymentID: "source", SubjectID: "employee", Kind: "agent", Exists: true,
		ObservedAt: now, Models: []ModelAccessModel{{ID: "chat"}}, Workloads: []ObservedModelWorkload{w},
		Licenses: []ModelAccessLicense{{ID: "fixture-model-license", Digest: "sha256:" + strings.Repeat("a", 64), Features: []string{"enterprise.models"}}}}
}

func priorWorkload(t *testing.T, source ModelAccessSource) ModelAccessPublication {
	t.Helper()
	intents, err := mixedAccessIntents(source, nil, 1, time.Minute, false)
	if err != nil || len(intents) != 1 {
		t.Fatal("fixture policy failed", err)
	}
	i := intents[0]
	return ModelAccessPublication{DeploymentID: source.DeploymentID, SubjectID: source.SubjectID, BindingKey: i.BindingKey,
		ContentHash: i.Snapshot.ContentHash, Payload: i.Snapshot.Payload, IssuedAt: i.IssuedAt, ValidUntil: i.ValidUntil}
}

func TestWorkloadIntentsRejectUntrustedIdentityAndPriorReplay(t *testing.T) {
	for _, name := range []string{"human", "missing subject", "bad SA", "expired allow", "future paused", "ambiguous", "foreign prior", "wrong key", "bad bytes", "retired replay"} {
		t.Run(name, func(t *testing.T) {
			source := workloadSourceFixture()
			prior := priorWorkload(t, source)
			switch name {
			case "human":
				source.Kind = "human"
			case "missing subject":
				source.Exists = false
			case "bad SA":
				source.Workloads[0].Identity.ServiceAccountName = "invalid SA"
			case "expired allow":
				source.Workloads[0].VerifiedUntil = source.ObservedAt.Add(-time.Second)
			case "future paused":
				source.UnverifiedWorkloads, source.Workloads = source.Workloads, nil
			case "ambiguous":
				source.UnverifiedWorkloads = source.Workloads
			case "foreign prior":
				prior.DeploymentID = "foreign"
			case "wrong key":
				prior.BindingKey = "wrong-binding-key"
			case "bad bytes":
				prior.Payload = []byte(`{"forged":true}`)
			case "retired replay":
				policy := capturedPolicy(t, prior)
				policy.Disabled, policy.Retired, policy.Models = true, true, []string{}
				var err error
				prior.Payload, prior.ContentHash, err = policy.Canonical()
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := mixedAccessIntents(source, []ModelAccessPublication{prior}, 2, time.Minute, false); !errors.Is(err, ErrAccessBinding) {
				t.Fatal("untrusted or retired workload was accepted", err)
			}
		})
	}
}

func TestWorkloadIntentsDeadlineAndRemovedIdentity(t *testing.T) {
	source := workloadSourceFixture()
	first := priorWorkload(t, source)
	if !first.ValidUntil.Equal(source.Workloads[0].VerifiedUntil) {
		t.Fatal("allow outlives trusted lifecycle observation")
	}
	source.Workloads = nil
	values, err := mixedAccessIntents(source, []ModelAccessPublication{first}, 2, time.Minute, false)
	if err != nil || len(values) != 1 {
		t.Fatal(err)
	}
	p, err := gatewayaccess.Decode(values[0].Snapshot.Payload, values[0].Snapshot.ContentHash)
	if err != nil || !p.Retired || !p.Disabled || len(p.Models) != 0 || p.BindingKey() != first.BindingKey {
		t.Fatal("removed identity lost immutable tombstone", err)
	}
	source = workloadSourceFixture()
	soon := source.ObservedAt.Add(500 * time.Millisecond)
	source.Workloads[0].VerifiedUntil = soon
	if _, err := mixedAccessIntents(source, nil, 1, time.Minute, false); !errors.Is(err, ErrAccessExpiring) {
		t.Fatal("subsecond observation deadline was extended", err)
	}
}
