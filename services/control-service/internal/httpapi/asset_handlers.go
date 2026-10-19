package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/assettypes"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// Asset administration (FR-AS-01/02): catalog CRUD, bindings, and the
// built-in type registry. Assets reference credentials by ID only; nothing
// in this file reads or writes credential material.

const (
	assetStatusDraft    = "draft"
	assetStatusActive   = "active"
	assetStatusDisabled = "disabled"
)

type assetUndertakerInput struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type assetCreate struct {
	TypeID               string                    `json:"typeId"`
	Name                 string                    `json:"name"`
	Description          *string                   `json:"description"`
	OrgTeamID            *string                   `json:"orgTeamId"`
	LocationID           *string                   `json:"locationId"`
	OwnerUserID          *string                   `json:"ownerUserId"`
	Undertakers          []assetUndertakerInput    `json:"undertakers"`
	Spec                 map[string]any            `json:"spec"`
	CredentialRefs       map[string]any            `json:"credentialRefs"`
	CapabilitiesOverride map[string]map[string]any `json:"capabilitiesOverride"`
	Status               *string                   `json:"status"`
}

type assetPatch struct {
	Name                 *string                   `json:"name"`
	Description          *string                   `json:"description"`
	OrgTeamID            *string                   `json:"orgTeamId"`
	LocationID           *string                   `json:"locationId"`
	OwnerUserID          *string                   `json:"ownerUserId"`
	Undertakers          []assetUndertakerInput    `json:"undertakers"`
	Spec                 map[string]any            `json:"spec"`
	CredentialRefs       map[string]any            `json:"credentialRefs"`
	CapabilitiesOverride map[string]map[string]any `json:"capabilitiesOverride"`
	Status               *string                   `json:"status"`
}

type assetBindingCreate struct {
	Subject             assetSubjectInput `json:"subject"`
	CapabilityAllowlist []string          `json:"capabilityAllowlist"`
	ExpiresAt           *time.Time        `json:"expiresAt"`
}

type assetSubjectInput struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (input assetPatch) hasChanges() bool {
	return input.Name != nil || input.Description != nil || input.OrgTeamID != nil ||
		input.LocationID != nil || input.OwnerUserID != nil || input.Undertakers != nil ||
		input.Spec != nil || input.CredentialRefs != nil || input.CapabilitiesOverride != nil ||
		input.Status != nil
}

func validAssetStatus(value string) bool {
	return value == assetStatusDraft || value == assetStatusActive || value == assetStatusDisabled
}

func validAssetUndertakerType(value string) bool {
	return value == "de_employee" || value == "companion"
}

func validAssetSubjectType(value string) bool {
	return value == "de_employee" || value == "companion" || value == "user" || value == "team"
}

func (s *Server) listAssetTypes(response http.ResponseWriter, request *http.Request) {
	definitions := assettypes.List()
	items := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		capabilities := make([]map[string]any, 0, len(definition.Capabilities))
		for _, capability := range definition.Capabilities {
			capabilities = append(capabilities, map[string]any{
				"key": capability.Key, "kind": capability.Kind, "risk": capability.Risk,
				"level": capability.Level, "connector": capability.Connector, "description": capability.Description,
			})
		}
		items = append(items, map[string]any{
			"id": definition.ID, "category": definition.Category, "displayName": definition.DisplayName,
			"description": definition.Description, "requiresCredential": definition.RequiresCredential,
			"capabilities": capabilities,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"types": items})
}

func (s *Server) listAssets(response http.ResponseWriter, request *http.Request) {
	pageSize := limit(request)
	records, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListAssetsPage(
		request.Context(), request.URL.Query().Get("cursor"), pageSize+1,
		repository.AssetFilters{TypeID: request.URL.Query().Get("typeId"), Status: request.URL.Query().Get("status")},
	)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	records, nextCursor := page(records, pageSize, func(item repository.Asset) string { return item.ID })
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, assetJSON(record))
	}
	writeJSON(response, http.StatusOK, map[string]any{"assets": items, "nextCursor": nextCursor})
}

