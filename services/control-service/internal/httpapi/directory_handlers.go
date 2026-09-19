package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
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
		Ephemeral     bool     `json:"ephemeral"`
		ExpiresAt     *string  `json:"expiresAt"`
		ScopeFrom     string   `json:"scopeFromUserId"`
		ModelIDs      []string `json:"modelIds"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.Username) || strings.TrimSpace(input.DisplayName) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent username and display name are invalid.")
		return
	}
	if err := auth.ValidatePassword(input.Password); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Agent passwords must contain 12 to 1024 characters.")
		return
	}
	if input.HomeTeamID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent home team is required.")
		return
	}
	// Ephemeral lifecycle coupling: expiry is mandatory exactly when the
	// account is conversation-scoped, and it must lie in the future.
	var expiresAt *time.Time
	if input.Ephemeral {
		if input.ExpiresAt == nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "Ephemeral agents require expiresAt.")
			return
		}
		parsed, err := time.Parse(time.RFC3339Nano, *input.ExpiresAt)
		if err != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "expiresAt must be an RFC 3339 timestamp.")
			return
		}
		if !parsed.After(time.Now().UTC()) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "expiresAt must lie in the future.")
			return
		}
		expiresAt = &parsed
	} else if input.ExpiresAt != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "expiresAt requires ephemeral=true.")
		return
	}
	// The home team always counts as a granted team, so delegated admins are
	// held to the same membership, existence, and grant-derivation guardrails
	// as the human createUser path.
	teams := append(append(make([]string, 0, len(input.TeamIDs)+1), input.TeamIDs...), input.HomeTeamID)
	if code, detail := userMembershipProblem(input.RoleIDs, teams); code != "" {
		writeProblem(response, request, http.StatusBadRequest, code, detail)
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
	for _, roleID := range input.RoleIDs {
		if _, err := store.GetRoleRecord(request.Context(), roleID); errors.Is(err, repository.ErrNotFound) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent contains an unknown role.")
			return
		} else if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	for _, teamID := range input.TeamIDs {
		if _, err := store.GetTeamRecord(request.Context(), teamID); errors.Is(err, repository.ErrNotFound) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The agent contains an unknown team.")
			return
		} else if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	if !s.authorizeRoleGrant(response, request, input.RoleIDs) {
		return
	}
	if !s.authorizeTeamGrant(response, request, teams) {
		return
	}
	if input.PromptSkillID != "" {
		if _, err := s.app.Store.GetSkill(request.Context(), input.PromptSkillID); errors.Is(err, repository.ErrNotFound) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The prompt skill does not exist.")
			return
		} else if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	// A frozen scope snapshot: every requested team must already be inside
	// the source user's visible scope, otherwise the ephemeral instance
	// would carry a wider org subtree than the requester it serves.
	var snapshot *app.RetrievalContext
	if input.ScopeFrom != "" {
		resolved, err := s.app.DataScopeContext(request.Context(), claimsFrom(request).DeploymentID, input.ScopeFrom)
		if err != nil || resolved.PrincipalID == "" {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_SCOPE_SOURCE", "The scope source user does not exist.")
			return
		}
		visible := make(map[string]bool, len(resolved.OrgScope))
		for _, team := range resolved.OrgScope {
			visible[team] = true
		}
		for _, ref := range resolved.DeniedResources {
			if ref.Kind == "team" {
				delete(visible, ref.ID)
			}
		}
		for _, team := range teams {
			if !visible[team] {
				writeProblem(response, request, http.StatusBadRequest, "INVALID_SCOPE_SOURCE", "homeTeamId and teamIds must lie inside the scope source user's visible teams.")
				return
			}
		}
		snapshot = &resolved
	}
	for _, modelID := range input.ModelIDs {
		if _, err := store.GetModel(request.Context(), modelID); errors.Is(err, repository.ErrNotFound) {
			writeProblem(response, request, http.StatusBadRequest, "UNKNOWN_MODEL", "The model assignment references an unknown model.")
			return
		} else if err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		writeProblem(response, request, http.StatusBadRequest, "PASSWORD_POLICY_VIOLATION", "Agent passwords must contain 12 to 1024 characters.")
		return
	}
	record, err := store.CreateUser(request.Context(), repository.CreateUserParams{
		ID: uuid.NewString(), Username: input.Username, DisplayName: strings.TrimSpace(input.DisplayName),
		PasswordHash: passwordHash, RequirePasswordChange: false, IsAdmin: false, Kind: "agent",
		RoleIDs: input.RoleIDs, TeamIDs: teams,
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
		HomeTeamID: input.HomeTeamID, Ephemeral: input.Ephemeral, ExpiresAt: expiresAt,
	}
	if input.PromptSkillID != "" {
		profile.PromptSkillID = &input.PromptSkillID
	}
	if err := store.UpsertAgentProfile(request.Context(), profile); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if snapshot != nil {
		// Freeze the source user's evaluation: visible subtrees become
		// management_scope grants, explicit denies are copied verbatim. The
		// rules are detached from the source; later source changes never
		// propagate into a running conversation.
		denied := make(map[string]bool)
		for _, ref := range snapshot.DeniedResources {
			if ref.Kind == "team" {
				denied[ref.ID] = true
			}
		}
		for _, team := range snapshot.OrgScope {
			rule := repository.DataScopeRule{
				ID: uuid.NewString(), RuleKind: "management_scope",
				SubjectType: "user", SubjectID: record.User.ID,
				ResourceKind: "team", ResourceID: team,
				Reason:    "ephemeral snapshot from " + input.ScopeFrom,
				CreatedBy: claimsFrom(request).Subject,
			}
			if _, err := store.CreateDataScopeRule(request.Context(), rule); err != nil {
				databaseFailure(response, request, err)
				return
			}
		}
		for team := range denied {
			rule := repository.DataScopeRule{
				ID: uuid.NewString(), RuleKind: "explicit_deny",
				SubjectType: "user", SubjectID: record.User.ID,
				ResourceKind: "team", ResourceID: team,
				Reason:    "ephemeral snapshot deny from " + input.ScopeFrom,
				CreatedBy: claimsFrom(request).Subject,
			}
			if _, err := store.CreateDataScopeRule(request.Context(), rule); err != nil {
				databaseFailure(response, request, err)
				return
			}
		}
	}
	for _, modelID := range input.ModelIDs {
		if _, err := store.CreateModelAssignment(request.Context(), repository.ModelAssignment{
			ID: uuid.NewString(), DeploymentID: claimsFrom(request).DeploymentID,
			ModelID: modelID, SubjectType: "user", SubjectID: record.User.ID,
		}); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	payload := map[string]any{
		"id": record.User.ID, "username": record.User.Username, "displayName": record.User.DisplayName,
		"homeTeamId": input.HomeTeamID, "displayTitle": input.DisplayTitle,
		"ephemeral": input.Ephemeral, "expiresAt": nil,
	}
	if expiresAt != nil {
		payload["expiresAt"] = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(response, http.StatusCreated, payload)
}

func (s *Server) listAgents(response http.ResponseWriter, request *http.Request) {
	entries, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		ListAgentDirectory(request.Context(), request.URL.Query().Get("cursor"), limit(request), request.URL.Query().Get("includeEphemeral") == "true")
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id": entry.User.ID, "username": entry.User.Username,
			"displayName": entry.User.DisplayName, "kind": entry.User.Kind, "status": entry.User.Status,
			"online": entry.Online,
		}
		if entry.LastHeartbeatAt != nil {
			item["lastHeartbeatAt"] = entry.LastHeartbeatAt.UTC().Format(time.RFC3339Nano)
		}
		item["ephemeral"] = false
		item["expiresAt"] = nil
		if entry.Profile != nil {
			item["homeTeamId"] = entry.Profile.HomeTeamID
			item["displayTitle"] = entry.Profile.DisplayTitle
			item["description"] = entry.Profile.Description
			item["ephemeral"] = entry.Profile.Ephemeral
			if entry.Profile.ExpiresAt != nil {
				item["expiresAt"] = entry.Profile.ExpiresAt.UTC().Format(time.RFC3339Nano)
			}
			if entry.Profile.PromptSkillID != nil {
				item["promptSkillId"] = *entry.Profile.PromptSkillID
			}
		}
		items = append(items, item)
	}
	var nextCursor any
	if len(entries) == int(limit(request)) {
		nextCursor = entries[len(entries)-1].User.ID
	}
	writeJSON(response, http.StatusOK, map[string]any{"agents": items, "nextCursor": nextCursor})
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
		store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
		if _, err := store.GetTeamRecord(request.Context(), *input.HomeTeamID); errors.Is(err, repository.ErrNotFound) {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_AGENT", "The home team does not exist.")
			return
		} else if err != nil {
			databaseFailure(response, request, err)
			return
		}
		if !s.authorizeTeamGrant(response, request, []string{*input.HomeTeamID}) {
			return
		}
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

// Identity source kinds are protocol categories only; product-specific
// connector flavors live inside `config` on the deployment side.
func validIdentitySourceKind(kind string) bool {
	switch kind {
	case "ldap", "oidc", "directory":
		return true
	}
	return false
}

var identitySourceConfigReservedKeys = map[string]struct{}{
	"password": {}, "secret": {}, "token": {}, "apikey": {}, "clientsecret": {}, "bindpassword": {},
}

func identitySourceConfigProblem(config json.RawMessage) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(config, &object); err != nil {
		return "The identity source config must be a JSON object."
	}
	for key := range object {
		if _, reserved := identitySourceConfigReservedKeys[strings.ToLower(key)]; reserved {
			return "The identity source config must not contain secret values; reference the credential store instead."
		}
	}
	return ""
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
	} else if problem := identitySourceConfigProblem(input.Config); problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_IDENTITY_SOURCE", problem)
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
	sources, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		ListIdentitySources(request.Context(), request.URL.Query().Get("cursor"), limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		items = append(items, map[string]any{"id": source.ID, "kind": source.Kind, "displayName": source.DisplayName, "enabled": source.Enabled})
	}
	var nextCursor any
	if len(sources) == int(limit(request)) {
		nextCursor = sources[len(sources)-1].ID
	}
	writeJSON(response, http.StatusOK, map[string]any{"identitySources": items, "nextCursor": nextCursor})
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
	cursorSubjectType, cursorExternalID := "", ""
	if cursor := request.URL.Query().Get("cursor"); cursor != "" {
		if separator := strings.Index(cursor, "/"); separator > 0 && separator < len(cursor)-1 {
			cursorSubjectType, cursorExternalID = cursor[:separator], cursor[separator+1:]
		}
	}
	mappings, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListIdentityMappings(
		request.Context(), chi.URLParam(request, "sourceId"), request.URL.Query().Get("subjectType"),
		cursorSubjectType, cursorExternalID, limit(request))
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
	var nextCursor any
	if len(mappings) == int(limit(request)) {
		last := mappings[len(mappings)-1]
		nextCursor = last.ExternalSubjectType + "/" + last.ExternalID
	}
	writeJSON(response, http.StatusOK, map[string]any{"mappings": items, "nextCursor": nextCursor})
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
	"management_scope": true, "exception_grant": true, "explicit_deny": true,
}

// validDataScopeResourceKind accepts the protocol-defined "team" or any
// deployment-defined lowercase identifier; the control plane treats the
// latter as opaque references.
func validDataScopeResourceKind(kind string) bool {
	if kind == "team" {
		return true
	}
	if kind == "" || len(kind) > 64 {
		return false
	}
	for index, character := range kind {
		lowercase := character >= 'a' && character <= 'z'
		digitOrSeparator := (character >= '0' && character <= '9') || character == '_' || character == '-'
		if index == 0 && !lowercase {
			return false
		}
		if !lowercase && !digitOrSeparator {
			return false
		}
	}
	return true
}

func (s *Server) createDataScopeRule(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ID           string     `json:"id"`
		RuleKind     string     `json:"ruleKind"`
		SubjectType  string     `json:"subjectType"`
		SubjectID    string     `json:"subjectId"`
		ResourceKind string     `json:"resourceKind"`
		ResourceID   string     `json:"resourceId"`
		StartsAt     *time.Time `json:"startsAt"`
		ExpiresAt    *time.Time `json:"expiresAt"`
		Reason       string     `json:"reason"`
	}
	if !decodeJSON(response, request, &input) || !validRBACID(input.ID) || !dataScopeRuleKinds[input.RuleKind] ||
		!validSubjectType(input.SubjectType) || strings.TrimSpace(input.SubjectID) == "" ||
		!validDataScopeResourceKind(input.ResourceKind) || strings.TrimSpace(input.ResourceID) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_DATA_SCOPE_RULE", "The rule kind, subject, and resource are invalid.")
		return
	}
	rule := repository.DataScopeRule{
		ID: input.ID, RuleKind: input.RuleKind, SubjectType: input.SubjectType, SubjectID: strings.TrimSpace(input.SubjectID),
		ResourceKind: input.ResourceKind, ResourceID: strings.TrimSpace(input.ResourceID),
		StartsAt: input.StartsAt, ExpiresAt: input.ExpiresAt, Reason: input.Reason, CreatedBy: claimsFrom(request).Subject,
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
		"resourceKind": rule.ResourceKind, "resourceId": rule.ResourceID, "reason": rule.Reason,
	}
	if rule.StartsAt != nil {
		item["startsAt"] = rule.StartsAt.UTC().Format(time.RFC3339Nano)
	}
	if rule.ExpiresAt != nil {
		item["expiresAt"] = rule.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return item
}

func (s *Server) listDataScopeRules(response http.ResponseWriter, request *http.Request) {
	rules, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		ListDataScopeRules(request.Context(), request.URL.Query().Get("cursor"), limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		items = append(items, publicDataScopeRule(rule))
	}
	var nextCursor any
	if len(rules) == int(limit(request)) {
		nextCursor = rules[len(rules)-1].ID
	}
	writeJSON(response, http.StatusOK, map[string]any{"rules": items, "nextCursor": nextCursor})
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

func (s *Server) deleteAgent(response http.ResponseWriter, request *http.Request) {
	userID := chi.URLParam(request, "agentId")
	store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
	if _, err := store.GetAgentProfile(request.Context(), userID); errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The agent profile was not found.")
		return
	} else if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := store.DeleteAgent(request.Context(), userID); err != nil {
		if errors.Is(err, repository.ErrAgentHasSessions) {
			writeProblem(response, request, http.StatusConflict, "AGENT_HAS_SESSIONS", "Revoke the agent sessions before deleting the account.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
