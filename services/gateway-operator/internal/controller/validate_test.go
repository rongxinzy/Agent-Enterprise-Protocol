package controller

import (
	"testing"

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
