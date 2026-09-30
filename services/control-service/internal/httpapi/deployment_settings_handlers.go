package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

const maxDeploymentSettingValueLength = 2048

// deploymentSettingState is the wire form of one deployment-level runtime
// setting: the stored override plus the resolved value and its origin.
type deploymentSettingState struct {
	Override       *string `json:"override"`
	EffectiveValue *string `json:"effectiveValue"`
	Source         string  `json:"source"`
}

type deploymentSettings struct {
	ModelGatewayBaseURL deploymentSettingState `json:"modelGatewayBaseUrl"`
}

// deploymentSettingsUpdate tracks field presence so an omitted field stays
// unchanged while an explicit null clears the runtime override. The standard
// decoder cannot distinguish the two for pointer fields, hence the custom
// unmarshaler; it also rejects unknown fields like decodeJSON would.
type deploymentSettingsUpdate struct {
	ModelGatewayBaseURL    *string
	HasModelGatewayBaseURL bool
}

func (update *deploymentSettingsUpdate) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "modelGatewayBaseUrl" {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	raw, present := fields["modelGatewayBaseUrl"]
	if !present {
		return nil
	}
	update.HasModelGatewayBaseURL = true
	if string(raw) == "null" {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("modelGatewayBaseUrl must be a string or null")
	}
	trimmed := strings.TrimSpace(value)
	update.ModelGatewayBaseURL = &trimmed
	return nil
}

func resolveDeploymentSetting(override *string, environmentValue string) deploymentSettingState {
	if override != nil && *override != "" {
		return deploymentSettingState{Override: override, EffectiveValue: override, Source: "override"}
	}
	if environmentValue != "" {
		return deploymentSettingState{EffectiveValue: &environmentValue, Source: "env"}
	}
	return deploymentSettingState{Source: "unset"}
}

func (s *Server) modelGatewayOverride(request *http.Request) (*string, error) {
	database := s.app.Database()
	if database == nil {
		return nil, errors.New("database unavailable")
	}
	var value sql.NullString
	err := database.QueryRow(request.Context(), `SELECT model_gateway_base_url FROM deployment_settings WHERE deployment_id=$1`, claimsFrom(request).DeploymentID).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	return &value.String, nil
}

func (s *Server) getDeploymentSettings(response http.ResponseWriter, request *http.Request) {
	override, err := s.modelGatewayOverride(request)
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, deploymentSettings{ModelGatewayBaseURL: resolveDeploymentSetting(override, s.app.Config.ModelGatewayBaseURL)})
}

func (s *Server) updateDeploymentSettings(response http.ResponseWriter, request *http.Request) {
	var input deploymentSettingsUpdate
	if !decodeJSON(response, request, &input) {
		return
	}
	database := s.app.Database()
	if database == nil {
		databaseFailure(response, request, errors.New("database unavailable"))
		return
	}
	if input.HasModelGatewayBaseURL {
		var stored any
		if input.ModelGatewayBaseURL != nil {
			value := *input.ModelGatewayBaseURL
			if problem := validateModelGatewayBaseURL(value, s.app.Config.Environment); problem != "" {
				writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_DEPLOYMENT_SETTINGS", problem)
				return
			}
			stored = value
		}
		if _, err := database.Exec(request.Context(), `INSERT INTO deployment_settings (deployment_id,model_gateway_base_url) VALUES ($1,$2)
ON CONFLICT (deployment_id) DO UPDATE SET model_gateway_base_url=EXCLUDED.model_gateway_base_url,updated_at=now()`, claimsFrom(request).DeploymentID, stored); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	s.getDeploymentSettings(response, request)
}

// validateModelGatewayBaseURL applies the write-time contract for the model
// gateway runtime override. It mirrors the startup environment validation and
// additionally rejects cluster-internal hostnames, which would silently break
// client-reachable model traffic, and loopback addresses in production.
func validateModelGatewayBaseURL(value string, environment string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxDeploymentSettingValueLength {
		return "The model gateway base URL must be an absolute http or https URL."
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "The model gateway base URL must be an absolute http or https URL."
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "The model gateway base URL must be an absolute http or https URL."
	}
	if address, addressErr := netip.ParseAddr(host); addressErr == nil {
		if environment == "production" && address.IsLoopback() {
			return "The model gateway base URL must not point to a loopback address in production."
		}
		return ""
	}
	if !strings.Contains(host, ".") || host == "svc.cluster.local" || strings.HasSuffix(host, ".svc.cluster.local") {
		return "The model gateway base URL must not use a cluster-internal hostname."
	}
	return ""
}

// effectiveModelGatewayBaseURL resolves the model gateway URL advertised
// through service metadata: the runtime override wins over the environment
// value. A settings lookup failure falls back to the environment value so a
// transient database error never hides an otherwise reachable gateway.
func effectiveModelGatewayBaseURL(application *app.App, request *http.Request) string {
	database := application.Database()
	if database == nil {
		return application.Config.ModelGatewayBaseURL
	}
	var value sql.NullString
	err := database.QueryRow(request.Context(), `SELECT model_gateway_base_url FROM deployment_settings WHERE deployment_id=$1`, application.DeploymentID()).Scan(&value)
	if err == nil && value.Valid && value.String != "" {
		return value.String
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("deployment settings lookup failed", "request_id", request.Context().Value(contextKey("request-id")), "error", err)
	}
	return application.Config.ModelGatewayBaseURL
}
