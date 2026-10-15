package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPublicationSnapshotWaitsForEveryRouteAndPolicy(t *testing.T) {
	sum := sha256.Sum256([]byte("demo"))
	metadata := func() metav1.ObjectMeta {
		return metav1.ObjectMeta{Annotations: testPublicationMarkers(), Labels: map[string]string{gateway.PublicationDeploymentLabel: hex.EncodeToString(sum[:16])}}
	}
	for _, stage := range []string{"matching", "direct", "child-first", "parent-first", "new-route", "new-policy", "missing-policy-marker", "missing-parent-marker", "foreign", "invalid", "terminating"} {
		t.Run(stage, func(t *testing.T) {
			d := gateway.DeploymentConfig{ObjectMeta: metadata(), Spec: gateway.DeploymentConfigSpec{DeploymentID: "demo"}}
			r := gateway.ModelRoute{ObjectMeta: metadata()}
			p := gateway.ModelPolicy{ObjectMeta: metadata()}
			switch stage {
			case "direct":
				d.ObjectMeta = metav1.ObjectMeta{}
				r.ObjectMeta = metav1.ObjectMeta{}
				p.ObjectMeta = metav1.ObjectMeta{}
			case "child-first":
				d.ObjectMeta = metav1.ObjectMeta{}
			case "parent-first":
				r.ObjectMeta = metav1.ObjectMeta{}
				p.ObjectMeta = metav1.ObjectMeta{}
			case "new-route":
				r.Annotations[gateway.PublicationSequence] = "43"
			case "new-policy":
				p.Annotations[gateway.PublicationSequence] = "43"
			case "missing-policy-marker":
				delete(p.Annotations, gateway.PublicationContentHash)
			case "missing-parent-marker":
				delete(d.Annotations, gateway.PublicationContentHash)
			case "foreign":
				p.Labels[gateway.PublicationDeploymentLabel] = "foreign"
			case "invalid":
				d.Annotations[gateway.PublicationSequence] = "invalid"
			case "terminating":
				now := metav1.Now()
				p.DeletionTimestamp = &now
			}
			err := consistentPublication(&d, []gateway.ModelRoute{r}, []gateway.ModelPolicy{p})
			if stage == "matching" || stage == "direct" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errPublicationPending) {
				t.Fatalf("mixed publication accepted: %v", err)
			}
		})
	}
}
