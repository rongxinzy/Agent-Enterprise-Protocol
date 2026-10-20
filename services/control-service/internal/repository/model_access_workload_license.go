package repository

import (
	"slices"
	"strings"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// workloadLicenseSource selects from the registered, signature-verified active
// deployment licenses in the locked business snapshot. Neither the owner nor
// the ServiceAccount supplies license evidence. Keep the previous exact license
// while eligible: unrelated license changes must not invalidate this binding.
func workloadLicenseSource(source ModelAccessSource, preferred *gateway.ModelAccessWorkloadEntitlement) (ModelAccessSource, *gateway.ModelAccessWorkloadEntitlement) {
	var selected *ModelAccessLicense
	if !source.Disabled {
		for i := range source.Licenses {
			candidate := &source.Licenses[i]
			if !slices.Contains(candidate.Features, "enterprise.models") ||
				candidate.ExpiresAt != nil && !candidate.ExpiresAt.UTC().Truncate(time.Second).After(source.ObservedAt) {
				continue
			}
			if preferred != nil && preferred.Feature == "enterprise.models" &&
				preferred.LicenseID == candidate.ID && preferred.LicenseDigest == candidate.Digest {
				selected = candidate
				break
			}
			if selected == nil || strings.Compare(candidate.ID, selected.ID) < 0 {
				selected = candidate
			}
		}
	}
	source.Licenses = nil
	if selected == nil {
		// Missing/expired entitlement pauses a current workload; it never retires
		// its immutable identity. A later eligible license can restore service.
		source.Disabled = true
		return source, nil
	}
	source.Licenses = []ModelAccessLicense{*selected}
	return source, &gateway.ModelAccessWorkloadEntitlement{
		LicenseID: selected.ID, LicenseDigest: selected.Digest, Feature: "enterprise.models",
	}
}
