package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Machine-to-machine model token issuance for governed runtimes: the runtime
// authenticates with its service account, names the AEP user whose message it
// is serving, and receives a data-plane token whose scopes are derived from
// that user's assignments — never wider. The caller-supplied sessionId (a
// conversation id) rides in the token for gateway log correlation; without
// one an m2m-prefixed synthetic session is minted so downstream validation
// (which requires a session claim) still holds.
func (s *Server) issueModelGatewayToken(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	var input struct {
		UserID    string `json:"userId"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<16)).Decode(&input); err != nil || input.UserID == "" || len(input.UserID) > 200 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "A target userId is required.")
		return
	}
	if input.SessionID != "" && len(input.SessionID) > 128 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The sessionId is invalid.")
		return
	}
	store := s.app.Store.Deployment(claims.DeploymentID)
	target, err := store.GetUser(request.Context(), input.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The target user was not found.")
		} else {
			databaseFailure(response, request, err)
		}
		return
	}
	if target.Status != "active" || target.DeploymentID != claims.DeploymentID {
		writeProblem(response, request, http.StatusUnprocessableEntity, "USER_INACTIVE", "The target user is not active.")
		return
	}
	scopes, err := s.app.ModelScopes(request.Context(), claims.DeploymentID, target.ID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = "m2m-" + uuid.NewString()
	}
	raw, expiresAt, err := s.app.Tokens.IssueModelToken(target.ID, claims.DeploymentID, sessionID, scopes)
	if err != nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "TOKEN_ISSUANCE_UNAVAILABLE", "The model token could not be issued.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]any{
		"userId": target.ID, "modelAccessToken": raw,
		"expiresIn": int(time.Until(expiresAt).Seconds()), "sessionId": sessionID, "modelScopes": scopes,
	})
}
