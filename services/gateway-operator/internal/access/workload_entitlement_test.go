package access

import (
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func entitledWorkload() *gateway.ModelAccessGrant {
	g := workloadFixture()
	g.Spec.Licenses = []gateway.ModelAccessLicense{{ID: "employee-license", Digest: "sha256:" + strings.Repeat("a", 64)}}
	g.Spec.WorkloadEntitlement = &gateway.ModelAccessWorkloadEntitlement{
		LicenseID: g.Spec.Licenses[0].ID, LicenseDigest: g.Spec.Licenses[0].Digest, Feature: "enterprise.models",
	}
	return g
}

func TestWorkloadEntitlementRequiresExplicitExactEvidence(t *testing.T) {
	o := workloadOptions()
	o.RequireEntitlement = true
	for name, mutate := range map[string]func(*gateway.ModelAccessGrant){
		"SA alone":            func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement = nil },
		"no accepted license": func(g *gateway.ModelAccessGrant) { g.Spec.Licenses = nil },
		"different license":   func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseID = "another-license" },
		"different digest": func(g *gateway.ModelAccessGrant) {
			g.Spec.WorkloadEntitlement.LicenseDigest = "sha256:" + strings.Repeat("b", 64)
		},
		"malformed digest":    func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseDigest = "sha256:invalid" },
		"empty id":            func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseID = "" },
		"oversized id":        func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseID = strings.Repeat("a", 257) },
		"missing feature":     func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.Feature = "" },
		"other feature":       func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.Feature = "enterprise.skills" },
		"duplicate selection": func(g *gateway.ModelAccessGrant) { g.Spec.Licenses = append(g.Spec.Licenses, g.Spec.Licenses[0]) },
		"zero deadline":       func(g *gateway.ModelAccessGrant) { g.Spec.Licenses[0].ValidUntil = &metav1.Time{} },
		"human session": func(g *gateway.ModelAccessGrant) {
			g.Spec.Subject.Kind = "human"
			g.Spec.Binding = gateway.ModelAccessBinding{Kind: "session", SessionID: "human-session"}
			g.Name = "access-" + bindingKey(g.Spec)
			markFixture(g)
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := entitledWorkload()
			mutate(g)
			if _, _, err := Render(g, o); err == nil {
				t.Fatal("workload entitlement boundary bypassed")
			}
		})
	}
	// Even an unlicensed development deployment cannot ignore malformed explicit
	// evidence. Omission remains its existing opt-in workload behavior.
	g := entitledWorkload()
	g.Spec.WorkloadEntitlement.Feature = "other"
	if _, _, err := Render(g, workloadOptions()); err == nil {
		t.Fatal("explicit invalid evidence ignored in development mode")
	}
}

func TestWorkloadEntitlementDeadlineAndCacheIsolation(t *testing.T) {
	g, o := entitledWorkload(), workloadOptions()
	o.RequireEntitlement = true
	deadline := metav1.NewTime(g.Spec.IssuedAt.Add(time.Minute))
	g.Spec.Licenses[0].ValidUntil = &deadline
	// Another perpetual license must not replace the explicitly selected one.
	g.Spec.Licenses = append(g.Spec.Licenses, gateway.ModelAccessLicense{ID: "other-active", Digest: "sha256:" + strings.Repeat("b", 64)})
	config, policy, err := Render(g, o)
	if err != nil {
		t.Fatal(err)
	}
	rego := policy.Data[g.Name+".rego"]
	if !strings.Contains(rego, "workload_license_current\n") ||
		!strings.Contains(rego, "workload_license_current { ") || !strings.Contains(rego, " > time.now_ns() / 1000000000 }") ||
		strings.Contains(rego, "workload_license_current { true }") {
		t.Fatal("selected finite license lacks an uncached absolute deadline")
	}
	if _, found, _ := unstructured.NestedMap(config.Object, "spec", "authentication", "workload", "kubernetesTokenReview"); !found {
		t.Fatal("entitlement replaced native workload authentication")
	}
	hash := config.GetAnnotations()[SemanticHash]
	g.Spec.Publication.Sequence++
	g.Spec.ValidUntil.Time = g.Spec.ValidUntil.Add(time.Minute)
	markFixture(g)
	renewed, _, err := Render(g, o)
	if err != nil || hash != renewed.GetAnnotations()[SemanticHash] {
		t.Fatalf("freshness renewal changed semantic policy: %v", err)
	}
	// Exact license changes invalidate this binding; a different workload does
	// not participate in its hash or authentication cache key.
	g.Spec.WorkloadEntitlement.LicenseID = g.Spec.Licenses[1].ID
	g.Spec.WorkloadEntitlement.LicenseDigest = g.Spec.Licenses[1].Digest
	replaced, perpetual, err := Render(g, o)
	if err != nil || hash == replaced.GetAnnotations()[SemanticHash] ||
		!strings.Contains(perpetual.Data[g.Name+".rego"], "workload_license_current { true }") {
		t.Fatalf("explicit replacement did not update license semantics: %v", err)
	}
}

func TestWorkloadLicenseRevocationCanPublishDenyTombstone(t *testing.T) {
	g, o := entitledWorkload(), workloadOptions()
	o.RequireEntitlement = true
	g.Spec.Disabled, g.Spec.Models = true, []string{}
	g.Spec.Licenses, g.Spec.WorkloadEntitlement = nil, nil
	config, policy, err := Render(g, o)
	if err != nil || !strings.Contains(policy.Data[g.Name+".rego"], "workload_license_current { false }") {
		t.Fatalf("missing license blocked fail-closed tombstone delivery: %v", err)
	}
	rego, _, _ := unstructured.NestedString(config.Object, "spec", "authorization", "access", "opa", "rego")
	if !strings.Contains(rego, "not true") {
		t.Fatal("disabled workload does not deny")
	}
}
