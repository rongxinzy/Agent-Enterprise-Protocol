package gatewayaccess

import (
	"bytes"
	"strings"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func TestCanonicalWorkloadEntitlementCannotBeStrippedOrSubstituted(t *testing.T) {
	s := snapshotFixture()
	s.Subject.Kind = "agent"
	s.Binding = gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
		Namespace: "employees", ServiceAccountName: "employee-a", ServiceAccountUID: "sa-a", EmployeeUID: "employee-a", Audience: "model-access",
	}}
	s.Licenses = []gateway.ModelAccessLicense{{ID: "license", Digest: "sha256:" + strings.Repeat("a", 64)}}
	s.WorkloadEntitlement = &gateway.ModelAccessWorkloadEntitlement{
		LicenseID: "license", LicenseDigest: s.Licenses[0].Digest, Feature: "enterprise.models",
	}
	payload, hash, err := s.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(payload, hash)
	if err != nil || decoded.WorkloadEntitlement == nil || *decoded.WorkloadEntitlement != *s.WorkloadEntitlement {
		t.Fatal("immutable snapshot lost workload entitlement")
	}
	decoded.WorkloadEntitlement = nil
	stripped, changedHash, err := decoded.Canonical()
	if err != nil || hash == changedHash || decoded.BindingKey() != s.BindingKey() {
		t.Fatal("entitlement is not semantic policy independent of binding identity")
	}
	if _, err := Decode(stripped, hash); err == nil {
		t.Fatal("stripped attestation reused an old immutable content hash")
	}
	for _, old := range []string{"enterprise.models", s.WorkloadEntitlement.LicenseDigest, `"licenseId":"license"`} {
		if _, err := Decode(bytes.Replace(payload, []byte(old), []byte("other"), 1), hash); err == nil {
			t.Fatal("mutated attestation accepted")
		}
	}
	decoded = s
	decoded.Binding = gateway.ModelAccessBinding{Kind: "session", SessionID: "session"}
	if _, _, err := decoded.Canonical(); err == nil {
		t.Fatal("session accepted workload-only attestation")
	}
}
