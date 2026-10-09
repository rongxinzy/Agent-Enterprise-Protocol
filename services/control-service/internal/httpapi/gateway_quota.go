package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
	"gorm.io/gorm"
)

func (s *Server) gatewayQuota(response http.ResponseWriter, request *http.Request) {
	cfg, tenant, user := s.app.Config, claimsFrom(request).DeploymentID, chi.URLParam(request, "userId")
	if cfg.GatewayQuotaURL == "" || cfg.GatewayQuotaToken == "" {
		writeProblem(response, request, 503, "GATEWAY_SOURCE_UNAVAILABLE", "Native quota management is not configured.")
		return
	}
	if _, err := s.app.Store.Deployment(tenant).GetUser(request.Context(), user); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeProblem(response, request, 404, "RESOURCE_NOT_FOUND", "The deployment user was not found.")
		} else {
			databaseFailure(response, request, err)
		}
		return
	}
	consumer := gatewaysource.Consumer(tenant, user)
	values := url.Values{"consumer": {consumer}}
	if request.Method == http.MethodPost {
		var input struct {
			Quota *int64 `json:"quota"`
			Value *int64 `json:"value"`
		}
		if !decodeJSON(response, request, &input) {
			return
		}
		path := "/delta"
		if strings.HasSuffix(request.URL.Path, "/refresh") {
			if input.Quota == nil || input.Value != nil || *input.Quota < 0 || *input.Quota > 1e12 {
				writeProblem(response, request, 400, "INVALID_GATEWAY_QUOTA", "A bounded nonnegative integer quota is required.")
				return
			}
			path = "/refresh"
			values.Set("quota", strconv.FormatInt(*input.Quota, 10))
		} else {
			if input.Value == nil || input.Quota != nil || *input.Value < -1e12 || *input.Value > 1e12 {
				writeProblem(response, request, 400, "INVALID_GATEWAY_QUOTA", "A bounded integer delta is required.")
				return
			}
			values.Set("value", strconv.FormatInt(*input.Value, 10))
		}
		if err := gatewaysource.MutateQuota(request.Context(), cfg.GatewayQuotaURL, cfg.GatewayQuotaToken, tenant, path, values); err != nil {
			writeProblem(response, request, 503, "GATEWAY_SOURCE_UNAVAILABLE", "The native quota operation could not be confirmed. Do not automatically retry a delta.")
			return
		}
	}
	data, err := gatewaysource.Fetch(request.Context(), cfg.GatewayQuotaURL, cfg.GatewayQuotaToken, tenant, "", http.MethodGet, url.Values{"consumer": {consumer}})
	var native struct {
		Consumer string       `json:"consumer"`
		Quota    *json.Number `json:"quota"`
	}
	if err != nil || json.Unmarshal(data, &native) != nil || native.Consumer != consumer || native.Quota == nil {
		writeProblem(response, request, 503, "GATEWAY_SOURCE_UNAVAILABLE", "The native quota balance is unavailable.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, native)
}
