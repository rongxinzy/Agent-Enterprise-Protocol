package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func (s *Server) replayDataPlanePublication(response http.ResponseWriter, request *http.Request) {
	if !s.publicationEnabled(response, request) {
		return
	}
	keys := request.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !publicationKeyPattern.MatchString(keys[0]) {
		writeProblem(response, request, 400, "INVALID_IDEMPOTENCY_KEY", "A valid Idempotency-Key is required.")
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "operationId"))
	if err != nil {
		writeProblem(response, request, 404, "RESOURCE_NOT_FOUND", "The publication was not found.")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	var body map[string]json.RawMessage
	var trailing any
	if decoder.Decode(&body) != nil || body == nil || len(body) != 0 || decoder.Decode(&trailing) != io.EOF {
		writeProblem(response, request, 400, "INVALID_REQUEST", "A single empty JSON object is required for recovery replay.")
		return
	}
	claims := claimsFrom(request)
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	value, err := s.app.Store.Deployment(claims.DeploymentID).ReplayDataPlanePublication(ctx, id.String(), publicationDigest([]byte(keys[0])), publicationDigest([]byte("replay:"+id.String())), claims.Subject)
	switch {
	case errors.Is(err, repository.ErrPublicationKeyConflict):
		writeProblem(response, request, 409, "IDEMPOTENCY_KEY_CONFLICT", "This key is bound to a different publication request.")
	case errors.Is(err, repository.ErrNotFound):
		writeProblem(response, request, 404, "RESOURCE_NOT_FOUND", "The publication was not found.")
	case errors.Is(err, repository.ErrPublicationNotLatest):
		writeProblem(response, request, 409, "PUBLICATION_NOT_LATEST", "Recovery replay requires the latest selected intent.")
	case errors.Is(err, repository.ErrPublicationReferencesChanged):
		writeProblem(response, request, 409, "PUBLICATION_REFERENCES_CHANGED", "Wait for pending reference withdrawals, then replay the resulting latest intent.")
	case err != nil:
		publicationUnavailable(response, request, err, "Recovery replay could not be confirmed; retry with the same key and source operation.")
	default:
		markAdminAuditRecorded(request)
		response.Header().Set("Location", "/aep/v1/admin/data-plane/publications/"+value.OperationID)
		writeJSON(response, http.StatusAccepted, s.observedPublication(request.Context(), value))
	}
}
