package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The generated deepcopy code is exercised, not hand-asserted: nil receivers
// and empty optionals take the defensive branches, populated objects take the
// allocation branches, and a mutated copy must never touch the original.
func TestDeepCopyRoundtrip(t *testing.T) {
	route := &ModelRoute{
		Spec: ModelRouteSpec{
			ModelID:  "deepseek-chat",
			Protocol: "openai",
			Upstream: ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "deepseek-chat"},
			CredentialRef: &CredentialReference{
				Name: "provider-secrets",
				Key:  "deepseek",
			},
			Enabled: true,
			TenantScope: &TenantScope{
				Teams: []string{"qa"},
				Roles: []string{"ops"},
			},
		},
		Status: ModelRouteStatus{
			ObservedPublication: &PublicationReference{Sequence: 42},
			ObservedGeneration:  3,
			Conditions:          []metav1.Condition{{Type: ConditionAccepted, Status: metav1.ConditionTrue, Reason: ReasonAccepted, Message: "ok"}},
		},
	}
	copied := route.DeepCopy()
	copied.Status.ObservedPublication.Sequence = 43
	if route.Status.ObservedPublication.Sequence != 42 {
		t.Fatal("publication reference aliased across deep copy")
	}
	copied.Spec.ModelID = "other"
	copied.Spec.Upstream.Model = "other-model"
	copied.Spec.CredentialRef.Key = "other-key"
	copied.Spec.TenantScope.Teams[0] = "other-team"
	copied.Spec.TenantScope.Roles[0] = "other-role"
	if route.Spec.ModelID != "deepseek-chat" || route.Spec.Upstream.Model != "deepseek-chat" ||
		route.Spec.CredentialRef.Key != "deepseek" || route.Spec.TenantScope.Teams[0] != "qa" || route.Spec.TenantScope.Roles[0] != "ops" {
		t.Fatalf("deep copy is not deep: %#v", route.Spec)
	}
	if object := route.DeepCopyObject(); object.(*ModelRoute).Spec.ModelID != "deepseek-chat" {
		t.Fatalf("DeepCopyObject = %#v", object)
	}
	dc := &DeploymentConfig{Status: DeploymentConfigStatus{ObservedPublication: &PublicationReference{Sequence: 42}}}
	dc.DeepCopy().Status.ObservedPublication.Sequence = 43
	if dc.Status.ObservedPublication.Sequence != 42 {
		t.Fatal("deployment publication reference aliased")
	}

	policy := &ModelPolicy{
		Spec: ModelPolicySpec{
			TargetRef:  PolicyTargetReference{Kind: "ModelRoute", Name: "route-a"},
			RateLimits: []RateLimit{{Maximum: 100, IntervalSeconds: 60}},
			Quota:      &Quota{Maximum: 1000, IntervalSeconds: 3600},
		},
		Status: ModelPolicyStatus{ObservedGeneration: 2},
	}
	policyCopy := policy.DeepCopy()
	policyCopy.Spec.RateLimits[0].Maximum = 5
	policyCopy.Spec.Quota.Maximum = 5
	if policy.Spec.RateLimits[0].Maximum != 100 || policy.Spec.Quota.Maximum != 1000 {
		t.Fatalf("policy deep copy is not deep: %#v", policy.Spec)
	}
	if object := policy.DeepCopyObject(); object.(*ModelPolicy).Spec.Quota.Maximum != 1000 {
		t.Fatalf("DeepCopyObject = %#v", object)
	}

	routes := &ModelRouteList{Items: []ModelRoute{*route}}
	routeListCopy := routes.DeepCopy()
	routeListCopy.Items[0].Spec.ModelID = "list-mutated"
	if routes.Items[0].Spec.ModelID != "deepseek-chat" {
		t.Fatalf("list deep copy is not deep: %#v", routes.Items[0].Spec)
	}
	if object := routes.DeepCopyObject(); len(object.(*ModelRouteList).Items) != 1 {
		t.Fatalf("list DeepCopyObject = %#v", object)
	}
	policies := &ModelPolicyList{Items: []ModelPolicy{*policy}}
	policyListCopy := policies.DeepCopy()
	policyListCopy.Items[0].Spec.TargetRef.Name = "list-mutated"
	if policies.Items[0].Spec.TargetRef.Name != "route-a" {
		t.Fatalf("policy list deep copy is not deep: %#v", policies.Items[0].Spec)
	}
	if object := policies.DeepCopyObject(); len(object.(*ModelPolicyList).Items) != 1 {
		t.Fatalf("policy list DeepCopyObject = %#v", object)
	}
}

