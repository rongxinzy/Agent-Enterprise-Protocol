package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func expectSettingsRead(pool pgxmock.PgxPoolIface, override *string, fallbackOverride *[]string) {
	rows := pgxmock.NewRows([]string{"model_gateway_base_url"})
	if override != nil {
		rows.AddRow(*override)
	}
	pool.ExpectQuery(`SELECT model_gateway_base_url FROM deployment_settings`).
		WithArgs("deployment-a").WillReturnRows(rows)
	pool.ExpectQuery(`SELECT agent_control_base_url FROM deployment_settings`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"agent_control_base_url"}).AddRow(nil))
	fallbackRows := pgxmock.NewRows([]string{"present", "model_fallback_ids"})
	if fallbackOverride != nil {
		fallbackRows.AddRow(true, *fallbackOverride)
	} else {
		fallbackRows.AddRow(false, []string{})
	}
	pool.ExpectQuery(`SELECT model_fallback_ids IS NOT NULL`).
		WithArgs("deployment-a").WillReturnRows(fallbackRows)
}

// The service metadata reads only the two advertised endpoints; it does not
// touch the failover chain.
func expectMetadataSettingsRead(pool pgxmock.PgxPoolIface, override *string) {
	rows := pgxmock.NewRows([]string{"model_gateway_base_url"})
	if override != nil {
		rows.AddRow(*override)
	}
	pool.ExpectQuery(`SELECT model_gateway_base_url FROM deployment_settings`).
		WithArgs("deployment-a").WillReturnRows(rows)
	pool.ExpectQuery(`SELECT agent_control_base_url FROM deployment_settings`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"agent_control_base_url"}).AddRow(nil))
}

func TestAdminDeploymentSettingsFallbackChain(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config.ModelFallbackIDs = []string{"env-fallback"}
	handler := New(application).Handler()

	// The environment-configured chain surfaces while no override is stored.
	expectSettingsRead(pool, nil, nil)
	initial := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if initial.Code != http.StatusOK ||
		!strings.Contains(initial.Body.String(), `"modelFallbackIds":{"override":null,"effectiveValue":["env-fallback"],"source":"env"}`) {
		t.Fatalf("initial fallback = %d %s", initial.Code, initial.Body.String())
	}

	// Every referenced id must name an enabled model of this deployment.
	pool.ExpectQuery(`SELECT count\(\*\) FROM models`).
		WithArgs("deployment-a", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	rejected := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelFallbackIds":["bench-qwen","ghost"]}`)
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), `"code":"INVALID_DEPLOYMENT_SETTINGS"`) {
		t.Fatalf("unknown fallback id = %d %s", rejected.Code, rejected.Body.String())
	}

	// A valid chain is trimmed, deduplicated, stored, and echoed back.
	stored := []string{"bench-qwen", "bench-glm"}
	pool.ExpectQuery(`SELECT count\(\*\) FROM models WHERE deployment_id=\$1 AND enabled AND source_type='gateway' AND endpoint IS NOT NULL AND endpoint<>'' AND upstream_model IS NOT NULL AND upstream_model<>'' AND endpoint ~ '\^https\?://'`).
		WithArgs("deployment-a", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSettingsRead(pool, nil, &stored)
	updated := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelFallbackIds":[" bench-qwen ","bench-glm","bench-qwen"]}`)
	if updated.Code != http.StatusOK ||
		!strings.Contains(updated.Body.String(), `"modelFallbackIds":{"override":["bench-qwen","bench-glm"],"effectiveValue":["bench-qwen","bench-glm"],"source":"override"}`) {
		t.Fatalf("fallback chain update = %d %s", updated.Code, updated.Body.String())
	}

	// An explicit empty array disables failover without clearing the override.
	empty := []string{}
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSettingsRead(pool, nil, &empty)
	disabled := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelFallbackIds":[]}`)
	if disabled.Code != http.StatusOK ||
		!strings.Contains(disabled.Body.String(), `"modelFallbackIds":{"override":[],"effectiveValue":[],"source":"override"}`) {
		t.Fatalf("disabled fallback = %d %s", disabled.Code, disabled.Body.String())
	}

	// Null clears the override so the environment chain applies again.
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSettingsRead(pool, nil, nil)
	cleared := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelFallbackIds":null}`)
	if cleared.Code != http.StatusOK ||
		!strings.Contains(cleared.Body.String(), `"modelFallbackIds":{"override":null,"effectiveValue":["env-fallback"],"source":"env"}`) {
		t.Fatalf("cleared fallback = %d %s", cleared.Code, cleared.Body.String())
	}
}

