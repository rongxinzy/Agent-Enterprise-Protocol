package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// The subject guard is the pair of statements every grant write runs inside its
// own transaction, before it inserts anything: take the transaction-scoped lock
// for the subject, then confirm the subject still exists. Subject deletion takes
// the same lock, so a create that probed the subject can no longer commit its
// grant after the delete removed the subject and its grants.
const (
	subjectLockPattern   = `SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0\)\)`
	subjectExistsPattern = `SELECT EXISTS\(SELECT 1 FROM (users|roles|teams) WHERE deployment_id=\$1 AND id=\$2\)`
)

// subjectLockKey spells out the key the guard hashes: deployment, subject type,
// subject id. It is written out rather than derived so a change to the format
// fails on both the creating and the deleting side.
func subjectLockKey(subjectType, subjectID string) string {
	return "deployment-a:" + subjectType + ":" + subjectID
}

// expectSubjectGuard queues the guard for a grant write that owns a pgx
// transaction (the skill assignment path): lock first, probe second.
func expectSubjectGuard(pool pgxmock.PgxPoolIface, subjectType, subjectID string, exists bool) {
	pool.ExpectExec(subjectLockPattern).WithArgs(subjectLockKey(subjectType, subjectID)).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(subjectExistsPattern).WithArgs("deployment-a", subjectID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(exists))
}

// expectStoreSubjectGuard is expectSubjectGuard on the store connection, for the
// grant writes that run through GORM.
func expectStoreSubjectGuard(mock sqlmock.Sqlmock, subjectType, subjectID string, exists bool) {
	mock.ExpectExec(subjectLockPattern).WithArgs(subjectLockKey(subjectType, subjectID)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(subjectExistsPattern).WithArgs("deployment-a", subjectID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
}

// A grant may only name a subject that exists. The probe runs after the subject
// lock and inside the write transaction, so a subject that a concurrent delete
// removed between the request arriving and the lock being granted is rejected
// exactly like one that never existed: no grant is stored, so a later subject
// that reuses the identifier cannot inherit it.
func TestGrantWritesRejectUnknownSubjectInsideTheWriteTransaction(t *testing.T) {
	t.Run("skill assignment", func(t *testing.T) {
		application, _, pool, adminToken := newUserHTTPApplication(t)
		pool.ExpectBegin()
		expectSubjectGuard(pool, "role", "member", false)
		pool.ExpectRollback()
		// No INSERT expectation is queued: a rejected subject must not be stored.
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"role","id":"member"}}`)
		requireInvalidSubject(t, response)
	})

	t.Run("model assignment", func(t *testing.T) {
		application, mock, adminToken := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		expectStoreSubjectGuard(mock, "team", "engineering", false)
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/model-assignments", `{"modelId":"chat-a","subject":{"type":"team","id":"engineering"}}`)
		requireInvalidSubject(t, response)
	})

	t.Run("credential assignment", func(t *testing.T) {
		application, mock, adminToken := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		expectStoreSubjectGuard(mock, "user", "user-a", false)
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/credential-assignments", `{"credentialId":"credential-a","subject":{"type":"user","id":"user-a"}}`)
		requireInvalidSubject(t, response)
	})

	t.Run("data scope rule", func(t *testing.T) {
		application, mock, adminToken := newStoreBackedHTTPApplication(t)
		// The identifier is derived before the transaction opens, so its lookup
		// precedes the guard.
		mock.ExpectQuery(`SELECT \* FROM "data_scope_rules" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
			WithArgs("deployment-a", "member", 1).
			WillReturnRows(sqlmock.NewRows(dataScopeColumns()))
		mock.ExpectBegin()
		expectStoreSubjectGuard(mock, "role", "member", false)
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules",
			`{"ruleKind":"exception_grant","subjectType":"role","subjectId":"member","resourceKind":"model","resourceId":"chat","reason":"member"}`)
		requireInvalidSubject(t, response)
	})
}

// The lock, the probe, and the insert must belong to one transaction, in that
// order. Lock-then-probe is what makes the probe conclusive against a concurrent
// delete; keeping the insert in the same transaction is what holds the lock
// until the grant is durable.
func TestGrantWritesLockThenProbeThenInsertInOneTransaction(t *testing.T) {
	t.Run("skill assignment", func(t *testing.T) {
		application, _, pool, adminToken := newUserHTTPApplication(t)
		pool.ExpectBegin()
		expectSubjectGuard(pool, "team", "engineering", true)
		pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(
			pgxmock.AnyArg(), "deployment-a", "writer", "team", "engineering", pgxmock.AnyArg(),
		).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		expectSkillAssignmentEvent(pool)
		pool.ExpectCommit()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"team","id":"engineering"}}`)
		requireCreated(t, response)
	})

	t.Run("model assignment", func(t *testing.T) {
		application, mock, adminToken := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		expectStoreSubjectGuard(mock, "team", "engineering", true)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
			WithArgs("deployment-a", "chat-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(`INSERT INTO "model_assignments"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/model-assignments", `{"modelId":"chat-a","subject":{"type":"team","id":"engineering"}}`)
		requireCreated(t, response)
	})

	t.Run("credential assignment", func(t *testing.T) {
		application, mock, adminToken := newStoreBackedHTTPApplication(t)
		mock.ExpectBegin()
		expectStoreSubjectGuard(mock, "role", "operator", true)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
			WithArgs("deployment-a", "credential-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(`INSERT INTO "credential_assignments"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/credential-assignments", `{"credentialId":"credential-a","subject":{"type":"role","id":"operator"}}`)
		requireCreated(t, response)
	})

	t.Run("data scope rule", func(t *testing.T) {
		application, mock, adminToken := newStoreBackedHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "data_scope_rules" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
			WithArgs("deployment-a", "member", 1).
			WillReturnRows(sqlmock.NewRows(dataScopeColumns()))
		mock.ExpectBegin()
		expectStoreSubjectGuard(mock, "role", "member", true)
		mock.ExpectExec(`INSERT INTO "data_scope_rules"`).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules",
			`{"ruleKind":"exception_grant","subjectType":"role","subjectId":"member","resourceKind":"model","resourceId":"chat","reason":"member"}`)
		requireCreated(t, response)
	})
}

func requireInvalidSubject(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_SUBJECT"`) {
		t.Fatalf("unknown subject = %d %s", response.Code, response.Body.String())
	}
}

func requireCreated(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
}
