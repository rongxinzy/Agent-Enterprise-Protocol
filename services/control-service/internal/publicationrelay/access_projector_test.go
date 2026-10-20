package publicationrelay

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func accessDelivery(t *testing.T, seq int64, subject, session string, disabled bool) repository.ModelAccessDelivery {
	t.Helper()
	policy := gatewayaccess.Snapshot{Schema: "aep-model-access/v1", DeploymentID: "demo",
		Subject: gateway.ModelAccessSubject{Kind: "human", ID: subject},
		Binding: gateway.ModelAccessBinding{Kind: "session", SessionID: session},
		Models:  []string{}, Disabled: disabled, Retired: disabled}
	if !disabled {
		policy.Models = []string{"chat"}
		policy.RoleIDs, policy.TeamIDs = []string{"role"}, []string{"team"}
		policy.Licenses = []gateway.ModelAccessLicense{{ID: "license", Digest: "sha256:" + strings.Repeat("a", 64)}}
	}
	payload, hash, err := policy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	return repository.ModelAccessDelivery{ClaimToken: uuid.NewString(), ModelAccessPublication: repository.ModelAccessPublication{
		DeploymentID: "demo", BindingKey: policy.BindingKey(), SubjectID: subject, Sequence: seq, SourceVersion: seq,
		OperationID: uuid.NewString(), Revision: "revision", ContentHash: hash, Payload: payload,
		IssuedAt: now.Add(-time.Second), ValidUntil: now.Add(5 * time.Minute), Phase: "projecting"}}
}

