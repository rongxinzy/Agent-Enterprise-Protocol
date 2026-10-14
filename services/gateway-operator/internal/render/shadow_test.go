package render_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	operatorrender "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/render"
)

// Shadow equivalence harness (hybrid architecture §9 step 1, the migration
// promotion gate): every fixture renders the same logical intent twice — once
// through the DB-driven reconciler path (desired-state JSON in, Render +
// RenderGatewayLimits/RenderGatewayQuota out, mirroring Reconciler.Sync and
// syncGatewayLimits) and once through the operator path (ModelRoute/
// ModelPolicy CRs in, render.RenderDeployment out) — and byte-compares the
// two resource lists in their pinned order. Identical fake credentials feed
// both sides through the CredentialFetcher seam.
//
// desiredStateWire mirrors the reconciler's DB-shaped DesiredState wire JSON;
// the reconciler type itself lives behind its internal package boundary, so
// the harness re-decodes the same document and applies the same three-line
// Input projection Reconciler.Sync performs.
type desiredStateWire struct {
	DeploymentID string                `json:"deploymentId"`
	Revision     string                `json:"revision"`
	PublishedAt  string                `json:"publishedAt"`
	ContentHash  string                `json:"contentHash"`
	Routes       []gatewayrender.Route `json:"routes"`
}

type shadowFixture struct {
	name     string
	desired  desiredStateWire
	routes   []gatewayv1alpha1.ModelRoute
	policies []gatewayv1alpha1.ModelPolicy
	// native carries the deployment-level native gateway fixture; nil means
	// the native gateway is disabled on both sides.
	native *nativeFixture
}

type nativeFixture struct {
	config      gatewayrender.NativeGatewayConfig
	publication gatewaypolicy.Publication
}

func routeJSON(modelID, protocol, endpoint, upstream string, enabled bool, ref *gatewayrender.SecretReference) gatewayrender.Route {
	return gatewayrender.Route{ModelID: modelID, Protocol: protocol, Endpoint: endpoint, UpstreamModel: upstream, Enabled: enabled, CredentialRef: ref}
}

func routeCR(name, modelID, protocol, endpoint, upstream string, enabled bool, ref *gatewayv1alpha1.CredentialReference) gatewayv1alpha1.ModelRoute {
	return gatewayv1alpha1.ModelRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: gatewayv1alpha1.ModelRouteSpec{
			ModelID:       modelID,
			Protocol:      protocol,
			Upstream:      gatewayv1alpha1.ModelRouteUpstream{Endpoint: endpoint, Model: upstream},
			Enabled:       enabled,
			CredentialRef: ref,
		},
	}
}

func policyCR(name, target string, rateLimits []gatewayv1alpha1.RateLimit, quota *gatewayv1alpha1.Quota) gatewayv1alpha1.ModelPolicy {
	return gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef:  gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: target},
			RateLimits: rateLimits,
			Quota:      quota,
		},
	}
}

func limitJSON(id, kind, modelID string, maximum int64, interval string) gatewaypolicy.Limit {
	return gatewaypolicy.Limit{ID: id, Version: 1, Configuration: gatewaypolicy.Configuration{
		Kind: kind, ScopeType: "model", ScopeID: &modelID, ModelID: &modelID, Maximum: maximum, Interval: interval, Enabled: true,
	}}
}

