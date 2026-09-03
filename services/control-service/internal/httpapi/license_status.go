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
	enterpriseID := request.Header.Get("X-AEP-Tenant-ID")
	if enterpriseID == "" {
		writeProblem(response, request, http.StatusBadRequest, "TENANT_REQUIRED", "The tenant header is required.")
		return
	}
	var status string
	var digest, deploymentID string
	err := s.app.Pool.QueryRow(request.Context(), `SELECT status,digest,deployment_id FROM licenses WHERE deployment_id=$1 AND license_id=$2 AND now() <= grace_ends_at`, enterpriseID, chi.URLParam(request, "licenseId")).Scan(&status, &digest, &deploymentID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(response, http.StatusOK, map[string]any{"active": false})
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"active": status == "active", "digest": digest, "deploymentId": deploymentID})
}
