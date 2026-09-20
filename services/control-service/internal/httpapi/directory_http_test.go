package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

func newDirectoryHTTPApplication(t *testing.T) (*app.App, sqlmock.Sqlmock, pgxmock.PgxPoolIface, string, string) {
	t.Helper()
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	userToken, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-user", false, false, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	application.SetRuntimeDatabase(pool)
	t.Cleanup(func() {
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		pool.Close()
	})
	return application, mock, pool, adminToken, userToken
}

func TestCreateAgentRequiresMembershipAndValidReferences(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()

	// The agent path enforces the same membership floor as createUser.
	missing := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"helper-1","displayName":"Helper","password":"long-password-123","roleIds":[],"teamIds":[],"homeTeamId":"eng"}`)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "USER_RBAC_REQUIRED") {
		t.Fatalf("missing membership = %d %s", missing.Code, missing.Body.String())
	}

	now := time.Now().UTC()

	// Home team must exist before any grant check.
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	missingTeam := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"helper-1","displayName":"Helper","password":"long-password-123","roleIds":["member"],"teamIds":[],"homeTeamId":"missing"}`)
	if missingTeam.Code != http.StatusBadRequest || !strings.Contains(missingTeam.Body.String(), "INVALID_AGENT") {
		t.Fatalf("missing team = %d %s", missingTeam.Code, missingTeam.Body.String())
	}

	// Roles must exist.
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "ghost", 1).
		WillReturnRows(sqlmock.NewRows(roleHTTPColumns()))
	unknownRole := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"helper-1","displayName":"Helper","password":"long-password-123","roleIds":["ghost"],"teamIds":[],"homeTeamId":"eng"}`)
	if unknownRole.Code != http.StatusBadRequest || !strings.Contains(unknownRole.Body.String(), "INVALID_AGENT") {
		t.Fatalf("unknown role = %d %s", unknownRole.Code, unknownRole.Body.String())
	}

	// The prompt skill must exist; a forged id must not strand a half-created agent.
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	expectRoleRecord(mock, "member", "Member", "", true, nil, now)
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).
		WithArgs("ghost-skill", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at", "updated_at"}))
	unknownSkill := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"helper-1","displayName":"Helper","password":"long-password-123","roleIds":["member"],"teamIds":[],"homeTeamId":"eng","promptSkillId":"ghost-skill"}`)
	if unknownSkill.Code != http.StatusBadRequest || !strings.Contains(unknownSkill.Body.String(), "INVALID_AGENT") {
		t.Fatalf("unknown skill = %d %s", unknownSkill.Code, unknownSkill.Body.String())
	}
}

func TestCreateAgentDelegatedAdminCannotEscalateThroughAgent(t *testing.T) {
	application, mock, pool, _, userToken := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	// The OR gate probes agents.write first (denied), then users.write.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("deployment-a", "user-a", "agents.write").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("deployment-a", "user-a", "users.write").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	// The requested role carries a permission the grantor does not hold; the
	// existence check resolves it, the grant check must reject it.
	expectRoleRecord(mock, "operator", "Operator", "", true, []string{"roles.write"}, now)
	expectRoleRecord(mock, "operator", "Operator", "", true, []string{"roles.write"}, now)
	mock.ExpectQuery(`SELECT DISTINCT rp\.permission_id FROM role_permissions AS rp`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).
			AddRow("users.read").AddRow("users.write"))

	response := adminRequest(handler, userToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"helper-1","displayName":"Helper","password":"long-password-123","roleIds":["operator"],"teamIds":[],"homeTeamId":"eng"}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ROLE_GRANT_FORBIDDEN") {
		t.Fatalf("escalation via agent = %d %s", response.Code, response.Body.String())
	}
}

func TestListAgentsPaginatesByCursor(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	seen := time.Now().UTC().Add(-time.Minute)
	mock.ExpectQuery(`SELECT u\.id, u\.username, u\.display_name, u\.status, u\.kind`).
		WithArgs("deployment-a", "", "", false, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "kind",
			"display_title", "description", "avatar_object_key", "home_team_id", "prompt_skill_id",
			"ephemeral", "expires_at", "created_at", "updated_at", "last_seen_at"}).
			AddRow("agent-a", "helper-1", "Helper", "active", "agent", "Assistant", "", nil, "eng", nil, false, nil, now, now, &seen).
			AddRow("agent-b", "helper-2", "Helper 2", "active", "agent", nil, nil, nil, "eng", nil, true, now.Add(time.Hour), now, now, nil))

	response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/agents?limit=2", "")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"nextCursor":"agent-b"`) ||
		!strings.Contains(body, `"kind":"agent"`) || !strings.Contains(body, `"online":true`) || !strings.Contains(body, `"lastHeartbeatAt"`) ||
		!strings.Contains(body, `"ephemeral":false`) || !strings.Contains(body, `"ephemeral":true`) {
		t.Fatalf("agent list = %d %s", response.Code, body)
	}
}

