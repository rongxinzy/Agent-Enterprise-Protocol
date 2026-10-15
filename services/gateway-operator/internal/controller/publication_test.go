package controller

import (
	"strings"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testPublicationMarkers() map[string]string {
	return map[string]string{gateway.PublicationSequence: "42", gateway.PublicationOperationID: "11347ddd-5234-4b3d-8936-a45f683e5f56", gateway.PublicationRevision: "release-42", gateway.PublicationContentHash: strings.Repeat("a", 64)}
}

func TestPublicationReferenceRejectsIncompleteMarkers(t *testing.T) {
	valid := testPublicationMarkers()
	for key, invalid := range map[string][]string{
		gateway.PublicationSequence:    {"", "invalid", "0", "-1"},
		gateway.PublicationOperationID: {"", "invalid", strings.ToUpper(valid[gateway.PublicationOperationID])},
		gateway.PublicationContentHash: {"", "invalid", strings.Repeat("A", 64)},
		gateway.PublicationRevision:    {"", "   ", strings.Repeat("r", 201)},
	} {
		for _, bad := range invalid {
			a := testPublicationMarkers()
			a[key] = bad
			if got := publicationReference(&metav1.ObjectMeta{Annotations: a}); got != nil {
				t.Fatalf("invalid marker %s=%s accepted: %+v", key, bad, got)
			}
		}
	}
	if publicationReference(&metav1.ObjectMeta{}) != nil {
		t.Fatal("unmarked CR accepted")
	}
	got := publicationReference(&metav1.ObjectMeta{Annotations: valid})
	if got == nil || got.Sequence != 42 || got.OperationID != valid[gateway.PublicationOperationID] || got.ContentHash != valid[gateway.PublicationContentHash] || got.Revision != "release-42" {
		t.Fatalf("valid marker = %+v", got)
	}
}
