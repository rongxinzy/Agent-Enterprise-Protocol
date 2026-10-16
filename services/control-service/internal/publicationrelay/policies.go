package publicationrelay

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

// Rules are never expanded by model: that would split an existing subject's
// shared Redis counter. Soft-deleted rows stay in snapshots as disabled fences.
func desiredPolicies(item repository.PublicationDelivery) (map[string]gateway.ModelPolicySpec, error) {
	var captured snapshot
	if json.Unmarshal(item.Payload, &captured) != nil {
		return nil, ErrSnapshot
	}
	if captured.GatewayLimits == nil {
		return nil, nil
	}
	routes, err := desiredRoutes(item)
	if err != nil {
		return nil, err
	}
	desired := make(map[string]gateway.ModelPolicySpec)
	for _, rule := range *captured.GatewayLimits {
		c := rule.Configuration
		if !gatewaypolicy.ValidID(rule.ID) || rule.Version < 1 || !c.Valid() {
			return nil, ErrSnapshot
		}
		name := "limit-" + owner(rule.ID)
		if _, exists := desired[name]; exists {
			return nil, ErrSnapshot
		}
		spec := gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "DeploymentConfig", Name: "deployment"},
			Disabled: !c.Enabled, NativeRule: &gateway.NativePolicyRule{ID: rule.ID, Version: rule.Version},
			Subject: &gateway.PolicySubject{Type: c.ScopeType}, Quota: &gateway.Quota{Unit: c.Kind, Maximum: c.Maximum}}
		if c.ScopeID != nil && c.ScopeType != "model" {
			spec.Subject.ID = *c.ScopeID
		}
		model := c.ModelID
		if c.ScopeType == "model" {
			model = c.ScopeID
		}
		if model != nil {
			if _, exists := routes[*model]; !exists && c.Enabled {
				return nil, ErrSnapshot
			}
			spec.TargetRef = gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "model-" + *model}
		}
		switch c.Interval {
		case "second":
			spec.Quota.IntervalSeconds = 1
		case "minute":
			spec.Quota.IntervalSeconds = 60
		case "hour":
			spec.Quota.IntervalSeconds = 3600
		case "day":
			spec.Quota.IntervalSeconds = 86400
		}
		desired[name] = spec
	}
	return desired, nil
}

func (p *Projector) projectPolicies(ctx context.Context, item repository.PublicationDelivery) error {
	desired, err := desiredPolicies(item)
	if err != nil || desired == nil {
		return err
	}
	var policies gateway.ModelPolicyList
	if err := p.Client.List(ctx, &policies, client.InNamespace(p.Namespace)); err != nil {
		return err
	}
	live := make(map[string]*gateway.ModelPolicy)
	for i := range policies.Items {
		obj := &policies.Items[i]
		if _, exists := desired[obj.Name]; !exists {
			return ErrOwnership
		}
		live[obj.Name] = obj
	}
	for name, spec := range desired {
		obj := live[name]
		if obj == nil {
			obj = &gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: name}, Spec: spec}
			mark(obj, item)
			if err := p.Client.Create(ctx, obj, client.FieldOwner("aep-publication-relay")); err != nil {
				return err
			}
			continue
		}
		if err := checkMarker(obj, item, true); err != nil {
			return err
		}
		base := obj.DeepCopy()
		obj.Spec = spec
		mark(obj, item)
		if !reflect.DeepEqual(base.Spec, obj.Spec) || !reflect.DeepEqual(base.ObjectMeta, obj.ObjectMeta) {
			if err := p.Client.Update(ctx, obj, client.FieldOwner("aep-publication-relay")); err != nil {
				return err
			}
		}
	}
	if err := p.Client.List(ctx, &policies, client.InNamespace(p.Namespace)); err != nil {
		return err
	}
	return verifyPolicies(&policies, desired, item)
}

func verifyPolicies(policies *gateway.ModelPolicyList, desired map[string]gateway.ModelPolicySpec, item repository.PublicationDelivery) error {
	if len(policies.Items) != len(desired) {
		return ErrOwnership
	}
	for i := range policies.Items {
		obj := &policies.Items[i]
		spec, exists := desired[obj.Name]
		if !exists {
			return ErrOwnership
		}
		if err := checkMarker(obj, item, true); err != nil {
			return err
		}
		if obj.Annotations[sequenceKey] != strconv.FormatInt(item.Sequence, 10) || !reflect.DeepEqual(obj.Spec, spec) {
			return ErrOwnership
		}
	}
	return nil
}
