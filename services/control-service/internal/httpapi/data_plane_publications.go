package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

var publicationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var errPublicationSnapshot = errors.New("invalid publication snapshot")

type publicationRequest struct {
	Revision *string `json:"revision,omitempty"`
}

type publicationCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	ObservedRevision   *string   `json:"observedRevision"`
	Reason             string    `json:"reason"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type publicationView struct {
	OperationID          string                 `json:"operationId"`
	DeploymentID         string                 `json:"deploymentId"`
	Revision             string                 `json:"revision"`
	ContentHash          string                 `json:"contentHash"`
	Phase                string                 `json:"phase"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
	IdempotencyExpiresAt time.Time              `json:"idempotencyExpiresAt"`
	Conditions           []publicationCondition `json:"conditions"`
	ErrorCode            *string                `json:"errorCode"`
}

// There is deliberately no legacy ready lookup: the relay/observation stage
// will supply revision-specific evidence. Queueing alone establishes none.
func publicationJSON(value repository.DataPlanePublication) publicationView {
	conditions := make([]publicationCondition, 0, 3)
	for _, kind := range []string{"Accepted", "Applied", "Ready"} {
		conditions = append(conditions, publicationCondition{Type: kind, Status: "Unknown", Reason: "NotObserved", LastTransitionTime: value.CreatedAt})
	}
	return publicationView{
		OperationID: value.OperationID, DeploymentID: value.DeploymentID, Revision: value.Revision, ContentHash: value.ContentHash,
		Phase: value.Phase, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, IdempotencyExpiresAt: value.IdempotencyExpiresAt,
		Conditions: conditions, ErrorCode: value.ErrorCode,
	}
}

func publicationDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func publicationUnavailable(response http.ResponseWriter, request *http.Request, err error, detail string) {
	// PostgreSQL codes are safe diagnostics; the original error can include
	// snapshot data or SQL parameters and must not be logged or returned.
	var databaseError *pgconn.PgError
	code := "unavailable"
	if errors.As(err, &databaseError) {
		code = databaseError.Code
	}
	if !errors.Is(err, context.Canceled) {
		slog.Error("data-plane publication database operation failed", "database_code", code, "request_id", request.Context().Value(contextKey("request-id")))
	}
	writeProblem(response, request, http.StatusServiceUnavailable, "PUBLICATION_UNAVAILABLE", detail)
}

func (s *Server) publicationEnabled(response http.ResponseWriter, request *http.Request) bool {
	response.Header().Set("Cache-Control", "no-store")
	if !s.app.Config.GatewayPublicationsEnabled {
		writeProblem(response, request, http.StatusServiceUnavailable, "DATA_PLANE_PUBLICATIONS_DISABLED", "Asynchronous route publication is not enabled.")
		return false
	}
	return true
}

func (s *Server) createDataPlanePublication(response http.ResponseWriter, request *http.Request) {
	if !s.publicationEnabled(response, request) {
		return
	}
	keys := request.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !publicationKeyPattern.MatchString(keys[0]) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "A valid Idempotency-Key is required.")
		return
	}
	// A strict object decoder also rejects JSON null, duplicate/unknown keys and
	// trailing documents. Unlike ordinary optional publish, {} is required.
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	token, decodeErr := decoder.Token()
	if decodeErr != nil || token != json.Delim('{') {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "A publication JSON object is required.")
		return
	}
	fields = make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The publication request is invalid.")
			return
		}
		name, ok := key.(string)
		_, duplicate := fields[name]
		var raw json.RawMessage
		if !ok || duplicate || decoder.Decode(&raw) != nil {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The publication request is invalid.")
			return
		}
		fields[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The publication request is invalid.")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "Only one publication JSON object is allowed.")
		return
	}
	var input publicationRequest
	for key, raw := range fields {
		if key != "revision" {
			writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The publication request contains an unknown field.")
			return
		}
		var revision string
		if string(raw) == "null" || json.Unmarshal(raw, &revision) != nil {
			writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_DATA_PLANE_STATE", "The revision must be a non-empty string.")
			return
		}
		revision = strings.TrimSpace(revision)
		if revision == "" || utf8.RuneCountInString(revision) > 200 {
			writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_DATA_PLANE_STATE", "The revision must contain 1-200 characters.")
			return
		}
		input.Revision = &revision
	}
	normalized, _ := json.Marshal(input)
	claims := claimsFrom(request)
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	value, err := s.app.Store.Deployment(claims.DeploymentID).QueueDataPlanePublication(ctx, publicationDigest([]byte(keys[0])), publicationDigest(normalized), claims.Subject, func(models []repository.Model) (repository.PublicationSnapshot, error) {
		revision := ""
		if input.Revision != nil {
			revision = *input.Revision
		}
		state, valid := deriveCatalogState(models, revision)
		if !valid {
			return repository.PublicationSnapshot{}, errPublicationSnapshot
		}
		payload, err := json.Marshal(state)
		return repository.PublicationSnapshot{Revision: state.Revision, ContentHash: dataPlaneHash(state), Payload: payload}, err
	})
	switch {
	case errors.Is(err, repository.ErrPublicationKeyConflict):
		writeProblem(response, request, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "This key is bound to a different publication request.")
	case errors.Is(err, repository.ErrPublicationRevisionConflict), errors.Is(err, errPublicationSnapshot):
		writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_DATA_PLANE_STATE", "The catalog snapshot or revision is invalid.")
	case err != nil:
		publicationUnavailable(response, request, err, "The publication could not be confirmed; retry with the same key and request.")
	default:
		// Includes replays: the original atomic audit suffices; the generic
		// best-effort audit middleware must not append a second success row.
		markAdminAuditRecorded(request)
		response.Header().Set("Location", "/aep/v1/admin/data-plane/publications/"+value.OperationID)
		writeJSON(response, http.StatusAccepted, publicationJSON(value))
	}
}

func (s *Server) getDataPlanePublication(response http.ResponseWriter, request *http.Request) {
	if !s.publicationEnabled(response, request) {
		return
	}
	operationID, err := uuid.Parse(chi.URLParam(request, "operationId"))
	if err != nil {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The publication was not found.")
		return
	}
	value, err := s.app.Store.Deployment(claimsFrom(request).DeploymentID).GetDataPlanePublication(request.Context(), operationID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The publication was not found.")
		return
	}
	if err != nil {
		publicationUnavailable(response, request, err, "The publication status could not be read.")
		return
	}
	writeJSON(response, http.StatusOK, publicationJSON(value))
}
