package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

const modelColumns = `id,display_name,source_type,protocol,endpoint,upstream_model,local_model_ref,credential_id,capabilities,reasoning_compatibility,context_window,is_default,enabled,created_at,updated_at`

type modelReasoningCompatibility struct {
	ThinkingFormat                              string `json:"thinkingFormat"`
	SupportsReasoningEffort                     bool   `json:"supportsReasoningEffort"`
	RequiresReasoningContentOnAssistantMessages bool   `json:"requiresReasoningContentOnAssistantMessages"`
}

type modelRecord = repository.Model

type modelWrite struct {
	ID                     string                       `json:"id"`
	DisplayName            string                       `json:"displayName"`
	SourceType             string                       `json:"sourceType"`
	Protocol               string                       `json:"protocol"`
	Endpoint               *string                      `json:"endpoint"`
	UpstreamModel          *string                      `json:"upstreamModel"`
	LocalModelRef          *string                      `json:"localModelRef"`
	CredentialID           *string                      `json:"credentialId"`
	Capabilities           *[]string                    `json:"capabilities"`
	ReasoningCompatibility *modelReasoningCompatibility `json:"reasoningCompatibility"`
	ContextWindow          *int32                       `json:"contextWindow"`
	IsDefault              *bool                        `json:"isDefault"`
	Enabled                *bool                        `json:"enabled"`
}

type nullableStringPatch struct {
	Set   bool
	Value *string
}

type nullableReasoningCompatibilityPatch struct {
	Set   bool
	Value *modelReasoningCompatibility
}

