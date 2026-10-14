// Package render translates ModelRoute/ModelPolicy CRs into the shared
// gatewayrender inputs and renders the deployment's Higress resources from
// them. Shadow phase (hybrid architecture §9 step 1): render only — nothing
// is applied, nothing is written outside the CRs' own status. The
// equivalence harness in shadow_test.go byte-compares this output against
// the DB-driven reconciler path; that byte-parity is the promotion gate
// metric for the config-plane migration.
package render

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// RouteFromCR projects one ModelRoute CR onto the shared render Route.
// TenantScope is authorization-plane data and never enters the render. The
// CRD carries no providerType, so ai-proxy renders the default ("openai");
// a providerType field on the CRD is a deliberate follow-up (see the
// documented exclusion in shadow_test.go).
func RouteFromCR(route *gatewayv1alpha1.ModelRoute) gatewayrender.Route {
	out := gatewayrender.Route{
		ModelID:       route.Spec.ModelID,
		Enabled:       route.Spec.Enabled,
		Endpoint:      route.Spec.Upstream.Endpoint,
		UpstreamModel: route.Spec.Upstream.Model,
		Protocol:      route.Spec.Protocol,
	}
	if ref := route.Spec.CredentialRef; ref != nil {
		out.CredentialRef = &gatewayrender.SecretReference{Name: ref.Name, Key: ref.Key}
	}
	return out
}

// intervalName maps a CRD seconds-based interval onto the native plugin's
// coarse interval enum; any other value is a mapping error, not a silent
// rounding.
func intervalName(seconds int64) (string, error) {
	switch seconds {
	case 1:
		return "second", nil
	case 60:
		return "minute", nil
	case 3600:
		return "hour", nil
	case 86400:
		return "day", nil
	default:
		return "", fmt.Errorf("interval %ds does not map to the native interval enum (second/minute/hour/day)", seconds)
	}
}

// LimitsFromCRs projects ModelPolicies into the native-gateway limit
// publication the shared renderer consumes. Rate limits map to "requests"
// rules and the quota to a "tokens" rule, all scoped to the target route's
// model ID. Rule IDs derive from the policy name so repeated renders are
// stable; the publication revision is recomputed exactly like the control
// plane computes it. A policy whose target route is missing fails the whole
// projection — silently dropping it would render a weaker policy than
// declared.
func LimitsFromCRs(policies []gatewayv1alpha1.ModelPolicy, routes map[string]gatewayv1alpha1.ModelRoute) (gatewaypolicy.Publication, error) {
	publication := gatewaypolicy.Publication{}
	for index := range policies {
		policy := &policies[index]
		route, ok := routes[policy.Spec.TargetRef.Name]
		if !ok {
			return gatewaypolicy.Publication{}, fmt.Errorf("policy %s targets unknown ModelRoute %s", policy.Name, policy.Spec.TargetRef.Name)
		}
		modelID := route.Spec.ModelID
		for i, rateLimit := range policy.Spec.RateLimits {
			interval, err := intervalName(rateLimit.IntervalSeconds)
			if err != nil {
				return gatewaypolicy.Publication{}, fmt.Errorf("policy %s rateLimits[%d]: %w", policy.Name, i, err)
			}
			publication.Items = append(publication.Items, gatewaypolicy.Limit{
				ID:      policy.Name + "-rl-" + strconv.Itoa(i),
				Version: 1,
				Configuration: gatewaypolicy.Configuration{
					Kind:      "requests",
					ScopeType: "model",
					ScopeID:   &modelID,
					ModelID:   &modelID,
					Maximum:   rateLimit.Maximum,
					Interval:  interval,
					Enabled:   true,
				},
			})
		}
		if quota := policy.Spec.Quota; quota != nil {
			interval, err := intervalName(quota.IntervalSeconds)
			if err != nil {
				return gatewaypolicy.Publication{}, fmt.Errorf("policy %s quota: %w", policy.Name, err)
			}
			publication.Items = append(publication.Items, gatewaypolicy.Limit{
				ID:      policy.Name + "-quota",
				Version: 1,
				Configuration: gatewaypolicy.Configuration{
					Kind:      "tokens",
					ScopeType: "model",
					ScopeID:   &modelID,
					ModelID:   &modelID,
					Maximum:   quota.Maximum,
					Interval:  interval,
					Enabled:   true,
				},
			})
		}
	}
	publication.Revision = gatewaypolicy.Revision(publication.Items)
	return publication, nil
}

