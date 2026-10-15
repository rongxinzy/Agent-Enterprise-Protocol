package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The lock key is the contract between the side that creates a grant and the
// side that deletes the subject: two connections only block each other when they
// hash the identical text, so the format is pinned here.
func TestSubjectLockKeyScopesDeploymentTypeAndID(t *testing.T) {
	if key := SubjectLockKey("deployment-a", "role", "member"); key != "deployment-a:role:member" {
		t.Fatalf("SubjectLockKey() = %q", key)
	}
}

// The guard must take the subject lock before it probes the subject. The mock is
// ordered, so this fails if the two statements swap or either one disappears.
func TestGuardSubjectLocksBeforeProbingTheSubject(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`).
		WithArgs("deployment-a:role:member").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM roles WHERE deployment_id=\$1 AND id=\$2\)`).
		WithArgs("deployment-a", "member").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	if err := store.Deployment("deployment-a").GuardSubject(context.Background(), "role", "member"); err != nil {
		t.Fatalf("GuardSubject() error = %v", err)
	}
}

func TestGuardSubjectReportsAMissingSubject(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`).
		WithArgs("deployment-a:team:engineering").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM teams WHERE deployment_id=\$1 AND id=\$2\)`).
		WithArgs("deployment-a", "engineering").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	err := store.Deployment("deployment-a").GuardSubject(context.Background(), "team", "engineering")
	if !errors.Is(err, ErrSubjectNotFound) {
		t.Fatalf("GuardSubject() error = %v", err)
	}
}

func TestGuardSubjectPropagatesStatementFailures(t *testing.T) {
	t.Run("lock", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`).
			WithArgs("deployment-a:user:user-a").WillReturnError(errors.New("lock unavailable"))
		err := store.Deployment("deployment-a").GuardSubject(context.Background(), "user", "user-a")
		if err == nil || errors.Is(err, ErrSubjectNotFound) {
			t.Fatalf("GuardSubject() error = %v", err)
		}
	})

	t.Run("probe", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`).
			WithArgs("deployment-a:user:user-a").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE deployment_id=\$1 AND id=\$2\)`).
			WithArgs("deployment-a", "user-a").WillReturnError(errors.New("probe unavailable"))
		err := store.Deployment("deployment-a").GuardSubject(context.Background(), "user", "user-a")
		if err == nil || errors.Is(err, ErrSubjectNotFound) {
			t.Fatalf("GuardSubject() error = %v", err)
		}
	})
}

// A delete that cannot take the subject lock must fail before it touches any
// grant: running the cleanup on a transaction that does not hold the lock is
// exactly the interleaving the lock exists to prevent.
func TestDeleteSubjectStopsWhenTheSubjectLockFails(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "member", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "member", "Member", "", false, true, now, now))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`).
		WithArgs("deployment-a:role:member").WillReturnError(errors.New("lock unavailable"))
	mock.ExpectRollback()
	// No grant-cleanup expectation is queued, so the guard failure has to be the
	// error that surfaces.
	err := store.Deployment("deployment-a").DeleteRole(context.Background(), "member")
	if err == nil || !strings.Contains(err.Error(), "lock unavailable") {
		t.Fatalf("DeleteRole() error = %v", err)
	}
}

// A subject type no table backs must fail before any statement runs, so a
// malformed type cannot reach the lock or the probe.
func TestGuardSubjectRejectsAnUnknownSubjectType(t *testing.T) {
	store, _ := newMockStore(t)
	err := store.Deployment("deployment-a").GuardSubject(context.Background(), "device", "device-a")
	if err == nil || errors.Is(err, ErrSubjectNotFound) || !strings.Contains(err.Error(), "unknown subject type") {
		t.Fatalf("GuardSubject() error = %v", err)
	}
}
