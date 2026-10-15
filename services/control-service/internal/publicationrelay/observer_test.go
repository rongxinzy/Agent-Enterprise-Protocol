package publicationrelay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func observationFixture(t *testing.T, item repository.PublicationDelivery) (*gateway.DeploymentConfig, []*gateway.ModelRoute) {
	t.Helper()
	ref := publicationRef(item)
	conditions := []metav1.Condition{}
	for _, kind := range []string{gateway.ConditionAccepted, gateway.ConditionApplied, gateway.ConditionReady} {
		conditions = append(conditions, metav1.Condition{Type: kind, Status: metav1.ConditionTrue, ObservedGeneration: 1, Reason: "ResourcesApplied", LastTransitionTime: metav1.NewTime(item.CreatedAt.Add(time.Second))})
	}
	dc := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "aep-system", UID: "aggregate-uid", Generation: 1}, Spec: gateway.DeploymentConfigSpec{DeploymentID: item.DeploymentID},
		Status: gateway.DeploymentConfigStatus{ObservedGeneration: 1, ObservedPublication: &ref, AppliedSnapshot: strings.Repeat("c", 64), Conditions: conditions}}
	mark(dc, item)
	desired, err := desiredRoutes(item)
	if err != nil {
		t.Fatal(err)
	}
	routes := []*gateway.ModelRoute{}
	for id, spec := range desired {
		route := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Name: "model-" + id, Namespace: dc.Namespace, UID: "route-uid", Generation: 1}, Spec: spec,
			Status: gateway.ModelRouteStatus{ObservedGeneration: 1, ObservedPublication: ref.DeepCopy(), AppliedSnapshot: dc.Status.AppliedSnapshot, Conditions: append([]metav1.Condition(nil), conditions...)}}
		mark(route, item)
		routes = append(routes, route)
	}
	return dc, routes
}

func observationReader(t *testing.T, dc *gateway.DeploymentConfig, routes []*gateway.ModelRoute) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{dc}
	for _, route := range routes {
		objects = append(objects, route)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&gateway.ModelRoute{}, &gateway.DeploymentConfig{}).WithObjects(objects...).Build()
}

func TestPublicationObservationRequiresExactEvidence(t *testing.T) {
	for _, test := range []struct {
		name, accepted, applied string
		mutate                  func(*gateway.DeploymentConfig, *gateway.ModelRoute)
	}{
		{"matching", "True", "True", func(*gateway.DeploymentConfig, *gateway.ModelRoute) {}},
		{"old operator", "Unknown", "Unknown", func(dc *gateway.DeploymentConfig, _ *gateway.ModelRoute) { dc.Status.ObservedPublication = nil }},
		{"annotation-only republish", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.Status.ObservedPublication.Sequence-- }},
		{"operation mismatch", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.ObservedPublication.OperationID = "other"
		}},
		{"revision mismatch", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.ObservedPublication.Revision = "other"
		}},
		{"hash mismatch", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.ObservedPublication.ContentHash = strings.Repeat("b", 64)
		}},
		{"spec mismatch", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.Spec.Upstream.Model = "other" }},
		{"foreign owner", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { delete(r.Labels, ownerLabel) }},
		{"marker mismatch", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.Annotations[auditResourceKey] = "other" }},
		{"deployment mismatch", "Unknown", "Unknown", func(dc *gateway.DeploymentConfig, _ *gateway.ModelRoute) { dc.Spec.DeploymentID = "other" }},
		{"unidentified resource", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.UID = "" }},
		{"stale status", "Unknown", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.Generation++ }},
		{"stale condition", "Unknown", "True", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.Status.Conditions[0].ObservedGeneration-- }},
		{"missing condition", "True", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.Conditions = r.Status.Conditions[:1]
		}},
		{"unknown condition", "True", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.Conditions[1].Status = metav1.ConditionUnknown
		}},
		{"invalid condition", "True", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) { r.Status.Conditions[1].Status = "invalid" }},
		{"missing timestamp", "Unknown", "True", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.Conditions[0].LastTransitionTime = metav1.Time{}
		}},
		{"mixed aggregate", "True", "Unknown", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.AppliedSnapshot = strings.Repeat("d", 64)
		}},
		{"missing aggregate digest", "True", "Unknown", func(dc *gateway.DeploymentConfig, _ *gateway.ModelRoute) { dc.Status.AppliedSnapshot = "" }},
		{"operator rejection", "False", "False", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.Conditions[0].Status = metav1.ConditionFalse
			r.Status.Conditions[1].Status = metav1.ConditionFalse
		}},
		{"apply rejection", "True", "False", func(_ *gateway.DeploymentConfig, r *gateway.ModelRoute) {
			r.Status.Conditions[1].Status = metav1.ConditionFalse
			r.Status.AppliedSnapshot = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := delivery(2, []gatewayrender.Route{testRoute("chat")}, nil)
			item.CreatedAt = time.Now().UTC()
			dc, routes := observationFixture(t, item)
			test.mutate(dc, routes[0])
			o := &Observer{Reader: observationReader(t, dc, routes), Namespace: dc.Namespace}
			got, err := o.Observe(t.Context(), item)
			if err != nil || len(got) != 3 || got[0].Status != test.accepted || got[1].Status != test.applied || got[2].Status != "Unknown" || got[2].Reason != "DataPlaneNotObserved" || got[2].ObservedRevision != nil {
				t.Fatalf("observation = %+v, %v", got, err)
			}
			for _, condition := range got {
				if (condition.Status == "Unknown") != (condition.ObservedRevision == nil) || condition.LastTransitionTime.Before(item.CreatedAt) {
					t.Fatalf("invalid revision/time = %+v", condition)
				}
			}
		})
	}
}

