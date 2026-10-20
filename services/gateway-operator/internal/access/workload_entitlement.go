package access

import (
	"fmt"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

// Admission is not the only caller of Render. Validate the complete reference
// again so malformed in-memory objects cannot bypass the license boundary.
func workloadLicense(spec gateway.ModelAccessGrantSpec) (*gateway.ModelAccessLicense, bool) {
	return gatewayaccess.SelectedWorkloadLicense(spec.Binding.Kind, spec.WorkloadEntitlement, spec.Licenses)
}

// Compile a constant or native absolute-time comparison; do not implement a
// license verifier, result cache, online DB query or fallback license selection.
func workloadLicenseRego(spec gateway.ModelAccessGrantSpec, options Options) string {
	license, _ := workloadLicense(spec) // Render has validated the reference.
	if license == nil {
		return fmt.Sprintf("workload_license_current { %t }\n", !options.RequireEntitlement)
	}
	if license.ValidUntil == nil {
		return "workload_license_current { true }\n"
	}
	return fmt.Sprintf("workload_license_current { %d > time.now_ns() / 1000000000 }\n", license.ValidUntil.Unix())
}
