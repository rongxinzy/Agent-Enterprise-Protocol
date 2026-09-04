package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

type createUserRequest struct {
	DeploymentID          string   `json:"deploymentId"`
	Username              string   `json:"username"`
	DisplayName           string   `json:"displayName"`
	Email                 *string  `json:"email"`
	TemporaryPassword     string   `json:"temporaryPassword"`
	TeamIDs               []string `json:"teamIds"`
	RoleIDs               []string `json:"roleIds"`
	RequirePasswordChange *bool    `json:"requirePasswordChange"`
}

func (s *Server) listUsers(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	items, err := s.app.Store.Deployment(claims.DeploymentID).
		ListUsers(request.Context(), request.URL.Query().Get("cursor"), limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, user := range items {
		result = append(result, publicUser(user))
	}
	var nextCursor any
	if len(items) == int(limit(request)) {
		nextCursor = items[len(items)-1].ID
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": result, "nextCursor": nextCursor})
}

func (s *Server) createUser(response http.ResponseWriter, request *http.Request) {
	var input createUserRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.DeploymentID != claimsFrom(request).DeploymentID {
		writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "Administrators can only create users in their enterprise.")
		return
	}
	user, err := s.insertUser(request, input)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordPolicy) {
			writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Temporary passwords must contain 12 to 1024 characters.")
			return
		}
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "USER_ALREADY_EXISTS", "The username already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, publicUser(user))
}

func (s *Server) importUsers(response http.ResponseWriter, request *http.Request) {
	var input struct {
		DeploymentID string `json:"deploymentId"`
		Users        []struct {
			ExternalRowID         string   `json:"externalRowId"`
			Username              string   `json:"username"`
			DisplayName           string   `json:"displayName"`
			Email                 *string  `json:"email"`
			TemporaryPassword     string   `json:"temporaryPassword"`
			TeamIDs               []string `json:"teamIds"`
			RoleIDs               []string `json:"roleIds"`
			RequirePasswordChange *bool    `json:"requirePasswordChange"`
		} `json:"users"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.DeploymentID != claimsFrom(request).DeploymentID || len(input.Users) == 0 || len(input.Users) > 1000 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The import must contain 1 to 1000 users for the current enterprise.")
		return
	}
	created := 0
	errorsResult := make([]map[string]string, 0)
	for _, item := range input.Users {
		_, err := s.insertUser(request, createUserRequest{DeploymentID: input.DeploymentID, Username: item.Username, DisplayName: item.DisplayName, Email: item.Email, TemporaryPassword: item.TemporaryPassword, TeamIDs: item.TeamIDs, RoleIDs: item.RoleIDs, RequirePasswordChange: item.RequirePasswordChange})
		if err != nil {
			code := "USER_IMPORT_FAILED"
			detail := "The account could not be created."
			if errors.Is(err, auth.ErrPasswordPolicy) {
				code = "PASSWORD_POLICY_VIOLATION"
				detail = "Temporary passwords must contain 12 to 1024 characters."
			}
			errorsResult = append(errorsResult, map[string]string{"externalRowId": item.ExternalRowID, "code": code, "detail": detail})
			continue
		}
		created++
	}
	writeJSON(response, http.StatusOK, map[string]any{"created": created, "rejected": len(errorsResult), "errors": errorsResult})
}

func (s *Server) insertUser(request *http.Request, input createUserRequest) (repository.UserRecord, error) {
	hash, err := auth.HashPassword(input.TemporaryPassword)
	if err != nil {
		return repository.UserRecord{}, err
	}
	requireChange := true
	if input.RequirePasswordChange != nil {
		requireChange = *input.RequirePasswordChange
	}
	return s.app.Store.Deployment(input.DeploymentID).CreateUser(request.Context(), repository.CreateUserParams{
		ID: uuid.NewString(), Username: input.Username, DisplayName: input.DisplayName,
		Email: input.Email, PasswordHash: hash, RequirePasswordChange: requireChange,
		RoleIDs: input.RoleIDs, TeamIDs: input.TeamIDs,
	})
}

func (s *Server) updateUser(response http.ResponseWriter, request *http.Request) {
	var input struct {
		DisplayName *string   `json:"displayName"`
		Email       *string   `json:"email"`
		Status      *string   `json:"status"`
		TeamIDs     *[]string `json:"teamIds"`
		RoleIDs     *[]string `json:"roleIds"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	user, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).UpdateUser(
		request.Context(), chi.URLParam(request, "userId"), repository.UpdateUserParams{
			DisplayName: input.DisplayName, Email: input.Email, Status: input.Status,
		},
	)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if user.User.Status == "disabled" {
		if err := s.app.RevokeUserSessionSet(request.Context(), user.User.ID); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	writeJSON(response, http.StatusOK, publicUser(user))
}

func (s *Server) resetUserPassword(response http.ResponseWriter, request *http.Request) {
	var input struct {
		TemporaryPassword     string `json:"temporaryPassword"`
		RequirePasswordChange *bool  `json:"requirePasswordChange"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if err := auth.ValidatePassword(input.TemporaryPassword); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Temporary passwords must contain 12 to 1024 characters.")
		return
	}
	hash, err := auth.HashPassword(input.TemporaryPassword)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	requireChange := true
	if input.RequirePasswordChange != nil {
		requireChange = *input.RequirePasswordChange
	}
	userID := chi.URLParam(request, "userId")
	if err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		UpdatePassword(request.Context(), userID, hash, requireChange); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.RevokeUserSessionSet(request.Context(), userID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func publicUser(record repository.UserRecord) map[string]any {
	user := record.User
	return map[string]any{
		"id": user.ID, "deploymentId": user.DeploymentID, "username": user.Username,
		"displayName": user.DisplayName, "email": user.Email, "status": user.Status,
		"teamIds": record.TeamIDs, "roleIds": record.RoleIDs,
		"createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt,
	}
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}
