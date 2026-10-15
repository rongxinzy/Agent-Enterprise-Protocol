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

// createUser creates a platform account with its role and team bindings. The
// account and the audit row recording who created it share one transaction (see
// createUserWithAudit): an account that cannot be audited is not created, so a
// privileged grant can never outlive the evidence that explains it.
func (s *Server) createUser(response http.ResponseWriter, request *http.Request) {
	var input createUserRequest
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.DeploymentID != claimsFrom(request).DeploymentID {
		writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "Administrators can only create users in their enterprise.")
		return
	}
	if code, detail := userMembershipProblem(input.RoleIDs, input.TeamIDs); code != "" {
		writeProblem(response, request, http.StatusBadRequest, code, detail)
		return
	}
	if !s.authorizeRoleGrant(response, request, input.RoleIDs) {
		return
	}
	if !s.authorizeTeamGrant(response, request, input.TeamIDs) {
		return
	}
	params, err := buildCreateUserParams(input)
	if err != nil {
		writeCreateUserFailure(response, request, err)
		return
	}
	user, err := s.createUserWithAudit(request, claimsFrom(request).DeploymentID, params)
	if err != nil {
		writeCreateUserFailure(response, request, err)
		return
	}
	markAdminAuditRecorded(request)
	writeJSON(response, http.StatusCreated, publicUser(user))
}

// writeCreateUserFailure maps the failures a single account creation can raise
// onto the stable problem codes its callers rely on.
func writeCreateUserFailure(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrPasswordEdgeWhitespace):
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Temporary passwords must not start or end with whitespace.")
	case errors.Is(err, auth.ErrPasswordPolicy):
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Temporary passwords must contain 6 to 1024 characters.")
	case isUniqueViolation(err):
		writeProblem(response, request, http.StatusConflict, "USER_ALREADY_EXISTS", "The username already exists.")
	default:
		databaseFailure(response, request, err)
	}
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
	roleIDs := make([]string, 0)
	teamIDs := make([]string, 0)
	for _, item := range input.Users {
		if code, _ := userMembershipProblem(item.RoleIDs, item.TeamIDs); code == "" {
			roleIDs = append(roleIDs, item.RoleIDs...)
			teamIDs = append(teamIDs, item.TeamIDs...)
		}
	}
	if !s.authorizeRoleGrant(response, request, roleIDs) {
		return
	}
	if !s.authorizeTeamGrant(response, request, teamIDs) {
		return
	}
	created := 0
	errorsResult := make([]map[string]string, 0)
	for _, item := range input.Users {
		if code, detail := userMembershipProblem(item.RoleIDs, item.TeamIDs); code != "" {
			errorsResult = append(errorsResult, map[string]string{"externalRowId": item.ExternalRowID, "code": code, "detail": detail})
			continue
		}
		// Imported rows come from exports and spreadsheets: edge whitespace is
		// data noise, so trim instead of rejecting the row. A whitespace-only
		// password falls through to the default below.
		item.TemporaryPassword = auth.TrimPassword(item.TemporaryPassword)
		params, err := buildCreateUserParams(createUserRequest{Username: item.Username, DisplayName: item.DisplayName, Email: item.Email, TemporaryPassword: item.TemporaryPassword, TeamIDs: item.TeamIDs, RoleIDs: item.RoleIDs, RequirePasswordChange: item.RequirePasswordChange})
		if err == nil {
			// Each imported row is created -- and audited -- on its own so a
			// rejected row cannot take a committed one down with it.
			_, err = s.createUserWithAudit(request, input.DeploymentID, params)
		}
		if err != nil {
			code := "USER_IMPORT_FAILED"
			detail := "The account could not be created."
			if errors.Is(err, auth.ErrPasswordEdgeWhitespace) {
				code = "PASSWORD_POLICY_VIOLATION"
				detail = "Temporary passwords must not start or end with whitespace."
			} else if errors.Is(err, auth.ErrPasswordPolicy) {
				code = "PASSWORD_POLICY_VIOLATION"
				detail = "Temporary passwords must contain 6 to 1024 characters."
			}
			errorsResult = append(errorsResult, map[string]string{"externalRowId": item.ExternalRowID, "code": code, "detail": detail})
			continue
		}
		created++
	}
	// Every account this request created already carries its own audit row, so
	// the collection-level fallback would only duplicate the report.
	if created > 0 {
		markAdminAuditRecorded(request)
	}
	writeJSON(response, http.StatusOK, map[string]any{"created": created, "rejected": len(errorsResult), "errors": errorsResult})
}

