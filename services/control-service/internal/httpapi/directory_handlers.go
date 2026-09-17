package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// ---- Digital employee directory ----

func (s *Server) createAgent(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Username      string   `json:"username"`
		DisplayName   string   `json:"displayName"`
		Password      string   `json:"password"`
		RoleIDs       []string `json:"roleIds"`
		TeamIDs       []string `json:"teamIds"`
		HomeTeamID    string   `json:"homeTeamId"`
		DisplayTitle  string   `json:"displayTitle"`
		Description   string   `json:"description"`
		PromptSkillID string   `json:"promptSkillId"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.Username) || strings.TrimSpace(input.DisplayName) == "" || len(input.Password) < 8 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent username, display name, and password are invalid.")
		return
	}
	if input.HomeTeamID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent home team is required.")
		return
	}
	store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
	if _, err := store.GetTeamRecord(request.Context(), input.HomeTeamID); errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The home team does not exist.")
		return
	} else if err != nil {
		databaseFailure(response, request, err)
		return
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent password is invalid.")
		return
	}
	record, err := store.CreateUser(request.Context(), repository.CreateUserParams{
		ID: uuid.NewString(), Username: input.Username, DisplayName: strings.TrimSpace(input.DisplayName),
		PasswordHash: passwordHash, RequirePasswordChange: false, IsAdmin: false, Kind: "agent",
		RoleIDs: input.RoleIDs, TeamIDs: input.TeamIDs,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "AGENT_EXISTS", "The agent username already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	profile := repository.AgentProfile{
		UserID: record.User.ID, DisplayTitle: input.DisplayTitle, Description: input.Description,
		HomeTeamID: input.HomeTeamID,
	}
	if input.PromptSkillID != "" {
		profile.PromptSkillID = &input.PromptSkillID
	}
	if err := store.UpsertAgentProfile(request.Context(), profile); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"id": record.User.ID, "username": record.User.Username, "displayName": record.User.DisplayName,
		"homeTeamId": input.HomeTeamID, "displayTitle": input.DisplayTitle,
	})
}

func (s *Server) listAgents(response http.ResponseWriter, request *http.Request) {
	entries, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListAgentDirectory(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id": entry.User.ID, "username": entry.User.Username,
			"displayName": entry.User.DisplayName, "status": entry.User.Status,
			"online": entry.Online,
		}
		if entry.LastHeartbeatAt != nil {
			item["lastHeartbeatAt"] = entry.LastHeartbeatAt.UTC().Format(time.RFC3339Nano)
		}
		if entry.Profile != nil {
			item["homeTeamId"] = entry.Profile.HomeTeamID
			item["displayTitle"] = entry.Profile.DisplayTitle
			item["description"] = entry.Profile.Description
			if entry.Profile.PromptSkillID != nil {
				item["promptSkillId"] = *entry.Profile.PromptSkillID
			}
		}
		items = append(items, item)
	}
	writeJSON(response, http.StatusOK, map[string]any{"agents": items})
}

func (s *Server) updateAgentProfile(response http.ResponseWriter, request *http.Request) {
	userID := chi.URLParam(request, "agentId")
	var input struct {
		DisplayTitle  *string `json:"displayTitle"`
		Description   *string `json:"description"`
		HomeTeamID    *string `json:"homeTeamId"`
		PromptSkillID *string `json:"promptSkillId"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
	profile, err := store.GetAgentProfile(request.Context(), userID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The agent profile was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if input.DisplayTitle != nil {
		profile.DisplayTitle = *input.DisplayTitle
	}
	if input.Description != nil {
		profile.Description = *input.Description
	}
	if input.HomeTeamID != nil {
		profile.HomeTeamID = *input.HomeTeamID
	}
	if input.PromptSkillID != nil {
		if *input.PromptSkillID == "" {
			profile.PromptSkillID = nil
		} else {
			profile.PromptSkillID = input.PromptSkillID
		}
	}
	if err := store.UpsertAgentProfile(request.Context(), profile); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"id": userID, "homeTeamId": profile.HomeTeamID, "displayTitle": profile.DisplayTitle})
}

