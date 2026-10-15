package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

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

// A subject type no table backs must fail before any statement runs, so a
// malformed type cannot reach the lock or the probe.
func TestGuardSubjectRejectsAnUnknownSubjectType(t *testing.T) {
	store, _ := newMockStore(t)
	err := store.Deployment("deployment-a").GuardSubject(context.Background(), "device", "device-a")
	if err == nil || errors.Is(err, ErrSubjectNotFound) || !strings.Contains(err.Error(), "unknown subject type") {
		t.Fatalf("GuardSubject() error = %v", err)
	}
}
