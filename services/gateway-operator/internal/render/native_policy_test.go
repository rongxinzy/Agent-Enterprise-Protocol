package render

import (
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"testing"
)

func TestPublishedNativeRulesKeepSharedHeadersAndIdentity(t *testing.T) {
	for _, scope := range []string{"global", "user", "team", "role"} {
		for _, modelScoped := range []bool{false, true} {
			p := gateway.ModelPolicy{Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "DeploymentConfig", Name: "deployment"}, Subject: &gateway.PolicySubject{Type: scope}, NativeRule: &gateway.NativePolicyRule{ID: "published-rule", Version: 9}, Quota: &gateway.Quota{Unit: "tokens", Maximum: 123, IntervalSeconds: 86400}}}
			id, model := "subject", "chat"
			expected := gatewaypolicy.Configuration{ScopeType: scope}
			if scope != "global" {
				p.Spec.Subject.ID = id
				expected.ScopeID = &id
			}
			if modelScoped {
				p.Spec.TargetRef = gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "route"}
				expected.ModelID = &model
			}
			got, err := Limits([]gateway.ModelPolicy{p}, map[string]gateway.ModelRoute{"route": {Spec: gateway.ModelRouteSpec{ModelID: model, Enabled: true}}})
			if err != nil || len(got.Items) != 1 {
				t.Fatalf("native projection: %+v %v", got, err)
			}
			rule := got.Items[0]
			if rule.ID != "published-rule" || rule.Version != 9 || gatewaypolicy.Header(rule.Configuration) != gatewaypolicy.Header(expected) || rule.Configuration.Maximum != 123 || rule.Configuration.Kind != "tokens" || rule.Configuration.Interval != "day" {
				t.Fatalf("counter changed: %+v", rule)
			}
			p.Spec.Disabled = true
			got, err = Limits([]gateway.ModelPolicy{p}, nil)
			if err != nil || len(got.Items) != 0 {
				t.Fatal("disabled fence rendered a rule")
			}
		}
	}
}

func TestNativeRuleRejectsAmbiguousIdentity(t *testing.T) {
	p := gateway.ModelPolicy{Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "DeploymentConfig", Name: "deployment"}, NativeRule: &gateway.NativePolicyRule{ID: "rule", Version: 1}, Quota: &gateway.Quota{Unit: "requests", Maximum: 1, IntervalSeconds: 60}}}
	if _, err := Limits([]gateway.ModelPolicy{p, p}, nil); err == nil {
		t.Fatal("duplicate business rule accepted")
	}
	p.Spec.RateLimits = []gateway.RateLimit{{Maximum: 1, IntervalSeconds: 60}}
	if _, err := Limits([]gateway.ModelPolicy{p}, nil); err == nil {
		t.Fatal("ambiguous multi-rule native identity accepted")
	}
}
