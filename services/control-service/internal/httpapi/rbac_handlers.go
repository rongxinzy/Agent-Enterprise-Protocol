package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func (s *Server) listPermissions(response http.ResponseWriter, request *http.Request) {
	permissions, err := s.app.Store.ListPermissions(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(permissions))
	for _, permission := range permissions {
		items = append(items, map[string]any{"id": permission.ID, "description": permission.Description})
	}
	writeJSON(response, http.StatusOK, map[string]any{"permissions": items})
}

func (s *Server) listRoles(response http.ResponseWriter, request *http.Request) {
	roles, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListRoles(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		items = append(items, map[string]any{
			"id": role.ID, "name": role.Name, "description": role.Description,
			"builtIn": role.BuiltIn, "enabled": role.Enabled, "permissions": role.Permissions,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"roles": items})
}

func (s *Server) createRole(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.ID) || strings.TrimSpace(input.Name) == "" || len(input.Permissions) > 128 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The role id, name, and permissions are invalid.")
		return
	}
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).CreateRole(
		request.Context(), repository.Role{ID: input.ID, Name: strings.TrimSpace(input.Name), Description: input.Description}, input.Permissions,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "ROLE_EXISTS", "The role already exists.")
			return
		}
		if errors.Is(err, repository.ErrUnknownPermission) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_PERMISSION", "The role contains an unknown permission.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": input.ID, "name": strings.TrimSpace(input.Name), "permissions": input.Permissions})
}

func (s *Server) listTeams(response http.ResponseWriter, request *http.Request) {
	teams, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListTeams(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(teams))
	for _, team := range teams {
		items = append(items, map[string]any{
			"id": team.ID, "name": team.Name, "description": team.Description,
			"builtIn": team.BuiltIn, "enabled": team.Enabled, "memberCount": team.MemberCount,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"teams": items})
}

func (s *Server) createTeam(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.ID) || strings.TrimSpace(input.Name) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "The team id and name are invalid.")
		return
	}
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).CreateTeam(request.Context(), repository.Team{
		ID: input.ID, Name: strings.TrimSpace(input.Name), Description: input.Description,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "TEAM_EXISTS", "The team already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": input.ID, "name": strings.TrimSpace(input.Name)})
}

func (s *Server) replaceUserRBAC(response http.ResponseWriter, request *http.Request) {
	var input struct {
		RoleIDs []string `json:"roleIds"`
		TeamIDs []string `json:"teamIds"`
	}
	if !decodeJSON(response, request, &input) || len(input.RoleIDs) == 0 || len(input.TeamIDs) == 0 || len(input.RoleIDs) > 64 || len(input.TeamIDs) > 64 {
		writeProblem(response, request, http.StatusBadRequest, "USER_RBAC_REQUIRED", "A user must have at least one role and one team.")
		return
	}
	userID := chi.URLParam(request, "userId")
	for _, roleID := range input.RoleIDs {
		if !validRBACID(roleID) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The user contains an invalid role.")
			return
		}
	}
	for _, teamID := range input.TeamIDs {
		if !validRBACID(teamID) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "The user contains an invalid team.")
			return
		}
	}
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		ReplaceUserRBAC(request.Context(), userID, input.RoleIDs, input.TeamIDs)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	}
	if errors.Is(err, repository.ErrUnknownRole) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The user contains an unknown role.")
		return
	}
	if errors.Is(err, repository.ErrUnknownTeam) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "The user contains an unknown team.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"userId": userID, "roleIds": input.RoleIDs, "teamIds": input.TeamIDs})
}

func validRBACID(value string) bool {
	if len(value) == 0 || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}
