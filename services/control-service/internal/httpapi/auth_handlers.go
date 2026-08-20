package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	db "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db/generated"
)

type passwordLoginRequest struct {
	app.AgentContext
	EnterpriseID string `json:"enterpriseId"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

func (s *Server) authenticationMethods(response http.ResponseWriter, request *http.Request) {
	enterpriseID := request.URL.Query().Get("enterpriseHint")
	enterprise, err := s.app.DB.GetEnterprise(request.Context(), enterpriseID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The enterprise was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"enterprise":        map[string]string{"id": enterprise.ID, "name": enterprise.Name},
		"preferredMethodId": "password",
		"methods": []map[string]any{
			{"id": "password", "type": "password", "displayName": "ZhiYuan password"},
			{"id": "mock-oidc", "type": "federated", "protocol": "oidc", "displayName": "Mock OIDC"},
		},
	})
}

func (s *Server) passwordLogin(response http.ResponseWriter, request *http.Request) {
	var input passwordLoginRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	user, err := s.app.DB.GetUserByUsername(request.Context(), db.GetUserByUsernameParams{EnterpriseID: input.EnterpriseID, Username: input.Username})
	if err != nil || user.Status != "active" || !auth.VerifyPassword(user.PasswordHash, input.Password) {
		writeProblem(response, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "The username or password is invalid.")
		return
	}
	tokens, err := s.app.IssueSession(request.Context(), user, input.AgentContext)
	if errors.Is(err, app.ErrAgentConflict) {
		writeProblem(response, request, http.StatusConflict, "AGENT_IDENTITY_CONFLICT", err.Error())
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) federatedStart(response http.ResponseWriter, request *http.Request) {
	var input struct {
		EnterpriseID        string `json:"enterpriseId"`
		MethodID            string `json:"methodId"`
		RedirectURI         string `json:"redirectUri"`
		CodeChallenge       string `json:"codeChallenge"`
		CodeChallengeMethod string `json:"codeChallengeMethod"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.MethodID != "mock-oidc" {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The authentication method was not found.")
		return
	}
	if _, err := s.app.DB.GetEnterprise(request.Context(), input.EnterpriseID); err != nil {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The enterprise was not found.")
		return
	}
	transactionID := uuid.NewString()
	state := uuid.NewString()
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	s.transactionMu.Lock()
	s.transactions[transactionID] = federatedTransaction{EnterpriseID: input.EnterpriseID, State: state, ExpiresAt: expiresAt}
	s.transactionMu.Unlock()
	writeJSON(response, http.StatusOK, map[string]any{
		"transactionId":    transactionID,
		"authorizationUrl": s.app.Config.Issuer + "/mock-idp?transactionId=" + transactionID + "&code=mock-code",
		"state":            state,
		"expiresIn":        300,
	})
}

func (s *Server) federatedExchange(response http.ResponseWriter, request *http.Request) {
	var input struct {
		app.AgentContext
		TransactionID     string `json:"transactionId"`
		AuthorizationCode string `json:"authorizationCode"`
		RedirectURI       string `json:"redirectUri"`
		CodeVerifier      string `json:"codeVerifier"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	s.transactionMu.Lock()
	transaction, exists := s.transactions[input.TransactionID]
	delete(s.transactions, input.TransactionID)
	s.transactionMu.Unlock()
	if !exists || time.Now().After(transaction.ExpiresAt) || input.AuthorizationCode != "mock-code" {
		writeProblem(response, request, http.StatusUnauthorized, "AUTHORIZATION_CODE_INVALID", "The authorization code is invalid or expired.")
		return
	}
	user, err := s.app.DB.GetUserByUsername(request.Context(), db.GetUserByUsernameParams{EnterpriseID: transaction.EnterpriseID, Username: s.app.Config.BootstrapAdminUsername})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	tokens, err := s.app.IssueSession(request.Context(), user, input.AgentContext)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) refreshSession(response http.ResponseWriter, request *http.Request) {
	var input struct {
		RefreshToken string `json:"refreshToken"`
		AgentID      string `json:"agentId"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	tokens, err := s.app.RefreshSession(request.Context(), input.RefreshToken, input.AgentID)
	if errors.Is(err, app.ErrRefreshTokenInvalid) {
		writeProblem(response, request, http.StatusUnauthorized, "REFRESH_TOKEN_INVALID", err.Error())
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) logout(response http.ResponseWriter, request *http.Request) {
	var input struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if err := s.app.DB.RevokeRefreshSession(request.Context(), auth.HashRefreshToken(input.RefreshToken)); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(response http.ResponseWriter, request *http.Request) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		AgentID         string `json:"agentId"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if len(input.NewPassword) < 12 {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_TOO_SHORT", "The new password must contain at least 12 characters.")
		return
	}
	claims := claimsFrom(request)
	user, err := s.app.DB.GetUser(request.Context(), claims.Subject)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, input.CurrentPassword) {
		writeProblem(response, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "The current password is invalid.")
		return
	}
	hash, err := auth.HashPassword(input.NewPassword)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.DB.UpdatePassword(request.Context(), db.UpdatePasswordParams{ID: user.ID, PasswordHash: hash, RequirePasswordChange: false}); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.DB.RevokeUserSessions(request.Context(), user.ID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	agent, err := s.app.DB.GetAgent(request.Context(), input.AgentID)
	if err != nil {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Agent was not found.")
		return
	}
	tokens, err := s.app.IssueSession(request.Context(), user, app.AgentContext{AgentID: agent.AgentID, AgentVersion: agent.AgentVersion, Platform: agent.Platform})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) currentIdentity(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	user, err := s.app.DB.GetUser(request.Context(), claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	enterprise, err := s.app.DB.GetEnterprise(request.Context(), user.EnterpriseID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"user":       map[string]any{"id": user.ID, "displayName": user.DisplayName, "email": nullablePGText(user.Email)},
		"enterprise": map[string]string{"id": enterprise.ID, "name": enterprise.Name},
		"roles":      user.RoleIds,
	})
}
