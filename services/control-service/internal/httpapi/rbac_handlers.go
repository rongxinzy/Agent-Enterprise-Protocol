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
	pageSize := limit(request)
	roles, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListRolesPage(request.Context(), request.URL.Query().Get("cursor"), pageSize+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	roles, nextCursor := page(roles, pageSize, func(item repository.RoleRecord) string { return item.ID })
	items := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		items = append(items, map[string]any{
			"id": role.ID, "name": role.Name, "description": role.Description,
			"builtIn": role.BuiltIn, "enabled": role.Enabled, "permissions": role.Permissions,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"roles": items, "nextCursor": nextCursor})
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

func (s *Server) getRole(response http.ResponseWriter, request *http.Request) {
	role, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GetRoleRecord(request.Context(), chi.URLParam(request, "roleId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The role was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicRole(role))
}

func (s *Server) updateRole(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		Enabled     *bool     `json:"enabled"`
		Permissions *[]string `json:"permissions"`
	}
	if !decodeJSON(response, request, &input) || (input.Name == nil && input.Description == nil && input.Enabled == nil && input.Permissions == nil) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "At least one role field is required.")
		return
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The role name is required.")
		return
	}
	if input.Permissions != nil && len(*input.Permissions) > 128 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The role contains too many permissions.")
		return
	}
	role, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).UpdateRole(request.Context(), chi.URLParam(request, "roleId"), input.Name, input.Description, input.Enabled, input.Permissions)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The role was not found.")
		return
	}
	if errors.Is(err, repository.ErrUnknownPermission) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_PERMISSION", "The role contains an unknown permission.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicRole(role))
}

func (s *Server) deleteRole(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).DeleteRole(request.Context(), chi.URLParam(request, "roleId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The role was not found.")
		return
	}
	if errors.Is(err, repository.ErrBuiltInResource) {
		writeProblem(response, request, http.StatusConflict, "BUILT_IN_RESOURCE", "Built-in roles cannot be deleted.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTeams(response http.ResponseWriter, request *http.Request) {
	pageSize := limit(request)
	teams, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListTeamsPage(request.Context(), request.URL.Query().Get("cursor"), pageSize+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	teams, nextCursor := page(teams, pageSize, func(item repository.TeamRecord) string { return item.ID })
	items := make([]map[string]any, 0, len(teams))
	for _, team := range teams {
		items = append(items, map[string]any{
			"id": team.ID, "name": team.Name, "description": team.Description,
			"builtIn": team.BuiltIn, "enabled": team.Enabled, "memberCount": team.MemberCount,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"teams": items, "nextCursor": nextCursor})
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

func (s *Server) getTeam(response http.ResponseWriter, request *http.Request) {
	team, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GetTeamRecord(request.Context(), chi.URLParam(request, "teamId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Team was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicTeam(team))
}

func (s *Server) updateTeam(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
	}
	if !decodeJSON(response, request, &input) || (input.Name == nil && input.Description == nil && input.Enabled == nil) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "At least one Team field is required.")
		return
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "The Team name is required.")
		return
	}
	team, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).UpdateTeam(request.Context(), chi.URLParam(request, "teamId"), input.Name, input.Description, input.Enabled)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Team was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicTeam(team))
}

func (s *Server) deleteTeam(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).DeleteTeam(request.Context(), chi.URLParam(request, "teamId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The Team was not found.")
		return
	}
	if errors.Is(err, repository.ErrBuiltInResource) {
		writeProblem(response, request, http.StatusConflict, "BUILT_IN_RESOURCE", "Built-in Teams cannot be deleted.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func publicRole(role repository.RoleRecord) map[string]any {
	return map[string]any{"id": role.ID, "name": role.Name, "description": role.Description, "builtIn": role.BuiltIn, "enabled": role.Enabled, "permissions": role.Permissions}
}
func publicTeam(team repository.TeamRecord) map[string]any {
	return map[string]any{"id": team.ID, "name": team.Name, "description": team.Description, "builtIn": team.BuiltIn, "enabled": team.Enabled, "memberCount": team.MemberCount}
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