func TestDataScopeRuleValidationRejectsUnimplementedKinds(t *testing.T) {
	application, _, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()

	departmentDefault := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules",
		`{"id":"rule-1","ruleKind":"department_default","subjectType":"user","subjectId":"user-a","resourceKind":"team","resourceId":"rd"}`)
	if departmentDefault.Code != http.StatusBadRequest || !strings.Contains(departmentDefault.Body.String(), "INVALID_DATA_SCOPE_RULE") {
		t.Fatalf("department_default = %d %s", departmentDefault.Code, departmentDefault.Body.String())
	}

	uppercaseKind := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules",
		`{"id":"rule-1","ruleKind":"management_scope","subjectType":"user","subjectId":"user-a","resourceKind":"Knowledge_Base","resourceId":"kb-1"}`)
	if uppercaseKind.Code != http.StatusBadRequest {
		t.Fatalf("uppercase resource kind = %d %s", uppercaseKind.Code, uppercaseKind.Body.String())
	}
}

func TestIdentitySourceValidationAndListing(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()

	vendorKind := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/identity-sources",
		`{"id":"src-1","kind":"feishu","displayName":"Feishu"}`)
	if vendorKind.Code != http.StatusBadRequest || !strings.Contains(vendorKind.Body.String(), "INVALID_IDENTITY_SOURCE") {
		t.Fatalf("vendor kind = %d %s", vendorKind.Code, vendorKind.Body.String())
	}

	arrayConfig := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/identity-sources",
		`{"id":"src-1","kind":"directory","displayName":"Directory","config":[1,2]}`)
	if arrayConfig.Code != http.StatusBadRequest {
		t.Fatalf("array config = %d %s", arrayConfig.Code, arrayConfig.Body.String())
	}

	secretConfig := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/identity-sources",
		`{"id":"src-1","kind":"directory","displayName":"Directory","config":{"password":"plain"}}`)
	if secretConfig.Code != http.StatusBadRequest || !strings.Contains(secretConfig.Body.String(), "credential") {
		t.Fatalf("secret config = %d %s", secretConfig.Code, secretConfig.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "identity_sources"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/identity-sources",
		`{"id":"src-1","kind":"directory","displayName":"Directory","config":{"vendor":"example"}}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"kind":"directory"`) {
		t.Fatalf("create source = %d %s", created.Code, created.Body.String())
	}
}

func TestListDataScopeRulesPaginatesByCursor(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "data_scope_rules" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at", "reason", "created_by", "created_at", "updated_at"}).
			AddRow("deployment-a", "rule-b", "management_scope", "role", "director", "team", "rd", nil, nil, "", "admin-user", now, now))

	response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope-rules?limit=1", "")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"nextCursor":"rule-b"`) || strings.Contains(body, "priority") {
		t.Fatalf("rule list = %d %s", response.Code, body)
	}
}

func TestCreateAgentEphemeralLifecycleValidation(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	const path = "/aep/v1/admin/agents"
	base := `"displayName":"Ephemeral Helper","password":"long-password-123","roleIds":["member"],"teamIds":[],"homeTeamId":"eng"`
	now := time.Now().UTC()

	// Pure payload coupling is validated before any database access.
	missing := adminRequest(handler, adminToken, http.MethodPost, path,
		`{"username":"ephemeral-1",`+base+`,"ephemeral":true}`)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "INVALID_AGENT") {
		t.Fatalf("ephemeral without expiry = %d %s", missing.Code, missing.Body.String())
	}
	resident := adminRequest(handler, adminToken, http.MethodPost, path,
		`{"username":"ephemeral-2",`+base+`,"expiresAt":"2030-01-01T00:00:00Z"}`)
	if resident.Code != http.StatusBadRequest || !strings.Contains(resident.Body.String(), "INVALID_AGENT") {
		t.Fatalf("resident with expiry = %d %s", resident.Code, resident.Body.String())
	}
	past := adminRequest(handler, adminToken, http.MethodPost, path,
		`{"username":"ephemeral-3",`+base+`,"ephemeral":true,"expiresAt":"2020-01-01T00:00:00Z"}`)
	if past.Code != http.StatusBadRequest {
		t.Fatalf("past expiry = %d %s", past.Code, past.Body.String())
	}

	// Unknown model: reference checks run before account creation.
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	expectRoleRecord(mock, "member", "Member", "", true, nil, now)
	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "missing-model", 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	unknownModel := adminRequest(handler, adminToken, http.MethodPost, path,
		`{"username":"ephemeral-5",`+base+`,"ephemeral":true,"expiresAt":"2030-01-01T00:00:00Z","modelIds":["missing-model"]}`)
	if unknownModel.Code != http.StatusBadRequest || !strings.Contains(unknownModel.Body.String(), "UNKNOWN_MODEL") {
		t.Fatalf("unknown model = %d %s", unknownModel.Code, unknownModel.Body.String())
	}
}