func TestAdminDeploymentSettingsLifecycle(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config.ModelGatewayBaseURL = "http://env-gateway.example.com:8090/v1"
	handler := New(application).Handler()

	expectSettingsRead(pool, nil, nil)
	initial := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"override":null`) ||
		!strings.Contains(initial.Body.String(), `"effectiveValue":"http://env-gateway.example.com:8090/v1"`) ||
		!strings.Contains(initial.Body.String(), `"source":"env"`) {
		t.Fatalf("initial settings = %d %s", initial.Code, initial.Body.String())
	}

	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", "https://runtime-gateway.example.com/v1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	override := "https://runtime-gateway.example.com/v1"
	expectSettingsRead(pool, &override, nil)
	updated := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelGatewayBaseUrl":"https://runtime-gateway.example.com/v1"}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"override":"https://runtime-gateway.example.com/v1"`) ||
		!strings.Contains(updated.Body.String(), `"source":"override"`) {
		t.Fatalf("updated settings = %d %s", updated.Code, updated.Body.String())
	}

	expectSettingsRead(pool, &override, nil)
	current := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"effectiveValue":"https://runtime-gateway.example.com/v1"`) ||
		strings.Contains(current.Body.String(), "env-gateway") {
		t.Fatalf("override must win over the environment value = %d %s", current.Code, current.Body.String())
	}

	// An empty update changes nothing and performs no write.
	expectSettingsRead(pool, &override, nil)
	noop := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{}`)
	if noop.Code != http.StatusOK || !strings.Contains(noop.Body.String(), `"source":"override"`) {
		t.Fatalf("noop update = %d %s", noop.Code, noop.Body.String())
	}

	// Explicit null clears the runtime override and restores the env value.
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSettingsRead(pool, nil, nil)
	cleared := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelGatewayBaseUrl":null}`)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"override":null`) ||
		!strings.Contains(cleared.Body.String(), `"source":"env"`) ||
		!strings.Contains(cleared.Body.String(), `"effectiveValue":"http://env-gateway.example.com:8090/v1"`) {
		t.Fatalf("cleared settings = %d %s", cleared.Code, cleared.Body.String())
	}
}

func TestAdminDeploymentSettingsUnsetWithoutEnvironmentValue(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()

	expectSettingsRead(pool, nil, nil)
	settings := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"effectiveValue":null`) ||
		!strings.Contains(settings.Body.String(), `"source":"unset"`) {
		t.Fatalf("unset settings = %d %s", settings.Code, settings.Body.String())
	}
}

func TestAdminDeploymentSettingsValidation(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()

	rejected := []struct {
		name string
		body string
		code int
	}{
		{"relative URL", `{"modelGatewayBaseUrl":"/openai/v1"}`, http.StatusUnprocessableEntity},
		{"missing host", `{"modelGatewayBaseUrl":"https://"}`, http.StatusUnprocessableEntity},
		{"unsupported scheme", `{"modelGatewayBaseUrl":"ftp://gateway.example.com/v1"}`, http.StatusUnprocessableEntity},
		{"kubernetes service host", `{"modelGatewayBaseUrl":"http://higress.svc.cluster.local/v1"}`, http.StatusUnprocessableEntity},
		{"kubernetes service apex", `{"modelGatewayBaseUrl":"https://svc.cluster.local/v1"}`, http.StatusUnprocessableEntity},
		{"single label host", `{"modelGatewayBaseUrl":"http://gateway:8080/v1"}`, http.StatusUnprocessableEntity},
		{"loopback name", `{"modelGatewayBaseUrl":"http://localhost:8090/v1"}`, http.StatusUnprocessableEntity},
		{"empty value", `{"modelGatewayBaseUrl":""}`, http.StatusUnprocessableEntity},
		{"overlong value", `{"modelGatewayBaseUrl":"https://gateway.example.com/` + strings.Repeat("a", 2050) + `"}`, http.StatusUnprocessableEntity},
		{"non-string value", `{"modelGatewayBaseUrl":42}`, http.StatusBadRequest},
		{"unknown field", `{"modelGatewayBaseUrl":null,"unknown":true}`, http.StatusBadRequest},
		{"agent control relative URL", `{"agentControlBaseUrl":"/agents"}`, http.StatusUnprocessableEntity},
		{"agent control missing host", `{"agentControlBaseUrl":"https://"}`, http.StatusUnprocessableEntity},
		{"agent control unsupported scheme", `{"agentControlBaseUrl":"ftp://agents.example.com"}`, http.StatusUnprocessableEntity},
		{"agent control empty value", `{"agentControlBaseUrl":""}`, http.StatusUnprocessableEntity},
		{"agent control non-string value", `{"agentControlBaseUrl":7}`, http.StatusBadRequest},
	}
	for _, test := range rejected {
		response := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", test.body)
		want := `"code":"INVALID_DEPLOYMENT_SETTINGS"`
		if test.code == http.StatusBadRequest {
			want = `"code":"INVALID_REQUEST"`
		}
		if response.Code != test.code || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("%s = %d %s", test.name, response.Code, response.Body.String())
		}
	}

	// IPv4 loopback remains configurable outside production.
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", "http://127.0.0.1:8090/v1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	loopback := "http://127.0.0.1:8090/v1"
	expectSettingsRead(pool, &loopback, nil)
	accepted := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelGatewayBaseUrl":"http://127.0.0.1:8090/v1"}`)
	if accepted.Code != http.StatusOK || !strings.Contains(accepted.Body.String(), `"source":"override"`) {
		t.Fatalf("non-production loopback override = %d %s", accepted.Code, accepted.Body.String())
	}
}

