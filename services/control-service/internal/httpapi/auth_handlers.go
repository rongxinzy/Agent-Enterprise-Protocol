package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

type passwordLoginRequest struct {
	DeploymentID string `json:"deploymentId"`
	SessionID    string `json:"sessionId"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

const zhiYuanPasswordMethodID = "zhiyuan-password"

func (s *Server) authenticationMethods(response http.ResponseWriter, request *http.Request) {
	deploymentID := request.URL.Query().Get("deploymentHint")
	if deploymentID != "" {
		if !s.acceptsDeployment(deploymentID) {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
			return
		}
	}
	deploymentID = s.storageTenantID()
	deployment, err := s.app.Store.GetDeployment(request.Context(), deploymentID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
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
		"deployment":        map[string]string{"id": deployment.ID, "name": deployment.Name},
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
	deploymentID, validDeployment := s.resolveTenant(input.DeploymentID)
	if !validDeployment {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
		return
	}
	now := time.Now().UTC()
	fingerprint := s.loginFingerprint(request, deploymentID, input.Username)
	retryAfter, err := s.loginThrottle(request.Context(), fingerprint.KeyHash, now)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if retryAfter > 0 {
		if err := s.recordLoginThrottled(request.Context(), fingerprint, deploymentID, now); err != nil {
			databaseFailure(response, request, err)
			return
		}
		response.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds(retryAfter)))
		slog.Warn("authentication event", "event", "login.throttled", "outcome", "denied", "principal_hash", fingerprint.PrincipalHash, "request_id", request.Context().Value(contextKey("request-id")))
		writeProblem(response, request, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "Too many login attempts. Retry later.")
		return
	}

	user, lookupErr := s.app.Store.Deployment(deploymentID).GetUserByUsername(request.Context(), input.Username)
	if lookupErr != nil && !errors.Is(lookupErr, repository.ErrNotFound) {
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
		backoff, err := s.recordLoginFailure(request.Context(), fingerprint, deploymentID, userID, now)
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
	tokens, err := s.app.IssueUserSession(request.Context(), user)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	s.recordLoginSuccess(request.Context(), fingerprint, user.DeploymentID, user.ID, now)
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
	deploymentID, validDeployment := s.resolveTenant(input.DeploymentID)
	if !validDeployment {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
		return
	}
	if _, err := s.app.Store.GetDeployment(request.Context(), deploymentID); err != nil {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The deployment was not found.")
		return
	}
	transactionID := uuid.NewString()
	state := uuid.NewString()
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	s.transactionMu.Lock()
	s.transactions[transactionID] = federatedTransaction{DeploymentID: deploymentID, State: state, ExpiresAt: expiresAt}
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

func (s *Server) resolveTenant(deploymentID string) (string, bool) {
	if deploymentID != "" && !s.acceptsDeployment(deploymentID) {
		return "", false
	}
	return s.storageTenantID(), true
}

func (s *Server) federatedExchange(response http.ResponseWriter, request *http.Request) {
	if !mockFederatedAuthEnabled(s.app.Config) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The authentication method is unavailable.")
		return
	}
	var input struct {
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
	user, err := s.app.Store.Deployment(transaction.DeploymentID).
		GetUserByUsername(request.Context(), s.app.Config.BootstrapAdminUsername)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	tokens, err := s.app.IssueUserSession(request.Context(), user)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) refreshSession(response http.ResponseWriter, request *http.Request) {
	var input struct {
		RefreshToken string `json:"refreshToken"`
		SessionID    string `json:"sessionId"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	tokens, err := s.app.RefreshUserSession(request.Context(), input.RefreshToken, input.SessionID)
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
	if err := s.app.RevokeUserSession(request.Context(), input.RefreshToken, claims.SessionID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(response http.ResponseWriter, request *http.Request) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if err := auth.ValidatePassword(input.NewPassword); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "The new password must contain 12 to 1024 characters.")
		return
	}
	claims := claimsFrom(request)
	deploymentID, validDeployment := s.resolveTenant(claims.DeploymentID)
	if !validDeployment {
		writeProblem(response, request, http.StatusUnauthorized, "INVALID_TOKEN", "The authenticated deployment is invalid.")
		return
	}
	user, err := s.app.Store.Deployment(deploymentID).GetUser(request.Context(), claims.Subject)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, input.CurrentPassword) {
		writeProblem(response, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "The current password is invalid.")
		return
	}
	hash, err := auth.HashPassword(input.NewPassword)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.Store.Deployment(user.DeploymentID).
		UpdatePassword(request.Context(), user.ID, hash, false); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.RevokeUserSessionSet(request.Context(), user.ID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	user.RequirePasswordChange = false
	tokens, err := s.app.IssueUserSession(request.Context(), user)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	fingerprint := s.loginFingerprint(request, user.DeploymentID, user.Username)
	s.recordPasswordChanged(request.Context(), fingerprint, user.DeploymentID, user.ID, time.Now().UTC())
	slog.Info("authentication event", "event", "password.changed", "outcome", "success", "principal_hash", fingerprint.PrincipalHash, "request_id", request.Context().Value(contextKey("request-id")))
	writeJSON(response, http.StatusOK, tokens)
}

func (s *Server) currentIdentity(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	deploymentID, validDeployment := s.resolveTenant(claims.DeploymentID)
	if !validDeployment {
		writeProblem(response, request, http.StatusUnauthorized, "INVALID_TOKEN", "The authenticated deployment is invalid.")
		return
	}
	user, err := s.app.Store.Deployment(deploymentID).GetUser(request.Context(), claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	roles, err := s.app.UserRoleIDs(request.Context(), user.DeploymentID, user.ID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"user":                   map[string]any{"id": user.ID, "displayName": user.DisplayName, "email": user.Email},
		"deployment":             map[string]string{"id": s.app.DeploymentID(), "name": s.app.DeploymentName()},
		"deploymentId":           s.app.DeploymentID(),
		"sessionId":              claims.SessionID,
		"roles":                  roles,
		"sessionExpiresAt":       claims.ExpiresAt.Time,
		"passwordChangeRequired": claims.PasswordChangeRequired,
	})
}
