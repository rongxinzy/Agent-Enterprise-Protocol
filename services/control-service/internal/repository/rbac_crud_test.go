package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRBACReadAndUpdateLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT \* FROM "permissions" ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "description"}).AddRow("models.read", "Read models"))
	permissions, err := store.ListPermissions(context.Background())
	if err != nil || len(permissions) != 1 || permissions[0].ID != "models.read" {
		t.Fatalf("ListPermissions() = %#v, %v", permissions, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "operator", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "operator", "Operator", "Operates models", false, true, now, now))
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "operator", "Operator", "Operates models", false, true, now, now))
	mock.ExpectQuery(`SELECT \* FROM "role_permissions" WHERE deployment_id = \$1 AND role_id IN \(\$2\) ORDER BY role_id, permission_id`).
		WithArgs("deployment-a", "operator").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "role_id", "permission_id"}).AddRow("deployment-a", "operator", "models.read"))
	role, err := store.Deployment("deployment-a").GetRoleRecord(context.Background(), "operator")
	if err != nil || role.ID != "operator" || len(role.Permissions) != 1 {
		t.Fatalf("GetRoleRecord() = %#v, %v", role, err)
	}

	name := "Model Operator"
	description := "Manages models"
	enabled := false
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "operator", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "operator", "Operator", "Operates models", false, true, now, now))
	mock.ExpectExec(`UPDATE "roles" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "permissions" WHERE id IN \(\$1\)`).
		WithArgs("models.read").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`DELETE FROM "role_permissions"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO "role_permissions"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "operator", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "operator", name, description, false, enabled, now, now))
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "operator", name, description, false, enabled, now, now))
	mock.ExpectQuery(`SELECT \* FROM "role_permissions" WHERE deployment_id = \$1 AND role_id IN \(\$2\) ORDER BY role_id, permission_id`).
		WithArgs("deployment-a", "operator").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "role_id", "permission_id"}).AddRow("deployment-a", "operator", "models.read"))
	updated, err := store.Deployment("deployment-a").UpdateRole(context.Background(), "operator", &name, &description, &enabled, &[]string{"models.read", "models.read"})
	if err != nil || updated.Name != name || updated.Enabled || len(updated.Permissions) != 1 {
		t.Fatalf("UpdateRole() = %#v, %v", updated, err)
	}
}

func TestTeamReadUpdateAndNotFound(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	teamRows := func(name string, enabled bool) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at", "member_count"}).
			AddRow("deployment-a", "engineering", name, "Builders", false, enabled, now, now, int64(3))
	}
	mock.ExpectQuery(`SELECT teams\.\*, COUNT\(user_team_bindings\.user_id\) AS member_count FROM "teams" LEFT JOIN user_team_bindings`).
		WithArgs("deployment-a").WillReturnRows(teamRows("Engineering", true))
	team, err := store.Deployment("deployment-a").GetTeamRecord(context.Background(), "engineering")
	if err != nil || team.ID != "engineering" || team.MemberCount != 3 {
		t.Fatalf("GetTeamRecord() = %#v, %v", team, err)
	}

	name := "Platform"
	description := "Core platform"
	enabled := false
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "teams" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT teams\.\*, COUNT\(user_team_bindings\.user_id\) AS member_count FROM "teams" LEFT JOIN user_team_bindings`).
		WithArgs("deployment-a").WillReturnRows(teamRows(name, enabled))
	updated, err := store.Deployment("deployment-a").UpdateTeam(context.Background(), "engineering", &name, &description, &enabled)
	if err != nil || updated.Name != name || updated.Enabled || updated.MemberCount != 3 {
		t.Fatalf("UpdateTeam() = %#v, %v", updated, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "teams" SET`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if _, err := store.Deployment("deployment-a").UpdateTeam(context.Background(), "missing", &name, nil, nil); err != ErrNotFound {
		t.Fatalf("UpdateTeam(missing) = %v", err)
	}
}