func userMembershipProblem(roleIDs, teamIDs []string) (string, string) {
	if len(roleIDs) == 0 || len(teamIDs) == 0 {
		return "USER_RBAC_REQUIRED", "A user must have at least one role and one team."
	}
	if len(roleIDs) > 64 {
		return "INVALID_ROLE", "A user cannot have more than 64 roles."
	}
	if len(teamIDs) > 64 {
		return "INVALID_TEAM", "A user cannot have more than 64 teams."
	}
	for _, roleID := range roleIDs {
		if !validRBACID(roleID) {
			return "INVALID_ROLE", "The user contains an invalid role."
		}
	}
	for _, teamID := range teamIDs {
		if !validRBACID(teamID) {
			return "INVALID_TEAM", "The user contains an invalid team."
		}
	}
	return "", ""
}

// buildCreateUserParams validates a create or import request and prepares the
// row parameters, applying the default temporary password. Hashing happens here
// rather than inside the write so the account transaction is not held open
// while the password is derived.
func buildCreateUserParams(input createUserRequest) (repository.CreateUserParams, error) {
	if input.TemporaryPassword == "" {
		input.TemporaryPassword = auth.DefaultTemporaryPassword
	}
	if auth.EdgeWhitespace(input.TemporaryPassword) {
		return repository.CreateUserParams{}, auth.ErrPasswordEdgeWhitespace
	}
	hash, err := auth.HashPassword(input.TemporaryPassword)
	if err != nil {
		return repository.CreateUserParams{}, err
	}
	requireChange := true
	if input.RequirePasswordChange != nil {
		requireChange = *input.RequirePasswordChange
	}
	return repository.CreateUserParams{
		ID: uuid.NewString(), Username: input.Username, DisplayName: input.DisplayName,
		Email: input.Email, PasswordHash: hash, RequirePasswordChange: requireChange,
		RoleIDs: input.RoleIDs, TeamIDs: input.TeamIDs,
	}, nil
}

// createUserWithAudit writes the account, its role and team bindings and its
// administrative audit row in one transaction. An account that cannot be
// audited is not created: reporting a durable account without the record of who
// created it would leave the audit trail unable to explain a privileged grant.
// The audit row commits or rolls back with the bindings it describes.
func (s *Server) createUserWithAudit(request *http.Request, deploymentID string, params repository.CreateUserParams) (repository.UserRecord, error) {
	var user repository.UserRecord
	err := s.app.Store.Deployment(deploymentID).WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		created, err := tx.CreateUser(request.Context(), params)
		if err != nil {
			return err
		}
		user = created
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claimsFrom(request).Subject, Action: "create", ResourceType: "user", ResourceID: created.ID, Result: "success",
			Changes: auditUserCreation(created),
		})
	})
	return user, err
}

