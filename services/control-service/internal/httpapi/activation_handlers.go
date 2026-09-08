package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

type licenseActivationRequest struct{}

// activateLicense uses the server-registered License and issues a short-lived
// entitlement. The client never uploads License material or signing keys.
func (s *Server) activateLicense(response http.ResponseWriter, request *http.Request) {
	var input licenseActivationRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	currentLicense := s.app.CurrentLicense()
	if s.app.LicenseVerifier == nil || currentLicense == nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_LICENSE_ACTIVATION", "The license activation evidence is invalid.")
		return
	}
	verified := *currentLicense
	if s.app.Config.LicenseDeploymentID != "" && claimsFrom(request).DeploymentID != s.app.Config.LicenseDeploymentID {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_MISMATCH", "The authenticated enterprise is not licensed for this deployment.")
		return
	}
	if verified.Status == "enterprise-expired" {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_EXPIRED", "The enterprise license is expired.")
		return
	}
	var expiresAt *time.Time
	if verified.Claims.ExpiresAt != nil {
		value, parseErr := time.Parse(time.RFC3339Nano, *verified.Claims.ExpiresAt)
		if parseErr != nil {
			writeProblem(response, request, http.StatusForbidden, "INVALID_LICENSE", "The enterprise license expiry is invalid.")
			return
		}
		expiresAt = &value
		if verified.Status == "enterprise-grace" {
			expiresAt = verified.GraceEndsAt
		}
	}
	claims := claimsFrom(request)
	if err := s.app.ActivateLicense(request.Context(), verified.Claims.LicenseID, claims.DeploymentID, claims.Subject); err != nil {
		code := "LICENSE_ACTIVATION_FAILED"
		status := http.StatusForbidden
		switch {
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
	var modelScopes []string
	var err error
	if s.app.Pool != nil {
		modelScopes, err = s.app.ModelScopes(request.Context(), claims.DeploymentID, claims.Subject)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	token, tokenExpiresAt, err := s.app.Tokens.IssueEntitlement(
		claims.Subject,
		verified.Claims.DeploymentID,
		verified.Claims.LicenseID,
		verified.Digest,
		features,
		modelScopes,
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
