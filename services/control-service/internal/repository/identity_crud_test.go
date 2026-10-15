package repository

import (
	"context"
	"errors"
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

// Deleting a user must not be blocked by the history that references it. The
// foreign keys from telemetry_events, credential_resolution_audit,
// skill_sync_results, control_events and data_scope_rules are ON DELETE SET
// NULL, so a row that already exists is anonymized by the database and the
// delete transaction must not touch those tables at all. sqlmock rejects every
// statement that was not expected, which is what makes this test cover a user
// that already has such rows.
func TestDeleteUserLeavesHistoryToTheForeignKeys(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).
		WithArgs("user-a").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE user_sessions SET revoked_at=now\(\)`).
		WithArgs("user-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "users"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "user_role_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "user_team_bindings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteUser(context.Background(), "user-a"); err != nil {
		t.Fatalf("DeleteUser(): %v", err)
	}
}

// Revoking the sessions and removing the user are one unit of work: when the
// removal cannot complete, the revocation must not survive on its own.
func TestDeleteUserRollsBackTheRevocation(t *testing.T) {
	t.Run("missing user", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE user_session_tokens`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE user_sessions`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`DELETE FROM "users"`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		if err := store.Deployment("deployment-a").DeleteUser(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteUser(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("session revocation failure", func(t *testing.T) {
		store, mock := newMockStore(t)
		revocationErr := errors.New("revocation failed")
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE user_session_tokens`).WillReturnError(revocationErr)
		mock.ExpectRollback()
		if err := store.Deployment("deployment-a").DeleteUser(context.Background(), "user-a"); !errors.Is(err, revocationErr) {
			t.Fatalf("DeleteUser() = %v, want the revocation failure", err)
		}
	})
}
