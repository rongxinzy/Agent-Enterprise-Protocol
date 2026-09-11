package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func newStoreBackedHTTPApplication(t *testing.T) (*app.App, sqlmock.Sqlmock, string) {
	t.Helper()
	application, adminToken, _ := testHTTPApplication(t)
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	ormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	application.Store = repository.New(ormDB)
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = sqlDB.Close()
	})
	return application, mock, adminToken
}

func adminRequest(handler http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestAdminResourceListRoutes(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT \* FROM "permissions" ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "description"}).
			AddRow("models.read", "Read models").AddRow("skills.read", "Read Skills"))
	permissions := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/permissions", "")
	if permissions.Code != http.StatusOK || !strings.Contains(permissions.Body.String(), `"id":"models.read"`) {
		t.Fatalf("permissions response = %d %s", permissions.Code, permissions.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 2).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "operator", "Operator", "Operates models", false, true, now, now).
			AddRow("deployment-a", "reviewer", "Reviewer", "Reviews changes", false, true, now, now))
	mock.ExpectQuery(`SELECT \* FROM "role_permissions" WHERE deployment_id = \$1 AND role_id IN \(\$2,\$3\) ORDER BY role_id, permission_id`).
		WithArgs("deployment-a", "operator", "reviewer").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "role_id", "permission_id"}).
			AddRow("deployment-a", "operator", "models.read"))
	roles := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/roles?limit=1", "")
	if roles.Code != http.StatusOK || !strings.Contains(roles.Body.String(), `"nextCursor":"operator"`) || strings.Contains(roles.Body.String(), `"id":"reviewer"`) {
		t.Fatalf("roles response = %d %s", roles.Code, roles.Body.String())
	}

	mock.ExpectQuery(`SELECT teams\.\*, COUNT\(user_team_bindings\.user_id\) AS member_count FROM "teams" LEFT JOIN user_team_bindings`).
		WithArgs("deployment-a", 2).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at", "member_count"}).
			AddRow("deployment-a", "engineering", "Engineering", "Builders", false, true, now, now, 4))
	teams := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/teams?limit=1", "")
	if teams.Code != http.StatusOK || !strings.Contains(teams.Body.String(), `"memberCount":4`) {
		t.Fatalf("teams response = %d %s", teams.Code, teams.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "skills" ORDER BY id LIMIT \$1`).
		WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "enabled", "created_at", "updated_at"}).
			AddRow("skill-a", "Skill A", "Description", true, now, now).
			AddRow("skill-b", "Skill B", "Description", true, now, now))
	mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).
		WithArgs("skill-a").
		WillReturnRows(sqlmock.NewRows([]string{"skill_id", "version", "object_key", "sha256", "size_bytes", "published", "created_at", "published_at"}).
			AddRow("skill-a", "1.0.0", "skills/a.zip", "sha256:a", 128, true, now, now))
	skills := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skills?limit=1", "")
	if skills.Code != http.StatusOK || !strings.Contains(skills.Body.String(), `"nextCursor":"skill-a"`) || !strings.Contains(skills.Body.String(), `"state":"published"`) {
		t.Fatalf("skills response = %d %s", skills.Code, skills.Body.String())
	}
}

func TestAdminModelAndCredentialReadRoutes(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	modelColumns := []string{
		"deployment_id", "id", "display_name", "source_type", "protocol", "endpoint", "upstream_model", "local_model_ref", "credential_id",
		"capabilities", "reasoning_compatibility", "context_window", "is_default", "enabled", "created_at", "updated_at",
	}
	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 2).
		WillReturnRows(sqlmock.NewRows(modelColumns).
			AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://gateway/v1", "upstream-a", nil, nil, `{text,reasoning}`, nil, 8192, true, true, now, now))
	models := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/models?limit=1", "")
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"id":"chat-a"`) || !strings.Contains(models.Body.String(), `"contextWindow":8192`) {
		t.Fatalf("models response = %d %s", models.Code, models.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 2).
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_id", "id", "name", "service", "type", "delivery_mode", "encrypted_value", "nonce", "key_id", "masked_value", "enabled", "created_at", "updated_at", "rotated_at",
		}).AddRow("deployment-a", "credential-a", "Provider", "deepseek", "api_key", "server_only", []byte("cipher"), []byte("nonce"), "key-a", "sk-***", true, now, now, now))
	credentials := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/credentials?limit=1", "")
	if credentials.Code != http.StatusOK || !strings.Contains(credentials.Body.String(), `"maskedValue":"sk-***"`) || strings.Contains(credentials.Body.String(), "cipher") {
		t.Fatalf("credentials response = %d %s", credentials.Code, credentials.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "model_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "model_id", "subject_type", "subject_id", "created_at"}).
			AddRow("assignment-a", "deployment-a", "chat-a", "team", "engineering", now))
	assignments := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/model-assignments", "")
	if assignments.Code != http.StatusOK || !strings.Contains(assignments.Body.String(), `"resourceId":"chat-a"`) {
		t.Fatalf("assignments response = %d %s", assignments.Code, assignments.Body.String())
	}
}