// ---- Identity sources and mappings ----

func validIdentitySourceKind(kind string) bool {
	switch kind {
	case "ldap", "ad", "oidc", "hr", "feishu", "wecom":
		return true
	}
	return false
}

func (s *Server) createIdentitySource(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ID          string          `json:"id"`
		Kind        string          `json:"kind"`
		DisplayName string          `json:"displayName"`
		Config      json.RawMessage `json:"config"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.ID) || !validIdentitySourceKind(input.Kind) || strings.TrimSpace(input.DisplayName) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_IDENTITY_SOURCE", "The identity source id, kind, and display name are invalid.")
		return
	}
	if len(input.Config) == 0 {
		input.Config = json.RawMessage("{}")
	} else if !json.Valid(input.Config) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_IDENTITY_SOURCE", "The identity source config must be a JSON object.")
		return
	}
	source, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).CreateIdentitySource(request.Context(), repository.IdentitySource{
		ID: input.ID, Kind: input.Kind, DisplayName: strings.TrimSpace(input.DisplayName), Config: input.Config, Enabled: true,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "IDENTITY_SOURCE_EXISTS", "The identity source already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": source.ID, "kind": source.Kind, "displayName": source.DisplayName, "enabled": source.Enabled})
}

func (s *Server) listIdentitySources(response http.ResponseWriter, request *http.Request) {
	sources, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListIdentitySources(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		items = append(items, map[string]any{"id": source.ID, "kind": source.Kind, "displayName": source.DisplayName, "enabled": source.Enabled})
	}
	writeJSON(response, http.StatusOK, map[string]any{"identitySources": items})
}

func (s *Server) upsertIdentityMapping(response http.ResponseWriter, request *http.Request) {
	sourceID := chi.URLParam(request, "sourceId")
	var input struct {
		ExternalSubjectType string `json:"externalSubjectType"`
		ExternalID          string `json:"externalId"`
		LocalSubjectID      string `json:"localSubjectId"`
	}
	if !decodeJSON(response, request, &input) || (input.ExternalSubjectType != "user" && input.ExternalSubjectType != "team") ||
		strings.TrimSpace(input.ExternalID) == "" || strings.TrimSpace(input.LocalSubjectID) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_IDENTITY_MAPPING", "The mapping subject type, external id, and local id are required.")
		return
	}
	store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
	if _, err := store.GetIdentitySource(request.Context(), sourceID); errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The identity source was not found.")
		return
	} else if err != nil {
		databaseFailure(response, request, err)
		return
	}
	mapping := repository.IdentityMapping{
		SourceID: sourceID, ExternalSubjectType: input.ExternalSubjectType,
		ExternalID: strings.TrimSpace(input.ExternalID), LocalSubjectID: strings.TrimSpace(input.LocalSubjectID),
		Status: "active",
	}
	if err := store.UpsertIdentityMapping(request.Context(), mapping); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"sourceId": sourceID, "externalId": mapping.ExternalID, "localSubjectId": mapping.LocalSubjectID})
}

func (s *Server) listIdentityMappings(response http.ResponseWriter, request *http.Request) {
	mappings, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListIdentityMappings(
		request.Context(), chi.URLParam(request, "sourceId"), request.URL.Query().Get("subjectType"))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(mappings))
	for _, mapping := range mappings {
		items = append(items, map[string]any{
			"sourceId": mapping.SourceID, "externalSubjectType": mapping.ExternalSubjectType,
			"externalId": mapping.ExternalID, "localSubjectId": mapping.LocalSubjectID, "status": mapping.Status,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"mappings": items})
}

func (s *Server) deleteIdentityMapping(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).DeleteIdentityMapping(
		request.Context(), chi.URLParam(request, "sourceId"), chi.URLParam(request, "subjectType"), chi.URLParam(request, "externalId"))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// ---- Data scope rules and retrieval context ----

var dataScopeRuleKinds = map[string]bool{
	"department_default": true, "management_scope": true, "exception_grant": true, "explicit_deny": true,
}

var dataScopeResourceKinds = map[string]bool{
	"knowledge_base": true, "classification": true, "team": true,
}

func (s *Server) createDataScopeRule(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ID           string     `json:"id"`
		RuleKind     string     `json:"ruleKind"`
		SubjectType  string     `json:"subjectType"`
		SubjectID    string     `json:"subjectId"`
		ResourceKind string     `json:"resourceKind"`
		ResourceID   string     `json:"resourceId"`
		Priority     *int       `json:"priority"`
		ExpiresAt    *time.Time `json:"expiresAt"`
		Reason       string     `json:"reason"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.ID) || !dataScopeRuleKinds[input.RuleKind] ||
		!validSubjectType(input.SubjectType) || strings.TrimSpace(input.SubjectID) == "" ||
		!dataScopeResourceKinds[input.ResourceKind] || strings.TrimSpace(input.ResourceID) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_SCOPE_RULE", "The rule kind, subject, and resource are invalid.")
		return
	}
	rule := repository.DataScopeRule{
		ID: input.ID, RuleKind: input.RuleKind, SubjectType: input.SubjectType, SubjectID: strings.TrimSpace(input.SubjectID),
		ResourceKind: input.ResourceKind, ResourceID: strings.TrimSpace(input.ResourceID),
		ExpiresAt: input.ExpiresAt, Reason: input.Reason, CreatedBy: claimsFrom(request).Subject,
	}
	if input.Priority != nil {
		rule.Priority = *input.Priority
	}
	created, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).CreateDataScopeRule(request.Context(), rule)
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "DATA_SCOPE_RULE_EXISTS", "The data scope rule already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, publicDataScopeRule(created))
}

