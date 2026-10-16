package render

import (
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestNativePolicyProjection(t *testing.T) {
	route := gateway.ModelRoute{Spec: gateway.ModelRouteSpec{ModelID: "alpha", Protocol: "openai", ProviderType: "deepseek", Enabled: true, Upstream: gateway.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "chat"}}}
	projected := RouteFromCR(route)
	if projected.Protocol != "openai-compatible" || projected.ProviderType != "deepseek" {
		t.Fatalf("provider mapping changed: %+v", projected)
	}
	for _, subject := range []string{"model", "user", "team", "role"} {
		policy := gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy"}, Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "route"}, Quota: &gateway.Quota{Maximum: 10, IntervalSeconds: 60}}}
		id := "alpha"
		if subject != "model" {
			id = "subject"
			policy.Spec.Subject = &gateway.PolicySubject{Type: subject, ID: id}
		}
		publication, err := Limits([]gateway.ModelPolicy{policy}, map[string]gateway.ModelRoute{"route": route})
		if err != nil {
			t.Fatal(err)
		}
		got := publication.Items[0].Configuration
		expected := gatewaypolicy.Configuration{ScopeType: subject, ScopeID: &id, ModelID: &route.Spec.ModelID}
		if got.Kind != "requests" || gatewaypolicy.Header(got) != gatewaypolicy.Header(expected) {
			t.Fatalf("scope/unit changed: %+v", got)
		}
		policy.Spec.Quota.Unit = "tokens"
		publication, err = Limits([]gateway.ModelPolicy{policy}, map[string]gateway.ModelRoute{"route": route})
		if err != nil || publication.Items[0].Configuration.Kind != "tokens" {
			t.Fatalf("token quota: %+v %v", publication, err)
		}
	}
	policy := gateway.ModelPolicy{Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "missing"}}}
	if _, err := Limits([]gateway.ModelPolicy{policy}, map[string]gateway.ModelRoute{}); err == nil {
		t.Fatal("orphan policy silently omitted")
	}
	policy.Spec.TargetRef.Name = "route"
	policy.Spec.RateLimits = []gateway.RateLimit{{Maximum: 1, IntervalSeconds: 59}}
	if _, err := Limits([]gateway.ModelPolicy{policy}, map[string]gateway.ModelRoute{"route": route}); err == nil {
		t.Fatal("unsupported interval silently rounded")
	}
}

func TestDisabledRouteOmitsAndRestoresItsPolicyWithoutPublicationMarkers(t *testing.T) {
	route := gateway.ModelRoute{Spec: gateway.ModelRouteSpec{ModelID: "alpha", Enabled: false}}
	policy := gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Name: "retained"}, Spec: gateway.ModelPolicySpec{
		TargetRef: gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "route"},
		Quota:     &gateway.Quota{Maximum: 37, IntervalSeconds: 60, Unit: "tokens"},
	}}
	publication, err := Limits([]gateway.ModelPolicy{policy}, map[string]gateway.ModelRoute{"route": route})
	if err != nil || len(publication.Items) != 0 {
		t.Fatalf("disabled route retained enforcement: %+v %v", publication, err)
	}
	route.Spec.Enabled = true
	publication, err = Limits([]gateway.ModelPolicy{policy}, map[string]gateway.ModelRoute{"route": route})
	if err != nil || len(publication.Items) != 1 || publication.Items[0].Configuration.Maximum != 37 || publication.Items[0].Configuration.Kind != "tokens" {
		t.Fatalf("re-enabled route lost its original policy: %+v %v", publication, err)
	}
}

func TestOwnershipPartition(t *testing.T) {
	input := gatewayrender.Input{Deployment: "local", Revision: "cr", Routes: []gatewayrender.Route{{ModelID: "alpha", Protocol: "openai-compatible", Enabled: true, Endpoint: "https://provider.example/v1", UpstreamModel: "chat"}}}
	_, all, _, err := gatewayrender.Render(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	routes, aggregate := Partition(all)
	if len(routes) != 2 || len(aggregate) != 2 {
		t.Fatalf("incorrect resource boundary: %d %d", len(routes), len(aggregate))
	}
	for _, resource := range aggregate {
		if resource.Kind != gatewayrender.ResourceWasmPlugin && resource.Kind != gatewayrender.ResourceAIStatistics {
			t.Fatal("route resource leaked into aggregate")
		}
	}
}
