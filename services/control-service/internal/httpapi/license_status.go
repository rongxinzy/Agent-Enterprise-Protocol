package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (s *Server) internalLicenseStatus(response http.ResponseWriter, request *http.Request) {
	if s.app.Config.GatewayLicenseStatusToken == "" || request.Header.Get("X-AEP-Gateway-Token") != s.app.Config.GatewayLicenseStatusToken {
		writeProblem(response, request, http.StatusUnauthorized, "INTERNAL_AUTH_REQUIRED", "A valid gateway service token is required.")
		return
	}
	deploymentID := request.Header.Get("X-AEP-Deployment-ID")
	if deploymentID == "" {
		writeProblem(response, request, http.StatusBadRequest, "DEPLOYMENT_REQUIRED", "The deployment header is required.")
		return
	}
	var status string
	var digest, storedDeploymentID string
	err := s.app.Pool.QueryRow(request.Context(), `SELECT status,digest,deployment_id FROM licenses WHERE deployment_id=$1 AND license_id=$2 AND now() <= grace_ends_at`, deploymentID, chi.URLParam(request, "licenseId")).Scan(&status, &digest, &storedDeploymentID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(response, http.StatusOK, map[string]any{"active": false})
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"active": status == "active", "digest": digest, "deploymentId": storedDeploymentID})
}