func validSubjectType(value string) bool {
	return value == "user" || value == "role" || value == "team"
}

func publicDataScopeRule(rule repository.DataScopeRule) map[string]any {
	item := map[string]any{
		"id": rule.ID, "ruleKind": rule.RuleKind, "subjectType": rule.SubjectType, "subjectId": rule.SubjectID,
		"resourceKind": rule.ResourceKind, "resourceId": rule.ResourceID, "priority": rule.Priority, "reason": rule.Reason,
	}
	if rule.ExpiresAt != nil {
		item["expiresAt"] = rule.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return item
}

func (s *Server) listDataScopeRules(response http.ResponseWriter, request *http.Request) {
	rules, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListDataScopeRules(request.Context(), limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		items = append(items, publicDataScopeRule(rule))
	}
	writeJSON(response, http.StatusOK, map[string]any{"rules": items})
}

func (s *Server) getDataScopeRule(response http.ResponseWriter, request *http.Request) {
	rule, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GetDataScopeRule(request.Context(), chi.URLParam(request, "ruleId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The data scope rule was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, publicDataScopeRule(rule))
}

func (s *Server) deleteDataScopeRule(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).DeleteDataScopeRule(request.Context(), chi.URLParam(request, "ruleId"))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// dataScopeContext is the PEP contract: the caller names a user; the server
// derives every scope value from stored bindings and rules.
func (s *Server) dataScopeContext(response http.ResponseWriter, request *http.Request) {
	userID := request.URL.Query().Get("userId")
	if userID == "" {
		writeProblem(response, request, http.StatusBadRequest, "USER_REQUIRED", "The userId query parameter is required.")
		return
	}
	if _, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GetUser(request.Context(), userID); errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user was not found.")
		return
	} else if err != nil {
		databaseFailure(response, request, err)
		return
	}
	context, err := s.app.DataScopeContext(request.Context(), claimsFrom(request).DeploymentID, userID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, context)
}
