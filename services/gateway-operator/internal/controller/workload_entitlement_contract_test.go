package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reuse the contract suite's real API server, not fake.Client admission.
func checkWorkloadEntitlementAdmission(t *testing.T, c client.Client, workload *gateway.ModelAccessGrant) {
	t.Helper()
	ctx := context.Background()
	workload.Spec.WorkloadEntitlement = &gateway.ModelAccessWorkloadEntitlement{
		LicenseID: workload.Spec.Licenses[0].ID, LicenseDigest: workload.Spec.Licenses[0].Digest, Feature: "enterprise.models",
	}
	workload.Spec.Publication.Sequence++
	if err := c.Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
	var stored gateway.ModelAccessGrant
	if err := c.Get(ctx, client.ObjectKeyFromObject(workload), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(workload.Spec.WorkloadEntitlement, stored.Spec.WorkloadEntitlement) {
		t.Fatal("API server pruned workload entitlement")
	}
	copy := stored.DeepCopy()
	copy.Spec.WorkloadEntitlement.LicenseID = "different"
	if stored.Spec.WorkloadEntitlement.LicenseID != workload.Spec.WorkloadEntitlement.LicenseID {
		t.Fatal("deepcopy shares workload entitlement")
	}
	for name, mutate := range map[string]func(*gateway.ModelAccessGrant){
		"missing license": func(g *gateway.ModelAccessGrant) { g.Spec.Licenses = nil },
		"unknown license": func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseID = "unknown" },
		"different digest": func(g *gateway.ModelAccessGrant) {
			g.Spec.WorkloadEntitlement.LicenseDigest = "sha256:" + strings.Repeat("b", 64)
		},
		"malformed digest":      func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseDigest = "invalid" },
		"empty license":         func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.LicenseID = "" },
		"missing feature":       func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.Feature = "" },
		"unlicensed feature":    func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement.Feature = "enterprise.skills" },
		"same-version mutation": func(g *gateway.ModelAccessGrant) { g.Spec.WorkloadEntitlement = nil },
	} {
		t.Run("workload entitlement/"+name, func(t *testing.T) {
			candidate := workload.DeepCopy()
			if name != "same-version mutation" {
				candidate.Spec.Publication.Sequence++
			}
			mutate(candidate)
			if err := c.Update(ctx, candidate); !apierrors.IsInvalid(err) {
				t.Fatalf("unsafe entitlement admitted: %v", err)
			}
		})
	}
	session := accessContractFixture()
	session.ObjectMeta = metav1.ObjectMeta{Name: "session-workload-entitlement", Namespace: "default"}
	session.Spec.WorkloadEntitlement = workload.Spec.WorkloadEntitlement.DeepCopy()
	if err := c.Create(ctx, session); !apierrors.IsInvalid(err) {
		t.Fatalf("session accepted workload attestation: %v", err)
	}
	workload.Spec.Publication.Sequence++
	workload.Spec.Disabled, workload.Spec.Models = true, []string{}
	workload.Spec.Licenses, workload.Spec.WorkloadEntitlement = nil, nil
	if err := c.Update(ctx, workload); err != nil {
		t.Fatalf("revocation tombstone rejected: %v", err)
	}
}
