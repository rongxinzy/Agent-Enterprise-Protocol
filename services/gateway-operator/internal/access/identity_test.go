package access

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIngressIdentityComesFromCurrentGrant(t *testing.T) {
	g := fixture()
	g.Spec.TeamIDs = []string{"team-b", "team-a"}
	g.Spec.RoleIDs = []string{"role-a"}
	o := options()
	o.IngressHost = "gateway.example.test"
	config, cm, err := Render(g, o)
	if err != nil {
		t.Fatal(err)
	}
	metadata, found, err := unstructured.NestedMap(config.Object, "spec", "response", "success", "dynamicMetadata", "aep_trusted_headers")
	if !found || err != nil || metadata["cache"] != nil {
		t.Fatal("trusted identity must be uncached native response metadata", metadata, err)
	}
	policy := cm.Data[g.Name+".rego"]
	for _, required := range []string{"crypto.sha256", "base64url.encode_no_pad", `"id":"team-a"`, `"id":"role-a"`, "requested_model", "trusted_headers", "x-mse-consumer", "x-aep-session-id"} {
		if !strings.Contains(policy, required) {
			t.Fatalf("missing trusted quota identity %q", required)
		}
	}
	if membership([]string{"b", "a", "a"}) != "|YQ|Yg|" || membership(nil) != "" {
		t.Fatal("membership encoding differs from the existing authorizer")
	}
	previous := config.GetAnnotations()[SemanticHash]
	g.Spec.TeamIDs = []string{"other-team"}
	config, _, err = Render(g, o)
	if err != nil || previous == config.GetAnnotations()[SemanticHash] {
		t.Fatal("membership changes must invalidate only the affected semantic policy", err)
	}
	o.IngressHost = ""
	config, cm, err = Render(g, o)
	if err != nil {
		t.Fatal(err)
	}
	_, found, _ = unstructured.NestedMap(config.Object, "spec", "response", "success", "dynamicMetadata")
	if found || strings.Contains(cm.Data[g.Name+".rego"], "trusted_headers") {
		t.Fatal("identity bridge must remain opt-in")
	}
}

func TestIngressIdentityFailsClosedAtExistingHeaderBound(t *testing.T) {
	g := fixture()
	for i := range 39 {
		g.Spec.RoleIDs = append(g.Spec.RoleIDs, fmt.Sprintf("role-%d", i))
	}
	o := options()
	o.IngressHost = "gateway.example.test"
	if _, _, err := Render(g, o); err == nil {
		t.Fatal("cannot render a grant that exceeds the existing 80 presence-header bound")
	}
}
