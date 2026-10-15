package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Applied reports Kubernetes writes, while Ready requires an independent
// data-plane observation. Successful SSA must never imply Ready=True.
const (
	// ConditionAccepted reports whether the spec passed the operator's
	// shallow validation (shape beyond the CRD schema, reference existence).
	ConditionAccepted = "Accepted"
	ConditionApplied  = "Applied"
	ConditionReady    = "Ready"
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
// Namespaces are fixed by the operator deployment, never supplied by a CR.
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
	// DeploymentRef selects the singleton DeploymentConfig in this namespace.
	// +optional
	// +kubebuilder:default=deployment
	// +kubebuilder:validation:Enum=deployment
	DeploymentRef string `json:"deploymentRef,omitempty"`
	// ModelID is the catalog model identifier this route serves. AEP caps
	// model ids at 40 characters (control-service identifier_slug).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]*$`
	ModelID string `json:"modelId"`
	// Protocol is the wire protocol the route serves.
	// +kubebuilder:validation:Enum=openai;anthropic
	Protocol string `json:"protocol"`
	// ProviderType selects the native ai-proxy adapter; empty defaults to openai.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]*$`
	ProviderType string             `json:"providerType,omitempty"`
	Upstream     ModelRouteUpstream `json:"upstream"`
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
	// Conditions report validation, Kubernetes writes and data-plane observation.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// AppliedResources records route-level resources only, never aggregate plugins.
	// +optional
	AppliedResources []AppliedResource `json:"appliedResources,omitempty"`
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
