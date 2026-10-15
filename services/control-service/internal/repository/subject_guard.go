package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrSubjectNotFound reports that a polymorphic grant named a subject that does
// not exist in this deployment. Subjects are stored as a bare
// (subject_type, subject_id) pair with no foreign key, so a grant that outlives
// its subject would silently attach itself to a later subject that reuses the
// id.
var ErrSubjectNotFound = errors.New("assignment subject does not exist")

// subjectTables maps the polymorphic subject_type discriminator onto the table
// that owns the identifier.
var subjectTables = map[string]string{
	"user": "users",
	"role": "roles",
	"team": "teams",
}

// SubjectTable returns the table that owns a polymorphic subject type. It is the
// single definition of which subjects can carry a grant, shared by the HTTP
// validators and by the guard below.
func SubjectTable(subjectType string) (string, bool) {
	table, ok := subjectTables[subjectType]
	return table, ok
}

// SubjectLockKey is the advisory-lock key of one polymorphic subject. The
// deployment is part of it because subject ids are only unique inside a
// deployment, and the type is part of it so a role and a team that happen to
// share an id do not serialize against each other.
func SubjectLockKey(deploymentID, subjectType, subjectID string) string {
	return deploymentID + ":" + subjectType + ":" + subjectID
}

// The guard reaches the database through two clients: request handlers that own
// a pgx transaction run the statements as written, while the store runs them
// through GORM's builder, which spells placeholders "?" and rewrites them into
// numbers. The store's spelling is derived from the same text rather than
// written twice, so the two paths cannot drift apart on the lock key, the locked
// statement, or the probed table.
const (
	subjectLockStatement   = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	subjectExistsStatement = `SELECT EXISTS(SELECT 1 FROM %s WHERE deployment_id=$1 AND id=$2)`
)

var gormStatement = strings.NewReplacer("$1", "?", "$2", "?").Replace

// subjectGuard takes the subject lock and, in the same transaction, confirms
// that the subject still exists. It is the create-side half of the fix for the
// check-then-insert window: because the probe runs after the lock, a concurrent
// subject deletion either commits before the probe (and this returns
// ErrSubjectNotFound) or waits for the transaction below to end, so a grant can
// never be committed for a subject that is already gone.
//
// run and probe must execute on the transaction that owns the caller's write;
// the lock is held only for as long as that transaction is open.
func subjectGuard(
	ctx context.Context,
	deploymentID, subjectType, subjectID string,
	run func(context.Context, string, ...any) error,
	probe func(context.Context, string, ...any) (bool, error),
) error {
	table, ok := SubjectTable(subjectType)
	if !ok {
		return fmt.Errorf("unknown subject type %q", subjectType)
	}
	if err := run(ctx, subjectLockStatement, SubjectLockKey(deploymentID, subjectType, subjectID)); err != nil {
		return err
	}
	exists, err := probe(ctx, fmt.Sprintf(subjectExistsStatement, table), deploymentID, subjectID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrSubjectNotFound
	}
	return nil
}

// GuardSubject is the create-side guard for store-owned transactions. Call it as
// the first statement of the transaction that inserts a grant, before the insert
// itself; deleting a subject takes the same lock, which is what closes the
// interleaving window.
func (s *DeploymentStore) GuardSubject(ctx context.Context, subjectType, subjectID string) error {
	return subjectGuard(ctx, s.deploymentID, subjectType, subjectID, s.runGuardStatement, s.probeGuardSubject)
}

// GuardSubjectTx is GuardSubject for handlers that own a pgx transaction instead
// of going through the store. Same key, same statements, same order.
func GuardSubjectTx(ctx context.Context, tx pgx.Tx, deploymentID, subjectType, subjectID string) error {
	return subjectGuard(ctx, deploymentID, subjectType, subjectID,
		func(ctx context.Context, statement string, args ...any) error {
			_, err := tx.Exec(ctx, statement, args...)
			return err
		},
		func(ctx context.Context, statement string, args ...any) (bool, error) {
			var exists bool
			err := tx.QueryRow(ctx, statement, args...).Scan(&exists)
			return exists, err
		})
}

// runGuardStatement executes one guard statement on this store's connection,
// which must be the transaction that owns the caller's write.
func (s *DeploymentStore) runGuardStatement(ctx context.Context, statement string, args ...any) error {
	return s.db.WithContext(ctx).Exec(gormStatement(statement), args...).Error
}

// probeGuardSubject runs one guard statement that answers a boolean.
func (s *DeploymentStore) probeGuardSubject(ctx context.Context, statement string, args ...any) (bool, error) {
	var exists bool
	err := s.db.WithContext(ctx).Raw(gormStatement(statement), args...).Scan(&exists).Error
	return exists, err
}

// lockSubject takes the subject lock on this store's transaction. Delete paths
// call it as the first statement of the transaction that removes the subject, so
// a grant creation that already passed the guard cannot slip in behind the
// cleanup.
func (s *DeploymentStore) lockSubject(ctx context.Context, subjectType, subjectID string) error {
	return s.runGuardStatement(ctx, subjectLockStatement, SubjectLockKey(s.deploymentID, subjectType, subjectID))
}