func (s *Server) createAsset(response http.ResponseWriter, request *http.Request) {
	var input assetCreate
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.TypeID = strings.TrimSpace(input.TypeID)
	if input.Name == "" || len(input.Name) > 128 || input.TypeID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "A non-empty typeId and name (max 128 characters) are required.")
		return
	}
	definition, ok := assettypes.Get(input.TypeID)
	if !ok {
		writeProblem(response, request, http.StatusBadRequest, "UNKNOWN_ASSET_TYPE", "The asset type is not registered.")
		return
	}
	status := assetStatusDraft
	if input.Status != nil {
		status = strings.TrimSpace(*input.Status)
	}
	if !validAssetStatus(status) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "The asset status must be draft, active, or disabled.")
		return
	}
	if err := assettypes.ValidateSpec(input.TypeID, input.Spec); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_SPEC", err.Error())
		return
	}
	undertakers, problem := normalizeUndertakers(input.Undertakers)
	if problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", problem)
		return
	}
	if invalid := s.validateAssetCredentialRefs(response, request, input.CredentialRefs); invalid {
		return
	}
	if problem := validateCapabilitiesOverride(input.TypeID, input.CapabilitiesOverride); problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", problem)
		return
	}
	if status == assetStatusActive && definition.RequiresCredential && credentialRefDefault(input.CredentialRefs) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "This asset type requires a default credential reference before it can become active.")
		return
	}
	claims := claimsFrom(request)
	record := repository.Asset{
		ID: uuid.NewString(), TypeID: input.TypeID, Name: input.Name,
		Description: toPgText(input.Description), OrgTeamID: toPgText(input.OrgTeamID),
		LocationID: toPgText(input.LocationID), OwnerUserID: toPgText(input.OwnerUserID),
		Undertakers:          undertakers,
		Spec:                 toJSONObject(input.Spec),
		CredentialRefs:       toJSONObject(input.CredentialRefs),
		CapabilitiesOverride: toJSONObjectOfOverride(input.CapabilitiesOverride),
		Status:               status, Source: "manual",
	}
	err := s.app.Store.Deployment(claims.DeploymentID).WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		created, err := tx.CreateAsset(request.Context(), record)
		if err != nil {
			return err
		}
		record = created
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "create", ResourceType: "asset", ResourceID: created.ID, Result: "success",
			Changes: auditAssetSnapshot(created),
		})
	})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, assetJSON(record))
}

func (s *Server) getAsset(response http.ResponseWriter, request *http.Request) {
	record, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).
		GetAsset(request.Context(), chi.URLParam(request, "assetId"))
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, assetJSON(record))
}

func (s *Server) updateAsset(response http.ResponseWriter, request *http.Request) {
	var input assetPatch
	if !decodeJSON(response, request, &input) {
		return
	}
	if !input.hasChanges() {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "At least one asset field is required.")
		return
	}
	claims := claimsFrom(request)
	assetID := chi.URLParam(request, "assetId")
	store := s.app.Store.Deployment(claims.DeploymentID)
	before, err := store.GetAsset(request.Context(), assetID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	definition, typeKnown := assettypes.Get(before.TypeID)
	if input.Spec != nil {
		if err := assettypes.ValidateSpec(before.TypeID, input.Spec); err != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_SPEC", err.Error())
			return
		}
	}
	if invalid := s.validateAssetCredentialRefs(response, request, input.CredentialRefs); invalid {
		return
	}
	if problem := validateCapabilitiesOverride(before.TypeID, input.CapabilitiesOverride); problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", problem)
		return
	}
	undertakers, problem := normalizeUndertakers(input.Undertakers)
	if problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", problem)
		return
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" || len(value) > 128 {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "The asset name must be 1 to 128 characters.")
			return
		}
		input.Name = &value
	}
	if input.Status != nil && !validAssetStatus(*input.Status) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "The asset status must be draft, active, or disabled.")
		return
	}
	// Activating a credential-requiring type needs a default credential,
	// whether it arrives in this request or is already stored.
	if input.Status != nil && *input.Status == assetStatusActive && typeKnown && definition.RequiresCredential {
		refs := input.CredentialRefs
		if refs == nil {
			refs = map[string]any(before.CredentialRefs)
		}
		if credentialRefDefault(refs) == "" {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "This asset type requires a default credential reference before it can become active.")
			return
		}
	}
	var record repository.Asset
	err = store.WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		updated, err := tx.UpdateAsset(request.Context(), assetID, repository.UpdateAssetParams{
			Name: input.Name, Description: pgTextPtr(input.Description), OrgTeamID: pgTextPtr(input.OrgTeamID),
			LocationID: pgTextPtr(input.LocationID), OwnerUserID: pgTextPtr(input.OwnerUserID),
			Undertakers:          &undertakers,
			Spec:                 jsonObjectPtr(toJSONObject(input.Spec)),
			CredentialRefs:       jsonObjectPtr(toJSONObject(input.CredentialRefs)),
			CapabilitiesOverride: jsonObjectPtr(toJSONObjectOfOverride(input.CapabilitiesOverride)),
			Status:               input.Status,
		})
		if err != nil {
			return err
		}
		record = updated
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "update", ResourceType: "asset", ResourceID: assetID, Result: "success",
			Changes: auditAssetChanges(before, updated),
		})
	})
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, assetJSON(record))
}