func shadowFixtures() []shadowFixture {
	sharedNative := gatewayrender.NativeGatewayConfig{
		Enabled:                 true,
		RedisService:            "redis.higress-system.svc.cluster.local",
		RedisPort:               6379,
		RedisDatabase:           1,
		RedisUsername:           "default",
		RedisPasswordRef:        &gatewayrender.SecretReference{Name: "redis", Key: "password"},
		QuotaAdminCredentialRef: &gatewayrender.SecretReference{Name: "quota-admin", Key: "key"},
	}
	fixtures := []shadowFixture{
		{
			name:    "empty route set renders only the idle deployment plugins",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-empty", PublishedAt: "2026-10-14T00:00:00Z"},
		},
		{
			name: "openai enabled with absolute https endpoint and credentialRef",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat", "openai", "https://api.example/v1", "upstream-chat", true, &gatewayrender.SecretReference{Name: "provider-secrets", Key: "chat"}),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-chat", "chat", "openai", "https://api.example/v1", "upstream-chat", true, &gatewayv1alpha1.CredentialReference{Name: "provider-secrets", Key: "chat"}),
			},
		},
		{
			name: "openai enabled with absolute http endpoint on a custom port, no credentialRef",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat", "openai", "http://internal.example:8000/api/v1", "upstream-chat", true, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-chat", "chat", "openai", "http://internal.example:8000/api/v1", "upstream-chat", true, nil),
			},
		},
		{
			name: "openai disabled renders no route resources and idle plugins",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat", "openai", "https://api.example/v1", "upstream-chat", false, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-chat", "chat", "openai", "https://api.example/v1", "upstream-chat", false, nil),
			},
		},
		{
			// The CRD admission pattern (^https?://) rejects relative
			// endpoints today; the render contract must still match the DB
			// path for preconfigured-backend routes, so the harness builds
			// the CR in memory, bypassing admission.
			name: "openai enabled with a relative endpoint keeps the preconfigured backend",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat", "openai", "/v1", "upstream-chat", true, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-chat", "chat", "openai", "/v1", "upstream-chat", true, nil),
			},
		},
		{
			name: "anthropic enabled with credentialRef",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("bench-anthropic", "anthropic", "https://open.bigmodel.cn/api/anthropic", "upstream", true, &gatewayrender.SecretReference{Name: "aep-credential-bigmodel", Key: "api-key"}),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-anthropic", "bench-anthropic", "anthropic", "https://open.bigmodel.cn/api/anthropic", "upstream", true, &gatewayv1alpha1.CredentialReference{Name: "aep-credential-bigmodel", Key: "api-key"}),
			},
		},
		{
			name: "anthropic disabled renders no pair",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("bench-anthropic", "anthropic", "https://open.bigmodel.cn/api/anthropic", "upstream", false, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-anthropic", "bench-anthropic", "anthropic", "https://open.bigmodel.cn/api/anthropic", "upstream", false, nil),
			},
		},
		{
			// Both sides are deliberately listed out of model-ID order; the
			// pinned render order must still make the outputs byte-identical.
			name: "multi-route mixed set with disabled member renders in pinned order",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("z-last", "openai", "https://z.example/v1", "upstream-z", true, nil),
				routeJSON("bench-anthropic", "anthropic", "https://open.bigmodel.cn/api/anthropic", "upstream", true, &gatewayrender.SecretReference{Name: "aep-credential-bigmodel", Key: "api-key"}),
				routeJSON("a-first", "openai", "/v1", "upstream-a", true, nil),
				routeJSON("off", "openai", "https://off.example/v1", "upstream-off", false, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-a-first", "a-first", "openai", "/v1", "upstream-a", true, nil),
				routeCR("route-off", "off", "openai", "https://off.example/v1", "upstream-off", false, nil),
				routeCR("route-z-last", "z-last", "openai", "https://z.example/v1", "upstream-z", true, nil),
				routeCR("route-anthropic", "bench-anthropic", "anthropic", "https://open.bigmodel.cn/api/anthropic", "upstream", true, &gatewayv1alpha1.CredentialReference{Name: "aep-credential-bigmodel", Key: "api-key"}),
			},
		},
		{
			name: "credentialRef whose value fails to resolve renders without tokens on both paths",
			desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat", "openai", "https://api.example/v1", "upstream-chat", true, &gatewayrender.SecretReference{Name: "provider-secrets", Key: "broken"}),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-chat", "chat", "openai", "https://api.example/v1", "upstream-chat", true, &gatewayv1alpha1.CredentialReference{Name: "provider-secrets", Key: "broken"}),
			},
		},
		{
			name: "native limits and quota from ModelPolicies",
			desired: desiredStateWire{DeploymentID: "tenant-a", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat-a", "openai", "/v1", "upstream-a", true, nil),
				routeJSON("chat-b", "openai", "https://api.example/v1", "upstream-b", true, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-a", "chat-a", "openai", "/v1", "upstream-a", true, nil),
				routeCR("route-b", "chat-b", "openai", "https://api.example/v1", "upstream-b", true, nil),
			},
			policies: []gatewayv1alpha1.ModelPolicy{
				policyCR("policy-a", "route-a", []gatewayv1alpha1.RateLimit{{Maximum: 100, IntervalSeconds: 60}}, &gatewayv1alpha1.Quota{Maximum: 1000, IntervalSeconds: 3600}),
				policyCR("policy-b", "route-b", []gatewayv1alpha1.RateLimit{{Maximum: 5, IntervalSeconds: 1}, {Maximum: 200, IntervalSeconds: 86400}}, nil),
			},
			native: &nativeFixture{
				config: sharedNative,
				publication: gatewaypolicy.Publication{Items: []gatewaypolicy.Limit{
					limitJSON("policy-a-quota", "tokens", "chat-a", 1000, "hour"),
					limitJSON("policy-a-rl-0", "requests", "chat-a", 100, "minute"),
					limitJSON("policy-b-rl-0", "requests", "chat-b", 5, "second"),
					limitJSON("policy-b-rl-1", "requests", "chat-b", 200, "day"),
				}},
			},
		},
		{
			// An enabled native gateway with zero policies still renders the
			// quota admin route, mirroring a published empty limit snapshot
			// (revision present, items empty).
			name: "native gateway with no policies renders only the quota admin route",
			desired: desiredStateWire{DeploymentID: "tenant-a", Revision: "rev-1", Routes: []gatewayrender.Route{
				routeJSON("chat-a", "openai", "/v1", "upstream-a", true, nil),
			}},
			routes: []gatewayv1alpha1.ModelRoute{
				routeCR("route-a", "chat-a", "openai", "/v1", "upstream-a", true, nil),
			},
			native: &nativeFixture{
				config:      sharedNative,
				publication: gatewaypolicy.Publication{},
			},
		},
	}
	for index := range fixtures {
		if fixtures[index].native != nil && fixtures[index].native.publication.Revision == "" {
			fixtures[index].native.publication.Revision = gatewaypolicy.Revision(fixtures[index].native.publication.Items)
		}
		fixtures[index].desired.ContentHash = gatewayrender.CanonicalHash(fixtures[index].desired.Revision, fixtures[index].desired.Routes)
	}
	return fixtures
}

