package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

type licenseActivationRequest struct {
	License json.RawMessage `json:"license"`
}

// activateLicense verifies the complete vendor-signed envelope against the
// deployment License and issues a short-lived entitlement. The service never
// receives a license private key or performs License signing.
func (s *Server) activateLicense(response http.ResponseWriter, request *http.Request) {
	var input licenseActivationRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if len(input.License) == 0 || s.app.LicenseVerifier == nil || s.app.License == nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_LICENSE_ACTIVATION", "The license activation evidence is invalid.")
		return
	}
	verified, err := s.app.LicenseVerifier.Verify(input.License)
	if err != nil {
		writeProblem(response, request, http.StatusForbidden, "INVALID_LICENSE", "The enterprise license could not be verified.")
		return
	}
	if verified.Digest != s.app.License.Digest || verified.Claims.LicenseID != s.app.License.Claims.LicenseID || verified.Claims.CustomerID != s.app.License.Claims.CustomerID {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_MISMATCH", "The license is not registered for this enterprise deployment.")
		return
	}
	if s.app.Config.LicenseEnterpriseID != "" && claimsFrom(request).Tenant != s.app.Config.LicenseEnterpriseID {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_MISMATCH", "The authenticated enterprise is not licensed for this deployment.")
		return
	}
	if verified.Status == "enterprise-expired" {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_EXPIRED", "The enterprise license is expired.")
		return
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, verified.Claims.ExpiresAt)
	if err != nil {
		writeProblem(response, request, http.StatusForbidden, "INVALID_LICENSE", "The enterprise license expiry is invalid.")
		return
	}
	if verified.Status == "enterprise-grace" {
		expiresAt = verified.GraceEndsAt
	}
	claims := claimsFrom(request)
	if err := s.app.ActivateLicense(request.Context(), verified.Claims.LicenseID, claims.Tenant, claims.Subject, claims.AgentID); err != nil {
		code := "LICENSE_ACTIVATION_FAILED"
		status := http.StatusForbidden
		switch {
		case errors.Is(err, app.ErrLicenseAgentLimit):
			code = "LICENSE_AGENT_LIMIT"
		case errors.Is(err, app.ErrLicenseUserLimit):
			code = "LICENSE_USER_LIMIT"
		case errors.Is(err, app.ErrLicenseRevoked):
			code = "LICENSE_REVOKED"
		case errors.Is(err, app.ErrLicenseNotRegistered):
			code = "LICENSE_NOT_REGISTERED"
		default:
			status = http.StatusInternalServerError
		}
		writeProblem(response, request, status, code, "The enterprise License activation was rejected.")
		return
	}
	features := normalizeActivationFeatures(verified.Claims.Features)
	token, tokenExpiresAt, err := s.app.Tokens.IssueEntitlement(
		claims.Subject,
		claims.Tenant,
		claims.AgentID,
		verified.Claims.DeploymentID,
		verified.Claims.LicenseID,
		verified.Digest,
		features,
		expiresAt,
	)
	if err != nil {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_EXPIRED", "The enterprise license is expired.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"entitlementToken": token,
		"tokenType":        "Bearer",
		"expiresAt":        tokenExpiresAt,
		"expiresIn":        maxInt64(1, int64(time.Until(tokenExpiresAt).Seconds())),
		"licenseId":        verified.Claims.LicenseID,
		"licenseDigest":    verified.Digest,
		"deploymentId":     verified.Claims.DeploymentID,
		"features":         features,
	})
}

func normalizeActivationFeatures(features []string) []string {
	set := make(map[string]struct{}, len(features))
	for _, feature := range features {
		set[feature] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for feature := range set {
		result = append(result, feature)
	}
	sort.Strings(result)
	return result
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
