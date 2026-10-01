package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The directory/resource CRUD surface: each cold path gets one happy-path
// walk (and an error branch where it is one statement away), in the same
// sqlmock + GORM style as the rest of the package.

func TestIdentitySourceAndMappingLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`.*"identity_sources".*`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	source, err := deployment.CreateIdentitySource(context.Background(), IdentitySource{
		ID: "wecom", Kind: "wecom", DisplayName: "WeCom", Enabled: true,
	})
	if err != nil || source.ID != "wecom" || source.DeploymentID != "deployment-a" {
		t.Fatalf("CreateIdentitySource() = %#v, %v", source, err)
	}

	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"deployment_id", "id", "kind", "display_name", "config", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "wecom", "wecom", "WeCom", nil, true, now, now)
	}
	mock.ExpectQuery(`SELECT \* FROM "identity_sources" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a", 20).WillReturnRows(rows())
	sources, err := deployment.ListIdentitySources(context.Background(), "", 20)
	if err != nil || len(sources) != 1 || sources[0].ID != "wecom" {
		t.Fatalf("ListIdentitySources() = %#v, %v", sources, err)
	}

	// Cursor path: id > cursor narrows the page.
	mock.ExpectQuery(`SELECT \* FROM "identity_sources" WHERE deployment_id = \$1 AND id > \$2 ORDER BY id`).
		WithArgs("deployment-a", "prev", 20).WillReturnRows(rows())
	if _, err := deployment.ListIdentitySources(context.Background(), "prev", 20); err != nil {
		t.Fatalf("cursor list: %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`.*"identity_mappings".*`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err = deployment.UpsertIdentityMapping(context.Background(), IdentityMapping{
		SourceID: "wecom", ExternalSubjectType: "user", ExternalID: "ext-1", LocalSubjectID: "user-a", Status: "linked",
	})
	if err != nil {
		t.Fatalf("UpsertIdentityMapping(): %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "identity_mappings"`).
		WithArgs("deployment-a", "wecom", "user", "ext-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := deployment.DeleteIdentityMapping(context.Background(), "wecom", "user", "ext-1"); err != nil {
		t.Fatalf("DeleteIdentityMapping(): %v", err)
	}
}