func (s *Server) deleteAsset(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	assetID := chi.URLParam(request, "assetId")
	err := s.app.Store.Deployment(claims.DeploymentID).WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		before, err := tx.GetAsset(request.Context(), assetID)
		if err != nil {
			return err
		}
		if err := tx.DeleteAsset(request.Context(), assetID); err != nil {
			return err
		}
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "delete", ResourceType: "asset", ResourceID: assetID, Result: "success",
			Changes: auditAssetSnapshot(before),
		})
	})
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAssetBindings(response http.ResponseWriter, request *http.Request) {
	pageSize := limit(request)
	records, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).ListAssetBindingsPage(
		request.Context(), chi.URLParam(request, "assetId"), request.URL.Query().Get("cursor"), pageSize+1)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	records, nextCursor := page(records, pageSize, func(item repository.AssetBinding) string { return item.ID })
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, assetBindingJSON(record))
	}
	writeJSON(response, http.StatusOK, map[string]any{"bindings": items, "nextCursor": nextCursor})
}

func (s *Server) createAssetBinding(response http.ResponseWriter, request *http.Request) {
	var input assetBindingCreate
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Subject.Type = strings.TrimSpace(input.Subject.Type)
	input.Subject.ID = strings.TrimSpace(input.Subject.ID)
	if !validAssetSubjectType(input.Subject.Type) || input.Subject.ID == "" || len(input.Subject.ID) > 128 {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_BINDING", "A subject type (de_employee, companion, user, or team) and non-empty subject id are required.")
		return
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_BINDING", "The binding expiry must be in the future.")
		return
	}
	claims := claimsFrom(request)
	store := s.app.Store.Deployment(claims.DeploymentID)
	assetID := chi.URLParam(request, "assetId")
	asset, err := store.GetAsset(request.Context(), assetID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if problem := validateCapabilityAllowlist(asset.TypeID, input.CapabilityAllowlist); problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_BINDING", problem)
		return
	}
	var record repository.AssetBinding
	err = store.WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		created, err := tx.CreateAssetBinding(request.Context(), repository.AssetBinding{
			ID: uuid.NewString(), AssetID: assetID,
			SubjectType: input.Subject.Type, SubjectID: input.Subject.ID,
			CapabilityAllowlist: toStringArray(input.CapabilityAllowlist),
			GrantedBy:          claims.Subject, ExpiresAt: input.ExpiresAt,
		})
		if err != nil {
			return err
		}
		record = created
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "create", ResourceType: "asset_binding", ResourceID: created.ID, Result: "success",
			Changes: auditAssetBindingSnapshot(created),
		})
	})
	if isUniqueViolation(err) {
		writeProblem(response, request, http.StatusConflict, "ASSET_BINDING_EXISTS", "The subject already has a binding on this asset.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusCreated, assetBindingJSON(record))
}

func (s *Server) deleteAssetBinding(response http.ResponseWriter, request *http.Request) {
	claims := claimsFrom(request)
	store := s.app.Store.Deployment(claims.DeploymentID)
	bindingID := chi.URLParam(request, "bindingId")
	// The route is nested under the asset so the caller states the owner;
	// a mismatched assetId is a not-found rather than a cross-asset delete.
	before, err := store.GetAssetBinding(request.Context(), bindingID)
	if errors.Is(err, repository.ErrNotFound) || err == nil && before.AssetID != chi.URLParam(request, "assetId") {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset binding was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	err = store.WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		if err := tx.DeleteAssetBinding(request.Context(), bindingID); err != nil {
			return err
		}
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "delete", ResourceType: "asset_binding", ResourceID: bindingID, Result: "success",
			Changes: auditAssetBindingSnapshot(before),
		})
	})
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset binding was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// validateAssetCredentialRefs writes a problem and reports true when the
// credential reference object is malformed or points at a credential that is
// missing, disabled, or not server-only. A nil map passes: assets can be
// drafted before a credential exists.
func (s *Server) validateAssetCredentialRefs(response http.ResponseWriter, request *http.Request, refs map[string]any) bool {
	if refs == nil {
		return false
	}
	for field := range refs {
		if field != "default" {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "credentialRefs only supports the \"default\" field.")
			return true
		}
	}
	reference, ok := refs["default"]
	if !ok || reference == nil || reference == "" {
		return false
	}
	id, ok := reference.(string)
	if !ok || strings.TrimSpace(id) == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "credentialRefs.default must be a credential id.")
		return true
	}
	credential, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GetCredential(request.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL_REF", "The referenced credential does not exist.")
		return true
	}
	if err != nil {
		databaseFailure(response, request, err)
		return true
	}
	if !credential.Enabled || credential.DeliveryMode != "server_only" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL_REF", "The referenced credential must be enabled and server-only.")
		return true
	}
	return false
}

