package publicationrelay

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func TestAccessRelayPreservesPodSetAndFencesSameVersionMutation(t *testing.T) {
	p := testProjector(t)
	item := accessDelivery(t, 1, "employee", "one", false)
	policy, err := gatewayaccess.Decode(item.Payload, item.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	policy.Subject.Kind = "agent"
	policy.Binding = gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
		Namespace: "employees", ServiceAccountName: "employee", ServiceAccountUID: "sa", EmployeeUID: "employee", Audience: "gateway",
	}}
	policy.PodUIDs = []string{"pod-a", "pod-b"}
	item.BindingKey = policy.BindingKey()
	item.Payload, item.ContentHash, err = policy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProjectAccess(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	before := readAccess(t, p, item)
	if !slices.Equal(before.Spec.WorkloadPodUIDs, policy.PodUIDs) {
		t.Fatal("relay dropped Pod evidence")
	}
	policy.PodUIDs = []string{"pod-b", "pod-c"}
	item.Sequence++
	item.OperationID = uuid.NewString()
	item.Payload, item.ContentHash, err = policy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProjectAccess(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	after := readAccess(t, p, item)
	if after.UID != before.UID || !slices.Equal(after.Spec.WorkloadPodUIDs, policy.PodUIDs) {
		t.Fatal("replacement lost exact Pod set or recreated binding")
	}
	after.Spec.WorkloadPodUIDs = []string{"injected-pod"}
	if err := p.Client.Update(t.Context(), &after); err != nil {
		t.Fatal(err)
	}
	if err := p.ProjectAccess(t.Context(), item); !errors.Is(err, ErrOwnership) {
		t.Fatal("same-version Pod mutation was not fenced", err)
	}
}
