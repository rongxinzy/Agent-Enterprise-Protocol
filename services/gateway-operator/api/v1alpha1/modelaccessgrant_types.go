package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ModelAccessSubject identifies an AEP account, including kind=agent accounts.
// These are derived effective grants, never direct user/team assignments.
type ModelAccessSubject struct {
	// +kubebuilder:validation:Enum=human;agent
	Kind string `json:"kind"`
	// ID is users.id, not a username, owner ID, Pod name or caller header.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	ID string `json:"id"`
}

// ModelAccessWorkload binds one employee to its dedicated ServiceAccount UID.
// Identity must be established by a trusted Kubernetes token or mTLS verifier;
// none of these declarations authenticate a request by themselves.
type ModelAccessWorkload struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ServiceAccountName string `json:"serviceAccountName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Type=string
	ServiceAccountUID types.UID `json:"serviceAccountUid"`
	// EmployeeUID fences deletion and recreation of a DigitalEmployee.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Type=string
	EmployeeUID types.UID `json:"employeeUid"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Audience string `json:"audience"`
}

// ModelAccessBinding scopes a derived grant to one session or workload.
// +kubebuilder:validation:XValidation:rule="self.kind == 'session' ? (has(self.sessionId) && !has(self.workload)) : (has(self.workload) && !has(self.sessionId))",message="binding requires exactly its session or workload identity"
type ModelAccessBinding struct {
	// +kubebuilder:validation:Enum=session;kubernetes
	Kind string `json:"kind"`
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	SessionID string `json:"sessionId,omitempty"`
	// +optional
	Workload *ModelAccessWorkload `json:"workload,omitempty"`
}

// ModelAccessLicense is an exact accepted entitlement binding, not a signed
// license or private key. Human verification checks JWT issuer/audience/use/time
// and matches BOTH license ID and digest; workload attestations reference the
// same exact pair. Empty/omitted bindings never waive deployment entitlement.
type ModelAccessLicense struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	ID string `json:"id"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	Digest string `json:"digest"`
	// ValidUntil is the existing license grace boundary; absent means perpetual.
	// Check it per request even when a complete authorization result is cached.
	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`
}

// ModelAccessGrantSpec is the immutable, versioned output of DB authorization.
// One resource covers one subject binding; a role change republishes only the
// affected resources. The ModelPolicy quota contract remains independent.
// +kubebuilder:validation:XValidation:rule="self.deploymentId == oldSelf.deploymentId && self.subject == oldSelf.subject && self.binding == oldSelf.binding",message="deployment, subject and binding are immutable"
// +kubebuilder:validation:XValidation:rule="self.publication.sequence > oldSelf.publication.sequence || self == oldSelf",message="changed grants require a newer publication; replay and same-version mutation are forbidden"
// +kubebuilder:validation:XValidation:rule="self.subject.kind == 'agent' || self.binding.kind == 'session'",message="human subjects require an AEP session binding"
// +kubebuilder:validation:XValidation:rule="self.validUntil > self.issuedAt && self.validUntil <= self.issuedAt + duration('1h')",message="publication validity must be positive and at most one hour"
// +kubebuilder:validation:XValidation:rule="!self.disabled || size(self.models) == 0",message="disabled tombstones cannot carry allowed models"
// +kubebuilder:validation:XValidation:rule="!has(self.workloadPodUids) || self.binding.kind == 'kubernetes'",message="Pod observations require a Kubernetes workload binding"
// +kubebuilder:validation:XValidation:rule="!has(self.workloadEntitlement) || (self.binding.kind == 'kubernetes' && has(self.licenses) && self.licenses.exists(l, l.id == self.workloadEntitlement.licenseId && l.digest == self.workloadEntitlement.licenseDigest))",message="workload entitlement requires its exact accepted license and a Kubernetes binding"
type ModelAccessGrantSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=80
	DeploymentID string               `json:"deploymentId"`
	Subject      ModelAccessSubject   `json:"subject"`
	Binding      ModelAccessBinding   `json:"binding"`
	Publication  PublicationReference `json:"publication"`
	// IssuedAt/ValidUntil bound propagation failures independently of token exp.
	// Expired publications must deny even when a complete result is cached.
	IssuedAt   metav1.Time `json:"issuedAt"`
	ValidUntil metav1.Time `json:"validUntil"`
	Disabled   bool        `json:"disabled"`
	// Models is the effective role-derived AEP model allow-set. Empty denies all.
	// +kubebuilder:validation:MaxItems=512
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=40
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9][a-z0-9-]*$`
	// +listType=set
	Models []string `json:"models"`
	// WorkloadPodUIDs is mutable live-Pod evidence, outside the immutable binding.
	// Missing/empty denies workload requests, including cached TokenReview results.
	// It is not a model grant and must be supplied by a trusted lifecycle observer.
	// +optional
	// +kubebuilder:validation:MaxItems=512
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z0-9][A-Za-z0-9._:-]*$`
	// +listType=set
	WorkloadPodUIDs []string `json:"workloadPodUids,omitempty"`
	// Licenses contains only current accepted ID/digest pairs for this deployment.
	// A different active license must never satisfy an entitlement token's binding.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=id
	Licenses []ModelAccessLicense `json:"licenses,omitempty"`
	// WorkloadEntitlement is an explicit trusted license-feature attestation.
	// Its absence never waives a deployment-wide entitlement requirement.
	// +optional
	WorkloadEntitlement *ModelAccessWorkloadEntitlement `json:"workloadEntitlement,omitempty"`
	// RoleIDs and TeamIDs preserve trusted quota/observability membership only.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +listType=set
	RoleIDs []string `json:"roleIds,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +listType=set
	TeamIDs []string `json:"teamIds,omitempty"`
}

// ModelAccessReplicaObservation is data-plane evidence for one serving process.
// Ready/Lease leadership and an API apply response are not adoption evidence.
type ModelAccessReplicaObservation struct {
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID types.UID `json:"podUid"`
	// InstanceID identifies a process incarnation, including in-place restarts.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	InstanceID  string               `json:"instanceId"`
	Publication PublicationReference `json:"publication"`
	ObservedAt  metav1.Time          `json:"observedAt"`
}

// ModelAccessGrantStatus separates spec acceptance, resource writes and serving
// adoption. Only independently observed, current serving replicas prove Ready.
type ModelAccessGrantStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ObservedPublication *PublicationReference `json:"observedPublication,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=podUid
	Replicas []ModelAccessReplicaObservation `json:"replicas,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=modelaccessgrants,scope=Namespaced
// +kubebuilder:printcolumn:name="Subject",type=string,JSONPath=`.spec.subject.id`
// +kubebuilder:printcolumn:name="Disabled",type=boolean,JSONPath=`.spec.disabled`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`

// ModelAccessGrant is a derived authorization projection, never RBAC's source.
type ModelAccessGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelAccessGrantSpec   `json:"spec"`
	Status            ModelAccessGrantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelAccessGrantList is the list form of ModelAccessGrant.
type ModelAccessGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelAccessGrant `json:"items"`
}
