package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/assetprobe"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// Runtime-facing asset surface (FR-AS-03): discovery for tool registration
// and governed command execution. These endpoints sit on the admin surface
// because the caller is the governance middleware's service account — the
// same trust hop the data-scope context endpoint uses — and every execution
// carries the declared subject and requester into the audit trail.

const maxExecCommandBytes = 8 * 1024

type assetExecuteInput struct {
	Subject         assetSubjectInput `json:"subject"`
	RequesterUserID string            `json:"requesterUserId"`
	Capability      string            `json:"capability"`
	Command         string            `json:"command"`
	TimeoutSeconds  *int              `json:"timeoutSeconds"`
}

// listRuntimeAssets answers "which capabilities may this subject use" for
// tool registration: unexpired bindings on active assets, narrowed by each
// binding's allowlist.
func (s *Server) listRuntimeAssets(response http.ResponseWriter, request *http.Request) {
	subjectType := strings.TrimSpace(request.URL.Query().Get("subjectType"))
	subjectID := strings.TrimSpace(request.URL.Query().Get("subjectId"))
	if !validAssetSubjectType(subjectType) || subjectID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_SUBJECT", "A subjectType (de_employee, companion, user, or team) and subjectId are required.")
		return
	}
	store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
	bindings, err := store.ListAssetBindingsForSubject(request.Context(), subjectType, subjectID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	items := make([]map[string]any, 0, len(bindings))
	for _, binding := range bindings {
		record, err := store.GetAsset(request.Context(), binding.AssetID)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		if record.Status != assetStatusActive {
			continue
		}
		capabilities := effectiveAssetCapabilities(record)
		if binding.CapabilityAllowlist != nil {
			allowed := make(map[string]struct{}, len(binding.CapabilityAllowlist))
			for _, key := range binding.CapabilityAllowlist {
				if key, ok := key.(string); ok {
					allowed[key] = struct{}{}
				}
			}
			filtered := capabilities[:0]
			for _, capability := range capabilities {
				if _, ok := allowed[capability["key"].(string)]; ok {
					filtered = append(filtered, capability)
				}
			}
			capabilities = filtered
		}
		items = append(items, map[string]any{
			"assetId": record.ID, "name": record.Name, "typeId": record.TypeID,
			"capabilities": capabilities, "bindingId": binding.ID, "expiresAt": binding.ExpiresAt,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"assets": items})
}

// executeAsset runs one governed capability invocation. First phase covers
// ssh_server exec only; the plan is per-capability growth in the registry.
func (s *Server) executeAsset(response http.ResponseWriter, request *http.Request) {
	var input assetExecuteInput
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Subject.Type = strings.TrimSpace(input.Subject.Type)
	input.Subject.ID = strings.TrimSpace(input.Subject.ID)
	input.RequesterUserID = strings.TrimSpace(input.RequesterUserID)
	input.Capability = strings.TrimSpace(input.Capability)
	if !validAssetSubjectType(input.Subject.Type) || input.Subject.ID == "" || input.RequesterUserID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_EXECUTION", "A subject, requesterUserId, and command are required.")
		return
	}
	if input.Capability != "exec" {
		writeProblem(response, request, http.StatusBadRequest, "UNSUPPORTED_CAPABILITY", "Only the exec capability is executable in this phase.")
		return
	}
	if input.Command == "" || len(input.Command) > maxExecCommandBytes {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_EXECUTION", "The command must be 1 to 8192 bytes.")
		return
	}
	timeout := 30 * time.Second
	if input.TimeoutSeconds != nil {
		if *input.TimeoutSeconds < 1 || time.Duration(*input.TimeoutSeconds)*time.Second > assetprobe.MaxExecTimeout {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET_EXECUTION", "timeoutSeconds must be between 1 and 120.")
			return
		}
		timeout = time.Duration(*input.TimeoutSeconds) * time.Second
	}

	claims := claimsFrom(request)
	store := s.app.Store.Deployment(claims.DeploymentID)
	assetID := chi.URLParam(request, "assetId")

	// Emergency stop, global first: a deployment-wide halt must hold even
	// when individual assets look healthy.
	halted, err := s.assetExecutionHalted(request, claims.DeploymentID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if halted {
		writeProblem(response, request, http.StatusForbidden, "ASSET_EXECUTION_HALTED", "Asset execution is stopped deployment-wide.")
		return
	}

	record, err := store.GetAsset(request.Context(), assetID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The asset was not found.")
		return
	}
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	if record.Status != assetStatusActive {
		writeProblem(response, request, http.StatusConflict, "ASSET_NOT_ACTIVE", "The asset is not active.")
		return
	}
	bindings, err := store.ListAssetBindingsForSubject(request.Context(), input.Subject.Type, input.Subject.ID)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	bound := false
	for _, binding := range bindings {
		if binding.AssetID != assetID {
			continue
		}
		bound = true
		if binding.CapabilityAllowlist != nil && !allowlistContains(binding.CapabilityAllowlist, input.Capability) {
			writeProblem(response, request, http.StatusForbidden, "CAPABILITY_NOT_ALLOWED", "The binding does not include this capability.")
			return
		}
	}
	if !bound {
		writeProblem(response, request, http.StatusForbidden, "ASSET_NOT_BOUND", "The subject has no binding on this asset.")
		return
	}
	if !capabilityEnabled(record, input.Capability) {
		writeProblem(response, request, http.StatusForbidden, "CAPABILITY_NOT_ALLOWED", "The capability is disabled on this asset.")
		return
	}

	auth, ok := s.resolveSSHAuth(response, request, store, claims.DeploymentID, record)
	if !ok {
		return
	}
	execCtx, cancel := context.WithTimeout(request.Context(), timeout)
	defer cancel()
	started := time.Now()
	execResult, execErr := assetprobe.ExecSSH(execCtx, assetString(record.Spec, "host"), assetNumber(record.Spec, "port"), auth, input.Command)
	duration := time.Since(started)

	auditChanges := map[string]any{
		"capability": input.Capability, "requesterUserId": input.RequesterUserID,
		"subjectType": input.Subject.Type, "subjectId": input.Subject.ID,
		"command": boundCommand(input.Command), "exitCode": execResult.ExitCode,
		"truncated": execResult.Truncated, "durationMs": duration.Milliseconds(),
	}
	auditResult := "success"
	if execErr != nil {
		auditResult = "failed"
		auditChanges["failure"] = execErr.Error()
	}
	auditErr := store.WithTransaction(request.Context(), func(tx *repository.DeploymentStore) error {
		return tx.RecordAdminAudit(request.Context(), repository.AdminAuditEntry{
			ActorUserID: claims.Subject, Action: "execute", ResourceType: "asset", ResourceID: assetID,
			Result: auditResult, Changes: auditChanges,
		})
	})
	if auditErr != nil {
		databaseFailure(response, request, auditErr)
		return
	}
	// The execution audit above is the record of this write; keep the
	// generic middleware row out. Rejections before this point still get
	// the middleware's failure rows.
	markAdminAuditRecorded(request)
	if execErr != nil {
		var classified *assetprobe.ClassifiedError
		if errors.As(execErr, &classified) {
			writeProblem(response, request, http.StatusBadGateway, "ASSET_ENDPOINT_FAILED", classified.Detail)
			return
		}
		if errors.Is(execErr, context.DeadlineExceeded) {
			writeProblem(response, request, http.StatusGatewayTimeout, "ASSET_EXECUTION_TIMEOUT", execErr.Error())
			return
		}
		writeProblem(response, request, http.StatusBadGateway, "ASSET_EXECUTION_FAILED", execErr.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"exitCode": execResult.ExitCode, "output": execResult.Output,
		"truncated": execResult.Truncated, "durationMs": execResult.Duration.Milliseconds(),
	})
}

// assetExecutionHalted reads the deployment-wide kill switch. No row and a
// NULL column both mean "not halted"; only an explicit false stops.
func (s *Server) assetExecutionHalted(request *http.Request, deploymentID string) (bool, error) {
	database := s.app.Database()
	if database == nil {
		return false, errors.New("database unavailable")
	}
	var enabled sql.NullBool
	err := database.QueryRow(request.Context(),
		`SELECT assets_execute_enabled FROM deployment_settings WHERE deployment_id = $1`, deploymentID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled.Valid && !enabled.Bool, nil
}

// resolveSSHAuth fetches, unseals, and interprets the asset's default
// credential; it writes the problem itself and reports false on failure.
func (s *Server) resolveSSHAuth(response http.ResponseWriter, request *http.Request, store *repository.DeploymentStore, deploymentID string, record repository.Asset) (assetprobe.SSHAuth, bool) {
	credentialID := credentialRefDefault(map[string]any(record.CredentialRefs))
	if credentialID == "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_ASSET", "The asset has no default credential reference.")
		return assetprobe.SSHAuth{}, false
	}
	if s.app.Credentials == nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "CREDENTIALS_NOT_CONFIGURED", "Credential management is not configured on this service.")
		return assetprobe.SSHAuth{}, false
	}
	stored, err := store.GetCredential(request.Context(), credentialID)
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL_REF", "The referenced credential does not exist.")
		return assetprobe.SSHAuth{}, false
	}
	if err != nil {
		databaseFailure(response, request, err)
		return assetprobe.SSHAuth{}, false
	}
	if !stored.Enabled {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL_REF", "The referenced credential is disabled.")
		return assetprobe.SSHAuth{}, false
	}
	secret, err := s.app.Credentials.Open(request.Context(), credential.Envelope{
		KeyID: stored.KeyID, Nonce: stored.Nonce, Ciphertext: stored.EncryptedValue,
	}, credential.AssociatedData(deploymentID, stored.ID))
	if err != nil {
		databaseFailure(response, request, err)
		return assetprobe.SSHAuth{}, false
	}
	auth, problem := sshAuthFromCredential(record, stored, secret)
	if problem != "" {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_CREDENTIAL_REF", problem)
		return assetprobe.SSHAuth{}, false
	}
	return auth, true
}

// capabilityEnabled checks the effective (template minus override) set.
func capabilityEnabled(record repository.Asset, capabilityKey string) bool {
	for _, capability := range effectiveAssetCapabilities(record) {
		if key, ok := capability["key"].(string); ok && key == capabilityKey {
			return true
		}
	}
	return false
}

func allowlistContains(allowlist repository.JSONArray, key string) bool {
	for _, item := range allowlist {
		if value, ok := item.(string); ok && value == key {
			return true
		}
	}
	return false
}

// boundCommand keeps the audited command echo readable.
func boundCommand(command string) string {
	if len(command) > 1024 {
		return command[:1024] + "…"
	}
	return command
}
