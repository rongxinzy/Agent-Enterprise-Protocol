package httpapi

import (
	"crypto/subtle"
	"net/http"
	"slices"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

// internalGatewayIdentity is called for each inference when organization
// enforcement/observation is enabled. Membership is never taken from the caller
// or a cached token, and disabled groups cannot become enforcement subjects.
func (s *Server) internalGatewayIdentity(response http.ResponseWriter, request *http.Request) {
	expected := s.app.Config.GatewayLicenseStatusToken
	if expected == "" || subtle.ConstantTimeCompare([]byte(request.Header.Get("X-AEP-Gateway-Token")), []byte(expected)) != 1 {
		writeProblem(response, request, http.StatusUnauthorized, "INTERNAL_AUTH_REQUIRED", "A valid gateway service token is required.")
		return
	}
	dep, user, session, model := request.Header.Get("X-AEP-Deployment-ID"), request.Header.Get("X-AEP-User-ID"), request.Header.Get("X-AEP-Session-ID"), request.Header.Get("X-AEP-Model-ID")
	if dep == "" || user == "" || session == "" || model == "" {
		writeProblem(response, request, http.StatusBadRequest, "GATEWAY_IDENTITY_REQUIRED", "Deployment, user, session and model context are required.")
		return
	}
	state, err := s.app.ValidateAccessSession(request.Context(), dep, user, session)
	if err != nil || state.PasswordChangeRequired {
		writeProblem(response, request, http.StatusForbidden, "GATEWAY_IDENTITY_INACTIVE", "The inference session is inactive.")
		return
	}
	scopes, err := s.app.ModelScopes(request.Context(), dep, user)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if !slices.Contains(scopes, model) {
		writeProblem(response, request, http.StatusForbidden, "MODEL_NOT_ALLOWED", "The model is no longer assigned to this session.")
		return
	}
	roles, teams, err := s.app.Store.Deployment(dep).GatewayMemberships(request.Context(), user)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]any{"deploymentId": dep, "userId": user, "consumer": gatewaysource.Consumer(dep, user), "roleIds": roles, "teamIds": teams, "observedAt": time.Now().UTC()})
}