func credentialRefDefault(refs map[string]any) string {
	if refs == nil {
		return ""
	}
	if id, ok := refs["default"].(string); ok {
		return id
	}
	return ""
}

func normalizeUndertakers(inputs []assetUndertakerInput) (repository.JSONArray, string) {
	if inputs == nil {
		return repository.JSONArray{}, ""
	}
	if len(inputs) > 16 {
		return nil, "At most 16 undertakers are allowed."
	}
	seen := make(map[string]struct{}, len(inputs))
	items := make([]any, 0, len(inputs))
	for _, input := range inputs {
		kind := strings.TrimSpace(input.Type)
		id := strings.TrimSpace(input.ID)
		if !validAssetUndertakerType(kind) || id == "" || len(id) > 128 {
			return nil, "Each undertaker needs a type (de_employee or companion) and a non-empty id."
		}
		key := kind + "\x00" + id
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, map[string]any{"type": kind, "id": id})
	}
	return repository.JSONArray(items), ""
}

func validateCapabilitiesOverride(typeID string, overrides map[string]map[string]any) string {
	if overrides == nil {
		return ""
	}
	if len(overrides) > 32 {
		return "At most 32 capability overrides are allowed."
	}
	for key, override := range overrides {
		if !assettypes.HasCapability(typeID, key) {
			return "Capability " + key + " is not defined by the asset type."
		}
		for field, value := range override {
			switch field {
			case "enabled":
				if _, ok := value.(bool); !ok {
					return "Capability override " + key + ".enabled must be a boolean."
				}
			case "risk":
				risk, ok := value.(string)
				if !ok || risk != assettypes.RiskLow && risk != assettypes.RiskMedium && risk != assettypes.RiskHigh && risk != assettypes.RiskCritical {
					return "Capability override " + key + ".risk must be low, medium, high, or critical."
				}
			case "level":
				level, ok := value.(string)
				if !ok || level < assettypes.LevelStandardInterface || level > assettypes.LevelHumanFallback {
					return "Capability override " + key + ".level must be one of L1 to L4."
				}
			default:
				return "Capability override " + key + " has an unsupported field " + field + "."
			}
		}
	}
	return ""
}

func validateCapabilityAllowlist(typeID string, allowlist []string) string {
	if allowlist == nil {
		return ""
	}
	if len(allowlist) > 32 {
		return "At most 32 capabilities may be allowlisted."
	}
	seen := make(map[string]struct{}, len(allowlist))
	for _, capability := range allowlist {
		capability = strings.TrimSpace(capability)
		if !assettypes.HasCapability(typeID, capability) {
			return "Capability " + capability + " is not defined by the asset type."
		}
		if _, duplicate := seen[capability]; duplicate {
			return "Capability " + capability + " appears twice in the allowlist."
		}
		seen[capability] = struct{}{}
	}
	return ""
}

