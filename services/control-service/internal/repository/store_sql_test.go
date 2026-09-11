package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = sqlDB.Close()
	})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(db), mock
}

func TestStoreScopesDeploymentAndUserQueries(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).
		WithArgs("deployment-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow("deployment-a", "Deployment A", now))
	deployment, err := store.GetDeployment(context.Background(), "deployment-a")
	if err != nil || deployment.ID != "deployment-a" {
		t.Fatalf("GetDeployment() = %#v, %v", deployment, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "username", "display_name", "status", "password_hash", "require_password_change", "is_admin", "created_at", "updated_at"}).
			AddRow("user-a", "deployment-a", "alice", "Alice", "active", "hash", false, false, now, now))
	user, err := store.Deployment("deployment-a").GetUser(context.Background(), "user-a")
	if err != nil || user.DeploymentID != "deployment-a" || user.Username != "alice" {
		t.Fatalf("GetUser() = %#v, %v", user, err)
	}
}

func TestListUsersReturnsStableMembershipsAndEmptySlices(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id > \$2 ORDER BY id LIMIT \$3`).
		WithArgs("deployment-a", "user-0", 3).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "username", "display_name", "status", "password_hash", "require_password_change", "is_admin", "created_at", "updated_at"}).
			AddRow("user-a", "deployment-a", "alice", "Alice", "active", "hash", false, false, now, now).
			AddRow("user-b", "deployment-a", "bob", "Bob", "active", "hash", false, false, now, now))
	mock.ExpectQuery(`SELECT \* FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id IN \(\$2,\$3\) ORDER BY user_id, role_id`).
		WithArgs("deployment-a", "user-a", "user-b").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "role_id", "is_primary", "created_at"}).
			AddRow("deployment-a", "user-a", "role-a", true, now))
	mock.ExpectQuery(`SELECT \* FROM "user_team_bindings" WHERE deployment_id = \$1 AND user_id IN \(\$2,\$3\) ORDER BY user_id, team_id`).
		WithArgs("deployment-a", "user-a", "user-b").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "user_id", "team_id", "is_primary", "created_at"}).
			AddRow("deployment-a", "user-a", "team-a", true, now))

	users, err := store.Deployment("deployment-a").ListUsers(context.Background(), "user-0", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || len(users[0].RoleIDs) != 1 || len(users[0].TeamIDs) != 1 {
		t.Fatalf("unexpected first user memberships: %#v", users)
	}
	if users[1].RoleIDs == nil || users[1].TeamIDs == nil || len(users[1].RoleIDs) != 0 || len(users[1].TeamIDs) != 0 {
		t.Fatalf("empty memberships must be non-nil JSON arrays: %#v", users[1])
	}
}

func TestCreateRoleRollsBackUnknownPermissions(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "roles"`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "permissions" WHERE id IN \(\$1,\$2\)`).
		WithArgs("models.read", "models.write").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err := store.Deployment("deployment-a").CreateRole(context.Background(), Role{ID: "model-operator", Name: "Model operator"}, []string{"models.read", "models.write", "models.read"})
	if !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("CreateRole() error = %v", err)
	}
}

func TestReplaceUserRBACRollsBackUnknownRole(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "roles" WHERE deployment_id = \$1 AND id IN \(\$2\)`).
		WithArgs("deployment-a", "missing-role").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()

	err := store.Deployment("deployment-a").ReplaceUserRBAC(context.Background(), "user-a", []string{"missing-role", "missing-role"}, []string{"team-a"})
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("ReplaceUserRBAC() error = %v", err)
	}
}

func TestBuiltInRoleCannotBeDeleted(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "admin", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled"}).
			AddRow("deployment-a", "admin", "Administrator", "Built in", true, true))
	if err := store.Deployment("deployment-a").DeleteRole(context.Background(), "admin"); !errors.Is(err, ErrBuiltInResource) {
		t.Fatalf("DeleteRole() error = %v", err)
	}
}

func TestResourceDeletionMapsZeroRowsToNotFound(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "missing-model").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteModel(context.Background(), "missing-model"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteModel() error = %v", err)
	}

	models, err := store.Deployment("deployment-a").ListEnabledModelsByIDs(context.Background(), nil)
	if err != nil || models == nil || len(models) != 0 {
		t.Fatalf("ListEnabledModelsByIDs(nil) = %#v, %v", models, err)
	}
}

func TestResultErrorPreservesDatabaseError(t *testing.T) {
	expected := errors.New("database unavailable")
	if err := resultError(&gorm.DB{Error: expected}); !errors.Is(err, expected) {
		t.Fatalf("resultError() = %v", err)
	}
}
