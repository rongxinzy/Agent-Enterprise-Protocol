package gatewayaccess

import (
	"bytes"
	"strings"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func snapshotFixture() Snapshot {
	return Snapshot{Schema: "aep-model-access/v1", DeploymentID: "deployment",
		Subject: gateway.ModelAccessSubject{Kind: "human", ID: "alice"},
		Binding: gateway.ModelAccessBinding{Kind: "session", SessionID: "session"},
		Models:  []string{"chat"}, RoleIDs: []string{"role"}}
}

func TestCanonicalAccessSnapshot(t *testing.T) {
	source := snapshotFixture()
	payload, hash, err := source.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(payload, hash)
	if err != nil || decoded.BindingKey() != source.BindingKey() {
		t.Fatal("canonical snapshot lost binding identity")
	}
	copy := source
	copy.Models = []string{"other"}
	if copy.BindingKey() != source.BindingKey() {
		t.Fatal("policy mutation changed immutable binding key")
	}
	copy.Binding.SessionID = "another"
	if copy.BindingKey() == source.BindingKey() {
		t.Fatal("another signed session inherited a binding key")
	}
	for _, corrupt := range [][]byte{
		append(append([]byte{}, payload...), []byte(` {}`)...),
		bytes.Replace(payload, []byte(`"chat"`), []byte(`"other"`), 1),
		bytes.Replace(payload, []byte(`"schema":`), []byte(`"unexpected":true,"schema":`), 1),
		append([]byte(" "), payload...),
	} {
		if _, err := Decode(corrupt, hash); err == nil {
			t.Fatal("noncanonical/mutated snapshot was accepted")
		}
	}
}

func TestAccessSnapshotRejectsUnsafeShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"schema", func(s *Snapshot) { s.Schema = "other" }},
		{"subject", func(s *Snapshot) { s.Subject.Kind = "owner" }},
		{"foreign binding", func(s *Snapshot) { s.Binding.Kind = "other" }},
		{"missing session", func(s *Snapshot) { s.Binding.SessionID = "" }},
		{"nil allow set", func(s *Snapshot) { s.Models = nil }},
		{"unsorted", func(s *Snapshot) { s.Models = []string{"z", "a"} }},
		{"duplicate", func(s *Snapshot) { s.Models = []string{"chat", "chat"} }},
		{"model id", func(s *Snapshot) { s.Models = []string{"provider/chat"} }},
		{"capacity", func(s *Snapshot) { s.RoleIDs = make([]string, 257) }},
		{"disabled allow", func(s *Snapshot) { s.Disabled = true }},
		{"retired allow", func(s *Snapshot) { s.Retired = true }},
		{"invalid digest", func(s *Snapshot) { s.Licenses = []gateway.ModelAccessLicense{{ID: "license", Digest: "wrong"}} }},
		{"duplicate license", func(s *Snapshot) {
			license := gateway.ModelAccessLicense{ID: "license", Digest: "sha256:" + strings.Repeat("a", 64)}
			s.Licenses = []gateway.ModelAccessLicense{license, license}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := snapshotFixture()
			test.mutate(&source)
			if _, _, err := source.Canonical(); err == nil {
				t.Fatal("unsafe snapshot was accepted")
			}
		})
	}
}
