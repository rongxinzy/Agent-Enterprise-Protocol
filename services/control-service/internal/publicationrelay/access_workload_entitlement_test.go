package publicationrelay

import (
	"errors"
	"strings"
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func TestAccessRelayPreservesExactWorkloadEntitlement(t *testing.T) {
	p := testProjector(t)
	item := accessDelivery(t, 1, "employee-agent", "unused", false)
	policy, err := gatewayaccess.Decode(item.Payload, item.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	policy.Subject.Kind = "agent"
	policy.Binding = gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
		Namespace: "employees", ServiceAccountName: "employee-a", ServiceAccountUID: "sa-a", EmployeeUID: "employee-a", Audience: "model-access",
	}}
	policy.WorkloadEntitlement = &gateway.ModelAccessWorkloadEntitlement{
		LicenseID: policy.Licenses[0].ID, LicenseDigest: policy.Licenses[0].Digest, Feature: "enterprise.models",
	}
	item.Payload, item.ContentHash, err = policy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	item.BindingKey = policy.BindingKey()
	if err := p.ProjectAccess(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	live := readAccess(t, p, item)
	if !apiequality.Semantic.DeepEqual(live.Spec.WorkloadEntitlement, policy.WorkloadEntitlement) {
		t.Fatal("relay lost exact workload license attestation")
	}
	if err := p.ProjectAccess(t.Context(), item); err != nil || readAccess(t, p, item).ResourceVersion != live.ResourceVersion {
		t.Fatalf("retry changed immutable entitlement delivery: %v", err)
	}
	// Exact read-back and replay guards must also cover the newly added field.
	live.Spec.WorkloadEntitlement.LicenseDigest = "sha256:" + strings.Repeat("b", 64)
	if err := p.Client.Update(t.Context(), &live); err != nil {
		t.Fatal(err)
	}
	if err := p.ProjectAccess(t.Context(), item); !errors.Is(err, ErrOwnership) {
		t.Fatalf("same-version attestation corruption was repaired silently: %v", err)
	}
	var stored gateway.ModelAccessGrant
	if err := p.Client.Get(t.Context(), client.ObjectKeyFromObject(&live), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.WorkloadEntitlement.LicenseDigest != live.Spec.WorkloadEntitlement.LicenseDigest {
		t.Fatal("relay clobbered a conflict rather than rejecting it")
	}
}