func readAccess(t *testing.T, p *Projector, item repository.ModelAccessDelivery) gateway.ModelAccessGrant {
	t.Helper()
	var value gateway.ModelAccessGrant
	if err := p.Client.Get(t.Context(), client.ObjectKey{Namespace: p.Namespace, Name: "access-" + item.BindingKey}, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAccessProjectionPreservesBindingsLicensesAndTombstones(t *testing.T) {
	p := testProjector(t)
	first := accessDelivery(t, 1, "alice", "one", false)
	other := accessDelivery(t, 1, "alice", "two", false)
	for _, item := range []repository.ModelAccessDelivery{first, other} {
		if err := p.ProjectAccess(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	live := readAccess(t, p, first)
	expected, err := desiredAccess(first)
	if err != nil || !apiequality.Semantic.DeepEqual(live.Spec, expected) || len(live.Spec.Licenses) != 1 || live.Spec.Licenses[0].ID != "license" {
		t.Fatal("access delivery lost immutable binding or exact entitlement")
	}
	if err := p.ProjectAccess(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if readAccess(t, p, first).ResourceVersion != live.ResourceVersion {
		t.Fatal("idempotent retry rewrote a grant")
	}
	otherBefore := readAccess(t, p, other)
	withdraw := accessDelivery(t, 2, "alice", "one", true)
	if err := p.ProjectAccess(t.Context(), withdraw); err != nil {
		t.Fatal(err)
	}
	denied := readAccess(t, p, first)
	if !denied.Spec.Disabled || len(denied.Spec.Models) != 0 || denied.Spec.Publication.OperationID != withdraw.OperationID {
		t.Fatal("withdrawal did not retain a versioned deny tombstone")
	}
	if err := p.ProjectAccess(t.Context(), first); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old allow replay = %v", err)
	}
	if !reflect.DeepEqual(readAccess(t, p, other), otherBefore) {
		t.Fatal("a different binding was rewritten")
	}
	var aggregate gateway.DeploymentConfig
	if err := p.Client.Get(t.Context(), client.ObjectKey{Namespace: p.Namespace, Name: "deployment"}, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate.Annotations[sequenceKey] != "" || denied.Status.ObservedPublication != nil || len(denied.Status.Replicas) != 0 {
		t.Fatal("delivery fabricated adoption evidence or stamped route aggregate")
	}
}

func TestAccessProjectionRejectsCorruptInputsAndOwnership(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*repository.ModelAccessDelivery)
	}{
		{"bytes", func(v *repository.ModelAccessDelivery) { v.Payload = append(v.Payload, ' ') }},
		{"hash", func(v *repository.ModelAccessDelivery) { v.ContentHash = strings.Repeat("b", 64) }},
		{"binding", func(v *repository.ModelAccessDelivery) { v.BindingKey = strings.Repeat("b", 64) }},
		{"subject", func(v *repository.ModelAccessDelivery) { v.SubjectID = "bob" }},
		{"deployment", func(v *repository.ModelAccessDelivery) { v.DeploymentID = "other" }},
		{"sequence", func(v *repository.ModelAccessDelivery) { v.Sequence = 0 }},
		{"revision", func(v *repository.ModelAccessDelivery) { v.Revision = "" }},
		{"operation", func(v *repository.ModelAccessDelivery) { v.OperationID = "bad" }},
		{"precision", func(v *repository.ModelAccessDelivery) { v.ValidUntil = v.ValidUntil.Add(time.Millisecond) }},
		{"window", func(v *repository.ModelAccessDelivery) { v.ValidUntil = v.IssuedAt.Add(2 * time.Hour) }},
		{"zero time", func(v *repository.ModelAccessDelivery) { v.IssuedAt = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testProjector(t)
			item := accessDelivery(t, 1, "alice", "one", false)
			test.edit(&item)
			if err := p.ProjectAccess(t.Context(), item); !errors.Is(err, ErrSnapshot) {
				t.Fatalf("invalid input = %v", err)
			}
		})
	}
	for _, test := range []struct {
		name string
		edit func(*gateway.ModelAccessGrant)
	}{
		{"unowned", func(v *gateway.ModelAccessGrant) { v.Labels = nil }},
		{"other binding", func(v *gateway.ModelAccessGrant) { v.Spec.Binding.SessionID = "someone-else" }},
		{"other subject", func(v *gateway.ModelAccessGrant) { v.Spec.Subject.ID = "bob" }},
		{"marker mismatch", func(v *gateway.ModelAccessGrant) { v.Spec.Publication.Sequence++ }},
		{"same sequence expansion", func(v *gateway.ModelAccessGrant) { v.Spec.Models = append(v.Spec.Models, "other") }},
		{"wrong binding marker", func(v *gateway.ModelAccessGrant) { v.Annotations[accessBindingKey] = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testProjector(t)
			item := accessDelivery(t, 1, "alice", "one", false)
			if err := p.ProjectAccess(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			live := readAccess(t, p, item)
			test.edit(&live)
			if err := p.Client.Update(t.Context(), &live); err != nil {
				t.Fatal(err)
			}
			if err := p.ProjectAccess(t.Context(), item); !errors.Is(err, ErrOwnership) {
				t.Fatalf("foreign/corrupt resource = %v", err)
			}
		})
	}
}

func TestAccessProjectionLostResponseAndReadbackMismatch(t *testing.T) {
	for _, mode := range []string{"create response lost", "update response lost", "readback changed"} {
		t.Run(mode, func(t *testing.T) {
			p := testProjector(t)
			item := accessDelivery(t, 1, "alice", "one", false)
			base := p.Client
			if mode == "update response lost" {
				if err := p.ProjectAccess(t.Context(), item); err != nil {
					t.Fatal(err)
				}
				item = accessDelivery(t, 2, "alice", "one", true)
			}
			fault := &faultClient{Client: base}
			switch mode {
			case "create response lost":
				fault.create = func(ctx context.Context, obj client.Object) error {
					if err := base.Create(ctx, obj); err != nil {
						return err
					}
					return errors.New("lost response")
				}
			case "update response lost":
				fault.update = func(ctx context.Context, obj client.Object) error {
					if err := base.Update(ctx, obj); err != nil {
						return err
					}
					return errors.New("lost response")
				}
			case "readback changed":
				fault.get = func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
					if v, ok := obj.(*gateway.ModelAccessGrant); ok {
						if err := base.Get(ctx, key, v); err == nil {
							v.Spec.Disabled, v.Spec.Models = true, []string{}
							return base.Update(ctx, v)
						}
					}
					return nil
				}
			}
			p.Client = fault
			if err := p.ProjectAccess(t.Context(), item); err == nil {
				t.Fatal("unconfirmed write reported successful")
			}
			p.Client = base
			if mode != "readback changed" {
				if err := p.ProjectAccess(t.Context(), item); err != nil {
					t.Fatalf("retry after lost response = %v", err)
				}
			}
		})
	}
}

func TestAccessProjectionCreateRaceAndMissingDeployment(t *testing.T) {
	p := testProjector(t)
	item := accessDelivery(t, 1, "alice", "one", false)
	base := p.Client
	p.Client = &faultClient{Client: base, create: func(ctx context.Context, obj client.Object) error {
		obj.SetLabels(nil)
		if err := base.Create(ctx, obj); err != nil {
			return err
		}
		return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "modelaccessgrants"}, obj.GetName())
	}}
	if err := p.ProjectAccess(t.Context(), item); !apierrors.IsAlreadyExists(err) {
		t.Fatal("concurrent create silently taken over")
	}
	p.Client = base
	if err := p.ProjectAccess(t.Context(), item); !errors.Is(err, ErrOwnership) {
		t.Fatal("foreign create silently adopted on retry")
	}
	dc := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: "deployment"}}
	if err := base.Delete(t.Context(), dc); err != nil {
		t.Fatal(err)
	}
	if err := p.ProjectAccess(t.Context(), item); !apierrors.IsNotFound(err) {
		t.Fatalf("missing deployment = %v", err)
	}
}
