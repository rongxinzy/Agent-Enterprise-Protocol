package publicationrelay

import (
	"encoding/json"
	"errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func withPolicies(t *testing.T, item repository.PublicationDelivery, rules []gatewaypolicy.Limit) repository.PublicationDelivery {
	t.Helper()
	var state snapshot
	if err := json.Unmarshal(item.Payload, &state); err != nil {
		t.Fatal(err)
	}
	state.GatewayLimits = &rules
	item.Payload, _ = json.Marshal(state)
	item.ContentHash = publicationHash(string(item.Payload))
	return item
}

func TestPolicyProjectionPreservesNativeCounterScope(t *testing.T) {
	for _, scope := range []string{"global", "model", "user", "team", "role"} {
		for _, modelScoped := range []bool{false, true} {
			t.Run(scope+"/"+map[bool]string{true: "model", false: "all"}[modelScoped], func(t *testing.T) {
				model, id := "chat", "subject"
				c := gatewaypolicy.Configuration{Kind: "tokens", ScopeType: scope, Maximum: 100, Interval: "day", Enabled: true}
				if scope != "global" {
					c.ScopeID = &id
				}
				if scope == "model" {
					c.ScopeID = &model
				}
				if modelScoped {
					c.ModelID = &model
				}
				item := withPolicies(t, delivery(1, []gatewayrender.Route{testRoute(model)}, nil), []gatewaypolicy.Limit{{ID: "native-rule", Version: 7, Configuration: c}})
				p := testProjector(t)
				if err := p.Project(t.Context(), item); err != nil {
					t.Fatal(err)
				}
				var policies gateway.ModelPolicyList
				if err := p.Client.List(t.Context(), &policies, client.InNamespace(p.Namespace)); err != nil {
					t.Fatal(err)
				}
				if len(policies.Items) != 1 {
					t.Fatal("shared rule was expanded by model")
				}
				got := policies.Items[0].Spec
				if got.NativeRule.ID != "native-rule" || got.NativeRule.Version != 7 || got.Subject.Type != scope || got.Quota.Maximum != 100 || got.Quota.Unit != "tokens" || got.Quota.IntervalSeconds != 86400 {
					t.Fatalf("counter identity or scope changed: %+v", got)
				}
				wantTarget := "DeploymentConfig"
				if modelScoped || scope == "model" {
					wantTarget = "ModelRoute"
				}
				if got.TargetRef.Kind != wantTarget || (scope != "global" && scope != "model" && got.Subject.ID != id) {
					t.Fatalf("subject/target changed: %+v", got)
				}
				rv := policies.Items[0].ResourceVersion
				if err := p.Project(t.Context(), item); err != nil {
					t.Fatal(err)
				}
				var replay gateway.ModelPolicyList
				if err := p.Client.List(t.Context(), &replay, client.InNamespace(p.Namespace)); err != nil {
					t.Fatal(err)
				}
				if replay.Items[0].ResourceVersion != rv {
					t.Fatal("replay rewrote identical native policy")
				}
			})
		}
	}
}

func TestPolicyTombstoneFencesLateDelivery(t *testing.T) {
	p := testProjector(t)
	rules := []gatewaypolicy.Limit{{ID: "shared", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 10, Interval: "minute", Enabled: true}}}
	first := withPolicies(t, delivery(1, []gatewayrender.Route{testRoute("chat")}, nil), rules)
	if err := p.Project(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	rules[0].Version = 2
	rules[0].Configuration.Enabled = false
	second := withPolicies(t, delivery(2, []gatewayrender.Route{testRoute("chat")}, nil), rules)
	if err := p.Project(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	var list gateway.ModelPolicyList
	if err := p.Client.List(t.Context(), &list, client.InNamespace(p.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || !list.Items[0].Spec.Disabled || list.Items[0].Annotations[operationKey] != second.OperationID {
		t.Fatal("removed policy fence lost")
	}
	if err := p.Project(t.Context(), first); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old rule resurrected: %v", err)
	}
	// A missing historical rule is an invalid snapshot, never authorization to
	// remove or replace an unrelated policy owned by another writer.
	empty := withPolicies(t, delivery(3, []gatewayrender.Route{testRoute("chat")}, nil), []gatewaypolicy.Limit{})
	if err := p.Project(t.Context(), empty); !errors.Is(err, ErrOwnership) {
		t.Fatalf("missing tombstone: %v", err)
	}
}

func TestPoliciesRejectInvalidAndForeignSnapshots(t *testing.T) {
	for _, change := range []func(*gatewaypolicy.Limit){
		func(r *gatewaypolicy.Limit) { r.ID = "bad id" }, func(r *gatewaypolicy.Limit) { r.Version = 0 },
		func(r *gatewaypolicy.Limit) { r.Configuration.Interval = "week" }, func(r *gatewaypolicy.Limit) { r.Configuration.ScopeID = new(string) },
	} {
		rule := gatewaypolicy.Limit{ID: "rule", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "minute", Enabled: true}}
		change(&rule)
		item := withPolicies(t, delivery(1, nil, nil), []gatewaypolicy.Limit{rule})
		if _, err := desiredPolicies(item); !errors.Is(err, ErrSnapshot) {
			t.Fatalf("invalid rule accepted: %v", err)
		}
	}
	p := testProjector(t)
	item := withPolicies(t, delivery(1, nil, nil), []gatewaypolicy.Limit{{ID: "rule", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "minute", Enabled: true}}})
	desired, _ := desiredPolicies(item)
	for name, spec := range desired {
		obj := &gateway.ModelPolicy{Spec: spec}
		obj.Name = name
		obj.Namespace = p.Namespace
		if err := p.Client.Create(t.Context(), obj); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Project(t.Context(), item); !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign policy adopted: %v", err)
	}
}

func TestPolicyEvidenceRejectsUnprocessedAndMixedRules(t *testing.T) {
	for _, stage := range []string{"matching", "unprocessed", "old-generation", "mixed-snapshot", "spec-drift", "missing"} {
		t.Run(stage, func(t *testing.T) {
			item := withPolicies(t, delivery(1, []gatewayrender.Route{testRoute("chat")}, nil), []gatewaypolicy.Limit{{ID: "rule", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "minute", Enabled: true}}})
			dc, routes := observationFixture(t, item)
			reader := observationReader(t, dc, routes)
			desired, _ := desiredPolicies(item)
			for name, spec := range desired {
				ref := publicationRef(item)
				policy := &gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dc.Namespace, UID: "policy-uid", Generation: 1}, Spec: spec,
					Status: gateway.ModelPolicyStatus{ObservedGeneration: 1, ObservedPublication: &ref, AppliedSnapshot: dc.Status.AppliedSnapshot, Conditions: append([]metav1.Condition(nil), dc.Status.Conditions...)}}
				mark(policy, item)
				switch stage {
				case "unprocessed":
					policy.Status.ObservedPublication = nil
				case "old-generation":
					policy.Generation++
				case "mixed-snapshot":
					policy.Status.AppliedSnapshot = "old"
				case "spec-drift":
					policy.Spec.Quota.Maximum++
				case "missing":
					continue
				}
				if err := reader.Create(t.Context(), policy); err != nil {
					t.Fatal(err)
				}
			}
			got, err := (&Observer{Reader: reader, Namespace: dc.Namespace}).Observe(t.Context(), item)
			if err != nil {
				t.Fatal(err)
			}
			want := "Unknown"
			if stage == "matching" {
				want = "True"
			}
			if got[1].Status != want || got[2].Status != "Unknown" {
				t.Fatalf("%s: %+v", stage, got)
			}
		})
	}
}
