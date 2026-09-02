package httpapi

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var licenseDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type licenseActivationRequest struct {
	LicenseID     string   `json:"licenseId"`
	LicenseDigest string   `json:"licenseDigest"`
	DeploymentID  string   `json:"deploymentId"`
	ExpiresAt     string   `json:"expiresAt"`
	Features      []string `json:"features"`
}

// activateLicense exchanges evidence from a locally verified license for a
// short-lived, service-signed entitlement. The service never receives a
// license private key or a signing operation.
func (s *Server) activateLicense(response http.ResponseWriter, request *http.Request) {
	var input licenseActivationRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if !validActivationRequest(input) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_LICENSE_ACTIVATION", "The license activation evidence is invalid.")
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, input.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		writeProblem(response, request, http.StatusForbidden, "LICENSE_EXPIRED", "The enterprise license is expired.")
		return
	}
	claims := claimsFrom(request)
	features := normalizeActivationFeatures(input.Features)
	token, tokenExpiresAt, err := s.app.Tokens.IssueEntitlement(
		claims.Subject,
		claims.Tenant,
		claims.AgentID,
		input.DeploymentID,
		input.LicenseID,
		input.LicenseDigest,
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
		"licenseId":        input.LicenseID,
		"licenseDigest":    input.LicenseDigest,
		"deploymentId":     input.DeploymentID,
		"features":         features,
	})
}

func validActivationRequest(input licenseActivationRequest) bool {
	return input.LicenseID != "" && len(input.LicenseID) <= 256 &&
		licenseDigestPattern.MatchString(input.LicenseDigest) &&
		input.DeploymentID != "" && len(input.DeploymentID) <= 256 &&
		input.ExpiresAt != "" && len(input.Features) <= 256 &&
		allActivationFeaturesValid(input.Features)
}

func allActivationFeaturesValid(features []string) bool {
	for _, feature := range features {
		if feature == "" || len(feature) > 128 || strings.TrimSpace(feature) != feature {
			return false
		}
	}
	return true
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