func (value *nullableReasoningCompatibilityPatch) UnmarshalJSON(data []byte) error {
	value.Set = true
	if bytes.Equal(data, []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded modelReasoningCompatibility
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

func (value *nullableStringPatch) UnmarshalJSON(data []byte) error {
	value.Set = true
	if bytes.Equal(data, []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

type modelPatch struct {
	DisplayName            *string                             `json:"displayName"`
	Endpoint               *string                             `json:"endpoint"`
	UpstreamModel          *string                             `json:"upstreamModel"`
	LocalModelRef          *string                             `json:"localModelRef"`
	CredentialID           nullableStringPatch                 `json:"credentialId"`
	Capabilities           *[]string                           `json:"capabilities"`
	ReasoningCompatibility nullableReasoningCompatibilityPatch `json:"reasoningCompatibility"`
	ContextWindow          *int32                              `json:"contextWindow"`
	IsDefault              *bool                               `json:"isDefault"`
	Enabled                *bool                               `json:"enabled"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanModel(row rowScanner) (modelRecord, error) {
	var model modelRecord
	err := row.Scan(
		&model.ID, &model.DisplayName, &model.SourceType, &model.Protocol,
		&model.Endpoint, &model.UpstreamModel, &model.LocalModelRef, &model.CredentialID,
		&model.Capabilities, &model.ReasoningCompatibility, &model.ContextWindow, &model.IsDefault, &model.Enabled,
		&model.CreatedAt, &model.UpdatedAt,
	)
	return model, err
}

func modelJSON(model modelRecord, admin bool) map[string]any {
	value := map[string]any{
		"id": model.ID, "displayName": model.DisplayName, "sourceType": model.SourceType,
		"protocol": model.Protocol, "capabilities": model.Capabilities,
		"isDefault": model.IsDefault, "enabled": model.Enabled,
	}
	if model.Endpoint.Valid {
		value["endpoint"] = model.Endpoint.String
	}
	if model.UpstreamModel.Valid {
		value["upstreamModel"] = model.UpstreamModel.String
	}
	if model.LocalModelRef.Valid {
		value["localModelRef"] = model.LocalModelRef.String
	}
	if model.ContextWindow.Valid {
		value["contextWindow"] = model.ContextWindow.Int32
	}
	if len(model.ReasoningCompatibility) > 0 {
		var compatibility modelReasoningCompatibility
		if json.Unmarshal(model.ReasoningCompatibility, &compatibility) == nil {
			value["reasoningCompatibility"] = compatibility
		}
	}
	if admin {
		value["credentialId"] = nil
		if model.CredentialID.Valid {
			value["credentialId"] = model.CredentialID.String
		}
	}
	return value
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt32(value *int32) any {
	if value == nil {
		return nil
	}
	return *value
}

func normalizeCapabilities(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(set))
	for value := range set {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func validModelWrite(input modelWrite) bool {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.DisplayName) == "" || input.Protocol != "openai-compatible" || input.Capabilities == nil || input.IsDefault == nil || input.Enabled == nil {
		return false
	}
	if input.SourceType != "gateway" && input.SourceType != "enterprise_open_source" && input.SourceType != "local" {
		return false
	}
	return (input.ContextWindow == nil || *input.ContextWindow > 0) && validReasoningCompatibility(input.ReasoningCompatibility)
}

func validReasoningCompatibility(value *modelReasoningCompatibility) bool {
	return value == nil || value.ThinkingFormat == "deepseek" && value.SupportsReasoningEffort && value.RequiresReasoningContentOnAssistantMessages
}

func (s *Server) listAgentModels(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	scopes, err := s.app.ModelScopes(request.Context(), claims.DeploymentID, claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	models, err := s.app.Store.Deployment(claims.DeploymentID).ListEnabledModelsByIDs(request.Context(), scopes)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(models))
	for _, model := range models {
		items = append(items, modelJSON(model, false))
	}
	writeJSON(response, http.StatusOK, map[string]any{"models": items})
}

func (s *Server) listModels(response http.ResponseWriter, request *http.Request) {
	pageSize := limit(request)
	models, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListModelsPage(request.Context(), request.URL.Query().Get("cursor"), pageSize+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	models, nextCursor := page(models, pageSize, func(item repository.Model) string { return item.ID })
	items := make([]map[string]any, 0, len(models))
	for _, model := range models {
		items = append(items, modelJSON(model, true))
	}
	writeJSON(response, http.StatusOK, map[string]any{"models": items, "nextCursor": nextCursor})
}

func (s *Server) createModel(response http.ResponseWriter, request *http.Request) {
	var input modelWrite
	if !decodeJSON(response, request, &input) {
		return
	}
	if !validModelWrite(input) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_MODEL", "The model descriptor is invalid or incomplete.")
		return
	}
	capabilities := normalizeCapabilities(*input.Capabilities)
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	tenant := claimsFrom(request).DeploymentID
	if exists, err := validateCredentialReference(request.Context(), tx, tenant, input.CredentialID); err != nil {
		databaseFailure(response, request, err)
		return
	} else if !exists {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The referenced credential was not found.")
		return
	}
	if *input.IsDefault {
		if _, err := tx.Exec(request.Context(), "UPDATE models SET is_default=false,updated_at=now() WHERE deployment_id=$1 AND is_default=true", tenant); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	model, err := scanModel(tx.QueryRow(request.Context(), `INSERT INTO models (deployment_id,id,display_name,source_type,protocol,endpoint,upstream_model,local_model_ref,credential_id,capabilities,reasoning_compatibility,context_window,is_default,enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+modelColumns,
		tenant, input.ID, input.DisplayName, input.SourceType, input.Protocol, optionalString(input.Endpoint), optionalString(input.UpstreamModel), optionalString(input.LocalModelRef), optionalString(input.CredentialID), capabilities, input.ReasoningCompatibility, optionalInt32(input.ContextWindow), *input.IsDefault, *input.Enabled))
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "MODEL_EXISTS", "A model with this identifier already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, modelJSON(model, true))
}

func (s *Server) getModel(response http.ResponseWriter, request *http.Request) {
	model, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		GetModel(request.Context(), chi.URLParam(request, "modelId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, modelJSON(model, true))
}

func (input modelPatch) hasChanges() bool {
	return input.DisplayName != nil || input.Endpoint != nil || input.UpstreamModel != nil || input.LocalModelRef != nil || input.CredentialID.Set || input.Capabilities != nil || input.ReasoningCompatibility.Set || input.ContextWindow != nil || input.IsDefault != nil || input.Enabled != nil
}

func appendModelUpdate(sets *[]string, arguments *[]any, column string, value any) {
	*arguments = append(*arguments, value)
	*sets = append(*sets, fmt.Sprintf("%s=$%d", column, len(*arguments)))
}

func (s *Server) updateModel(response http.ResponseWriter, request *http.Request) {
	var input modelPatch
	if !decodeJSON(response, request, &input) {
		return
	}
	if !input.hasChanges() || input.DisplayName != nil && strings.TrimSpace(*input.DisplayName) == "" || input.ContextWindow != nil && *input.ContextWindow <= 0 || input.ReasoningCompatibility.Set && !validReasoningCompatibility(input.ReasoningCompatibility.Value) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_MODEL", "At least one valid model field is required.")
		return
	}
	tenant, modelID := claimsFrom(request).DeploymentID, chi.URLParam(request, "modelId")
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	if input.CredentialID.Set {
		if exists, err := validateCredentialReference(request.Context(), tx, tenant, input.CredentialID.Value); err != nil {
			databaseFailure(response, request, err)
			return
		} else if !exists {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The referenced credential was not found.")
			return
		}
	}
	if input.IsDefault != nil && *input.IsDefault {
		if _, err := tx.Exec(request.Context(), "UPDATE models SET is_default=false,updated_at=now() WHERE deployment_id=$1 AND id<>$2 AND is_default=true", tenant, modelID); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	sets := []string{"updated_at=now()"}
	arguments := []any{tenant, modelID}
	if input.DisplayName != nil {
		appendModelUpdate(&sets, &arguments, "display_name", *input.DisplayName)
	}
	if input.Endpoint != nil {
		appendModelUpdate(&sets, &arguments, "endpoint", *input.Endpoint)
	}
	if input.UpstreamModel != nil {
		appendModelUpdate(&sets, &arguments, "upstream_model", *input.UpstreamModel)
	}
	if input.LocalModelRef != nil {
		appendModelUpdate(&sets, &arguments, "local_model_ref", *input.LocalModelRef)
	}
	if input.CredentialID.Set {
		appendModelUpdate(&sets, &arguments, "credential_id", optionalString(input.CredentialID.Value))
	}
	if input.Capabilities != nil {
		appendModelUpdate(&sets, &arguments, "capabilities", normalizeCapabilities(*input.Capabilities))
	}
	if input.ReasoningCompatibility.Set {
		appendModelUpdate(&sets, &arguments, "reasoning_compatibility", input.ReasoningCompatibility.Value)
	}
	if input.ContextWindow != nil {
		appendModelUpdate(&sets, &arguments, "context_window", *input.ContextWindow)
	}
	if input.IsDefault != nil {
		appendModelUpdate(&sets, &arguments, "is_default", *input.IsDefault)
	}
	if input.Enabled != nil {
		appendModelUpdate(&sets, &arguments, "enabled", *input.Enabled)
	}
	query := "UPDATE models SET " + strings.Join(sets, ",") + " WHERE deployment_id=$1 AND id=$2 RETURNING " + modelColumns
	model, err := scanModel(tx.QueryRow(request.Context(), query, arguments...))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, modelJSON(model, true))
}

func (s *Server) deleteModel(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		DeleteModel(request.Context(), chi.URLParam(request, "modelId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func validModelSubject(subjectType string) bool {
	return subjectType == "user" || subjectType == "role" || subjectType == "team"
}

func (s *Server) listModelAssignments(response http.ResponseWriter, request *http.Request) {
	items, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListModelAssignments(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	assignments := make([]map[string]any, 0, len(items))
	for _, item := range items {
		assignments = append(assignments, map[string]any{
			"id": item.ID, "resourceType": "model", "resourceId": item.ModelID,
			"subject": map[string]string{"type": item.SubjectType, "id": item.SubjectID}, "createdAt": item.CreatedAt,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"assignments": assignments})
}

func (s *Server) createModelAssignment(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ModelID string `json:"modelId"`
		Subject struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"subject"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.ModelID == "" || input.Subject.ID == "" || !validModelSubject(input.Subject.Type) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_SUBJECT", "The model and a valid assignment subject are required.")
		return
	}
	tenant := claimsFrom(request).DeploymentID
	exists, err := s.app.Store.Deployment(tenant).HasModel(request.Context(), input.ModelID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if !exists {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
		return
	}
	id := uuid.NewString()
	assignment, err := s.app.Store.Deployment(tenant).CreateModelAssignment(request.Context(), repository.ModelAssignment{
		ID: id, ModelID: input.ModelID, SubjectType: input.Subject.Type, SubjectID: input.Subject.ID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(response, request, http.StatusConflict, "ASSIGNMENT_EXISTS", "The model assignment already exists.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"id": id, "resourceType": "model", "resourceId": input.ModelID,
		"subject": input.Subject, "createdAt": assignment.CreatedAt,
	})
}

func (s *Server) deleteModelAssignment(response http.ResponseWriter, request *http.Request) {
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		DeleteModelAssignment(request.Context(), chi.URLParam(request, "assignmentId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model assignment was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
