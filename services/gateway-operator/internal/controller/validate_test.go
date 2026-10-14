package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func TestValidateModelRoute(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		ref      *gatewayv1alpha1.CredentialReference
		wantErr  bool
	}{
		{name: "valid", endpoint: "https://provider.example/v1"},
		{name: "empty host", endpoint: "http://", wantErr: true},
		{name: "not a URL", endpoint: "://broken", wantErr: true},
		{name: "credentials in URL", endpoint: "http://user:pass@provider.example/v1", wantErr: true},
		{name: "query in URL", endpoint: "https://provider.example/v1?x=1", wantErr: true},
		{name: "fragment in URL", endpoint: "https://provider.example/v1#frag", wantErr: true},
		{name: "empty credential name", endpoint: "https://provider.example/v1", ref: &gatewayv1alpha1.CredentialReference{Name: " ", Key: "k"}, wantErr: true},
		{name: "empty credential key", endpoint: "https://provider.example/v1", ref: &gatewayv1alpha1.CredentialReference{Name: "n", Key: " "}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := &gatewayv1alpha1.ModelRoute{
				Spec: gatewayv1alpha1.ModelRouteSpec{
					Upstream:      gatewayv1alpha1.ModelRouteUpstream{Endpoint: test.endpoint, Model: "m"},
					CredentialRef: test.ref,
				},
			}
			if err := validateModelRoute(route); (err != nil) != test.wantErr {
				t.Fatalf("validateModelRoute(%q) = %v", test.endpoint, err)
			}
		})
	}
}

func TestSetConditionUpsertSemantics(t *testing.T) {
	accepted := metav1.Condition{Type: gatewayv1alpha1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: gatewayv1alpha1.ReasonAccepted, Message: "ok"}
	var conditions []metav1.Condition
	if !setCondition(&conditions, accepted) {
		t.Fatal("first insert must report a change")
	}
	// Identical condition is a no-op.
	if setCondition(&conditions, metav1.Condition{Type: gatewayv1alpha1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: gatewayv1alpha1.ReasonAccepted, Message: "ok"}) {
		t.Fatal("identical condition must not report a change")
	}
	// A different type in the list is skipped, not matched.
	other := metav1.Condition{Type: "Stalled", Status: metav1.ConditionTrue, Reason: "x", Message: "y"}
	if !setCondition(&conditions, other) {
		t.Fatal("new condition type must report a change")
	}
	if setCondition(&conditions, accepted) {
		t.Fatal("accepted condition unchanged while another type exists")
	}
	// A changed status replaces in place.
	if !setCondition(&conditions, metav1.Condition{Type: gatewayv1alpha1.ConditionAccepted, Status: metav1.ConditionFalse, Reason: gatewayv1alpha1.ReasonInvalidSpec, Message: "bad"}) {
		t.Fatal("changed condition must report a change")
	}
	if len(conditions) != 2 {
		t.Fatalf("conditions = %#v", conditions)
	}
}
