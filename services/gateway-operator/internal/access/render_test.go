package access

import (
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderValidityAndCredentialBounds(t *testing.T) {
	g := fixture()
	deadline := metav1.NewTime(g.Spec.IssuedAt.Add(time.Minute))
	g.Spec.Licenses = []gateway.ModelAccessLicense{{ID: "license-a", Digest: "sha256:" + strings.Repeat("a", 64), ValidUntil: &deadline}}
	o := options()
	o.RequireEntitlement = true
	config, policy, err := Render(g, o)
	if err != nil {
		t.Fatal(err)
	}
	data := policy.Data[g.Name+".rego"]
	for _, guard := range []string{"is_number(input.auth.identity.exp)", "input.auth.identity.model_scopes[_] == model", "body := json.unmarshal", "license.digest == input.auth.identity.license_digest", "license.deadline > time.now_ns()"} {
		if !strings.Contains(data, guard) {
			t.Fatalf("missing uncached bound: %s", guard)
		}
	}
	if config.GetAnnotations()[SemanticHash] == "" || len(config.GetAnnotations()[SemanticHash]) != 64 {
		t.Fatal("missing semantic identity")
	}
	g.Spec.Publication.Sequence++
	g.Spec.IssuedAt = metav1.NewTime(g.Spec.IssuedAt.Add(time.Second))
	markFixture(g)
	renewed, _, err := Render(g, o)
	if err != nil {
		t.Fatal(err)
	}
	if config.GetAnnotations()[SemanticHash] != renewed.GetAnnotations()[SemanticHash] {
		t.Fatal("freshness changed semantic hash")
	}
	for _, mutate := range []func(*gateway.ModelAccessGrant){
		func(g *gateway.ModelAccessGrant) { g.UID = "" },
		func(g *gateway.ModelAccessGrant) { g.Spec.Binding.SessionID = "" },
		func(g *gateway.ModelAccessGrant) { g.Spec.Publication.OperationID = "bad" },
		func(g *gateway.ModelAccessGrant) { g.Spec.Disabled = true },
		func(g *gateway.ModelAccessGrant) { g.Spec.ValidUntil = g.Spec.IssuedAt },
	} {
		bad := g.DeepCopy()
		mutate(bad)
		if _, _, err := Render(bad, o); err == nil {
			t.Fatal("invalid grant rendered")
		}
	}
	for _, value := range []string{"http://user:password@example.test", "file:///tmp/policy", "http://example.test?token=bad", " http://example.test"} {
		bad := options()
		bad.PolicyBaseURL = value
		if bad.Validate() == nil {
			t.Fatalf("unsafe URL accepted: %s", value)
		}
	}
}
