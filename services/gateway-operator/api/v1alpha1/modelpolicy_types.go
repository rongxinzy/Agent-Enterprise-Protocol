package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyTargetReference selects the ModelRoute a ModelPolicy applies to. Only
// ModelRoute targets exist at the pilot stage.
type PolicyTargetReference struct {
	// Kind is the target resource kind; only ModelRoute is supported.
	// +kubebuilder:validation:Enum=ModelRoute
	Kind string `json:"kind"`
	// Name is the target ModelRoute name in the same namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// RateLimit caps requests per interval for the policy target.
type RateLimit struct {
	// Maximum is the number of requests allowed per interval.
	// +kubebuilder:validation:Minimum=1
	Maximum int64 `json:"maximum"`
	// IntervalSeconds is the window length in seconds.
	// +kubebuilder:validation:Minimum=1
	IntervalSeconds int64 `json:"intervalSeconds"`
}

// Quota caps total consumption for the policy target over an interval.
type Quota struct {
	// Maximum is the number of requests allowed per interval.
	// +kubebuilder:validation:Minimum=1
	Maximum int64 `json:"maximum"`
	// IntervalSeconds is the window length in seconds.
	// +kubebuilder:validation:Minimum=1
	IntervalSeconds int64 `json:"intervalSeconds"`
}

// ModelPolicySpec holds rate limiting and quota intent for one ModelRoute.
type ModelPolicySpec struct {
	TargetRef PolicyTargetReference `json:"targetRef"`
	// +optional
	// +kubebuilder:validation:MaxItems=32
	RateLimits []RateLimit `json:"rateLimits,omitempty"`
	// +optional
	Quota *Quota `json:"quota,omitempty"`
}

// ModelPolicyStatus carries the operator's observations for one ModelPolicy.
type ModelPolicyStatus struct {
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
// +kubebuilder:resource:path=modelpolicies,scope=Namespaced
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ModelPolicy is rate limiting and quota intent bound to one ModelRoute.
type ModelPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelPolicySpec   `json:"spec,omitempty"`
	Status ModelPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelPolicyList is the list form of ModelPolicy.
type ModelPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelPolicy `json:"items"`
}