func TestAdminDeploymentSettingsProductionLoopbackRejected(t *testing.T) {
	application, _, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config.Environment = "production"
	handler := New(application).Handler()

	for _, body := range []string{
		`{"modelGatewayBaseUrl":"http://127.0.0.1:8090/v1"}`,
		`{"modelGatewayBaseUrl":"http://[::1]:8090/v1"}`,
		`{"modelGatewayBaseUrl":"http://127.1.2.3/v1"}`,
	} {
		response := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", body)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"INVALID_DEPLOYMENT_SETTINGS"`) {
			t.Fatalf("production %s = %d %s", body, response.Code, response.Body.String())
		}
	}
}

func TestAdminDeploymentSettingsRejectsBeforeWriting(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()

	// A valid gateway field alongside an invalid fallback chain: the whole
	// request fails and nothing may be persisted. No INSERT expectation is
	// registered — pgxmock verifies on cleanup, so any executed write fails
	// this test.
	pool.ExpectQuery(`SELECT count\(\*\) FROM models`).
		WithArgs("deployment-a", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	rejected := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings",
		`{"modelGatewayBaseUrl":"https://runtime-gateway.example.com/v1","modelFallbackIds":["bench-qwen","ghost"]}`)
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), `"code":"INVALID_DEPLOYMENT_SETTINGS"`) {
		t.Fatalf("partial write = %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestAdminDeploymentSettingsRequirePermission(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()

	pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "user-a", "deployment.read").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	denied := adminRequest(handler, userToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("read without permission = %d %s", denied.Code, denied.Body.String())
	}

	pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "user-a", "deployment.read").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	expectSettingsRead(pool, nil, nil)
	allowed := adminRequest(handler, userToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), `"source":"unset"`) {
		t.Fatalf("read with permission = %d %s", allowed.Code, allowed.Body.String())
	}

	pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "user-a", "deployment.write").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	deniedWrite := adminRequest(handler, userToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"modelGatewayBaseUrl":null}`)
	if deniedWrite.Code != http.StatusForbidden || !strings.Contains(deniedWrite.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("write without permission = %d %s", deniedWrite.Code, deniedWrite.Body.String())
	}
}

func TestMetadataResolvesDeploymentSettingsOverride(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	application.Config.ModelGatewayBaseURL = "http://env-gateway.example.com:8090/v1"
	handler := New(application).Handler()

	metadataGateway := func() map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("metadata status = %d", response.Code)
		}
		var document map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		gateway, _ := document["modelGateway"].(map[string]any)
		return gateway
	}

	expectMetadataSettingsRead(pool, nil)
	if gateway := metadataGateway(); gateway["baseUrl"] != "http://env-gateway.example.com:8090/v1" {
		t.Fatalf("metadata without override = %#v", gateway)
	}

	override := "https://runtime-gateway.example.com/v1"
	expectMetadataSettingsRead(pool, &override)
	if gateway := metadataGateway(); gateway["baseUrl"] != "https://runtime-gateway.example.com/v1" {
		t.Fatalf("metadata with override = %#v", gateway)
	}

	// A transient settings lookup failure falls back to the environment value.
	pool.ExpectQuery(`SELECT model_gateway_base_url FROM deployment_settings`).
		WithArgs("deployment-a").WillReturnError(errors.New("database unavailable"))
	if gateway := metadataGateway(); gateway["baseUrl"] != "http://env-gateway.example.com:8090/v1" {
		t.Fatalf("metadata fallback = %#v", gateway)
	}

	// No environment value and no override keeps the gateway capability hidden.
	application.Config.ModelGatewayBaseURL = ""
	expectMetadataSettingsRead(pool, nil)
	if gateway := metadataGateway(); gateway != nil {
		t.Fatalf("metadata without any gateway configuration = %#v", gateway)
	}
}

func TestRequiredAdminPermissionForDeploymentSettings(t *testing.T) {
	if got := requiredAdminPermission(http.MethodGet, "/aep/v1/admin/deployment/settings"); len(got) != 1 || got[0] != "deployment.read" {
		t.Fatalf("settings read permission = %v, want [deployment.read]", got)
	}
	if got := requiredAdminPermission(http.MethodPut, "/aep/v1/admin/deployment/settings"); len(got) != 1 || got[0] != "deployment.write" {
		t.Fatalf("settings write permission = %v, want [deployment.write]", got)
	}
}

func TestValidateModelGatewayBaseURL(t *testing.T) {
	tests := []struct {
		value       string
		environment string
		valid       bool
	}{
		{"https://gateway.example.com/v1", "production", true},
		{"http://gateway.internal:8090/v1", "production", true},
		{"http://10.0.0.8:8090/v1", "production", true},
		{"https://GATEWAY.example.com./v1", "development", true},
		{" http://env-gateway.example.com/v1 ", "development", true},
		{"gateway.example.com/v1", "development", false},
		{"/openai/v1", "development", false},
		{"ftp://gateway.example.com/v1", "development", false},
		{"https://", "development", false},
		{"http://higress.svc.cluster.local/v1", "development", false},
		{"http://higress.svc.cluster.local./v1", "development", false},
		{"http://svc.cluster.local/v1", "development", false},
		{"http://gateway/v1", "development", false},
		{"http://localhost:8090/v1", "development", false},
		{"http://127.0.0.1:8090/v1", "development", true},
		{"http://127.0.0.1:8090/v1", "production", false},
		{"http://[::1]/v1", "production", false},
		{"http://[fd00::1]/v1", "production", true},
		{"", "development", false},
	}
	for _, test := range tests {
		problem := validateModelGatewayBaseURL(test.value, test.environment)
		if (problem == "") != test.valid {
			t.Errorf("validateModelGatewayBaseURL(%q, %q) valid = %v, want %v (problem %q)", test.value, test.environment, problem == "", test.valid, problem)
		}
	}
}

func TestAdminDeploymentSettingsAgentControlOverride(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config.AgentControlBaseURL = "http://env-agents.example.com"
	handler := New(application).Handler()

	expectSettingsRead(pool, nil, nil)
	initial := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/deployment/settings", "")
	if initial.Code != http.StatusOK ||
		!strings.Contains(initial.Body.String(), `"agentControlBaseUrl":{"override":null,"effectiveValue":"http://env-agents.example.com","source":"env"}`) {
		t.Fatalf("initial agent control setting = %d %s", initial.Code, initial.Body.String())
	}

	// Unlike the model gateway, cluster-internal hostnames are allowed: a
	// split deployment may front the agent surface with an internal ingress.
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", "http://aep-agent-control.aep-system.svc.cluster.local:8080").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	clusterInternal := "http://aep-agent-control.aep-system.svc.cluster.local:8080"
	expectSettingsRead(pool, &clusterInternal, nil)
	updated := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"agentControlBaseUrl":"http://aep-agent-control.aep-system.svc.cluster.local:8080"}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"source":"override"`) {
		t.Fatalf("cluster-internal agent control override = %d %s", updated.Code, updated.Body.String())
	}

	// Explicit null clears the override back to the environment value.
	pool.ExpectExec(`INSERT INTO deployment_settings`).
		WithArgs("deployment-a", nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSettingsRead(pool, nil, nil)
	cleared := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/deployment/settings", `{"agentControlBaseUrl":null}`)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"source":"env"`) {
		t.Fatalf("cleared agent control override = %d %s", cleared.Code, cleared.Body.String())
	}
}
