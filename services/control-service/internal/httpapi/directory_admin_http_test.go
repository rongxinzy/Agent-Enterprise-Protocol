package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// Admin directory surface: identity sources, their mappings, and data-scope
// rules — route-level walks against the store mock in the package style.

func identitySourceColumns() []string {
	return []string{"deployment_id", "id", "kind", "display_name", "config", "enabled", "created_at", "updated_at"}
}

func identityMappingColumns() []string {
	return []string{"deployment_id", "source_id", "external_subject_type", "external_id", "local_subject_id", "status", "linked_at", "last_synced_at"}
}

func dataScopeColumns() []string {
	return []string{"deployment_id", "id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at", "reason", "created_by", "created_at", "updated_at"}
}

func TestIdentitySourceAdminRoutes(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("create validates the payload", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/identity-sources", `{"kind":"directory"}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_IDENTITY_SOURCE") {
			t.Fatalf("create invalid = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("create persists and returns the source", func(t *testing.T) {
		// The identifier is derived from the display name; the uniqueness
		// probe runs before the insert.
		mock.ExpectQuery(`SELECT \* FROM "identity_sources" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
			WithArgs("deployment-a", "wecom", 1).
			WillReturnRows(sqlmock.NewRows(identitySourceColumns()))
		mock.ExpectBegin()
		mock.ExpectExec(`.*"identity_sources".*`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/identity-sources",
			`{"kind":"directory","displayName":"WeCom"}`)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"id":"wecom"`) {
			t.Fatalf("create = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("list returns the page", func(t *testing.T) {
		rows := sqlmock.NewRows(identitySourceColumns()).
			AddRow("deployment-a", "wecom", "directory", "WeCom", nil, true, now, now)
		mock.ExpectQuery(`SELECT \* FROM "identity_sources"`).
			WithArgs("deployment-a", 50).WillReturnRows(rows)
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/identity-sources", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "wecom") {
			t.Fatalf("list = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestIdentityMappingAdminRoutes(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("upsert validates the payload", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/identity-sources/wecom/mappings",
			`{"externalId":"","localSubjectId":"user-a"}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_IDENTITY_MAPPING") {
			t.Fatalf("upsert invalid = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("upsert accepts a full mapping", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "identity_sources"`).
			WithArgs("deployment-a", "wecom", 1).
			WillReturnRows(sqlmock.NewRows(identitySourceColumns()).AddRow("deployment-a", "wecom", "directory", "WeCom", nil, true, now, now))
		mock.ExpectBegin()
		mock.ExpectExec(`.*"identity_mappings".*`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/identity-sources/wecom/mappings",
			`{"externalSubjectType":"user","externalId":"ext-1","localSubjectId":"user-a"}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("upsert = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("list filters by the source", func(t *testing.T) {
		rows := sqlmock.NewRows(identityMappingColumns()).
			AddRow("deployment-a", "wecom", "user", "ext-1", "user-a", "linked", now, nil)
		mock.ExpectQuery(`SELECT \* FROM "identity_mappings"`).
			WithArgs("deployment-a", "wecom", 50).WillReturnRows(rows)
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/identity-sources/wecom/mappings", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ext-1") {
			t.Fatalf("list mappings = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("delete removes the mapping", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM "identity_mappings"`).
			WithArgs("deployment-a", "wecom", "user", "ext-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodDelete,
			"/aep/v1/admin/identity-sources/wecom/mappings/user/ext-1", "")
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete mapping = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestDataScopeRuleAdminRoutes(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()

	t.Run("create validates the payload", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules", `{}`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("create invalid = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("create persists the grant", func(t *testing.T) {
		// The identifier is derived from the reason; probe before insert.
		mock.ExpectQuery(`SELECT \* FROM "data_scope_rules" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
			WithArgs("deployment-a", "team-needs-reporting", 1).
			WillReturnRows(sqlmock.NewRows(dataScopeColumns()))
		mock.ExpectBegin()
		mock.ExpectExec(`.*"data_scope_rules".*`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules",
			`{"ruleKind":"exception_grant","subjectType":"team","subjectId":"rd","resourceKind":"skill","resourceId":"reporting","reason":"team needs reporting"}`)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "team-needs-reporting") {
			t.Fatalf("create = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("missing rule is a 404", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "data_scope_rules"`).
			WithArgs("deployment-a", "rule-x", 1).WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope-rules/rule-x", "")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "RESOURCE_NOT_FOUND") {
			t.Fatalf("get missing = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("get returns the rule", func(t *testing.T) {
		rows := sqlmock.NewRows(dataScopeColumns()).
			AddRow("deployment-a", "rule-1", "exception_grant", "team", "rd", "skill", "reporting", nil, nil, "team needs reporting", "admin", time.Now().UTC(), time.Now().UTC())
		mock.ExpectQuery(`SELECT \* FROM "data_scope_rules"`).
			WithArgs("deployment-a", "rule-1", 1).WillReturnRows(rows)
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope-rules/rule-1", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "reporting") {
			t.Fatalf("get = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("delete removes the rule", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM "data_scope_rules"`).
			WithArgs("deployment-a", "rule-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/data-scope-rules/rule-1", "")
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestListDataScopeRulesRoute(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	rows := sqlmock.NewRows(dataScopeColumns())
	mock.ExpectQuery(`SELECT \* FROM "data_scope_rules"`).
		WithArgs("deployment-a", 50).WillReturnRows(rows)
	response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope-rules", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list rules = %d %s", response.Code, response.Body.String())
	}
}

// Compile-time reference so the repository import stays meaningful if the
// assertions above evolve.
var _ = repository.ErrNotFound

func TestUpdateAgentProfileRoute(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	profileRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"deployment_id", "user_id", "display_title", "description", "avatar_object_key", "home_team_id", "prompt_skill_id", "ephemeral", "expires_at", "created_at", "updated_at"}).
			AddRow("deployment-a", "agent-1", "助手", "帮忙的", nil, "rd", nil, false, nil, now, now)
	}

	t.Run("missing profile is a 404", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).
			WithArgs("deployment-a", "agent-1", 1).WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/agents/agent-1/profile", `{"displayTitle":"x"}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("patch missing = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("title patch persists", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).
			WithArgs("deployment-a", "agent-1", 1).WillReturnRows(profileRows())
		mock.ExpectBegin()
		mock.ExpectExec(`.*"agent_profiles".*`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/agents/agent-1/profile", `{"displayTitle":"新标题"}`)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "新标题") {
			t.Fatalf("patch = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("home team patch validates the team exists", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).
			WithArgs("deployment-a", "agent-1", 1).WillReturnRows(profileRows())
		mock.ExpectQuery(`FROM "teams"`).
			WithArgs("deployment-a").WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/agents/agent-1/profile", `{"homeTeamId":"ghost-team"}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "home team does not exist") {
			t.Fatalf("patch bad team = %d %s", response.Code, response.Body.String())
		}
	})
}
