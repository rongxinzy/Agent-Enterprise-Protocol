package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyTargetReference selects the ModelRoute a ModelPolicy applies to. Only
// ModelRoute targets are currently supported.
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
	// +kubebuilder:validation:Maximum=1000000000000
	Maximum int64 `json:"maximum"`
	// IntervalSeconds is the window length in seconds.
	// +kubebuilder:validation:Enum=1;60;3600;86400
	IntervalSeconds int64 `json:"intervalSeconds"`
}

// Quota caps total consumption for the policy target over an interval.
type Quota struct {
	// Unit is explicit; existing request quotas never silently become tokens.
	// +optional
	// +kubebuilder:default=requests
	// +kubebuilder:validation:Enum=requests;tokens
	Unit string `json:"unit,omitempty"`
	// Maximum is consumption in Unit per interval.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000000000000
	Maximum int64 `json:"maximum"`
	// IntervalSeconds is the window length in seconds.
	// +kubebuilder:validation:Enum=1;60;3600;86400
	IntervalSeconds int64 `json:"intervalSeconds"`
}

// PolicySubject uses trusted membership headers injected by the authorizer.
// +kubebuilder:validation:XValidation:rule="self.type == 'model' ? !has(self.id) : has(self.id)",message="user/team/role subjects require an id; model subjects must omit it"
type PolicySubject struct {
	// +kubebuilder:validation:Enum=model;user;team;role
	Type string `json:"type"`
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	ID string `json:"id,omitempty"`
}

// ModelPolicySpec holds rate limiting and quota intent for one ModelRoute.
type ModelPolicySpec struct {
	TargetRef PolicyTargetReference `json:"targetRef"`
	// Subject defaults to the target model. User/team/role subjects are intersected
	// with that model, preserving AEP's composite membership header semantics.
	// +optional
	Subject *PolicySubject `json:"subject,omitempty"`
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
	// Conditions report semantic acceptance, aggregate application and
	// independent data-plane readiness.
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
