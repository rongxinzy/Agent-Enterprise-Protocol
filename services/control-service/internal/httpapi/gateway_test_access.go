package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

func (s *Server) gatewayTestAccess(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	modelID := chi.URLParam(request, "modelId")
	model, err := s.app.Store.Deployment(claims.DeploymentID).GetModel(request.Context(), modelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeProblem(response, request, 404, "RESOURCE_NOT_FOUND", "The deployment model was not found.")
		} else {
			databaseFailure(response, request, err)
		}
		return
	}
	if !model.Enabled || model.SourceType != "gateway" || (model.Protocol != "openai-compatible" && model.Protocol != "anthropic") {
		writeProblem(response, request, 422, "MODEL_TEST_UNSUPPORTED", "The model is not enabled on the gateway.")
		return
	}
	scopes, err := s.app.ModelScopes(request.Context(), claims.DeploymentID, claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if !slices.Contains(scopes, modelID) {
		writeProblem(response, request, 403, "MODEL_NOT_ALLOWED", "The model is not assigned to the authenticated test session.")
		return
	}
	base := s.app.Config.ModelGatewayBaseURL
	var override sql.NullString
	err = s.app.Database().QueryRow(request.Context(), `SELECT model_gateway_base_url FROM deployment_settings WHERE deployment_id=$1`, claims.DeploymentID).Scan(&override)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		databaseFailure(response, request, err)
		return
	}
	if err == nil && override.Valid && override.String != "" {
		base = override.String
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		writeProblem(response, request, 503, "GATEWAY_SOURCE_UNAVAILABLE", "The client gateway endpoint is not configured.")
		return
	}
	var raw string
	var expires time.Time
	verified := s.app.CurrentLicense()
	if verified != nil && s.app.LicenseVerifier != nil {
		if verified.Claims.DeploymentID != claims.DeploymentID || verified.Status == "enterprise-expired" || !slices.Contains(verified.Claims.Features, "enterprise.models") {
			writeProblem(response, request, 403, "LICENSE_MISMATCH", "The model test requires an active deployment license.")
			return
		}
		expires = time.Now().UTC().Add(2 * time.Minute)
		if verified.Claims.ExpiresAt != nil {
			end, parseErr := time.Parse(time.RFC3339Nano, *verified.Claims.ExpiresAt)
			if parseErr != nil {
				writeProblem(response, request, 403, "INVALID_LICENSE", "The deployment license expiry is invalid.")
				return
			}
			if verified.Status == "enterprise-grace" && verified.GraceEndsAt != nil {
				end = *verified.GraceEndsAt
			}
			if end.Before(expires) {
				expires = end
			}
		}
		if err = s.app.ActivateLicense(request.Context(), verified.Claims.LicenseID, claims.DeploymentID, claims.Subject); err != nil {
			writeProblem(response, request, 403, "LICENSE_INACTIVE", "The deployment license is inactive or revoked.")
			return
		}
		raw, expires, err = s.app.Tokens.IssueEntitlement(claims.Subject, claims.DeploymentID, claims.SessionID, verified.Claims.LicenseID, verified.Digest, normalizeActivationFeatures(verified.Claims.Features), []string{modelID}, &expires)
	} else {
		if s.app.Config.Environment == "production" {
			writeProblem(response, request, 403, "LICENSE_INACTIVE", "The model test requires an active deployment license.")
			return
		}
		raw, expires, err = s.app.Tokens.IssueModelTestAccess(claims.Subject, claims.DeploymentID, claims.SessionID, modelID)
	}
	if err != nil {
		writeProblem(response, request, 403, "LICENSE_EXPIRED", "The model test access could not be issued.")
		return
	}
	// JWT NumericDate uses second precision; return the same expiry to clients.
	expires = expires.Truncate(time.Second)
	path := "/chat/completions"
	if model.Protocol == "anthropic" {
		parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/v1") + "/" + model.ID
		base = parsed.String()
		path = "/v1/messages"
	} else {
		base = strings.TrimRight(base, "/")
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
	writeJSON(response, http.StatusOK, map[string]any{"modelId": modelID, "protocol": model.Protocol, "baseUrl": base, "path": path, "modelAccessToken": raw, "expiresAt": expires})
}