// fakeCredentials resolves every reference deterministically except the
// "broken" key, which simulates an unreadable Secret.
func fakeCredentials(_ context.Context, ref gatewayrender.SecretReference) (string, error) {
	if ref.Key == "broken" {
		return "", errors.New("secret unavailable")
	}
	return "fixture-value-" + ref.Name + "-" + ref.Key, nil
}

// reconcilerRender mirrors Reconciler.Sync plus syncGatewayLimits: decode the
// desired-state wire JSON, resolve credentials, Render, then the native
// gateway resources (limits, then quota) when the native gateway is enabled.
func reconcilerRender(ctx context.Context, t *testing.T, fixture shadowFixture, fetch gatewayrender.CredentialFetcher) []gatewayrender.RenderedResource {
	t.Helper()
	raw, err := json.Marshal(fixture.desired)
	if err != nil {
		t.Fatal(err)
	}
	var desired desiredStateWire
	if err := json.Unmarshal(raw, &desired); err != nil {
		t.Fatal(err)
	}
	input := gatewayrender.Input{Deployment: desired.DeploymentID, Revision: desired.Revision, Routes: desired.Routes}
	_, resources, _, err := gatewayrender.Render(input, gatewayrender.FetchCredentials(ctx, desired.Routes, fetch))
	if err != nil {
		t.Fatalf("reconciler path render: %v", err)
	}
	if fixture.native == nil {
		return resources
	}
	cfg := fixture.native.config
	password := ""
	if ref := cfg.RedisPasswordRef; ref != nil {
		password, err = fetch(ctx, *ref)
		if err != nil {
			t.Fatalf("reconciler path redis secret: %v", err)
		}
	}
	limits, err := gatewayrender.RenderGatewayLimits(input, fixture.native.publication, cfg, password)
	if err != nil {
		t.Fatalf("reconciler path limits: %v", err)
	}
	resources = append(resources, limits...)
	if ref := cfg.QuotaAdminCredentialRef; ref != nil {
		credential, err := fetch(ctx, *ref)
		if err != nil {
			t.Fatalf("reconciler path quota secret: %v", err)
		}
		quota, err := gatewayrender.RenderGatewayQuota(input, cfg, password, credential)
		if err != nil {
			t.Fatalf("reconciler path quota: %v", err)
		}
		resources = append(resources, quota...)
	}
	return resources
}

// operatorRender renders the same intent from the CR snapshot.
func operatorRender(ctx context.Context, t *testing.T, fixture shadowFixture, fetch gatewayrender.CredentialFetcher) []gatewayrender.RenderedResource {
	t.Helper()
	input := operatorrender.DeploymentInput{
		Deployment: fixture.desired.DeploymentID,
		Revision:   fixture.desired.Revision,
		Routes:     fixture.routes,
		Policies:   fixture.policies,
	}
	if fixture.native != nil {
		input.Native = fixture.native.config
	}
	resources, err := operatorrender.RenderDeployment(ctx, input, fetch)
	if err != nil {
		t.Fatalf("operator path render: %v", err)
	}
	return resources
}

