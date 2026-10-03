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
	AgentControlBaseURL deploymentSettingState `json:"agentControlBaseUrl"`
}

// deploymentSettingsUpdate tracks field presence so an omitted field stays
// unchanged while an explicit null clears the runtime override. The standard
// decoder cannot distinguish the two for pointer fields, hence the custom
// unmarshaler; it also rejects unknown fields like decodeJSON would.
type deploymentSettingsUpdate struct {
	ModelGatewayBaseURL    *string
	HasModelGatewayBaseURL bool
	AgentControlBaseURL    *string
	HasAgentControlBaseURL bool
}

func (update *deploymentSettingsUpdate) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "modelGatewayBaseUrl" && key != "agentControlBaseUrl" {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	if raw, present := fields["modelGatewayBaseUrl"]; present {
		update.HasModelGatewayBaseURL = true
		if string(raw) != "null" {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("modelGatewayBaseUrl must be a string or null")
			}
			trimmed := strings.TrimSpace(value)
			update.ModelGatewayBaseURL = &trimmed
		}
	}
	if raw, present := fields["agentControlBaseUrl"]; present {
		update.HasAgentControlBaseURL = true
		if string(raw) != "null" {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("agentControlBaseUrl must be a string or null")
			}
			trimmed := strings.TrimSpace(value)
			update.AgentControlBaseURL = &trimmed
		}
	}
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

func (s *Server) deploymentSettingOverride(request *http.Request, column string) (*string, error) {
	database := s.app.Database()
	if database == nil {
		return nil, errors.New("database unavailable")
	}
	var value sql.NullString
	err := database.QueryRow(request.Context(), `SELECT `+column+` FROM deployment_settings WHERE deployment_id=$1`, claimsFrom(request).DeploymentID).Scan(&value)
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
	override, err := s.deploymentSettingOverride(request, "model_gateway_base_url")
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	agentOverride, err := s.deploymentSettingOverride(request, "agent_control_base_url")
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, deploymentSettings{
		ModelGatewayBaseURL: resolveDeploymentSetting(override, s.app.Config.ModelGatewayBaseURL),
		AgentControlBaseURL: resolveDeploymentSetting(agentOverride, s.app.Config.AgentControlBaseURL),
	})
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
	if input.HasAgentControlBaseURL {
		var stored any
		if input.AgentControlBaseURL != nil {
			value := *input.AgentControlBaseURL
			// Unlike the model gateway, the agent-control endpoint may be a
			// cluster-internal hostname when a split deployment fronts it with
			// an ingress; only the absolute-URL shape is enforced here.
			if problem := validateAbsoluteSettingURL(value, "agent control base URL"); problem != "" {
				writeProblem(response, request, http.StatusUnprocessableEntity, "INVALID_DEPLOYMENT_SETTINGS", problem)
				return
			}
			stored = value
		}
		if _, err := database.Exec(request.Context(), `INSERT INTO deployment_settings (deployment_id,agent_control_base_url) VALUES ($1,$2)
ON CONFLICT (deployment_id) DO UPDATE SET agent_control_base_url=EXCLUDED.agent_control_base_url,updated_at=now()`, claimsFrom(request).DeploymentID, stored); err != nil {
			databaseFailure(response, request, err)
			return
		}
	}
	s.getDeploymentSettings(response, request)
}

// validateAbsoluteSettingURL enforces the shared write-time shape for URL
// runtime settings: an absolute http(s) URL of bounded length.
func validateAbsoluteSettingURL(value string, label string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxDeploymentSettingValueLength {
		return "The " + label + " must be an absolute http or https URL."
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "The " + label + " must be an absolute http or https URL."
	}
	return ""
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

// effectiveAgentControlBaseURL resolves the split agent-control endpoint
// advertised through service metadata, mirroring the model-gateway override
// precedence: runtime setting over environment value.
func effectiveAgentControlBaseURL(application *app.App, request *http.Request) string {
	database := application.Database()
	if database == nil {
		return application.Config.AgentControlBaseURL
	}
	var value sql.NullString
	err := database.QueryRow(request.Context(), `SELECT agent_control_base_url FROM deployment_settings WHERE deployment_id=$1`, application.DeploymentID()).Scan(&value)
	if err == nil && value.Valid && value.String != "" {
		return value.String
	}
	return application.Config.AgentControlBaseURL
}
