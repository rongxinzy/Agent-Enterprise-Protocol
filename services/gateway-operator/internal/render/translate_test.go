package render

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func routeCR(name string) *gatewayv1alpha1.ModelRoute {
	return &gatewayv1alpha1.ModelRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: gatewayv1alpha1.ModelRouteSpec{
			ModelID:  "deepseek-chat",
			Protocol: "openai",
			Upstream: gatewayv1alpha1.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "deepseek-chat"},
			Enabled:  true,
		},
	}
}

func TestRouteFromCR(t *testing.T) {
	route := routeCR("route-a")
	route.Spec.TenantScope = &gatewayv1alpha1.TenantScope{Teams: []string{"qa"}}
	projected := RouteFromCR(route)
	want := gatewayrender.Route{ModelID: "deepseek-chat", Enabled: true, Endpoint: "https://provider.example/v1", UpstreamModel: "deepseek-chat", Protocol: "openai-compatible"}
	if projected != want {
		t.Fatalf("RouteFromCR = %#v, want %#v", projected, want)
	}
	route.Spec.CredentialRef = &gatewayv1alpha1.CredentialReference{Name: "provider-secrets", Key: "deepseek"}
	projected = RouteFromCR(route)
	if projected.CredentialRef == nil || *projected.CredentialRef != (gatewayrender.SecretReference{Name: "provider-secrets", Key: "deepseek"}) {
		t.Fatalf("credentialRef = %#v", projected.CredentialRef)
	}
}

func TestLimitsFromCRs(t *testing.T) {
	routes := map[string]gatewayv1alpha1.ModelRoute{"route-a": *routeCR("route-a")}
	policies := []gatewayv1alpha1.ModelPolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-a", Namespace: "default"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef:  gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-a"},
			RateLimits: []gatewayv1alpha1.RateLimit{{Maximum: 100, IntervalSeconds: 60}},
			Quota:      &gatewayv1alpha1.Quota{Maximum: 1000, IntervalSeconds: 3600},
		},
	}}
	publication, err := LimitsFromCRs(policies, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(publication.Items) != 2 {
		t.Fatalf("items = %#v", publication.Items)
	}
	rateLimit := publication.Items[0].Configuration
	if publication.Items[0].ID != "policy-a-rl-0" || rateLimit.Kind != "requests" || rateLimit.ScopeType != "model" || *rateLimit.ScopeID != "deepseek-chat" || rateLimit.Maximum != 100 || rateLimit.Interval != "minute" || !rateLimit.Enabled {
		t.Fatalf("rate limit projection = %#v", publication.Items[0])
	}
	quota := publication.Items[1].Configuration
	if publication.Items[1].ID != "policy-a-quota" || quota.Kind != "tokens" || quota.Interval != "hour" || quota.Maximum != 1000 {
		t.Fatalf("quota projection = %#v", publication.Items[1])
	}
	if publication.Revision != gatewaypolicy.Revision(publication.Items) {
		t.Fatal("publication revision does not match the control-plane computation")
	}
	for _, item := range publication.Items {
		if !gatewaypolicy.ValidID(item.ID) || !item.Configuration.Valid() {
			t.Fatalf("projected item rejected by the shared validation: %#v", item)
		}
	}
}

func TestLimitsFromCRsRejectsOrphanTargetAndUnknownInterval(t *testing.T) {
	routes := map[string]gatewayv1alpha1.ModelRoute{"route-a": *routeCR("route-a")}
	orphan := []gatewayv1alpha1.ModelPolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "default"},
		Spec:       gatewayv1alpha1.ModelPolicySpec{TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "missing"}},
	}}
	if _, err := LimitsFromCRs(orphan, routes); err == nil {
		t.Fatal("orphan policy accepted")
	}
	badInterval := []gatewayv1alpha1.ModelPolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "odd", Namespace: "default"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef:  gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-a"},
			RateLimits: []gatewayv1alpha1.RateLimit{{Maximum: 10, IntervalSeconds: 90}},
		},
	}}
	if _, err := LimitsFromCRs(badInterval, routes); err == nil || !strings.Contains(err.Error(), "90s") {
		t.Fatalf("unsupported interval error = %v", err)
	}
}

func TestRenderDeploymentSkipsNativeResourcesWhenDisabled(t *testing.T) {
	// A zero NativeGatewayConfig must skip the limit/quota resources exactly
	// like a disabled native gateway on the reconciler side — even when
	// policies exist.
	policy := gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-a", Namespace: "default"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef:  gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-a"},
			RateLimits: []gatewayv1alpha1.RateLimit{{Maximum: 100, IntervalSeconds: 60}},
		},
	}
	resources, err := RenderDeployment(context.Background(), DeploymentInput{
		Deployment: "demo",
		Revision:   "cr-shadow",
		Routes:     []gatewayv1alpha1.ModelRoute{*routeCR("route-a")},
		Policies:   []gatewayv1alpha1.ModelPolicy{policy},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		if resource.Kind == gatewayrender.ResourceGatewayNative {
			t.Fatalf("native resource rendered with the native gateway disabled: %s", resource.APIPath)
		}
	}
}
