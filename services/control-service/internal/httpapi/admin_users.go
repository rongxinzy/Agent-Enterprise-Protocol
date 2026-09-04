package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	db "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db/generated"
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
	items, err := s.app.DB.ListUsers(request.Context(), db.ListUsersParams{DeploymentID: claims.DeploymentID, Column2: request.URL.Query().Get("cursor"), Limit: limit(request)})
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

func (s *Server) insertUser(request *http.Request, input createUserRequest) (db.User, error) {
	hash, err := auth.HashPassword(input.TemporaryPassword)
	if err != nil {
		return db.User{}, err
	}
	requireChange := true
	if input.RequirePasswordChange != nil {
		requireChange = *input.RequirePasswordChange
	}
	email := pgtype.Text{}
	if input.Email != nil {
		email = pgtype.Text{String: *input.Email, Valid: true}
	}
	userID := uuid.NewString()
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		return db.User{}, err
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	user, err := db.New(tx).CreateUser(request.Context(), db.CreateUserParams{
		ID: userID, DeploymentID: input.DeploymentID, Username: input.Username,
		DisplayName: input.DisplayName, Email: email, PasswordHash: hash,
		RequirePasswordChange: requireChange, IsAdmin: false,
	})
	if err != nil {
		return db.User{}, err
	}
	for _, roleID := range input.RoleIDs {
		if _, err = tx.Exec(request.Context(), `INSERT INTO user_role_bindings (deployment_id,user_id,role_id,is_primary) VALUES ($1,$2,$3,$4)`, input.DeploymentID, userID, roleID, roleID == input.RoleIDs[0]); err != nil {
			return db.User{}, err
		}
	}
	for _, teamID := range input.TeamIDs {
		if _, err = tx.Exec(request.Context(), `INSERT INTO user_team_bindings (deployment_id,user_id,team_id,is_primary) VALUES ($1,$2,$3,$4)`, input.DeploymentID, userID, teamID, teamID == input.TeamIDs[0]); err != nil {
			return db.User{}, err
		}
	}
	if err = tx.Commit(request.Context()); err != nil {
		return db.User{}, err
	}
	return user, nil
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
	params := db.UpdateUserParams{ID: chi.URLParam(request, "userId")}
	if input.DisplayName != nil {
		params.DisplayName = pgtype.Text{String: *input.DisplayName, Valid: true}
	}
	if input.Email != nil {
		params.Email = pgtype.Text{String: *input.Email, Valid: true}
	}
	if input.Status != nil {
		params.Status = pgtype.Text{String: *input.Status, Valid: true}
	}
	user, err := s.app.DB.UpdateUser(request.Context(), params)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if user.Status == "disabled" {
		if err := s.app.RevokeUserSessionSet(request.Context(), user.ID); err != nil {
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
	if err := s.app.DB.UpdatePassword(request.Context(), db.UpdatePasswordParams{ID: userID, PasswordHash: hash, RequirePasswordChange: requireChange}); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := s.app.RevokeUserSessionSet(request.Context(), userID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func publicUser(user db.User) map[string]any {
	var email any
	if user.Email.Valid {
		email = user.Email.String
	}
	return map[string]any{
		"id": user.ID, "deploymentId": user.DeploymentID, "username": user.Username,
		"displayName": user.DisplayName, "email": email, "status": user.Status,
		"teamIds": []string{}, "roleIds": []string{},
		"createdAt": user.CreatedAt.Time, "updatedAt": user.UpdatedAt.Time,
	}
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}
