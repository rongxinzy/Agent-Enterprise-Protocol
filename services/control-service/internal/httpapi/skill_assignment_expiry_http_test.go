package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const assignmentUpdateQuery = `UPDATE skill_assignments SET expires_at`

// Editing the expiry in place keeps the grant (and the Skill on the employee's
// manifest) in place; the change still emits the assignment control event so
// agents reconcile promptly.
func TestAdminSkillAssignmentExpiryUpdate(t *testing.T) {
	application, _, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectBegin()
	// The write itself is pinned: writing null instead of the requested expiry
	// must fail this test, not just change the response.
	pool.ExpectQuery(assignmentUpdateQuery).
		WithArgs(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), "assignment-a", "deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"skill_id", "subject_type", "subject_id"}).AddRow("writer", "user", "user-a"))
	// Pinned rather than AnyArg: the event must carry the deployment, the
	// subject scope, the Skill and the revision that names the new expiry.
	pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
		pgxmock.AnyArg(), "deployment-a", "user", new("user-a"), "writer",
		"expiry:assignment-a:2026-12-31T00:00:00Z", "skill-manifest:writer:user:user-a", pgxmock.AnyArg(), "admin-user",
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`WITH old AS \(UPDATE control_events SET state='superseded'`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	response := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/skill-assignments/assignment-a", `{"expiresAt":"2026-12-31T00:00:00Z"}`)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"expiresAt":"2026-12-31T00:00:00Z"`) ||
		!strings.Contains(response.Body.String(), `"skillId":"writer"`) ||
		!strings.Contains(response.Body.String(), `"type":"user"`) {
		t.Fatalf("update Skill assignment expiry = %d %s", response.Code, response.Body.String())
	}
}

// A null expiry makes the grant perpetual again; the revision says so.
func TestAdminSkillAssignmentExpiryPerpetual(t *testing.T) {
	application, _, pool, adminToken := newUserHTTPApplication(t)
	pool.ExpectBegin()
	pool.ExpectQuery(assignmentUpdateQuery).WithArgs(nil, "assignment-a", "deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"skill_id", "subject_type", "subject_id"}).AddRow("writer", "role", "member"))
	pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
		pgxmock.AnyArg(), "deployment-a", "role", new("member"), "writer",
		"expiry:assignment-a:perpetual", "skill-manifest:writer:role:member", pgxmock.AnyArg(), "admin-user",
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`WITH old AS \(UPDATE control_events SET state='superseded'`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	response := adminRequest(New(application).Handler(), adminToken, http.MethodPatch, "/aep/v1/admin/skill-assignments/assignment-a", `{"expiresAt":null}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"expiresAt":null`) {
		t.Fatalf("perpetual Skill assignment = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminSkillAssignmentExpiryBoundaries(t *testing.T) {
	t.Run("unknown assignment", func(t *testing.T) {
		application, _, pool, adminToken := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(assignmentUpdateQuery).WithArgs(pgxmock.AnyArg(), "missing", "deployment-a").
			WillReturnError(pgx.ErrNoRows)
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPatch, "/aep/v1/admin/skill-assignments/missing", `{"expiresAt":null}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("malformed bodies", func(t *testing.T) {
		for _, body := range []string{
			`{}`,
			`{"expiresAt":"not-a-timestamp"}`,
			`{"expiresAt":null,"skillId":"writer"}`,
		} {
			application, _, _, adminToken := newUserHTTPApplication(t)
			// No database expectation: the request is refused before the tx opens.
			response := adminRequest(New(application).Handler(), adminToken, http.MethodPatch, "/aep/v1/admin/skill-assignments/assignment-a", body)
			requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
		}
	})

	t.Run("event failure", func(t *testing.T) {
		application, _, pool, adminToken := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(assignmentUpdateQuery).WithArgs(pgxmock.AnyArg(), "assignment-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"skill_id", "subject_type", "subject_id"}).AddRow("writer", "user", "user-a"))
		pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnError(errors.New("event unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), adminToken, http.MethodPatch, "/aep/v1/admin/skill-assignments/assignment-a", `{"expiresAt":null}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("requires permission", func(t *testing.T) {
		// Changing a grant needs skills.assign (the assignment routes gained a
		// permission mapping); a caller without it is refused after the lookup.
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "skills.assign").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		response := userRequest(New(application).Handler(), userToken, http.MethodPatch, "/aep/v1/admin/skill-assignments/assignment-a", `{"expiresAt":null}`)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"ACCESS_DENIED"`) {
			t.Fatalf("non-admin Skill assignment update = %d %s", response.Code, response.Body.String())
		}
	})
}
