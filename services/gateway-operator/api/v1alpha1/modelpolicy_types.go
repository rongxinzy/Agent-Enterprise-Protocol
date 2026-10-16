package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyTargetReference selects one model or deployment-wide policy scope.
type PolicyTargetReference struct {
	// Kind selects one model or the whole deployment.
	// +kubebuilder:validation:Enum=ModelRoute;DeploymentConfig
	Kind string `json:"kind"`
	// Name is the target in the same namespace; deployments use "deployment".
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
// +kubebuilder:validation:XValidation:rule="self.type in ['model','global'] ? !has(self.id) : has(self.id)",message="user/team/role subjects require an id; model/global subjects must omit it"
type PolicySubject struct {
	// +kubebuilder:validation:Enum=model;global;user;team;role
	Type string `json:"type"`
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	ID string `json:"id,omitempty"`
}

// ModelPolicySpec holds native rules for one model or the whole deployment.
// +kubebuilder:validation:XValidation:rule="self.targetRef.kind != 'DeploymentConfig' || self.targetRef.name == 'deployment'",message="deployment target must name deployment"
// +kubebuilder:validation:XValidation:rule="!has(self.subject) || self.subject.type != 'model' || self.targetRef.kind == 'ModelRoute'",message="model subjects require a model target"
// +kubebuilder:validation:XValidation:rule="!has(self.nativeRule) || (has(self.quota) && (!has(self.rateLimits) || size(self.rateLimits) == 0))",message="native rule identity requires exactly one quota"
type ModelPolicySpec struct {
	TargetRef PolicyTargetReference `json:"targetRef"`
	// Disabled retains a publication fence while rendering no native rule.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// NativeRule preserves the business rule identity/version used by Higress.
	// +optional
	NativeRule *NativePolicyRule `json:"nativeRule,omitempty"`
	// Subject defaults to model or global according to the target. User/team/role
	// subjects use trusted AEP membership headers, optionally intersected by model.
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
	// +optional
	ObservedPublication *PublicationReference `json:"observedPublication,omitempty"`
	// +optional
	AppliedSnapshot string `json:"appliedSnapshot,omitempty"`
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

// NativePolicyRule prevents recreation or projection from resetting counters.
type NativePolicyRule struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`
	ID string `json:"id"`
	// +kubebuilder:validation:Minimum=1
	Version int64 `json:"version"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=modelpolicies,scope=Namespaced
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=='Accepted')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ModelPolicy is native rate limiting and quota intent for a model or deployment.
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
