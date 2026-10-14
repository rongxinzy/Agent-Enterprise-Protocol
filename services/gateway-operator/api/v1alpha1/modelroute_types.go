package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported by the gateway operator. Only Accepted exists at
// the watch-only pilot stage; Applied/Ready land with the render/apply stage
// (see the hybrid architecture doc: Accepted = API server accepted, Applied =
// rendered resources in place, Ready = data plane observed effective).
const (
	// ConditionAccepted reports whether the spec passed the operator's
	// shallow validation (shape beyond the CRD schema, reference existence).
	ConditionAccepted = "Accepted"
)

// Accepted condition reasons.
const (
	ReasonAccepted       = "Accepted"
	ReasonInvalidSpec    = "InvalidSpec"
	ReasonTargetNotFound = "TargetNotFound"
)

// ModelRouteUpstream selects the provider endpoint for one model route.
type ModelRouteUpstream struct {
	// Endpoint is the absolute provider base URL.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	Endpoint string `json:"endpoint"`
	// Model is the upstream model identifier sent to the provider.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	Model string `json:"model"`
}

// CredentialReference names the Secret key holding the provider credential.
// The operator never reads the value at this stage; later stages resolve it
// through the protected delivery path only.
type CredentialReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// TenantScope narrows a route to the listed teams/roles; empty means visible
// to every tenant subject.
type TenantScope struct {
	// +optional
	// +kubebuilder:validation:MaxItems=256
	Teams []string `json:"teams,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=256
	Roles []string `json:"roles,omitempty"`
}

// ModelRouteSpec is one model route, owned at the route level by this CRD.
type ModelRouteSpec struct {
	// ModelID is the catalog model identifier this route serves. AEP caps
	// model ids at 40 characters (control-service identifier_slug).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=40
	ModelID string `json:"modelId"`
	// Protocol is the wire protocol the route serves.
	// +kubebuilder:validation:Enum=openai;anthropic
	Protocol string             `json:"protocol"`
	Upstream ModelRouteUpstream `json:"upstream"`
	// +optional
	CredentialRef *CredentialReference `json:"credentialRef,omitempty"`
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
	// +optional
	TenantScope *TenantScope `json:"tenantScope,omitempty"`
}

// ModelRouteStatus carries the operator's observations for one ModelRoute.
type ModelRouteStatus struct {
	// ObservedGeneration is the metadata.generation the operator last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions report the reconciliation state; the pilot stage writes only
	// the Accepted condition.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=modelroutes,scope=Namespaced
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.modelId`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ModelRoute is one route-level model routing resource.
type ModelRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelRouteSpec   `json:"spec,omitempty"`
	Status ModelRouteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelRouteList is the list form of ModelRoute.
type ModelRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelRoute `json:"items"`
}