func TestDataScopeRuleCRUD(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")

	mock.ExpectBegin()
	mock.ExpectExec(`.*"data_scope_rules".*`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	rule, err := deployment.CreateDataScopeRule(context.Background(), DataScopeRule{
		ID: "rule-1", RuleKind: "grant", SubjectType: "team", SubjectID: "rd", ResourceKind: "skill", ResourceID: "s1", Reason: "needs it",
	})
	if err != nil || rule.DeploymentID != "deployment-a" {
		t.Fatalf("CreateDataScopeRule() = %#v, %v", rule, err)
	}

	rows := sqlmock.NewRows([]string{"deployment_id", "id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at", "reason"}).
		AddRow("deployment-a", "rule-1", "grant", "team", "rd", "skill", "s1", nil, nil, "needs it")
	mock.ExpectQuery(`SELECT \* FROM "data_scope_rules" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a", 20).WillReturnRows(rows)
	rules, err := deployment.ListDataScopeRules(context.Background(), "", 20)
	if err != nil || len(rules) != 1 || rules[0].ID != "rule-1" {
		t.Fatalf("ListDataScopeRules() = %#v, %v", rules, err)
	}

	single := sqlmock.NewRows([]string{"deployment_id", "id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at", "reason"}).
		AddRow("deployment-a", "rule-1", "grant", "team", "rd", "skill", "s1", nil, nil, "needs it")
	mock.ExpectQuery(`SELECT \* FROM "data_scope_rules" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "rule-1", 1).WillReturnRows(single)
	if _, err := deployment.GetDataScopeRule(context.Background(), "rule-1"); err != nil {
		t.Fatalf("GetDataScopeRule(): %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "data_scope_rules"`).
		WithArgs("deployment-a", "rule-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := deployment.DeleteDataScopeRule(context.Background(), "rule-1"); err != nil {
		t.Fatalf("DeleteDataScopeRule(): %v", err)
	}
}

func TestAgentDirectoryQueries(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`.*agent_profiles.*`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err := deployment.UpsertAgentProfile(context.Background(), AgentProfile{
		UserID: "agent-1", DisplayTitle: "助手", Description: "帮忙的", HomeTeamID: "rd", Ephemeral: false,
	})
	if err != nil {
		t.Fatalf("UpsertAgentProfile(): %v", err)
	}

	one := sqlmock.NewRows([]string{"deployment_id", "user_id", "display_title", "description", "avatar_object_key", "home_team_id", "prompt_skill_id", "ephemeral", "expires_at", "created_at", "updated_at"}).
		AddRow("deployment-a", "agent-1", "助手", "帮忙的", nil, "rd", nil, false, nil, now, now)
	mock.ExpectQuery(`SELECT \* FROM "agent_profiles" WHERE deployment_id = \$1 AND user_id = \$2`).
		WithArgs("deployment-a", "agent-1", 1).WillReturnRows(one)
	got, err := deployment.GetAgentProfile(context.Background(), "agent-1")
	if err != nil || got.DisplayTitle != "助手" {
		t.Fatalf("GetAgentProfile() = %#v, %v", got, err)
	}

	mock.ExpectQuery(`FROM users u LEFT JOIN agent_profiles`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "kind", "display_title", "description", "avatar_object_key", "home_team_id", "prompt_skill_id", "ephemeral", "expires_at", "created_at", "updated_at", "last_seen_at"}).
			AddRow("agent-1", "agent-1", "助手", "active", "agent", "助手", "帮忙的", nil, "rd", nil, false, nil, now, now, nil))
	entries, err := deployment.ListAgentDirectory(context.Background(), "", 20, false)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListAgentDirectory() = %#v, %v", entries, err)
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM agent_profiles`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	count, err := deployment.CountResidentAgents(context.Background())
	if err != nil || count != 3 {
		t.Fatalf("CountResidentAgents() = %d, %v", count, err)
	}
}

func TestDeleteAgentGuardsLiveSessions(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM user_sessions`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectRollback()
	if err := deployment.DeleteAgent(context.Background(), "agent-1"); err == nil {
		t.Fatal("deleting an agent with live sessions must fail")
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM user_sessions`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	for _, stmt := range []string{
		`DELETE FROM user_role_bindings`,
		`DELETE FROM user_team_bindings`,
		`DELETE FROM model_assignments`,
		`DELETE FROM skill_assignments`,
		`DELETE FROM credential_assignments`,
		`DELETE FROM data_scope_rules`,
		`DELETE FROM agent_profiles`,
		`DELETE FROM users`,
	} {
		mock.ExpectExec(stmt).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	if err := deployment.DeleteAgent(context.Background(), "agent-1"); err != nil {
		t.Fatalf("DeleteAgent(): %v", err)
	}
}

func TestUserAndRBACDeletes(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "users"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := deployment.DeleteUser(context.Background(), "user-a"); err != nil {
		t.Fatalf("DeleteUser(): %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM "users"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "roles"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "teams"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`DELETE FROM "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := deployment.ReplaceUserRBAC(context.Background(), "user-a", []string{"member"}, []string{"rd"}); err != nil {
		t.Fatalf("ReplaceUserRBAC(): %v", err)
	}
}

func TestResourceListHelpers(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")

	mock.ExpectQuery(`SELECT \* FROM "skills" ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "enabled", "created_at", "updated_at"}))
	if _, err := store.ListSkills(context.Background()); err != nil {
		t.Fatalf("ListSkills(): %v", err)
	}

	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "service", "auth_type", "created_at", "updated_at"}))
	if _, err := deployment.ListCredentials(context.Background()); err != nil {
		t.Fatalf("ListCredentials(): %v", err)
	}

	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "display_name", "created_at", "updated_at"}))
	if _, err := deployment.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels(): %v", err)
	}
}

func TestGetUserByID(t *testing.T) {
	store, mock := newMockStore(t)
	deployment := store.Deployment("deployment-a")
	now := time.Now().UTC()

	rows := sqlmock.NewRows([]string{"deployment_id", "id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "kind", "created_at", "updated_at"}).
		AddRow("deployment-a", "user-a", "alice", "Alice", "a@x.test", "hash", "active", false, false, "human", now, now)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "user-a", 1).WillReturnRows(rows)
	user, err := deployment.GetUserByID(context.Background(), "user-a")
	if err != nil || user.Username != "alice" {
		t.Fatalf("GetUserByID() = %#v, %v", user, err)
	}
}