func TestAdminResourceDeleteRoutes(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "skills" WHERE id = \$1`).WithArgs("skill-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	skill := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/skills/skill-a", "")
	if skill.Code != http.StatusNoContent {
		t.Fatalf("delete Skill response = %d %s", skill.Code, skill.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "chat-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	model := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/models/chat-a", "")
	if model.Code != http.StatusNoContent {
		t.Fatalf("delete model response = %d %s", model.Code, model.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "credential-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	credential := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/credentials/credential-a", "")
	if credential.Code != http.StatusNoContent {
		t.Fatalf("delete Credential response = %d %s", credential.Code, credential.Body.String())
	}
}

func TestAdminSemanticValidationRoutes(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		code   string
	}{
		{name: "empty role patch", method: http.MethodPatch, path: "/aep/v1/admin/roles/operator", body: `{}`, code: "INVALID_ROLE"},
		{name: "empty Team patch", method: http.MethodPatch, path: "/aep/v1/admin/teams/engineering", body: `{}`, code: "INVALID_TEAM"},
		{name: "conflicting Skill state", method: http.MethodPatch, path: "/aep/v1/admin/skills/skill-a", body: `{"state":"active","enabled":false}`, code: "INVALID_SKILL_STATE"},
		{name: "invalid model", method: http.MethodPost, path: "/aep/v1/admin/models", body: `{"id":"chat-a"}`, code: "INVALID_MODEL"},
		{name: "empty model patch", method: http.MethodPatch, path: "/aep/v1/admin/models/chat-a", body: `{}`, code: "INVALID_MODEL"},
		{name: "invalid model subject", method: http.MethodPost, path: "/aep/v1/admin/model-assignments", body: `{"modelId":"chat-a","subject":{"type":"agent","id":"agent-a"}}`, code: "INVALID_SUBJECT"},
		{name: "empty Credential patch", method: http.MethodPatch, path: "/aep/v1/admin/credentials/credential-a", body: `{}`, code: "INVALID_CREDENTIAL"},
		{name: "empty Credential rotation", method: http.MethodPost, path: "/aep/v1/admin/credentials/credential-a/rotate", body: `{"value":""}`, code: "INVALID_CREDENTIAL"},
		{name: "invalid Credential subject", method: http.MethodPost, path: "/aep/v1/admin/credential-assignments", body: `{"credentialId":"credential-a","subject":{"type":"agent","id":"agent-a"}}`, code: "INVALID_SUBJECT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := adminRequest(handler, adminToken, test.method, test.path, test.body)
			if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			var problem map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem["code"] != test.code || problem["requestId"] == "" {
				t.Fatalf("problem = %#v, %v", problem, err)
			}
		})
	}
}