func TestShadowRenderByteParity(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range shadowFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			dbSide := reconcilerRender(ctx, t, fixture, fakeCredentials)
			crSide := operatorRender(ctx, t, fixture, fakeCredentials)

			// The CR snapshot must also reproduce the published contentHash:
			// canonical-hash parity proves the JSON-canonical route sets match.
			routes := make([]gatewayrender.Route, 0, len(fixture.routes))
			for index := range fixture.routes {
				routes = append(routes, operatorrender.RouteFromCR(&fixture.routes[index]))
			}
			if hash := gatewayrender.CanonicalHash(fixture.desired.Revision, routes); hash != fixture.desired.ContentHash {
				t.Fatalf("content hash drift: db=%s cr=%s", fixture.desired.ContentHash, hash)
			}

			// The operator render is deterministic across repeated runs.
			again := operatorRender(ctx, t, fixture, fakeCredentials)
			assertResourcesEqual(t, "operator render is not deterministic", crSide, again)

			assertResourcesEqual(t, "shadow byte parity", dbSide, crSide)
		})
	}
}

func assertResourcesEqual(t *testing.T, label string, want, got []gatewayrender.RenderedResource) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: resource count %d != %d (want %s, got %s)", label, len(want), len(got), resourceNames(want), resourceNames(got))
	}
	for index := range want {
		if want[index].Kind != got[index].Kind || want[index].APIPath != got[index].APIPath {
			t.Fatalf("%s: resource %d identity %s %s != %s %s", label, index, want[index].Kind, want[index].APIPath, got[index].Kind, got[index].APIPath)
		}
		if want[index].Body != got[index].Body {
			t.Fatalf("%s: resource %d (%s) body drifted:\n%s", label, index, want[index].APIPath, firstDifference(want[index].Body, got[index].Body))
		}
	}
}

func resourceNames(resources []gatewayrender.RenderedResource) string {
	names := make([]string, 0, len(resources))
	for _, resource := range resources {
		names = append(names, string(resource.Kind)+":"+resource.APIPath)
	}
	return strings.Join(names, ", ")
}

func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for index := 0; index < len(wantLines) || index < len(gotLines); index++ {
		w, g := "", ""
		if index < len(wantLines) {
			w = wantLines[index]
		}
		if index < len(gotLines) {
			g = gotLines[index]
		}
		if w != g {
			return "line " + strings.TrimSpace(w) + " != " + strings.TrimSpace(g) + " (db vs cr)"
		}
	}
	return ""
}

// TestShadowDocumentedExclusions pins the known expressiveness gap instead of
// hiding it: ModelRoute v1alpha1 has no providerType field, so a DB route
// with providerType "deepseek" renders `type: 'deepseek'` in the ai-proxy
// provider block while the CR-driven render emits the "openai" default.
// Everything else must be byte-identical. The CRD needs a providerType field
// before deepseek-typed routes can migrate (follow-up, tracked in the PR).
func TestShadowDocumentedExclusions(t *testing.T) {
	ctx := context.Background()
	fixture := shadowFixture{
		name: "deepseek provider type",
		desired: desiredStateWire{DeploymentID: "demo", Revision: "rev-1", Routes: []gatewayrender.Route{{
			ModelID: "reasoner", Protocol: "openai", Endpoint: "/v1/chat", UpstreamModel: "deepseek-reasoner", Enabled: true, ProviderType: "deepseek",
		}}},
		routes: []gatewayv1alpha1.ModelRoute{
			routeCR("route-reasoner", "reasoner", "openai", "/v1/chat", "deepseek-reasoner", true, nil),
		},
	}
	dbSide := reconcilerRender(ctx, t, fixture, fakeCredentials)
	crSide := operatorRender(ctx, t, fixture, fakeCredentials)
	if len(dbSide) != len(crSide) {
		t.Fatalf("resource count %d != %d", len(dbSide), len(crSide))
	}
	drifted := 0
	for index := range dbSide {
		normalized := strings.Replace(dbSide[index].Body, "type: 'deepseek'", "type: 'openai'", 1)
		if normalized == crSide[index].Body {
			if dbSide[index].Body != crSide[index].Body {
				drifted++
			}
			continue
		}
		t.Fatalf("unexpected drift beyond the providerType default at %s:\n%s", dbSide[index].APIPath, firstDifference(normalized, crSide[index].Body))
	}
	if drifted != 1 {
		t.Fatalf("expected exactly the ai-proxy provider type line to drift, got %d drifted resources", drifted)
	}
}