func (s *Server) updateUser(response http.ResponseWriter, request *http.Request) {
	var input struct {
		DisplayName *string `json:"displayName"`
		Email       *string `json:"email"`
		Status      *string `json:"status"`
		// Membership fields are decoded only to be rejected: they were
		// previously accepted and silently dropped, leaving callers under
		// the impression team/role relationships had changed (scale-round
		// A3). Membership is set at creation or import.
		TeamIDs *[]string `json:"teamIds"`
		RoleIDs *[]string `json:"roleIds"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.TeamIDs != nil || input.RoleIDs != nil {
		writeProblem(response, request, http.StatusBadRequest, "MEMBERSHIP_UPDATE_UNSUPPORTED", "teamIds and roleIds cannot be changed on user update; they are set at creation or import.")
		return
	}
	claims := claimsFrom(request)
	userID := chi.URLParam(request, "userId")
	var user repository.UserRecord
	err := s.app.Store.Deployment(claims.DeploymentID).WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		before, err := tx.GetUserRecord(request.Context(), userID)
		if err != nil {
			return err
		}
		updated, err := tx.UpdateUser(request.Context(), userID, repository.UpdateUserParams{
			DisplayName: input.DisplayName, Email: input.Email, Status: input.Status,
		})
		if err != nil {
			return err
		}
		user = updated
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "update", ResourceType: "user", ResourceID: userID, Result: "success",
			Changes: auditUserChanges(before, updated),
		})
	})
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	markAdminAuditRecorded(request)
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
	if auth.EdgeWhitespace(input.TemporaryPassword) {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Temporary passwords must not start or end with whitespace.")
		return
	}
	if err := auth.ValidatePassword(input.TemporaryPassword); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Temporary passwords must contain 6 to 1024 characters.")
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
	claims := claimsFrom(request)
	userID := chi.URLParam(request, "userId")
	if requireChange && userID == claims.Subject {
		writeProblem(response, request, http.StatusConflict, "SELF_PASSWORD_RESET_RESTRICTED", "Requiring a password change on your own account would lock you into a restricted session; use the self-service password change endpoint or set requirePasswordChange to false.")
		return
	}
	err = s.app.Store.Deployment(claims.DeploymentID).WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		if err := tx.UpdatePassword(request.Context(), userID, hash, requireChange); err != nil {
			return err
		}
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "reset_password", ResourceType: "user", ResourceID: userID, Result: "success",
			Changes: map[string]any{"after": map[string]any{"requirePasswordChange": requireChange}},
		})
	})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	markAdminAuditRecorded(request)
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
		"displayName": user.DisplayName, "email": user.Email, "kind": user.Kind, "status": user.Status,
		"teamIds": record.TeamIDs, "roleIds": record.RoleIDs,
		"createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt,
	}
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

// getUser returns a single platform user by ID with roles and teams.
func (s *Server) getUser(response http.ResponseWriter, request *http.Request) {
	user, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		GetUserRecord(request.Context(), chi.URLParam(request, "userId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicUser(user))
}

// deleteUser removes a platform user and their bindings. The user's sessions
// are revoked inside the same transaction as the removal (see
// repository.DeleteUser), so a failed deletion can never leave the account
// alive but signed out everywhere. The bootstrap admin cannot be deleted.
func (s *Server) deleteUser(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	userID := chi.URLParam(request, "userId")
	store := s.app.Store.Deployment(claims.DeploymentID)
	user, err := store.GetUser(request.Context(), userID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if user.Username == "admin" {
		writeProblem(response, request, http.StatusForbidden, "FORBIDDEN", "The bootstrap admin user cannot be deleted.")
		return
	}
	var removed repository.UserRecord
	err = store.WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		record, err := tx.GetUserRecord(request.Context(), userID)
		if err != nil {
			return err
		}
		removed = record
		if err := tx.DeleteUser(request.Context(), userID); err != nil {
			return err
		}
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "delete", ResourceType: "user", ResourceID: userID, Result: "success",
			Changes: auditRemoval(auditUserSnapshot(removed)),
		})
	})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	markAdminAuditRecorded(request)
	response.WriteHeader(http.StatusNoContent)
}

// auditUserChanges reports the profile fields an update can touch, masked by
// the same allowlist the request payload goes through.
func auditUserChanges(before, after repository.UserRecord) map[string]any {
	return auditChanges(auditUserSnapshot(before), auditUserSnapshot(after))
}

func auditUserSnapshot(record repository.UserRecord) map[string]any {
	email := any(nil)
	if record.Email != nil {
		email = *record.Email
	}
	return map[string]any{"displayName": record.DisplayName, "email": email, "status": record.Status}
}

// auditUserCreation documents the account a create established: its identity
// plus the role and team grants that make it privileged. The snapshot goes
// through the same fail-closed allowlist as every other audit document, so the
// temporary password cannot reach the row.
func auditUserCreation(record repository.UserRecord) map[string]any {
	snapshot := auditUserSnapshot(record)
	snapshot["username"] = record.Username
	snapshot["roleIds"] = record.RoleIDs
	snapshot["teamIds"] = record.TeamIDs
	return auditCreation(snapshot)
}
