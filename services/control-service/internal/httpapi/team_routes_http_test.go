package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"gorm.io/gorm"
)

func teamJoinColumns() []string {
	return []string{"deployment_id", "id", "name", "description", "built_in", "enabled", "parent_team_id", "path", "depth", "created_at", "updated_at", "member_count"}
}

func teamJoinRows(now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(teamJoinColumns()).
		AddRow("deployment-a", "engineering", "Engineering", "Builds things", false, true, nil, "/engineering", 0, now, now, 3)
}

func TestCreateTeamRoute(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("invalid payload is rejected", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams", `{"name":""}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_TEAM") {
			t.Fatalf("invalid = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("undecodable payload writes exactly one problem document", func(t *testing.T) {
		// Unknown fields (the retired client-provided id shape) must fail
		// decode once; the response body is a single RFC 9457 problem, not
		// the decode problem concatenated with a validation problem.
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams", `{"id":"legacy","name":"Platform Team"}`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d %s", response.Code, response.Body.String())
		}
		var problem struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Code != "INVALID_REQUEST" {
			t.Fatalf("body is not a single problem document: %v %s", err, response.Body.String())
		}
	})

	t.Run("root team persists with its materialized path", func(t *testing.T) {
		// "Platform Team" slugifies to the generated id platform-team; the
		// probe lists existing teams before the insert.
		mock.ExpectQuery(`FROM "teams"`).
			WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(teamJoinColumns()))
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO "teams"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams",
			`{"name":"Platform Team"}`)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"id":"platform-team"`) || !strings.Contains(response.Body.String(), `"path":"/platform-team"`) {
			t.Fatalf("create = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("child team resolves its parent and nests the path", func(t *testing.T) {
		// The generated id (backend-team) is probed first, then the parent
		// (engineering) is resolved from the same listing.
		mock.ExpectQuery(`FROM "teams"`).
			WithArgs("deployment-a").WillReturnRows(teamJoinRows(now))
		mock.ExpectQuery(`FROM "teams"`).
			WithArgs("deployment-a").WillReturnRows(teamJoinRows(now))
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO "teams"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams",
			`{"name":"Backend Team","parentId":"engineering"}`)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"path":"/engineering/backend-team"`) {
			t.Fatalf("create child = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("missing parent is rejected", func(t *testing.T) {
		mock.ExpectQuery(`FROM "teams"`).
			WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(teamJoinColumns()))
		mock.ExpectQuery(`FROM "teams"`).
			WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(teamJoinColumns()))
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams",
			`{"name":"Backend Team","parentId":"engineering"}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "parent team does not exist") {
			t.Fatalf("orphan = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestDeleteTeamRoute(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("built-in teams are protected", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "teams"`).
			WithArgs("deployment-a", "everyone", 1).
			WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "parent_team_id", "path", "depth", "created_at", "updated_at"}).
				AddRow("deployment-a", "everyone", "Everyone", "All users", true, true, nil, "/everyone", 0, now, now))
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/teams/everyone", "")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "BUILT_IN_RESOURCE") {
			t.Fatalf("builtin = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("missing team is a 404", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "teams"`).
			WithArgs("deployment-a", "ghost", 1).WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/teams/ghost", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("deletable team is removed", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "teams"`).
			WithArgs("deployment-a", "engineering", 1).
			WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "parent_team_id", "path", "depth", "created_at", "updated_at"}).
				AddRow("deployment-a", "engineering", "Engineering", "Builds things", false, true, nil, "/engineering", 0, now, now))
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM "teams"`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/teams/engineering", "")
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestUpdateRoleRoute(t *testing.T) {
	application, mock, _, adminToken, _ := newDirectoryHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("empty patch is rejected", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/roles/operator", `{}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "At least one role field") {
			t.Fatalf("empty = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("rename walks the update flow", func(t *testing.T) {
		roleRows := func() *sqlmock.Rows {
			return sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
				AddRow("deployment-a", "operator", "Operator", "Operates", false, true, now, now)
		}
		// Transaction: take the role, apply the name update, commit.
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2`).
			WithArgs("deployment-a", "operator", 1).WillReturnRows(roleRows())
		mock.ExpectExec(`UPDATE "roles"`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		// Post-transaction read: the record with its permissions.
		mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2`).
			WithArgs("deployment-a", "operator", 1).WillReturnRows(roleRows())
		mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 ORDER BY id`).
			WithArgs("deployment-a").WillReturnRows(roleRows())
		mock.ExpectQuery(`SELECT \* FROM "role_permissions" WHERE deployment_id = \$1 AND role_id IN`).
			WithArgs("deployment-a", "operator").
			WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "role_id", "permission_id"}))
		response := userRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/roles/operator", `{"name":"运维"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("rename = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestDataScopeContextRoute(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	t.Run("userId is required", func(t *testing.T) {
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope/context", "")
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "USER_REQUIRED") {
			t.Fatalf("missing = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("unknown user is a 404", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "users"`).
			WithArgs("deployment-a", "ghost", 1).WillReturnError(gorm.ErrRecordNotFound)
		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope/context?userId=ghost", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("unknown = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("known user returns the resolved context", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "users"`).
			WithArgs("deployment-a", "user-a", 1).
			WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", nil, "hash", "active", false, false, "human", now, now))
		mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
			WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}))
		pool.ExpectQuery(`JOIN user_team_bindings`).
			WithArgs("deployment-a", "user-a").WillReturnRows(pgxmock.NewRows([]string{"id", "parent", "path"}))
		pool.ExpectQuery(`FROM teams WHERE deployment_id`).
			WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"id", "parent", "path"}))
		pool.ExpectQuery(`FROM data_scope_rules`).
			WithArgs("deployment-a", "user-a", pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at"}))

		response := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-scope/context?userId=user-a", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "principalId") {
			t.Fatalf("context = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestDeleteAgentRoute(t *testing.T) {
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
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/agents/agent-1", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("agent with live sessions is refused", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).
			WithArgs("deployment-a", "agent-1", 1).WillReturnRows(profileRows())
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT count\(\*\) FROM user_sessions`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectRollback()
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/agents/agent-1", "")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "AGENT_HAS_SESSIONS") {
			t.Fatalf("live sessions = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("deletable agent is removed", func(t *testing.T) {
		mock.ExpectQuery(`SELECT \* FROM "agent_profiles"`).
			WithArgs("deployment-a", "agent-1", 1).WillReturnRows(profileRows())
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT count\(\*\) FROM user_sessions`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		for _, stmt := range []string{
			`DELETE FROM user_role_bindings`, `DELETE FROM user_team_bindings`,
			`DELETE FROM model_assignments`, `DELETE FROM skill_assignments`,
			`DELETE FROM credential_assignments`, `DELETE FROM data_scope_rules`,
			`DELETE FROM agent_profiles`, `DELETE FROM users`,
		} {
			mock.ExpectExec(stmt).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		response := userRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/agents/agent-1", "")
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete = %d %s", response.Code, response.Body.String())
		}
	})
}
