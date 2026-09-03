package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
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
	DeploymentID string `json:"deploymentId"`
	EnterpriseID string `json:"enterpriseId"` // Deprecated v1 alias.
	SessionID    string `json:"sessionId"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

const zhiYuanPasswordMethodID = "zhiyuan-password"

func (s *Server) authenticationMethods(response http.ResponseWriter, request *http.Request) {
	enterpriseID := request.URL.Query().Get("enterpriseHint")
	if deploymentHint := request.URL.Query().Get("deploymentHint"); deploymentHint != "" {
		if !s.acceptsDeployment(deploymentHint) {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
			return
		}
		enterpriseID = s.storageTenantID()
	}
	if enterpriseID == "" {
		enterpriseID = s.storageTenantID()
	}
	enterprise, err := s.app.DB.GetDeployment(request.Context(), enterpriseID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The enterprise was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	methods := []map[string]any{
		{"id": zhiYuanPasswordMethodID, "type": "password", "displayName": "ZhiYuan account"},
	}
	if mockFederatedAuthEnabled(s.app.Config) {
		methods = append(methods, map[string]any{
			"id": "mock-oidc", "type": "federated", "protocol": "oidc", "displayName": "Mock OIDC",
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"enterprise":        map[string]string{"id": enterprise.ID, "name": enterprise.Name},
		"deployment":        map[string]string{"id": s.app.DeploymentID(), "name": s.app.DeploymentName()},
		"deploymentId":      s.app.DeploymentID(),
		"preferredMethodId": zhiYuanPasswordMethodID,
		"methods":           methods,
	})
}

func (s *Server) passwordLogin(response http.ResponseWriter, request *http.Request) {
	var input passwordLoginRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	enterpriseID, validDeployment := s.resolveTenant(input.DeploymentID, input.EnterpriseID)
	if !validDeployment {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
		return
	}
	now := time.Now().UTC()
	fingerprint := s.loginFingerprint(request, enterpriseID, input.Username)
	retryAfter, err := s.loginThrottle(request.Context(), fingerprint.KeyHash, now)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if retryAfter > 0 {
		if err := s.recordLoginThrottled(request.Context(), fingerprint, enterpriseID, input.AgentID, now); err != nil {
			databaseFailure(response, request, err)
			return
		}
		response.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds(retryAfter)))
		slog.Warn("authentication event", "event", "login.throttled", "outcome", "denied", "principal_hash", fingerprint.PrincipalHash, "request_id", request.Context().Value(contextKey("request-id")))
		writeProblem(response, request, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "Too many login attempts. Retry later.")
		return
	}

	user, lookupErr := s.app.DB.GetUserByUsername(request.Context(), db.GetUserByUsernameParams{DeploymentID: enterpriseID, Username: input.Username})
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		databaseFailure(response, request, lookupErr)
		return
	}
	passwordHash := ""
	if lookupErr == nil {
		passwordHash = user.PasswordHash
	}
	passwordValid := auth.VerifyPasswordOrDummy(passwordHash, input.Password)
	if lookupErr != nil || user.Status != "active" || !passwordValid {
		userID := ""
		if lookupErr == nil {
			userID = user.ID
		}
		backoff, err := s.recordLoginFailure(request.Context(), fingerprint, enterpriseID, userID, input.AgentID, now)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		slog.Warn("authentication event", "event", "login.failed", "outcome", "failure", "principal_hash", fingerprint.PrincipalHash, "request_id", request.Context().Value(contextKey("request-id")))
		if backoff > 0 {
			response.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds(backoff)))
			writeProblem(response, request, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "Too many login attempts. Retry later.")
			return
		}
		writeProblem(response, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "The username or password is invalid.")
		return
	}
	var tokens app.TokenResponse
	if input.SessionID != "" || input.AgentID == "" {
		tokens, err = s.app.IssueUserSession(request.Context(), user)
	} else {
		tokens, err = s.app.IssueSession(request.Context(), user, input.AgentContext)
	}
	if errors.Is(err, app.ErrAgentConflict) {
		writeProblem(response, request, http.StatusConflict, "AGENT_IDENTITY_CONFLICT", err.Error())
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	s.recordLoginSuccess(request.Context(), fingerprint, user.DeploymentID, user.ID, input.AgentID, now)
	slog.Info("authentication event", "event", "login.succeeded", "outcome", "success", "principal_hash", fingerprint.PrincipalHash, "request_id", request.Context().Value(contextKey("request-id")))
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) federatedStart(response http.ResponseWriter, request *http.Request) {
	if !mockFederatedAuthEnabled(s.app.Config) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The authentication method is unavailable.")
		return
	}
	var input struct {
		DeploymentID        string `json:"deploymentId"`
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
	enterpriseID, validDeployment := s.resolveTenant(input.DeploymentID, input.DeploymentID)
	if !validDeployment {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
		return
	}
	if _, err := s.app.DB.GetDeployment(request.Context(), enterpriseID); err != nil {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The enterprise was not found.")
		return
	}
	transactionID := uuid.NewString()
	state := uuid.NewString()
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	s.transactionMu.Lock()
	s.transactions[transactionID] = federatedTransaction{DeploymentID: enterpriseID, State: state, ExpiresAt: expiresAt}
	s.transactionMu.Unlock()
	writeJSON(response, http.StatusOK, map[string]any{
		"transactionId":    transactionID,
		"authorizationUrl": s.app.Config.Issuer + "/mock-idp?transactionId=" + transactionID + "&code=mock-code",
		"state":            state,
		"expiresIn":        300,
	})
}

func (s *Server) storageTenantID() string {
	if value := s.app.Config.BootstrapDeploymentID; value != "" {
		return value
	}
	return s.app.DeploymentID()
}

func (s *Server) acceptsDeployment(deploymentID string) bool {
	return deploymentID == s.app.DeploymentID() || deploymentID == s.storageTenantID()
}

func (s *Server) resolveTenant(deploymentID, enterpriseID string) (string, bool) {
	if deploymentID != "" && !s.acceptsDeployment(deploymentID) {
		return "", false
	}
	if enterpriseID != "" && enterpriseID != s.storageTenantID() && enterpriseID != s.app.DeploymentID() {
		return "", false
	}
	if enterpriseID != "" {
		return enterpriseID, true
	}
	return s.storageTenantID(), true
}

func (s *Server) federatedExchange(response http.ResponseWriter, request *http.Request) {
	if !mockFederatedAuthEnabled(s.app.Config) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The authentication method is unavailable.")
		return
	}
	var input struct {
		app.AgentContext
		SessionID         string `json:"sessionId"`
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
	user, err := s.app.DB.GetUserByUsername(request.Context(), db.GetUserByUsernameParams{DeploymentID: transaction.DeploymentID, Username: s.app.Config.BootstrapAdminUsername})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	var tokens app.TokenResponse
	if input.SessionID != "" || input.AgentID == "" {
		tokens, err = s.app.IssueUserSession(request.Context(), user)
	} else {
		tokens, err = s.app.IssueSession(request.Context(), user, input.AgentContext)
	}
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
		SessionID    string `json:"sessionId"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	var tokens app.TokenResponse
	var err error
	if input.SessionID != "" || input.AgentID == "" {
		tokens, err = s.app.RefreshUserSession(request.Context(), input.RefreshToken, input.SessionID)
	} else {
		tokens, err = s.app.RefreshSession(request.Context(), input.RefreshToken, input.AgentID)
	}
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
	claims := claimsFrom(request)
	if claims.SessionID != "" {
		if err := s.app.RevokeUserSession(request.Context(), input.RefreshToken, claims.SessionID); err != nil {
			databaseFailure(response, request, err)
			return
		}
	} else if err := s.app.DB.RevokeRefreshSession(request.Context(), auth.HashRefreshToken(input.RefreshToken)); err != nil {
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
	if err := auth.ValidatePassword(input.NewPassword); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "The new password must contain 12 to 1024 characters.")
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
	if err := s.app.RevokeUserSessionSet(request.Context(), user.ID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	user.RequirePasswordChange = false
	if claims.SessionID != "" {
		tokens, err := s.app.IssueUserSession(request.Context(), user)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		fingerprint := s.loginFingerprint(request, user.DeploymentID, user.Username)
		s.recordPasswordChanged(request.Context(), fingerprint, user.DeploymentID, user.ID, "", time.Now().UTC())
		writeJSON(response, http.StatusOK, tokens)
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
	fingerprint := s.loginFingerprint(request, user.DeploymentID, user.Username)
	s.recordPasswordChanged(request.Context(), fingerprint, user.DeploymentID, user.ID, input.AgentID, time.Now().UTC())
	slog.Info("authentication event", "event", "password.changed", "outcome", "success", "principal_hash", fingerprint.PrincipalHash, "request_id", request.Context().Value(contextKey("request-id")))
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) currentIdentity(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	user, err := s.app.DB.GetUser(request.Context(), claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	enterprise, err := s.app.DB.GetDeployment(request.Context(), user.DeploymentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"user":                   map[string]any{"id": user.ID, "displayName": user.DisplayName, "email": nullablePGText(user.Email)},
		"enterprise":             map[string]string{"id": enterprise.ID, "name": enterprise.Name},
		"deployment":             map[string]string{"id": s.app.DeploymentID(), "name": s.app.DeploymentName()},
		"deploymentId":           s.app.DeploymentID(),
		"sessionId":              claims.SessionID,
		"roles":                  user.RoleIds,
		"sessionExpiresAt":       claims.ExpiresAt.Time,
		"passwordChangeRequired": claims.PasswordChangeRequired,
	})
}
