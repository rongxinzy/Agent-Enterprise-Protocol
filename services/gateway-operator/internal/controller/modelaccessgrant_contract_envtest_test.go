package controller

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func accessContractFixture() *gateway.ModelAccessGrant {
	now := time.Now().UTC().Truncate(time.Second)
	return &gateway.ModelAccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "session-grant", Namespace: "default"}, Spec: gateway.ModelAccessGrantSpec{
		DeploymentID: "deployment", Subject: gateway.ModelAccessSubject{Kind: "human", ID: "alice"},
		Binding:     gateway.ModelAccessBinding{Kind: "session", SessionID: "session-a"},
		Publication: gateway.PublicationReference{Sequence: 2, OperationID: "11111111-1111-1111-1111-111111111111", Revision: "revision-2", ContentHash: strings.Repeat("a", 64)},
		IssuedAt:    metav1.NewTime(now), ValidUntil: metav1.NewTime(now.Add(5 * time.Minute)),
		Models: []string{"model-a"}, RoleIDs: []string{"role-a"}, TeamIDs: []string{"team-a"},
		Licenses: []gateway.ModelAccessLicense{{ID: "license-a", Digest: "sha256:" + strings.Repeat("a", 64)}},
	}}
}

// Admission uses a real API server: fake.Client does not enforce CEL or status
// subresource isolation and therefore cannot prove these replay boundaries.
func TestModelAccessGrantContractAdmission(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	grant := accessContractFixture()
	if err := c.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gateway.ModelAccessGrant)
	}{
		{"sequence rollback", func(g *gateway.ModelAccessGrant) { g.Spec.Publication.Sequence = 1 }},
		{"same-version privilege expansion", func(g *gateway.ModelAccessGrant) { g.Spec.Models = append(g.Spec.Models, "model-b") }},
		{"same-version content replacement", func(g *gateway.ModelAccessGrant) { g.Spec.Publication.ContentHash = strings.Repeat("b", 64) }},
		{"same-version license replacement", func(g *gateway.ModelAccessGrant) { g.Spec.Licenses[0].Digest = "sha256:" + strings.Repeat("b", 64) }},
		{"subject rebinding", func(g *gateway.ModelAccessGrant) { g.Spec.Subject.ID = "bob" }},
		{"session rebinding", func(g *gateway.ModelAccessGrant) { g.Spec.Binding.SessionID = "session-b" }},
		{"deployment rebinding", func(g *gateway.ModelAccessGrant) { g.Spec.DeploymentID = "other" }},
		{"disabled allow-set", func(g *gateway.ModelAccessGrant) { g.Spec.Disabled = true }},
		{"nonpositive validity", func(g *gateway.ModelAccessGrant) { g.Spec.ValidUntil = g.Spec.IssuedAt }},
		{"unbounded validity", func(g *gateway.ModelAccessGrant) {
			g.Spec.ValidUntil = metav1.NewTime(g.Spec.IssuedAt.Add(2 * time.Hour))
		}},
		{"duplicate models", func(g *gateway.ModelAccessGrant) { g.Spec.Models = []string{"model-a", "model-a"} }},
		{"invalid model identifier", func(g *gateway.ModelAccessGrant) { g.Spec.Models = []string{"provider/model"} }},
		{"invalid license digest", func(g *gateway.ModelAccessGrant) { g.Spec.Licenses[0].Digest = "wrong" }},
		{"missing license id", func(g *gateway.ModelAccessGrant) { g.Spec.Licenses[0].ID = "" }},
		{"duplicate license binding", func(g *gateway.ModelAccessGrant) { g.Spec.Licenses = append(g.Spec.Licenses, g.Spec.Licenses[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := grant.DeepCopy()
			// Shape/identity mutations must remain invalid even with a fresh version.
			if !strings.HasPrefix(test.name, "same-version") && test.name != "sequence rollback" {
				candidate.Spec.Publication.Sequence++
			}
			test.mutate(candidate)
			if err := c.Update(ctx, candidate); !apierrors.IsInvalid(err) {
				t.Fatalf("unsafe update accepted: %v", err)
			}
		})
	}
	// A genuine license replacement must carry a fresh publication. Finite and
	// perpetual licenses remain distinguishable; neither embeds a signed payload.
	deadline := metav1.NewTime(grant.Spec.IssuedAt.Add(time.Minute))
	grant.Spec.Licenses = []gateway.ModelAccessLicense{{ID: "license-b", Digest: "sha256:" + strings.Repeat("b", 64), ValidUntil: &deadline}}
	grant.Spec.Publication.Sequence++
	if err := c.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	var licensed gateway.ModelAccessGrant
	if err := c.Get(ctx, client.ObjectKeyFromObject(grant), &licensed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(grant.Spec.Licenses, licensed.Spec.Licenses) {
		t.Fatal("license binding or grace deadline was pruned")
	}
	copy := grant.DeepCopy()
	copy.Spec.Licenses[0].ValidUntil.Time = copy.Spec.Licenses[0].ValidUntil.Add(time.Minute)
	if !grant.Spec.Licenses[0].ValidUntil.Equal(&deadline) {
		t.Fatal("deepcopy shared a mutable license deadline")
	}

	// A higher version can revoke, and a repeated identical write is idempotent.
	grant.Spec.Disabled = true
	grant.Spec.Models = []string{}
	grant.Spec.Publication.Sequence++
	if err := c.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	before := grant.Spec.DeepCopy()
	grant.Spec.Disabled = false // Status updates must not restore privileges.
	grant.Status.ObservedGeneration = grant.Generation
	grant.Status.ObservedPublication = &before.Publication
	if err := c.Status().Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	var stored gateway.ModelAccessGrant
	if err := c.Get(ctx, client.ObjectKeyFromObject(grant), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Spec, *before) || stored.Status.ObservedPublication == nil || stored.Status.ObservedPublication.Sequence != before.Publication.Sequence {
		t.Fatal("status write changed the grant or lost observation")
	}

	workload := accessContractFixture()
	workload.Name = "employee-grant"
	workload.Spec.Subject.Kind = "agent"
	workload.Spec.Binding = gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
		Namespace: "employees", ServiceAccountName: "employee-a", ServiceAccountUID: "sa-uid", EmployeeUID: "employee-uid", Audience: "aep-model-gateway",
	}}
	if err := c.Create(ctx, workload); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gateway.ModelAccessGrant)
	}{
		{"human-workload", func(g *gateway.ModelAccessGrant) { g.Spec.Subject.Kind = "human" }},
		{"mixed-binding", func(g *gateway.ModelAccessGrant) { g.Spec.Binding.SessionID = "session-a" }},
		{"missing-workload", func(g *gateway.ModelAccessGrant) { g.Spec.Binding.Workload = nil }},
		{"missing-service-account-uid", func(g *gateway.ModelAccessGrant) { g.Spec.Binding.Workload.ServiceAccountUID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := workload.DeepCopy()
			candidate.ObjectMeta = metav1.ObjectMeta{Name: test.name, Namespace: "default"}
			test.mutate(candidate)
			if err := c.Create(ctx, candidate); !apierrors.IsInvalid(err) {
				t.Fatalf("unsafe binding accepted: %v", err)
			}
		})
	}
}