// DeploymentInput is one deployment scope's CR snapshot plus the
// deployment-level native gateway settings. Until DeploymentConfig lands the
// native settings are supplied by the caller; a zero NativeGatewayConfig
// (Enabled=false) skips the limit/quota resources exactly like a disabled
// native gateway on the reconciler side.
type DeploymentInput struct {
	// Deployment is the render name scope and must equal the reconciler's
	// tenant/deployment ID for the shadow comparison to be meaningful.
	Deployment string
	// Revision only gates Render's non-empty check and never enters the
	// rendered bytes; the CR snapshot passes a constant marker.
	Revision string
	Routes   []gatewayv1alpha1.ModelRoute
	Policies []gatewayv1alpha1.ModelPolicy
	Native   gatewayrender.NativeGatewayConfig
}

// RenderDeployment renders every resource the DB-driven reconciler produces
// for the equivalent desired state, in the same pinned order: route-level
// Ingress/EnvoyFilter pairs and the deployment-level ai-proxy/ai-statistics
// WasmPlugins, then (when Native.Enabled) the limit plugins and the quota
// admin route. Credential references resolve through the injected fetcher,
// mirroring the reconciler's seam; nil renders without tokens.
func RenderDeployment(ctx context.Context, in DeploymentInput, fetch gatewayrender.CredentialFetcher) ([]gatewayrender.RenderedResource, error) {
	routes := make([]gatewayrender.Route, 0, len(in.Routes))
	routesByName := make(map[string]gatewayv1alpha1.ModelRoute, len(in.Routes))
	for index := range in.Routes {
		routes = append(routes, RouteFromCR(&in.Routes[index]))
		routesByName[in.Routes[index].Name] = in.Routes[index]
	}
	input := gatewayrender.Input{Deployment: in.Deployment, Revision: in.Revision, Routes: routes}
	credentials := gatewayrender.FetchCredentials(ctx, routes, fetch)
	_, resources, _, err := gatewayrender.Render(input, credentials)
	if err != nil {
		return nil, err
	}
	if !in.Native.Enabled {
		return resources, nil
	}
	// Mirror Reconciler.syncGatewayLimits: resolve the Redis password, render
	// the limit publication, then the quota admin route.
	password := ""
	if ref := in.Native.RedisPasswordRef; ref != nil {
		if fetch == nil {
			return nil, errors.New("native Redis secret resolver unavailable")
		}
		password, err = fetch(ctx, *ref)
		if err != nil || password == "" {
			return nil, errors.New("native Redis secret unavailable")
		}
	}
	publication, err := LimitsFromCRs(in.Policies, routesByName)
	if err != nil {
		return nil, err
	}
	limits, err := gatewayrender.RenderGatewayLimits(input, publication, in.Native, password)
	if err != nil {
		return nil, err
	}
	resources = append(resources, limits...)
	if ref := in.Native.QuotaAdminCredentialRef; ref != nil {
		if fetch == nil {
			return nil, errors.New("native quota secret resolver unavailable")
		}
		credential, fetchErr := fetch(ctx, *ref)
		if fetchErr != nil {
			return nil, errors.New("native quota secret unavailable")
		}
		quota, quotaErr := gatewayrender.RenderGatewayQuota(input, in.Native, password, credential)
		if quotaErr != nil {
			return nil, quotaErr
		}
		resources = append(resources, quota...)
	}
	return resources, nil
}
