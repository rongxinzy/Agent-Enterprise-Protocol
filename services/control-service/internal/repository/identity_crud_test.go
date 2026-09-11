package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUserCreateAndRecordLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	email := "alice@example.com"

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "users"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created, err := store.Deployment("deployment-a").CreateUser(context.Background(), CreateUserParams{
		ID: "user-a", Username: "alice", DisplayName: "Alice", Email: &email, PasswordHash: "hash",
		RequirePasswordChange: true, RoleIDs: []string{"member", "member"}, TeamIDs: []string{"engineering", "engineering"},
	})
	if err != nil || created.ID != "user-a" || len(created.RoleIDs) != 1 || len(created.TeamIDs) != 1 || !created.RequirePasswordChange {
		t.Fatalf("CreateUser() = %#v, %v", created, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "created_at", "updated_at"}).
			AddRow("user-a", "deployment-a", "alice", "Alice", email, "hash", "active", true, false, now, now))
	mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
		WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("member"))
	mock.ExpectQuery(`SELECT "team_id" FROM "user_team_bindings"`).
		WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("engineering"))
	record, err := store.Deployment("deployment-a").GetUserRecord(context.Background(), "user-a")
	if err != nil || record.ID != "user-a" || len(record.RoleIDs) != 1 || len(record.TeamIDs) != 1 {
		t.Fatalf("GetUserRecord() = %#v, %v", record, err)
	}
}

func TestUserUpdateAndPasswordNotFound(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	displayName := "Alice Updated"
	email := "updated@example.com"

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "created_at", "updated_at"}).
			AddRow("user-a", "deployment-a", "alice", displayName, email, "new-hash", "disabled", false, false, now, now))
	mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
		WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}))
	mock.ExpectQuery(`SELECT "team_id" FROM "user_team_bindings"`).
		WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
	updated, err := store.Deployment("deployment-a").UpdateUser(context.Background(), "user-a", UpdateUserParams{DisplayName: &displayName, Email: &email, Status: stringPtr("disabled")})
	if err != nil || updated.DisplayName != displayName || updated.Status != "disabled" {
		t.Fatalf("UpdateUser() = %#v, %v", updated, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").UpdatePassword(context.Background(), "user-a", "new-hash", false); err != nil {
		t.Fatalf("UpdatePassword() = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").UpdatePassword(context.Background(), "missing", "hash", false); err != ErrNotFound {
		t.Fatalf("UpdatePassword(missing) = %v", err)
	}
}

func stringPtr(value string) *string { return &value }