// effectiveAssetCapabilities merges the type templates with the per-asset
// overrides: disabled capabilities drop out, risk and level can be tightened
// or relaxed per asset.
func effectiveAssetCapabilities(record repository.Asset) []map[string]any {
	definition, ok := assettypes.Get(record.TypeID)
	if !ok {
		return []map[string]any{}
	}
	capabilities := make([]map[string]any, 0, len(definition.Capabilities))
	for _, template := range definition.Capabilities {
		out := map[string]any{
			"key": template.Key, "kind": template.Kind, "risk": template.Risk,
			"level": template.Level, "connector": template.Connector, "description": template.Description,
		}
		if raw, present := record.CapabilitiesOverride[template.Key]; present {
			override, _ := raw.(map[string]any)
			if enabled, ok := override["enabled"].(bool); ok && !enabled {
				continue
			}
			if risk, ok := override["risk"].(string); ok {
				out["risk"] = risk
			}
			if level, ok := override["level"].(string); ok {
				out["level"] = level
			}
		}
		capabilities = append(capabilities, out)
	}
	return capabilities
}

func assetJSON(record repository.Asset) map[string]any {
	var health any
	if record.HealthState.Valid {
		health = map[string]any{
			"state": record.HealthState.String, "checkedAt": record.HealthCheckedAt, "detail": textOrNull(record.HealthDetail),
		}
	}
	return map[string]any{
		"id": record.ID, "typeId": record.TypeID, "name": record.Name,
		"description": textOrNull(record.Description), "orgTeamId": textOrNull(record.OrgTeamID),
		"locationId": textOrNull(record.LocationID), "ownerUserId": textOrNull(record.OwnerUserID),
		"undertakers": record.Undertakers, "spec": record.Spec, "credentialRefs": record.CredentialRefs,
		"capabilitiesOverride": record.CapabilitiesOverride, "capabilities": effectiveAssetCapabilities(record),
		"status": record.Status, "health": health, "source": record.Source,
		"createdAt": record.CreatedAt, "updatedAt": record.UpdatedAt,
	}
}

func assetBindingJSON(record repository.AssetBinding) map[string]any {
	var allowlist any
	if record.CapabilityAllowlist != nil {
		allowlist = record.CapabilityAllowlist
	}
	return map[string]any{
		"id": record.ID, "assetId": record.AssetID,
		"subject": map[string]any{"type": record.SubjectType, "id": record.SubjectID},
		"capabilityAllowlist": allowlist, "grantedBy": record.GrantedBy,
		"expiresAt": record.ExpiresAt, "createdAt": record.CreatedAt,
	}
}

func auditAssetSnapshot(record repository.Asset) map[string]any {
	return map[string]any{
		"typeId": record.TypeID, "name": record.Name, "status": record.Status,
		"orgTeamId": textOrNull(record.OrgTeamID), "ownerUserId": textOrNull(record.OwnerUserID),
		"credentialDefault": credentialRefDefault(map[string]any(record.CredentialRefs)),
	}
}

func auditAssetChanges(before, after repository.Asset) map[string]any {
	return auditChanges(auditAssetSnapshot(before), auditAssetSnapshot(after))
}

func auditAssetBindingSnapshot(record repository.AssetBinding) map[string]any {
	return map[string]any{
		"assetId": record.AssetID, "subjectType": record.SubjectType, "subjectId": record.SubjectID,
		"expiresAt": record.ExpiresAt,
	}
}

func toPgText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: trimmed, Valid: true}
}

func textOrNull(value pgtype.Text) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func pgTextPtr(value *string) *pgtype.Text {
	if value == nil {
		return nil
	}
	text := toPgText(value)
	return &text
}

func jsonObjectPtr(value repository.JSONObject) *repository.JSONObject {
	if value == nil {
		return nil
	}
	return &value
}

func toJSONObject(value map[string]any) repository.JSONObject {
	if value == nil {
		return nil
	}
	return repository.JSONObject(value)
}

func toJSONObjectOfOverride(value map[string]map[string]any) repository.JSONObject {
	if value == nil {
		return nil
	}
	converted := make(map[string]any, len(value))
	for key, override := range value {
		converted[key] = override
	}
	return repository.JSONObject(converted)
}

func toStringArray(values []string) repository.JSONArray {
	if values == nil {
		return nil
	}
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	return repository.JSONArray(items)
}
