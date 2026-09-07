package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

const credentialColumns = `id,name,service,type,delivery_mode,encrypted_value,nonce,key_id,masked_value,enabled,created_at,updated_at,rotated_at`
const credentialColumnsQualified = `c.id,c.name,c.service,c.type,c.delivery_mode,c.encrypted_value,c.nonce,c.key_id,c.masked_value,c.enabled,c.created_at,c.updated_at,c.rotated_at`

type credentialRecord = repository.Credential

type credentialCreate struct {
	Name         string `json:"name"`
	Service      string `json:"service"`
	Type         string `json:"type"`
	DeliveryMode string `json:"deliveryMode"`
	Value        string `json:"value"`
	Enabled      *bool  `json:"enabled"`
}

type credentialPatch struct {
	Name         *string `json:"name"`
	Service      *string `json:"service"`
	DeliveryMode *string `json:"deliveryMode"`
	Enabled      *bool   `json:"enabled"`
}

type credentialAssignmentInput struct {
	CredentialID string `json:"credentialId"`
	Subject      struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"subject"`
}

func scanCredential(row rowScanner) (credentialRecord, error) {
	var record credentialRecord
	err := row.Scan(
		&record.ID, &record.Name, &record.Service, &record.Type, &record.DeliveryMode,
		&record.EncryptedValue, &record.Nonce, &record.KeyID, &record.MaskedValue,
		&record.Enabled, &record.CreatedAt, &record.UpdatedAt, &record.RotatedAt,
	)
	return record, err
}

func credentialJSON(record credentialRecord) map[string]any {
	return map[string]any{
		"id": record.ID, "name": record.Name, "service": record.Service,
		"type": record.Type, "deliveryMode": record.DeliveryMode,
		"maskedValue": record.MaskedValue, "enabled": record.Enabled, "updatedAt": record.UpdatedAt,
	}
}

func (s *Server) requireCredentialService(response http.ResponseWriter, request *http.Request) bool {
	if s.app.Credentials == nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "CREDENTIALS_NOT_CONFIGURED", "Credential management is not configured on this service.")
		return false
	}
	return true
}

func validCredentialValue(value string) bool {
	length := utf8.RuneCountInString(value)
	return length > 0 && length <= 32768
}

func validDeliveryMode(value string) bool {
	return value == "server_only" || value == "client"
}

func validCredentialSubject(value string) bool {
	return value == "user" || value == "role" || value == "team"
}

func (s *Server) listAgentCredentials(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	claims := claimsFrom(request)
	rows, err := s.app.Pool.Query(request.Context(), `SELECT `+credentialColumnsQualified+`
FROM credentials c
JOIN users u ON u.id=$2 AND u.deployment_id=$1
WHERE c.deployment_id=$1 AND c.enabled=true AND c.delivery_mode='client'
AND EXISTS (
  SELECT 1 FROM credential_assignments ca
  WHERE ca.deployment_id=c.deployment_id AND ca.credential_id=c.id AND (
    (ca.subject_type='user' AND ca.subject_id=$2)
    OR (ca.subject_type='role' AND EXISTS (SELECT 1 FROM user_role_bindings urb JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true WHERE urb.deployment_id=$1 AND urb.user_id=u.id AND urb.role_id=ca.subject_id))
    OR (ca.subject_type='team' AND EXISTS (SELECT 1 FROM user_team_bindings utb JOIN teams t ON t.deployment_id=utb.deployment_id AND t.id=utb.team_id AND t.enabled=true WHERE utb.deployment_id=$1 AND utb.user_id=u.id AND utb.team_id=ca.subject_id))
  )
)
ORDER BY c.id`, claims.DeploymentID, claims.Subject)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		record, err := scanCredential(rows)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, credentialJSON(record))
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"credentials": items})
}

func (s *Server) resolveAgentCredential(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	var input struct {
		Purpose string `json:"purpose"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Purpose = strings.TrimSpace(input.Purpose)
	if input.Purpose == "" || utf8.RuneCountInString(input.Purpose) > 500 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_PURPOSE", "A purpose between 1 and 500 characters is required.")
		return
	}
	claims := claimsFrom(request)
	credentialID := chi.URLParam(request, "credentialId")
	tx, err := s.app.Pool.Begin(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	record, err := scanCredential(tx.QueryRow(request.Context(), "SELECT "+credentialColumns+" FROM credentials WHERE deployment_id=$1 AND id=$2 FOR SHARE", claims.DeploymentID, credentialID))
	if errors.Is(err, pgx.ErrNoRows) {
		if !s.writeCredentialResolutionAudit(response, request, tx, credentialID, input.Purpose, "denied", "not_found") {
			return
		}
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	reason := ""
	if !record.Enabled {
		reason = "disabled"
	} else if record.DeliveryMode != "client" {
		reason = "server_only"
	} else {
		var authorized bool
		err = tx.QueryRow(request.Context(), `SELECT EXISTS (
SELECT 1 FROM credential_assignments ca
JOIN users u ON u.id=$2 AND u.deployment_id=$1
WHERE ca.deployment_id=$1 AND ca.credential_id=$3 AND (
  (ca.subject_type='user' AND ca.subject_id=$2)
  OR (ca.subject_type='role' AND EXISTS (SELECT 1 FROM user_role_bindings urb JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true WHERE urb.deployment_id=$1 AND urb.user_id=u.id AND urb.role_id=ca.subject_id))
  OR (ca.subject_type='team' AND EXISTS (SELECT 1 FROM user_team_bindings utb JOIN teams t ON t.deployment_id=utb.deployment_id AND t.id=utb.team_id AND t.enabled=true WHERE utb.deployment_id=$1 AND utb.user_id=u.id AND utb.team_id=ca.subject_id))
))`, claims.DeploymentID, claims.Subject, credentialID).Scan(&authorized)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		if !authorized {
			reason = "not_assigned"
		}
	}
	if reason != "" {
		if !s.writeCredentialResolutionAudit(response, request, tx, credentialID, input.Purpose, "denied", reason) {
			return
		}
		code, detail := "ACCESS_DENIED", "The credential is not assigned to this user."
		if reason == "disabled" {
			code, detail = "CREDENTIAL_DISABLED", "The credential is disabled."
		} else if reason == "server_only" {
			code, detail = "CREDENTIAL_SERVER_ONLY", "The credential is restricted to server-side use."
		}
		writeProblem(response, request, http.StatusForbidden, code, detail)
		return
	}
	plaintext, err := s.app.Credentials.Open(request.Context(), credential.Envelope{
		KeyID: record.KeyID, Nonce: record.Nonce, Ciphertext: record.EncryptedValue,
	}, credential.AssociatedData(claims.DeploymentID, record.ID))
	if err != nil {
		if !s.writeCredentialResolutionAudit(response, request, tx, credentialID, input.Purpose, "denied", "decrypt_failed") {
			return
		}
		writeProblem(response, request, http.StatusInternalServerError, "CREDENTIAL_DECRYPT_FAILED", "The credential could not be resolved.")
		return
	}
	defer func() { clear(plaintext) }()
	if !s.writeCredentialResolutionAudit(response, request, tx, credentialID, input.Purpose, "resolved", "") {
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
	writeJSON(response, http.StatusOK, map[string]any{
		"credentialId": record.ID, "type": record.Type, "value": string(plaintext), "expiresAt": nil,
	})
}

func (s *Server) writeCredentialResolutionAudit(response http.ResponseWriter, request *http.Request, tx pgx.Tx, credentialID, purpose, outcome, reason string) bool {
	claims := claimsFrom(request)
	if _, err := tx.Exec(request.Context(), `INSERT INTO credential_resolution_audit (id,deployment_id,credential_id,user_id,session_id,purpose,outcome,reason) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.NewString(), claims.DeploymentID, credentialID, claims.Subject, claims.SessionID, purpose, outcome, optionalAuditReason(reason)); err != nil {
		databaseFailure(response, request, err)
		return false
	}
	if err := tx.Commit(request.Context()); err != nil {
		databaseFailure(response, request, err)
		return false
	}
	return true
}

func optionalAuditReason(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Server) listCredentials(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	pageSize := limit(request)
	credentials, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListCredentialsPage(request.Context(), request.URL.Query().Get("cursor"), pageSize+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	credentials, nextCursor := page(credentials, pageSize, func(item repository.Credential) string { return item.ID })
	items := make([]map[string]any, 0, len(credentials))
	for _, record := range credentials {
		items = append(items, credentialJSON(record))
	}
	writeJSON(response, http.StatusOK, map[string]any{"credentials": items, "nextCursor": nextCursor})
}

func (s *Server) createCredential(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	var input credentialCreate
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Name, input.Service = strings.TrimSpace(input.Name), strings.TrimSpace(input.Service)
	if input.Name == "" || input.Service == "" || input.Type != "api_key" || !validDeliveryMode(input.DeliveryMode) || !validCredentialValue(input.Value) || input.Enabled == nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL", "The credential descriptor is invalid or incomplete.")
		return
	}
	id, tenant := uuid.NewString(), claimsFrom(request).DeploymentID
	envelope, err := s.app.Credentials.Seal(request.Context(), []byte(input.Value), credential.AssociatedData(tenant, id))
	if err != nil {
		writeProblem(response, request, http.StatusInternalServerError, "CREDENTIAL_ENCRYPT_FAILED", "The credential could not be encrypted.")
		return
	}
	record, err := s.app.Store.Deployment(tenant).CreateCredential(request.Context(), repository.Credential{
		ID: id, Name: input.Name, Service: input.Service, Type: input.Type,
		DeliveryMode: input.DeliveryMode, EncryptedValue: envelope.Ciphertext, Nonce: envelope.Nonce,
		KeyID: envelope.KeyID, MaskedValue: credential.Mask(input.Value), Enabled: *input.Enabled,
	})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, credentialJSON(record))
}

func (s *Server) getCredential(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	record, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		GetCredential(request.Context(), chi.URLParam(request, "credentialId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, credentialJSON(record))
}

func (input credentialPatch) hasChanges() bool {
	return input.Name != nil || input.Service != nil || input.DeliveryMode != nil || input.Enabled != nil
}

func (s *Server) updateCredential(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	var input credentialPatch
	if !decodeJSON(response, request, &input) {
		return
	}
	if !input.hasChanges() || input.Name != nil && strings.TrimSpace(*input.Name) == "" || input.Service != nil && strings.TrimSpace(*input.Service) == "" || input.DeliveryMode != nil && !validDeliveryMode(*input.DeliveryMode) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL", "At least one valid credential field is required.")
		return
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		input.Name = &value
	}
	if input.Service != nil {
		value := strings.TrimSpace(*input.Service)
		input.Service = &value
	}
	record, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).UpdateCredential(
		request.Context(), chi.URLParam(request, "credentialId"), repository.UpdateCredentialParams{
			Name: input.Name, Service: input.Service, DeliveryMode: input.DeliveryMode, Enabled: input.Enabled,
		},
	)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, credentialJSON(record))
}

func (s *Server) rotateCredential(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	var input struct {
		Value string `json:"value"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if !validCredentialValue(input.Value) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL", "A credential value between 1 and 32768 characters is required.")
		return
	}
	tenant, id := claimsFrom(request).DeploymentID, chi.URLParam(request, "credentialId")
	exists, err := s.app.Store.Deployment(tenant).HasCredential(request.Context(), id)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if !exists {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential was not found.")
		return
	}
	envelope, err := s.app.Credentials.Seal(request.Context(), []byte(input.Value), credential.AssociatedData(tenant, id))
	if err != nil {
		writeProblem(response, request, http.StatusInternalServerError, "CREDENTIAL_ENCRYPT_FAILED", "The credential could not be encrypted.")
		return
	}
	record, err := s.app.Store.Deployment(tenant).RotateCredential(
		request.Context(), id, envelope.Ciphertext, envelope.Nonce, envelope.KeyID, credential.Mask(input.Value),
	)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, credentialJSON(record))
}

func (s *Server) deleteCredential(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		DeleteCredential(request.Context(), chi.URLParam(request, "credentialId"))
	if isForeignKeyViolation(err) {
		writeProblem(response, request, http.StatusConflict, "CREDENTIAL_IN_USE", "The credential is referenced by a model and cannot be deleted.")
		return
	}
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func isForeignKeyViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23503"
}

func (s *Server) listCredentialAssignments(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	items, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListCredentialAssignments(request.Context())
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	assignments := make([]map[string]any, 0, len(items))
	for _, item := range items {
		assignments = append(assignments, map[string]any{
			"id": item.ID, "resourceType": "credential", "resourceId": item.CredentialID,
			"subject": map[string]string{"type": item.SubjectType, "id": item.SubjectID}, "createdAt": item.CreatedAt,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"assignments": assignments})
}

func (s *Server) createCredentialAssignment(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	var input credentialAssignmentInput
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.CredentialID == "" || input.Subject.ID == "" || !validCredentialSubject(input.Subject.Type) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_SUBJECT", "The credential and a valid assignment subject are required.")
		return
	}
	tenant := claimsFrom(request).DeploymentID
	exists, err := s.app.Store.Deployment(tenant).HasCredential(request.Context(), input.CredentialID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if !exists {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential was not found.")
		return
	}
	id := uuid.NewString()
	assignment, err := s.app.Store.Deployment(tenant).CreateCredentialAssignment(request.Context(), repository.CredentialAssignment{
		ID: id, CredentialID: input.CredentialID, SubjectType: input.Subject.Type, SubjectID: input.Subject.ID,
	})
	if isUniqueViolation(err) {
		writeProblem(response, request, http.StatusConflict, "ASSIGNMENT_EXISTS", "The credential assignment already exists.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"id": id, "resourceType": "credential", "resourceId": input.CredentialID,
		"subject": input.Subject, "createdAt": assignment.CreatedAt,
	})
}

func (s *Server) deleteCredentialAssignment(response http.ResponseWriter, request *http.Request) {
	if !s.requireCredentialService(response, request) {
		return
	}
	err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		DeleteCredentialAssignment(request.Context(), chi.URLParam(request, "assignmentId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The credential assignment was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type credentialReferenceQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func validateCredentialReference(ctx context.Context, query credentialReferenceQuerier, tenant string, id *string) (bool, error) {
	if id == nil {
		return true, nil
	}
	var exists bool
	err := query.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM credentials WHERE deployment_id=$1 AND id=$2)", tenant, *id).Scan(&exists)
	return exists, err
}