func TestDeleteAgentGuards(t *testing.T) {
	application, mock, pool, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	// Missing profile → 404.
	mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).WithArgs("deployment-a", "ghost", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id"}))
	missing := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/agents/ghost", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing agent delete = %d %s", missing.Code, missing.Body.String())
	}

	// Existing profile with live sessions → 409 AGENT_HAS_SESSIONS.
	mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).WithArgs("deployment-a", "agent-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "display_title", "description",
			"avatar_object_key", "home_team_id", "prompt_skill_id", "ephemeral", "expires_at", "created_at", "updated_at"}).
			AddRow("deployment-a", "agent-a", "Assistant", "", nil, "eng", nil, true, now.Add(time.Hour), now, now))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM user_sessions`).WithArgs("deployment-a", "agent-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()
	blocked := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/agents/agent-a", "")
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "AGENT_HAS_SESSIONS") {
		t.Fatalf("session-blocked delete = %d %s", blocked.Code, blocked.Body.String())
	}
	_ = pool
}

func TestAgentsWriteIsDelegableIndependentlyOfUsersWrite(t *testing.T) {
	// requiredAdminPermission returns the OR set for agent writes and
	// single-permission sets elsewhere; the route gate honors any one.
	if got := requiredAdminPermission(http.MethodPost, "/aep/v1/admin/agents"); len(got) != 2 ||
		got[0] != "agents.write" || got[1] != "users.write" {
		t.Fatalf("agent write gate = %v", got)
	}
	if got := requiredAdminPermission(http.MethodGet, "/aep/v1/admin/agents"); len(got) != 1 || got[0] != "users.read" {
		t.Fatalf("agent read gate = %v", got)
	}
	if got := requiredAdminPermission(http.MethodPost, "/aep/v1/admin/users"); len(got) != 1 || got[0] != "users.write" {
		t.Fatalf("human write gate = %v", got)
	}
	if got := requiredAdminPermission(http.MethodGet, "/aep/v1/admin/nothing"); got != nil {
		t.Fatalf("unknown route = %v", got)
	}
}

func TestCreateAgentEnforcesResidentQuota(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	application.Config.MaxResidentAgents = 1
	handler := New(application).Handler()
	now := time.Now().UTC()

	// One resident agent already exists: a second is rejected with 409.
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	expectRoleRecord(mock, "member", "Member", "", true, nil, now)
	mock.ExpectQuery(`SELECT count\(\*\) FROM agent_profiles`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	quota := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"over-quota","displayName":"Over","password":"long-password-123","roleIds":["member"],"teamIds":[],"homeTeamId":"eng"}`)
	if quota.Code != http.StatusConflict || !strings.Contains(quota.Body.String(), "AGENT_QUOTA_EXCEEDED") {
		t.Fatalf("quota = %d %s", quota.Code, quota.Body.String())
	}

	// Ephemeral accounts bypass the quota: the count query is never issued
	// (sqlmock fails any unexpected query with a 500), so reaching the
	// validation error below proves the quota check was skipped.
	application.Config.MaxResidentAgents = 1
	expectTeamRecord(mock, "eng", "Engineering", "", true, 3, now)
	expectRoleRecord(mock, "member", "Member", "", true, nil, now)
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).
		WithArgs("ghost-skill", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at", "updated_at"}))
	ephemeral := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/agents",
		`{"username":"fork-quota","displayName":"Fork","password":"long-password-123","roleIds":["member"],"teamIds":[],"homeTeamId":"eng","ephemeral":true,"expiresAt":"2030-01-01T00:00:00Z","promptSkillId":"ghost-skill"}`)
	if ephemeral.Code != http.StatusBadRequest || !strings.Contains(ephemeral.Body.String(), "INVALID_AGENT") {
		t.Fatalf("ephemeral over quota = %d %s", ephemeral.Code, ephemeral.Body.String())
	}
}
