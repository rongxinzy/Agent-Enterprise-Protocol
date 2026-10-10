package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/modelpricing"
)

func (s *Server) getModelPricing(response http.ResponseWriter, request *http.Request) {
	store := s.app.Store.Deployment(claimsFrom(request).DeploymentID)
	modelID := chi.URLParam(request, "modelId")
	if _, err := store.GetModel(request.Context(), modelID); err != nil {
		modelPricingFailure(response, request, err)
		return
	}
	value, err := store.GetModelPricing(request.Context(), modelID)
	if err != nil {
		modelPricingFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

type modelPricingWrite struct {
	Pricing         json.RawMessage `json:"pricing"`
	ExpectedVersion *int64          `json:"expectedVersion"`
}

func validModelPricingWrite(input modelPricingWrite) bool {
	if input.ExpectedVersion == nil || *input.ExpectedVersion < 0 || *input.ExpectedVersion > modelpricing.MaxVersion || len(input.Pricing) == 0 {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(input.Pricing), []byte("null")) {
		return true
	}
	var cfg modelpricing.Configuration
	decoder := json.NewDecoder(bytes.NewReader(input.Pricing))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || !cfg.Valid() {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input.Pricing, &fields) != nil {
		return false
	}
	for _, key := range []string{"source", "cachedInputPricePerMillionTokens"} {
		if raw, present := fields[key]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	return true
}

func (s *Server) putModelPricing(response http.ResponseWriter, request *http.Request) {
	var input modelPricingWrite
	if !decodeJSON(response, request, &input) {
		return
	}
	if !validModelPricingWrite(input) {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_MODEL_PRICING", "The model price configuration or observed version is invalid.")
		return
	}
	pricing := input.Pricing
	if bytes.Equal(bytes.TrimSpace(pricing), []byte("null")) {
		pricing = nil
	}
	claims := claimsFrom(request)
	modelID := chi.URLParam(request, "modelId")
	value, err := s.app.Store.Deployment(claims.DeploymentID).PutModelPricing(request.Context(), modelID, pricing, *input.ExpectedVersion)
	if err != nil {
		modelPricingFailure(response, request, err)
		return
	}
	slog.Info("model reference prices updated", "deployment_id", claims.DeploymentID, "user_id", claims.Subject, "model_id", modelID, "version", value.Version)
	writeJSON(response, http.StatusOK, value)
}

func modelPricingFailure(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model was not found.")
	case errors.Is(err, repository.ErrModelPricingVersion):
		writeProblem(response, request, http.StatusConflict, "MODEL_PRICING_VERSION_CONFLICT", "The model prices have changed. Read the current version before saving.")
	default:
		databaseFailure(response, request, err)
	}
}
