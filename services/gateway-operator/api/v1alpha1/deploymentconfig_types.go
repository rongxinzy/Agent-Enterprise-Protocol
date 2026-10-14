package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AppliedResource identifies an operator-owned object. Cross-namespace
// ownership uses UID labels and finalizers, not invalid OwnerReferences.
type AppliedResource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// NativeGatewaySpec configures the existing Higress Redis-backed plugins.
// Credential values never enter the CR; these references resolve only in the
// configured native Secret namespace.
type NativeGatewaySpec struct {
	Enabled bool `json:"enabled"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RedisService string `json:"redisService"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	RedisPort int `json:"redisPort"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	RedisUsername string `json:"redisUsername,omitempty"`
	// +kubebuilder:validation:Minimum=0
	RedisDatabase int `json:"redisDatabase"`
	// +optional
	RedisPasswordRef *CredentialReference `json:"redisPasswordRef,omitempty"`
	// +optional
	QuotaAdminCredentialRef *CredentialReference `json:"quotaAdminCredentialRef,omitempty"`
}

// DeploymentConfigSpec is the gateway aggregation scope. Business defaults,
// compute assignments and async control APIs are separate migration stages.
type DeploymentConfigSpec struct {
	// DeploymentID keeps the existing reconciler's resource-name scope.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._-]*$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="deploymentId is immutable"
	DeploymentID string `json:"deploymentId"`
	// +optional
	NativeGateway *NativeGatewaySpec `json:"nativeGateway,omitempty"`
}

type DeploymentConfigStatus struct {
	// AppliedSnapshot changes when any render input or referenced credential changes.
	// It is an opaque digest, never a data-plane readiness proof.
	// +optional
	AppliedSnapshot string `json:"appliedSnapshot,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// AppliedResources contains deployment-level plugins and quota-admin resources.
	// +optional
	AppliedResources []AppliedResource `json:"appliedResources,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=deploymentconfigs,scope=Namespaced
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'deployment'",message="DeploymentConfig must be named deployment"
// +kubebuilder:printcolumn:name="Deployment",type=string,JSONPath=`.spec.deploymentId`
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=`.status.conditions[?(@.type=='Applied')].status`
type DeploymentConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeploymentConfigSpec   `json:"spec,omitempty"`
	Status            DeploymentConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeploymentConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeploymentConfig `json:"items"`
}
