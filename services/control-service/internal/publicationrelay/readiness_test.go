package publicationrelay

import (
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestReadyRequiresFreshMatchingFullFleetEvidence(t *testing.T) {
	for _, scenario := range []string{"fresh", "missing", "expired", "future", "wrong-snapshot", "no-gateway", "no-pods", "duplicate-pods", "stale-generation", "child-not-ready", "applied-false", "wrong-publication"} {
		t.Run(scenario, func(t *testing.T) {
			item := delivery(2, []gatewayrender.Route{testRoute("chat")}, nil)
			item.CreatedAt = time.Now().Add(-time.Minute)
			dc, routes := observationFixture(t, item)
			dc.Status.DataPlane = &gateway.DataPlaneEvidence{Snapshot: dc.Status.AppliedSnapshot, ObservedAt: metav1.NewTime(time.Now().Add(-time.Second)), GatewayUID: "gateway", PodUIDs: []types.UID{"one", "two"}}
			switch scenario {
			case "missing":
				dc.Status.DataPlane = nil
			case "expired":
				dc.Status.DataPlane.ObservedAt = metav1.NewTime(time.Now().Add(-91 * time.Second))
			case "future":
				dc.Status.DataPlane.ObservedAt = metav1.NewTime(time.Now().Add(time.Hour))
			case "wrong-snapshot":
				dc.Status.DataPlane.Snapshot = strings.Repeat("a", 64)
			case "no-gateway":
				dc.Status.DataPlane.GatewayUID = ""
			case "no-pods":
				dc.Status.DataPlane.PodUIDs = nil
			case "duplicate-pods":
				dc.Status.DataPlane.PodUIDs = []types.UID{"one", "one"}
			case "stale-generation":
				dc.Generation++
			case "child-not-ready":
				routes[0].Status.Conditions[2].Status = metav1.ConditionUnknown
			case "applied-false":
				routes[0].Status.Conditions[1].Status = metav1.ConditionFalse
			case "wrong-publication":
				routes[0].Status.ObservedPublication.Sequence--
			}
			o := &Observer{Reader: observationReader(t, dc, routes), Namespace: dc.Namespace}
			result, err := o.Observe(t.Context(), item)
			if err != nil {
				t.Fatal(err)
			}
			if (result[2].Status == "True") != (scenario == "fresh") {
				t.Fatalf("unexpected Ready=%s", result[2].Status)
			}
			if scenario == "fresh" && (result[2].ObservedRevision == nil || *result[2].ObservedRevision != item.Revision) {
				t.Fatal("revision not bound")
			}
		})
	}
}
