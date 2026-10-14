package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// computeTierEntry is one row of the deployment-owned tier catalog file; the
// wire form adds the fixed source marker. Quantities stay opaque strings — the
// control plane never interprets Kubernetes resource syntax.
type computeTierEntry struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	CPURequest    string `json:"cpuRequest"`
	CPULimit      string `json:"cpuLimit"`
	MemoryRequest string `json:"memoryRequest"`
	MemoryLimit   string `json:"memoryLimit"`
	Source        string `json:"source"`
}

type computeAssignmentTier struct {
	Override  *string `json:"override"`
	Effective string  `json:"effective"`
	Source    string  `json:"source"`
}

type computeAssignment struct {
	AgentID   string                `json:"agentId"`
	AgentName string                `json:"agentName,omitempty"`
	Tier      computeAssignmentTier `json:"tier"`
	UpdatedAt *string               `json:"updatedAt,omitempty"`
	UpdatedBy *string               `json:"updatedBy,omitempty"`
}

// computeAssignmentUpdate tracks the required tier key: it must always be
// present, with null clearing the override.
type computeAssignmentUpdate struct {
	Tier *string
}

func (update *computeAssignmentUpdate) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "tier" {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	raw, present := fields["tier"]
	if !present {
		return fmt.Errorf("tier is required")
	}
	if string(raw) == "null" {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("tier must be a catalog tier name or null")
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 200 {
		return fmt.Errorf("tier must be a catalog tier name of at most 200 characters")
	}
	update.Tier = &trimmed
	return nil
}

// loadComputeTierCatalog reads the deployment-owned tier catalog. The file is
// deliberately re-read per request so operators can rotate it without a
// restart; a configured but unreadable or malformed catalog fails closed
// rather than silently presenting an empty or partial catalog.
func loadComputeTierCatalog(path string) ([]computeTierEntry, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Items []computeTierEntry `json:"items"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	for index, item := range catalog.Items {
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.CPURequest) == "" ||
			strings.TrimSpace(item.CPULimit) == "" || strings.TrimSpace(item.MemoryRequest) == "" ||
			strings.TrimSpace(item.MemoryLimit) == "" {
			return nil, fmt.Errorf("compute tier catalog entry %d is incomplete", index)
		}
	}
	return catalog.Items, nil
}

func computeTierNames(items []computeTierEntry) map[string]struct{} {
	names := make(map[string]struct{}, len(items))
	for _, item := range items {
		names[item.Name] = struct{}{}
	}
	return names
}

// computeTiersUnavailable reports a configured-but-broken catalog. Operators
// see a server error, never a silently empty catalog.
func computeTiersUnavailable(response http.ResponseWriter, request *http.Request, err error) {
	slog.Error("compute tier catalog cannot be read", "request_id", request.Context().Value(contextKey("request-id")), "error", err)
	writeProblem(response, request, http.StatusInternalServerError, "COMPUTE_TIERS_UNAVAILABLE", "The compute tier catalog is configured but cannot be read.")
}

func (s *Server) listComputeTiers(response http.ResponseWriter, request *http.Request) {
	items, err := loadComputeTierCatalog(s.app.Config.ComputeTiersFile)
	if err != nil {
		computeTiersUnavailable(response, request, err)
		return
	}
	wire := make([]computeTierEntry, 0, len(items))
	for _, item := range items {
		item.Source = "deployment"
		wire = append(wire, item)
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": wire})
}

// effectiveComputeDefaultTier resolves the deployment default tier: the
// runtime override wins over the environment value, and a settings lookup
// failure falls back to the environment value (mirroring the gateway URL
// resolution) so a transient database error never invents a default.
func (s *Server) effectiveComputeDefaultTier(request *http.Request) string {
	override, err := s.deploymentSettingOverride(request, "compute_default_tier")
	if err != nil {
		slog.Warn("compute default tier lookup failed", "request_id", request.Context().Value(contextKey("request-id")), "error", err)
		return s.app.Config.ComputeDefaultTier
	}
	if override != nil {
		return *override
	}
	return s.app.Config.ComputeDefaultTier
}

func (s *Server) listComputeAssignments(response http.ResponseWriter, request *http.Request) {
	deploymentID := claimsFrom(request).DeploymentID
	store := s.app.Store.Deployment(deploymentID)
	// Non-ephemeral agents only, mirroring the directory default: ephemeral
	// conversation-scoped instances carry no operator allocation intent.
	var agents []repository.AgentDirectoryEntry
	cursor := ""
	for {
		entries, err := store.ListAgentDirectory(request.Context(), cursor, 200, false)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		agents = append(agents, entries...)
		if int32(len(entries)) < 200 {
			break
		}
		cursor = entries[len(entries)-1].User.ID
	}
	agentIDs := make([]string, 0, len(agents))
	for _, entry := range agents {
		agentIDs = append(agentIDs, entry.User.ID)
	}
	overrides, err := store.ListComputeAssignmentOverrides(request.Context(), agentIDs)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defaultTier := s.effectiveComputeDefaultTier(request)
	items := make([]computeAssignment, 0, len(agents))
	for _, entry := range agents {
		override, hasOverride := overrides[entry.User.ID]
		items = append(items, computeAssignmentRow(entry.User.ID, entry.User.DisplayName, override, hasOverride, defaultTier))
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func computeAssignmentRow(agentID, agentName string, override repository.ComputeAssignmentOverride, hasOverride bool, defaultTier string) computeAssignment {
	row := computeAssignment{AgentID: agentID, AgentName: agentName}
	if hasOverride {
		tier := override.Tier
		row.Tier = computeAssignmentTier{Override: &tier, Effective: tier, Source: "override"}
		updatedAt := override.UpdatedAt.UTC().Format(time.RFC3339Nano)
		row.UpdatedAt = &updatedAt
		row.UpdatedBy = override.UpdatedBy
		return row
	}
	row.Tier = computeAssignmentTier{Effective: defaultTier, Source: "default"}
	return row
}

func (s *Server) updateComputeAssignment(response http.ResponseWriter, request *http.Request) {
	agentID := chi.URLParam(request, "agentId")
	var input computeAssignmentUpdate
	if !decodeJSON(response, request, &input) {
		return
	}
	claims := claimsFrom(request)
	store := s.app.Store.Deployment(claims.DeploymentID)
	// The agent id is untrusted client input: only digital employees of this
	// deployment are assignable, everything else is a plain 404.
	user, err := store.GetUserByID(request.Context(), agentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The agent does not exist.")
			return
		}
		databaseFailure(response, request, err)
		return
	}
	if user.Kind != "agent" {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The agent does not exist.")
		return
	}
	if input.Tier != nil {
		// Tier names are validated against the deployment-owned catalog when
		// one is configured; without a catalog file there is nothing to
		// validate against and the value stays inert.
		items, err := loadComputeTierCatalog(s.app.Config.ComputeTiersFile)
		if err != nil {
			computeTiersUnavailable(response, request, err)
			return
		}
		if len(items) > 0 {
			if _, ok := computeTierNames(items)[*input.Tier]; !ok {
				writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_COMPUTE_TIER", "The tier is not in the compute tier catalog.")
				return
			}
		}
		updatedBy := claims.Subject
		if err := store.UpsertComputeAssignment(request.Context(), repository.AgentComputeAssignment{
			DeploymentID: claims.DeploymentID,
			AgentID:      agentID,
			Tier:         *input.Tier,
			UpdatedBy:    &updatedBy,
			UpdatedAt:    time.Now().UTC(),
		}); err != nil {
			databaseFailure(response, request, err)
			return
		}
	} else {
		if err := store.DeleteComputeAssignment(request.Context(), agentID); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	overrides, err := store.ListComputeAssignmentOverrides(request.Context(), []string{agentID})
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	override, hasOverride := overrides[agentID]
	writeJSON(response, http.StatusOK, computeAssignmentRow(agentID, user.DisplayName, override, hasOverride, s.effectiveComputeDefaultTier(request)))
}
