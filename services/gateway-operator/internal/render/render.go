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
	ids := map[string]bool{}
	for _, policy := range policies {
		if policy.Spec.Disabled {
			continue
		}
		model, scope, id := "", "global", ""
		var modelID, scopeID *string
		if policy.Spec.TargetRef.Kind == "DeploymentConfig" {
			if policy.Spec.TargetRef.Name != "deployment" {
				return publication, errors.New("policy deployment target invalid")
			}
		} else if policy.Spec.TargetRef.Kind == "ModelRoute" {
			route, ok := routes[policy.Spec.TargetRef.Name]
			if !ok {
				return publication, errors.New("policy target unavailable")
			}
			// Disabled/terminating routes have no serving ingress. Omit their
			// model-scoped rules in both publication and direct-management modes;
			// keep the ModelPolicy so re-enabling restores its original limits.
			if !route.DeletionTimestamp.IsZero() || !route.Spec.Enabled {
				continue
			}
			model, scope = route.Spec.ModelID, "model"
			id = model
			modelID = &model
		} else {
			return publication, errors.New("policy target kind invalid")
		}
		if subject := policy.Spec.Subject; subject != nil {
			scope = subject.Type
			if scope != "model" {
				id = subject.ID
			}
		}
		if scope != "global" {
			scopeID = &id
		}
		add := func(suffix, unit string, maximum, seconds int64) error {
			interval, err := Interval(seconds)
			if err != nil {
				return err
			}
			config := gatewaypolicy.Configuration{Kind: unit, ScopeType: scope, ScopeID: scopeID, ModelID: modelID, Maximum: maximum, Interval: interval, Enabled: true}
			if !config.Valid() {
				return errors.New("invalid native policy")
			}
			// Kubernetes names can exceed the native rule ID's 80-character bound.
			ruleID := gatewayrender.ResourceSuffix(string(policy.UID)+"/"+policy.Name) + suffix
			version := int64(1)
			if native := policy.Spec.NativeRule; native != nil {
				if !gatewaypolicy.ValidID(native.ID) || native.Version < 1 || len(policy.Spec.RateLimits) != 0 || policy.Spec.Quota == nil {
					return errors.New("native policy identity invalid")
				}
				ruleID, version = native.ID, native.Version
			}
			if ids[ruleID] {
				return errors.New("duplicate native rule identity")
			}
			ids[ruleID] = true
			publication.Items = append(publication.Items, gatewaypolicy.Limit{ID: ruleID, Version: version, Configuration: config})
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
