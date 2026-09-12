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
	userID := request.Header.Get("X-AEP-User-ID")
	sessionID := request.Header.Get("X-AEP-Session-ID")
	modelID := request.Header.Get("X-AEP-Model-ID")
	if deploymentID == "" || userID == "" || sessionID == "" || modelID == "" {
		writeProblem(response, request, http.StatusBadRequest, "ENTITLEMENT_CONTEXT_REQUIRED", "The deployment, user, session, and model headers are required.")
		return
	}
	var status string
	var digest, storedDeploymentID string
	err := s.app.Database().QueryRow(request.Context(), `SELECT status,digest,deployment_id FROM licenses WHERE deployment_id=$1 AND license_id=$2 AND (expires_at IS NULL OR now() <= grace_ends_at)`, deploymentID, chi.URLParam(request, "licenseId")).Scan(&status, &digest, &storedDeploymentID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(response, http.StatusOK, map[string]any{"active": false})
			return
		}
		databaseFailure(response, request, err)
		return
	}
	active := status == "active"
	if active {
		err = s.app.Database().QueryRow(request.Context(), `SELECT EXISTS(
SELECT 1
FROM user_sessions s
JOIN users u ON u.deployment_id=s.deployment_id AND u.id=s.user_id
JOIN models m ON m.deployment_id=s.deployment_id AND m.id=$4 AND m.enabled=true
WHERE s.deployment_id=$1 AND s.session_id=$2 AND s.user_id=$3 AND s.revoked_at IS NULL AND u.status='active'
  AND EXISTS (SELECT 1 FROM user_session_tokens st WHERE st.session_id=s.session_id AND st.revoked_at IS NULL AND st.expires_at>now())
  AND EXISTS (
    SELECT 1 FROM model_assignments ma
    WHERE ma.deployment_id=s.deployment_id AND ma.model_id=m.id AND (
      (ma.subject_type='user' AND ma.subject_id=s.user_id)
      OR (ma.subject_type='role' AND EXISTS (
        SELECT 1 FROM user_role_bindings urb
        JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true
        WHERE urb.deployment_id=s.deployment_id AND urb.user_id=s.user_id AND urb.role_id=ma.subject_id
      ))
      OR (ma.subject_type='team' AND EXISTS (
        SELECT 1 FROM user_team_bindings utb
        JOIN teams t ON t.deployment_id=utb.deployment_id AND t.id=utb.team_id AND t.enabled=true
        WHERE utb.deployment_id=s.deployment_id AND utb.user_id=s.user_id AND utb.team_id=ma.subject_id
      ))
    )
  )
)`, deploymentID, sessionID, userID, modelID).Scan(&active)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"active": active, "digest": digest, "deploymentId": storedDeploymentID})
}
