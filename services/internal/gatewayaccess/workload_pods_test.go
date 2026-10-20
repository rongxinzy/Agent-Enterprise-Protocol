package gatewayaccess

import (
	"slices"
	"strings"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func TestWorkloadPodEvidenceChangesPublicationNotBinding(t *testing.T) {
	s := snapshotFixture()
	s.Subject.Kind = "agent"
	s.Binding = gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
		Namespace: "employees", ServiceAccountName: "employee", ServiceAccountUID: "sa-uid", EmployeeUID: "employee-uid", Audience: "gateway",
	}}
	s.PodUIDs = []string{"pod-a", "pod-b"}
	payload, hash, err := s.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(payload, hash)
	if err != nil || !slices.Equal(decoded.PodUIDs, s.PodUIDs) {
		t.Fatal("lost exact Pod observation", err)
	}
	oldKey := s.BindingKey()
	s.PodUIDs = []string{"pod-b", "pod-c"}
	_, changed, err := s.Canonical()
	if err != nil || hash == changed || s.BindingKey() != oldKey {
		t.Fatal("Pod replacement must republish without changing binding identity", err)
	}
	for _, pods := range [][]string{{"z", "a"}, {"a", "a"}, {""}, {"a\n"}, {"a/b"}, {strings.Repeat("a", 129)}, make([]string, 513)} {
		s.PodUIDs = pods
		if _, _, err := s.Canonical(); err == nil {
			t.Fatal("unsafe or noncanonical Pod set accepted", pods)
		}
	}
	// Old snapshots still decode; they cannot authorize native workload requests.
	s.PodUIDs = nil
	if _, _, err := s.Canonical(); err != nil {
		t.Fatal("missing evidence must remain transport-compatible", err)
	}
	human := snapshotFixture()
	human.PodUIDs = []string{"pod-a"}
	if _, _, err := human.Canonical(); err == nil {
		t.Fatal("human/session publication accepted workload-only evidence")
	}
}