// The generated guards: nil receivers return nil, and absent optional fields
// take the nil branches of DeepCopyInto.
func TestDeepCopyGuardsNilAndEmpty(t *testing.T) {
	var nilRoute *ModelRoute
	if nilRoute.DeepCopy() != nil || nilRoute.DeepCopyObject() != nil {
		t.Fatal("nil ModelRoute copy must be nil")
	}
	var nilPolicy *ModelPolicy
	if nilPolicy.DeepCopy() != nil || nilPolicy.DeepCopyObject() != nil {
		t.Fatal("nil ModelPolicy copy must be nil")
	}
	var nilRoutes *ModelRouteList
	if nilRoutes.DeepCopy() != nil || nilRoutes.DeepCopyObject() != nil {
		t.Fatal("nil ModelRouteList copy must be nil")
	}
	var nilPolicies *ModelPolicyList
	if nilPolicies.DeepCopy() != nil || nilPolicies.DeepCopyObject() != nil {
		t.Fatal("nil ModelPolicyList copy must be nil")
	}

	bareRoute := &ModelRoute{}
	if copy := bareRoute.DeepCopy(); copy.Spec.CredentialRef != nil || copy.Spec.TenantScope != nil {
		t.Fatalf("empty route gained optional fields: %#v", copy.Spec)
	}
	barePolicy := &ModelPolicy{}
	if copy := barePolicy.DeepCopy(); copy.Spec.Quota != nil || copy.Spec.RateLimits != nil {
		t.Fatalf("empty policy gained optional fields: %#v", copy.Spec)
	}

	// The nested types' own DeepCopy methods (nil and populated receivers).
	for _, copy := range []any{
		(&ModelRouteSpec{}).DeepCopy(), (&ModelRouteStatus{}).DeepCopy(), (&ModelRouteUpstream{}).DeepCopy(),
		(&CredentialReference{}).DeepCopy(), (&TenantScope{}).DeepCopy(), (&PolicyTargetReference{}).DeepCopy(),
		(&RateLimit{}).DeepCopy(), (&Quota{}).DeepCopy(), (&ModelPolicySpec{}).DeepCopy(), (&ModelPolicyStatus{}).DeepCopy(),
		(&PublicationReference{}).DeepCopy(),
	} {
		if copy == nil {
			t.Fatal("populated nested deep copy must not be nil")
		}
	}
	var nilSpec *ModelRouteSpec
	var nilRouteStatus *ModelRouteStatus
	var nilUpstream *ModelRouteUpstream
	var nilCredential *CredentialReference
	var nilScope *TenantScope
	var nilTarget *PolicyTargetReference
	var nilRate *RateLimit
	var nilQuota *Quota
	var nilPolicySpec *ModelPolicySpec
	var nilPolicyStatus *ModelPolicyStatus
	var nilPublication *PublicationReference
	if nilSpec.DeepCopy() != nil || nilRouteStatus.DeepCopy() != nil || nilUpstream.DeepCopy() != nil ||
		nilCredential.DeepCopy() != nil || nilScope.DeepCopy() != nil || nilTarget.DeepCopy() != nil ||
		nilRate.DeepCopy() != nil || nilQuota.DeepCopy() != nil || nilPolicySpec.DeepCopy() != nil ||
		nilPolicyStatus.DeepCopy() != nil || nilPublication.DeepCopy() != nil {
		t.Fatal("nil nested deep copy must be nil")
	}
}
