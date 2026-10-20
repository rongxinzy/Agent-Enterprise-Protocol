package repository

import (
	"slices"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func testWorkloadLicense(id, digest string, expires *time.Time, features ...string) ModelAccessLicense {
	return ModelAccessLicense{ID: id, Digest: "sha256:" + strings.Repeat(digest, 64), ExpiresAt: expires, Features: features}
}

func TestWorkloadLicenseRequiresExactFeatureAndLiveBound(t *testing.T) {
	for _, name := range []string{"missing", "legacy feature", "prefix", "expired", "equal", "subsecond", "disabled"} {
		t.Run(name, func(t *testing.T) {
			source := workloadSourceFixture()
			license := &source.Licenses[0]
			switch name {
			case "missing":
				source.Licenses = nil
			case "legacy feature":
				license.Features = []string{"model", "chat"}
			case "prefix":
				license.Features = []string{"enterprise.models.extra"}
			case "expired", "equal", "subsecond":
				at := source.ObservedAt
				switch name {
				case "expired":
					at = at.Add(-time.Second)
				case "subsecond":
					at = at.Add(500 * time.Millisecond)
				}
				license.ExpiresAt = &at
			case "disabled":
				source.Disabled = true
			}
			values, err := mixedAccessIntents(source, nil, 1, time.Minute, false)
			if err != nil || len(values) != 1 {
				t.Fatal("deny publication could not be captured", err)
			}
			p, err := gatewayaccess.Decode(values[0].Snapshot.Payload, values[0].Snapshot.ContentHash)
			if err != nil || !p.Disabled || p.Retired || len(p.Models) != 0 || len(p.Licenses) != 0 ||
				len(p.RoleIDs) != 0 || len(p.TeamIDs) != 0 || p.WorkloadEntitlement != nil {
				t.Fatal("invalid license retained authority or permanently retired identity", err)
			}
		})
	}
}

func TestWorkloadLicenseDeterministicSelectionPreservesPriorExactLicense(t *testing.T) {
	source := workloadSourceFixture()
	expires := source.ObservedAt.Add(10 * time.Second)
	source.Licenses = []ModelAccessLicense{
		testWorkloadLicense("z-selected", "a", &expires, "enterprise.models"),
		testWorkloadLicense("a-other-feature", "b", nil, "enterprise.skills"),
	}
	first := priorWorkload(t, source)
	p := capturedPolicy(t, first)
	if p.WorkloadEntitlement == nil || p.WorkloadEntitlement.LicenseID != "z-selected" || len(p.Licenses) != 1 || !first.ValidUntil.Equal(expires) {
		t.Fatal("explicit license or its natural grace deadline was not published")
	}
	// A newly added earlier-sorting license and a changed non-model license
	// cannot replace an otherwise eligible exact selection or reset its cache.
	soon := source.ObservedAt.Add(2 * time.Second)
	source.Licenses[1].ExpiresAt = &soon
	source.Licenses = append(source.Licenses, testWorkloadLicense("b-new", "c", nil, "enterprise.models"))
	values, err := mixedAccessIntents(source, []ModelAccessPublication{first}, 2, time.Minute, false)
	if err != nil || values[0].Snapshot.ContentHash != first.ContentHash || !values[0].ValidUntil.Equal(first.ValidUntil) {
		t.Fatal("unrelated license changed selected policy or shortened its deadline", err)
	}
	// Losing the exact feature requires a new immutable publication, never
	// implicit request-side substitution. The next eligible ID is deterministic.
	source.Licenses[0].Features = nil
	values, err = mixedAccessIntents(source, []ModelAccessPublication{first}, 3, time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err = gatewayaccess.Decode(values[0].Snapshot.Payload, values[0].Snapshot.ContentHash)
	if err != nil || values[0].Snapshot.ContentHash == first.ContentHash || p.WorkloadEntitlement.LicenseID != "b-new" || p.BindingKey() != first.BindingKey {
		t.Fatal("replacement did not publish a newer exact selection on the same identity", err)
	}
	slices.Reverse(source.Licenses)
	state, evidence := workloadLicenseSource(source, nil)
	if state.Disabled || evidence == nil || evidence.LicenseID != "b-new" {
		t.Fatal("initial selection depends on input order")
	}
}

func TestWorkloadLicenseCannotBorrowPreviousEvidenceAndLeavesSessionsUntouched(t *testing.T) {
	source := workloadSourceFixture()
	source.Sessions = []ModelAccessSession{{ID: "employee-session", ExpiresAt: source.ObservedAt.Add(time.Hour)}}
	prior := priorWorkload(t, workloadSourceFixture())
	source.Licenses = []ModelAccessLicense{testWorkloadLicense("different-feature", "b", nil, "enterprise.skills")}
	values, err := mixedAccessIntents(source, []ModelAccessPublication{prior}, 2, time.Minute, false)
	if err != nil || len(values) != 2 {
		t.Fatal(err)
	}
	for _, value := range values {
		p, err := gatewayaccess.Decode(value.Snapshot.Payload, value.Snapshot.ContentHash)
		if err != nil {
			t.Fatal(err)
		}
		if p.Binding.Kind == "session" {
			if p.Disabled || p.WorkloadEntitlement != nil || len(p.Models) != 1 || len(p.Licenses) != 1 || p.Licenses[0].ID != "different-feature" {
				t.Fatal("workload selection changed the existing signed session contract")
			}
		} else if !p.Disabled || p.Retired || p.WorkloadEntitlement != nil || len(p.Licenses) != 0 {
			t.Fatal("missing source license borrowed prior evidence")
		}
	}
	// A previous matching ID with the wrong digest is not a preferred match.
	source = workloadSourceFixture()
	source.Licenses = []ModelAccessLicense{
		testWorkloadLicense("z", "a", nil, "enterprise.models"),
		testWorkloadLicense("a", "b", nil, "enterprise.models"),
	}
	_, selected := workloadLicenseSource(source, &gateway.ModelAccessWorkloadEntitlement{
		LicenseID: "z", LicenseDigest: "sha256:" + strings.Repeat("c", 64), Feature: "enterprise.models",
	})
	if selected == nil || selected.LicenseID != "a" {
		t.Fatal("stale digest retained a preferred identity")
	}
}

func TestWorkloadLicensedRetirementClearsAttestation(t *testing.T) {
	source := workloadSourceFixture()
	prior := priorWorkload(t, source)
	source.Workloads = nil
	values, err := mixedAccessIntents(source, []ModelAccessPublication{prior}, 2, time.Minute, false)
	if err != nil || len(values) != 1 {
		t.Fatal("license removal blocked retirement delivery", err)
	}
	p, err := gatewayaccess.Decode(values[0].Snapshot.Payload, values[0].Snapshot.ContentHash)
	if err != nil || !p.Retired || !p.Disabled || p.WorkloadEntitlement != nil || len(p.Licenses) != 0 {
		t.Fatal("retirement retained license attestation", err)
	}
}
