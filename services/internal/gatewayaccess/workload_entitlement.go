package gatewayaccess

import gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"

// SelectedWorkloadLicense validates a publication's exact feature attestation.
// This is shared contract validation, not license verification or selection:
// only the trusted business producer may attest a verified deployment license.
func SelectedWorkloadLicense(kind string, e *gateway.ModelAccessWorkloadEntitlement, licenses []gateway.ModelAccessLicense) (*gateway.ModelAccessLicense, bool) {
	if e == nil {
		return nil, true
	}
	if kind != "kubernetes" || e.Feature != "enterprise.models" ||
		!bounded(e.LicenseID, 256) || !digest.MatchString(e.LicenseDigest) {
		return nil, false
	}
	var selected *gateway.ModelAccessLicense
	for i := range licenses {
		license := &licenses[i]
		if license.ID != e.LicenseID {
			continue
		}
		if selected != nil || license.Digest != e.LicenseDigest || license.ValidUntil != nil && license.ValidUntil.IsZero() {
			return nil, false
		}
		selected = license
	}
	return selected, selected != nil
}
