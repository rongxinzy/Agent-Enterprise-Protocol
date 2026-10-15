// Package render projects admitted CRs into the shared Higress renderer.
// Kubernetes access and credential resolution remain controller concerns.
package render

import (
	"errors"
	"fmt"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	"strconv"
)

func RouteFromCR(route gateway.ModelRoute) gatewayrender.Route {
	protocol := route.Spec.Protocol
	if protocol == "openai" {
		protocol = "openai-compatible"
	}
	out := gatewayrender.Route{ModelID: route.Spec.ModelID, Enabled: route.Spec.Enabled,
		Endpoint: route.Spec.Upstream.Endpoint, UpstreamModel: route.Spec.Upstream.Model,
		Protocol: protocol, ProviderType: route.Spec.ProviderType}
	if ref := route.Spec.CredentialRef; ref != nil {
		out.CredentialRef = &gatewayrender.SecretReference{Name: ref.Name, Key: ref.Key}
	}
	return out
}

func Interval(seconds int64) (string, error) {
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
		return "", errors.New("unsupported native interval")
	}
}

func Limits(policies []gateway.ModelPolicy, routes map[string]gateway.ModelRoute) (gatewaypolicy.Publication, error) {
	publication := gatewaypolicy.Publication{}
	for _, policy := range policies {
		route, ok := routes[policy.Spec.TargetRef.Name]
		if !ok {
			return publication, errors.New("policy target unavailable")
		}
		// A terminating route is being removed from both route and aggregate sets.
		if !route.DeletionTimestamp.IsZero() {
			continue
		}
		model := route.Spec.ModelID
		scope, id := "model", model
		if subject := policy.Spec.Subject; subject != nil {
			scope = subject.Type
			if scope != "model" {
				id = subject.ID
			}
		}
		add := func(suffix, unit string, maximum, seconds int64) error {
			interval, err := Interval(seconds)
			if err != nil {
				return err
			}
			config := gatewaypolicy.Configuration{Kind: unit, ScopeType: scope, ScopeID: &id, ModelID: &model, Maximum: maximum, Interval: interval, Enabled: true}
			if !config.Valid() {
				return errors.New("invalid native policy")
			}
			// Kubernetes names can exceed the native rule ID's 80-character bound.
			ruleID := gatewayrender.ResourceSuffix(string(policy.UID)+"/"+policy.Name) + suffix
			publication.Items = append(publication.Items, gatewaypolicy.Limit{ID: ruleID, Version: 1, Configuration: config})
			return nil
		}
		for i, limit := range policy.Spec.RateLimits {
			if err := add("-rl-"+strconv.Itoa(i), "requests", limit.Maximum, limit.IntervalSeconds); err != nil {
				return publication, fmt.Errorf("rate limit: %w", err)
			}
		}
		if quota := policy.Spec.Quota; quota != nil {
			unit := quota.Unit
			if unit == "" {
				unit = "requests"
			}
			if err := add("-quota", unit, quota.Maximum, quota.IntervalSeconds); err != nil {
				return publication, fmt.Errorf("quota: %w", err)
			}
		}
	}
	publication.Revision = gatewaypolicy.Revision(publication.Items)
	return publication, nil
}

// Partition is the exclusive route/aggregate ownership boundary.
func Partition(resources []gatewayrender.RenderedResource) (routes, aggregate []gatewayrender.RenderedResource) {
	for _, resource := range resources {
		switch resource.Kind {
		case gatewayrender.ResourceOpenAIIngress, gatewayrender.ResourceAnthropicIngress, gatewayrender.ResourceEnvoyFilter:
			routes = append(routes, resource)
		default:
			aggregate = append(aggregate, resource)
		}
	}
	return
}