func TestPublicationObservationMissingAndChangingInventory(t *testing.T) {
	for _, stage := range []string{"empty", "tombstone", "missing", "extra", "wrong-name", "deleting", "missing-aggregate", "list-error", "get-error", "confirm-error", "aggregate-changed", "invalid-snapshot"} {
		t.Run(stage, func(t *testing.T) {
			item := delivery(1, []gatewayrender.Route{}, nil)
			if stage != "empty" {
				item = delivery(1, []gatewayrender.Route{}, []gatewayrender.Route{testRoute("removed")})
			}
			dc, routes := observationFixture(t, item)
			switch stage {
			case "missing":
				routes = nil
			case "extra":
				extra := routes[0].DeepCopy()
				extra.Name = "model-extra"
				routes = append(routes, extra)
			case "wrong-name":
				routes[0].Name = "not-the-published-name"
			case "deleting":
				routes[0].DeletionTimestamp = &metav1.Time{Time: time.Now()}
				routes[0].Finalizers = []string{"test/finalizer"}
			case "invalid-snapshot":
				item.ContentHash = "invalid"
			}
			base := observationReader(t, dc, routes)
			gets := 0
			reader := &faultClient{Client: base, get: func(ctx context.Context, key client.ObjectKey, _ client.Object) error {
				gets++
				if stage == "get-error" || (stage == "confirm-error" && gets == 2) {
					return errors.New("sensitive diagnostic")
				}
				if stage == "aggregate-changed" && gets == 2 {
					var latest gateway.DeploymentConfig
					if err := base.Get(ctx, key, &latest); err != nil {
						return err
					}
					latest.Annotations[revisionKey] = "changed"
					return base.Update(ctx, &latest)
				}
				return nil
			}, list: func(ctx context.Context, list client.ObjectList) error {
				if stage == "list-error" {
					return errors.New("sensitive diagnostic")
				}
				return base.List(ctx, list, client.InNamespace(dc.Namespace))
			}}
			if stage == "missing-aggregate" {
				if err := base.Delete(t.Context(), dc); err != nil {
					t.Fatal(err)
				}
			}
			got, err := (&Observer{Reader: reader, Namespace: dc.Namespace}).Observe(t.Context(), item)
			wantErr := stage == "missing-aggregate" || stage == "list-error" || stage == "get-error" || stage == "confirm-error" || stage == "invalid-snapshot"
			if (err != nil) != wantErr {
				t.Fatalf("observation = %+v %v", got, err)
			}
			if wantErr {
				return
			}
			want := "Unknown"
			if stage == "empty" || stage == "tombstone" {
				want = "True"
			}
			if got[0].Status != want || got[1].Status != want || got[2].Status != "Unknown" {
				t.Fatalf("observation = %+v", got)
			}
			// The read-only reader must never overwrite CR metadata/spec/status.
			var live gateway.DeploymentConfig
			if err := base.Get(t.Context(), client.ObjectKeyFromObject(dc), &live); err != nil {
				t.Fatal(err)
			}
			if !meta.IsStatusConditionTrue(live.Status.Conditions, gateway.ConditionReady) {
				t.Fatal("observer mutated status")
			}
		})
	}
}
