package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const modelColumns = `id,display_name,source_type,protocol,endpoint,upstream_model,local_model_ref,credential_id,capabilities,context_window,is_default,enabled,created_at,updated_at`

type modelRecord struct {
	ID            string
	DisplayName   string
	SourceType    string
	Protocol      string
	Endpoint      pgtype.Text
	UpstreamModel pgtype.Text
	LocalModelRef pgtype.Text
	CredentialID  pgtype.Text
	Capabilities  []string
	ContextWindow pgtype.Int4
	IsDefault     bool
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type modelWrite struct {
	ID            string    `json:"id"`
	DisplayName   string    `json:"displayName"`
	SourceType    string    `json:"sourceType"`
	Protocol      string    `json:"protocol"`
	Endpoint      *string   `json:"endpoint"`
	UpstreamModel *string   `json:"upstreamModel"`
	LocalModelRef *string   `json:"localModelRef"`
	CredentialID  *string   `json:"credentialId"`
	Capabilities  *[]string `json:"capabilities"`
	ContextWindow *int32    `json:"contextWindow"`
	IsDefault     *bool     `json:"isDefault"`
	Enabled       *bool     `json:"enabled"`
}

type nullableStringPatch struct {
	Set   bool
	Value *string
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
	DisplayName   *string             `json:"displayName"`
	Endpoint      *string             `json:"endpoint"`
	UpstreamModel *string             `json:"upstreamModel"`
	LocalModelRef *string             `json:"localModelRef"`
	CredentialID  nullableStringPatch `json:"credentialId"`
	Capabilities  *[]string           `json:"capabilities"`
	ContextWindow *int32              `json:"contextWindow"`
	IsDefault     *bool               `json:"isDefault"`
	Enabled       *bool               `json:"enabled"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanModel(row rowScanner) (modelRecord, error) {
	var model modelRecord
	err := row.Scan(
		&model.ID, &model.DisplayName, &model.SourceType, &model.Protocol,
		&model.Endpoint, &model.UpstreamModel, &model.LocalModelRef, &model.CredentialID,
		&model.Capabilities, &model.ContextWindow, &model.IsDefault, &model.Enabled,
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
	return input.ContextWindow == nil || *input.ContextWindow > 0
}

func (s *Server) listAgentModels(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	scopes, err := s.app.ModelScopes(request.Context(), claims.Tenant, claims.Subject, claims.AgentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	rows, err := s.app.Pool.Query(request.Context(), "SELECT "+modelColumns+" FROM models WHERE enterprise_id=$1 AND enabled=true AND id=ANY($2::text[]) ORDER BY is_default DESC,id", claims.Tenant, scopes)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	models := make([]map[string]any, 0)
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		models = append(models, modelJSON(model, false))
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) listModels(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), "SELECT "+modelColumns+" FROM models WHERE enterprise_id=$1 ORDER BY id", claimsFrom(request).Tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	models := make([]map[string]any, 0)
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		models = append(models, modelJSON(model, true))
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"models": models})
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
	tenant := claimsFrom(request).Tenant
	if *input.IsDefault {
		if _, err := tx.Exec(request.Context(), "UPDATE models SET is_default=false,updated_at=now() WHERE enterprise_id=$1 AND is_default=true", tenant); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	model, err := scanModel(tx.QueryRow(request.Context(), `INSERT INTO models (enterprise_id,id,display_name,source_type,protocol,endpoint,upstream_model,local_model_ref,credential_id,capabilities,context_window,is_default,enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+modelColumns,
		tenant, input.ID, input.DisplayName, input.SourceType, input.Protocol, optionalString(input.Endpoint), optionalString(input.UpstreamModel), optionalString(input.LocalModelRef), optionalString(input.CredentialID), capabilities, optionalInt32(input.ContextWindow), *input.IsDefault, *input.Enabled))
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
	model, err := scanModel(s.app.Pool.QueryRow(request.Context(), "SELECT "+modelColumns+" FROM models WHERE enterprise_id=$1 AND id=$2", claimsFrom(request).Tenant, chi.URLParam(request, "modelId")))
	if errors.Is(err, pgx.ErrNoRows) {
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
	return input.DisplayName != nil || input.Endpoint != nil || input.UpstreamModel != nil || input.LocalModelRef != nil || input.CredentialID.Set || input.Capabilities != nil || input.ContextWindow != nil || input.IsDefault != nil || input.Enabled != nil
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
	if !input.hasChanges() || input.DisplayName != nil && strings.TrimSpace(*input.DisplayName) == "" || input.ContextWindow != nil && *input.ContextWindow <= 0 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_MODEL", "At least one valid model field is required.")
		return
	}
	tenant, modelID := claimsFrom(request).Tenant, chi.URLParam(request, "modelId")
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	if input.IsDefault != nil && *input.IsDefault {
		if _, err := tx.Exec(request.Context(), "UPDATE models SET is_default=false,updated_at=now() WHERE enterprise_id=$1 AND id<>$2 AND is_default=true", tenant, modelID); err != nil {
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
	if input.ContextWindow != nil {
		appendModelUpdate(&sets, &arguments, "context_window", *input.ContextWindow)
	}
	if input.IsDefault != nil {
		appendModelUpdate(&sets, &arguments, "is_default", *input.IsDefault)
	}
	if input.Enabled != nil {
		appendModelUpdate(&sets, &arguments, "enabled", *input.Enabled)
	}
	query := "UPDATE models SET " + strings.Join(sets, ",") + " WHERE enterprise_id=$1 AND id=$2 RETURNING " + modelColumns
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
	result, err := s.app.Pool.Exec(request.Context(), "DELETE FROM models WHERE enterprise_id=$1 AND id=$2", claimsFrom(request).Tenant, chi.URLParam(request, "modelId"))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func validModelSubject(subjectType string) bool {
	return subjectType == "enterprise" || subjectType == "organization" || subjectType == "user" || subjectType == "agent"
}

func (s *Server) listModelAssignments(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), "SELECT id,model_id,subject_type,subject_id,created_at FROM model_assignments WHERE enterprise_id=$1 ORDER BY created_at,id", claimsFrom(request).Tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	assignments := make([]map[string]any, 0)
	for rows.Next() {
		var id, modelID, subjectType, subjectID string
		var createdAt time.Time
		if err := rows.Scan(&id, &modelID, &subjectType, &subjectID, &createdAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		assignments = append(assignments, map[string]any{
			"id": id, "resourceType": "model", "resourceId": modelID,
			"subject": map[string]string{"type": subjectType, "id": subjectID}, "createdAt": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
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
	tenant := claimsFrom(request).Tenant
	var exists bool
	if err := s.app.Pool.QueryRow(request.Context(), "SELECT EXISTS (SELECT 1 FROM models WHERE enterprise_id=$1 AND id=$2)", tenant, input.ModelID).Scan(&exists); err != nil {
		databaseFailure(response, request, err)
		return
	}
	if !exists {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
		return
	}
	id := uuid.NewString()
	var createdAt time.Time
	err := s.app.Pool.QueryRow(request.Context(), `INSERT INTO model_assignments (id,enterprise_id,model_id,subject_type,subject_id) VALUES ($1,$2,$3,$4,$5) RETURNING created_at`, id, tenant, input.ModelID, input.Subject.Type, input.Subject.ID).Scan(&createdAt)
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
		"subject": input.Subject, "createdAt": createdAt,
	})
}

func (s *Server) deleteModelAssignment(response http.ResponseWriter, request *http.Request) {
	result, err := s.app.Pool.Exec(request.Context(), "DELETE FROM model_assignments WHERE id=$1 AND enterprise_id=$2", chi.URLParam(request, "assignmentId"), claimsFrom(request).Tenant)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model assignment was not found.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
