package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/publicationrelay"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

func (s *Server) legacyGatewayEnabled(response http.ResponseWriter, request *http.Request) bool {
	if !s.app.Config.UsesGatewayOperator() {
		return true
	}
	response.Header().Set("Cache-Control", "no-store")
	writeProblem(response, request, http.StatusServiceUnavailable, "LEGACY_GATEWAY_PUBLICATION_DISABLED", "The gateway uses asynchronous publications; the legacy reconciler path is disabled.")
	return false
}

// Compatibility reads expose selected immutable intent, never legacy DB status
// or unpublished drafts. Operator observations retain the publication fence.
func (s *Server) operatorIntent(ctx context.Context, deploymentID string) (repository.DataPlanePublication, dataPlaneDesiredState, error) {
	store := s.app.Store.Deployment(deploymentID)
	value, err := store.LatestDataPlanePublication(ctx)
	state := dataPlaneDesiredState{DeploymentID: deploymentID, dataPlaneDesiredStateWrite: dataPlaneDesiredStateWrite{Routes: []dataPlaneRoute{}}}
	if errors.Is(err, repository.ErrNotFound) {
		state.ContentHash = dataPlaneHash(state.dataPlaneDesiredStateWrite)
		return repository.DataPlanePublication{}, state, nil
	}
	if err != nil {
		return value, state, err
	}
	inputs, err := store.ReadDataPlanePublicationInputs(ctx, value)
	if err != nil {
		return value, state, err
	}
	var captured struct {
		dataPlaneDesiredStateWrite
		GatewayLimits *[]gatewaypolicy.Limit `json:"gatewayLimits,omitempty"`
	}
	if json.Unmarshal(inputs.Payload, &captured) != nil {
		return value, state, errPublicationSnapshot
	}
	// jsonb may reorder keys/whitespace. Hash the established typed canonical
	// snapshot, exactly as queueing and relay do, never its database byte format.
	canonical, err := json.Marshal(captured)
	state.dataPlaneDesiredStateWrite = captured.dataPlaneDesiredStateWrite
	if err != nil || state.Revision != value.Revision || state.Routes == nil || publicationDigest(canonical) != value.ContentHash {
		return value, state, errPublicationSnapshot
	}
	state.PublishedAt, state.ContentHash = value.CreatedAt, value.ContentHash
	return value, state, nil
}

func (s *Server) operatorIntentCurrent(ctx context.Context, deploymentID string, value repository.DataPlanePublication) bool {
	current, err := s.app.Store.Deployment(deploymentID).LatestDataPlanePublication(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		return value.OperationID == ""
	}
	return err == nil && current.OperationID == value.OperationID && current.Sequence == value.Sequence && current.ContentHash == value.ContentHash
}

func (s *Server) getOperatorDesiredState(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	deploymentID := claimsFrom(request).DeploymentID
	value, state, err := s.operatorIntent(ctx, deploymentID)
	if err != nil || !s.operatorIntentCurrent(ctx, deploymentID, value) {
		publicationUnavailable(response, request, err, "The selected gateway intent could not be read consistently; retry the read.")
		return
	}
	writeJSON(response, http.StatusOK, state)
}

func operatorStatus(value repository.DataPlanePublication, conditions []publicationrelay.Condition) dataPlaneStatus {
	status := dataPlaneStatus{State: "pending"}
	if value.OperationID == "" {
		return status
	}
	status.ContentHash = &value.ContentHash
	if value.Phase == "failed" {
		status.State, status.ErrorCode = "error", value.ErrorCode
		return status
	}
	for _, condition := range conditions {
		if condition.ObservedRevision == nil || *condition.ObservedRevision != value.Revision {
			continue
		}
		if condition.Status == "False" {
			status.State = "error"
			code := "GATEWAY_OPERATOR_REJECTED"
			status.ErrorCode = &code
			return status
		}
	}
	for _, condition := range conditions {
		if condition.Status != "True" || condition.ObservedRevision == nil || *condition.ObservedRevision != value.Revision {
			continue
		}
		if condition.Type == "Applied" {
			status.ObservedRevision = &value.Revision
			at := condition.LastTransitionTime
			status.LastAppliedAt = &at
			status.State = "applying"
		}
	}
	// Ready is only the independent, fresh full-fleet evidence supplied by the
	// observer, never the outbox phase, a successful SSA, or legacy ready flag.
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedRevision != nil && *condition.ObservedRevision == value.Revision && status.LastAppliedAt != nil {
			status.State = "ready"
		}
	}
	return status
}

func (s *Server) getOperatorDataPlaneStatus(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	deploymentID := claimsFrom(request).DeploymentID
	value, state, err := s.operatorIntent(ctx, deploymentID)
	if err != nil {
		publicationUnavailable(response, request, err, "The selected gateway intent could not be read.")
		return
	}
	models, err := s.app.Store.Deployment(deploymentID).ListModels(ctx)
	if err != nil {
		publicationUnavailable(response, request, err, "The model catalog could not be compared.")
		return
	}
	derived, valid := deriveCatalogState(models, "")
	if !valid {
		publicationUnavailable(response, request, errPublicationSnapshot, "The model catalog could not be compared.")
		return
	}
	view := s.observedPublication(ctx, value)
	status := operatorStatus(value, view.Conditions)
	status.ResourceCount = len(state.Routes)
	if !s.operatorIntentCurrent(ctx, deploymentID, value) {
		publicationUnavailable(response, request, nil, "The selected gateway intent changed during the read; retry the read.")
		return
	}
	writeJSON(response, http.StatusOK, dataPlaneStatusView{dataPlaneStatus: status, CatalogComparison: compareCatalogRoutes(state.Routes, derived.Routes)})
}
