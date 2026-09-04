package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) listPermissions(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), "SELECT id,description FROM permissions ORDER BY id")
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, description string
		if err := rows.Scan(&id, &description); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"id": id, "description": description})
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"permissions": items})
}

func (s *Server) listRoles(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT r.id,r.name,r.description,r.built_in,r.enabled,COALESCE(array_agg(rp.permission_id ORDER BY rp.permission_id) FILTER (WHERE rp.permission_id IS NOT NULL),'{}')
FROM roles r LEFT JOIN role_permissions rp ON rp.deployment_id=r.deployment_id AND rp.role_id=r.id
WHERE r.deployment_id=$1 GROUP BY r.id,r.name,r.description,r.built_in,r.enabled ORDER BY r.id`, claimsFrom(request).DeploymentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, description string
		var builtIn, enabled bool
		var permissions []string
		if err := rows.Scan(&id, &name, &description, &builtIn, &enabled, &permissions); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "description": description, "builtIn": builtIn, "enabled": enabled, "permissions": permissions})
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
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
	ctx := request.Context()
	tx, err := s.app.Pool.Begin(ctx)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO roles (deployment_id,id,name,description) VALUES ($1,$2,$3,$4)`, claimsFrom(request).DeploymentID, input.ID, strings.TrimSpace(input.Name), input.Description); err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "ROLE_EXISTS", "The role already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	for _, permission := range input.Permissions {
		if !validRBACID(permission) || !strings.Contains(permission, ".") {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_PERMISSION", "The role contains an invalid permission.")
			return
		}
		result, execErr := tx.Exec(ctx, `INSERT INTO role_permissions (deployment_id,role_id,permission_id) SELECT $1,$2,id FROM permissions WHERE id=$3`, claimsFrom(request).DeploymentID, input.ID, permission)
		if execErr != nil {
			err = execErr
			databaseFailure(response, request, err)
			return
		}
		if result.RowsAffected() != 1 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_PERMISSION", "The role contains an unknown permission.")
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": input.ID, "name": strings.TrimSpace(input.Name), "permissions": input.Permissions})
}

func (s *Server) listTeams(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `SELECT t.id,t.name,t.description,t.built_in,t.enabled,COUNT(utb.user_id)
FROM teams t LEFT JOIN user_team_bindings utb ON utb.deployment_id=t.deployment_id AND utb.team_id=t.id
WHERE t.deployment_id=$1 GROUP BY t.id,t.name,t.description,t.built_in,t.enabled ORDER BY t.id`, claimsFrom(request).DeploymentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, description string
		var builtIn, enabled bool
		var memberCount int
		if err := rows.Scan(&id, &name, &description, &builtIn, &enabled, &memberCount); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "description": description, "builtIn": builtIn, "enabled": enabled, "memberCount": memberCount})
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
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
	_, err := s.app.Pool.Exec(request.Context(), `INSERT INTO teams (deployment_id,id,name,description) VALUES ($1,$2,$3,$4)`, claimsFrom(request).DeploymentID, input.ID, strings.TrimSpace(input.Name), input.Description)
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
	ctx := request.Context()
	tx, err := s.app.Pool.Begin(ctx)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	userID := chi.URLParam(request, "userId")
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id=$1 AND deployment_id=$2)`, userID, claimsFrom(request).DeploymentID).Scan(&exists); err != nil || !exists {
		if err != nil {
			databaseFailure(response, request, err)
		} else {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		}
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_role_bindings WHERE deployment_id=$1 AND user_id=$2`, claimsFrom(request).DeploymentID, userID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_team_bindings WHERE deployment_id=$1 AND user_id=$2`, claimsFrom(request).DeploymentID, userID); err != nil {
		databaseFailure(response, request, err)
		return
	}
	for index, roleID := range input.RoleIDs {
		if !validRBACID(roleID) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The user contains an invalid role.")
			return
		}
		result, execErr := tx.Exec(ctx, `INSERT INTO user_role_bindings (deployment_id,user_id,role_id,is_primary) SELECT $1,$2,id,$3 FROM roles WHERE deployment_id=$1 AND id=$4`, claimsFrom(request).DeploymentID, userID, index == 0, roleID)
		if execErr != nil {
			err = execErr
			databaseFailure(response, request, err)
			return
		}
		if result.RowsAffected() != 1 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ROLE", "The user contains an unknown role.")
			return
		}
	}
	for index, teamID := range input.TeamIDs {
		if !validRBACID(teamID) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "The user contains an invalid team.")
			return
		}
		result, execErr := tx.Exec(ctx, `INSERT INTO user_team_bindings (deployment_id,user_id,team_id,is_primary) SELECT $1,$2,id,$3 FROM teams WHERE deployment_id=$1 AND id=$4`, claimsFrom(request).DeploymentID, userID, index == 0, teamID)
		if execErr != nil {
			err = execErr
			databaseFailure(response, request, err)
			return
		}
		if result.RowsAffected() != 1 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_TEAM", "The user contains an unknown team.")
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
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
