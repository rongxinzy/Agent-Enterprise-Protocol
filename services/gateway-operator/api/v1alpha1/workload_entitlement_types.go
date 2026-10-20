package v1alpha1

// ModelAccessWorkloadEntitlement is the trusted publication writer's explicit
// attestation that this deployment's registered, verified license includes the
// enterprise.models feature. It applies to the exact immutable workload binding
// in the same grant, not its human owner or any login session. This is not a
// credential, signed license payload or a license selected by the request.
// The referenced ID AND digest must occur in the grant's accepted Licenses;
// that entry supplies the grace deadline checked on every model request.
type ModelAccessWorkloadEntitlement struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	LicenseID string `json:"licenseId"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=71
	LicenseDigest string `json:"licenseDigest"`
	// The writer must derive this from verified license features, never infer it
	// from the existence of an active license or from the ServiceAccount token.
	// +kubebuilder:validation:Enum=enterprise.models
	// +kubebuilder:validation:MaxLength=17
	Feature string `json:"feature"`
}
