package httpapi

import (
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func requireSkillProblem(t *testing.T, responseStatus int, body string, status int, code string) {
	t.Helper()
	if responseStatus != status || !strings.Contains(body, `"code":"`+code+`"`) {
		t.Fatalf("response = %d %s, want status %d and code %s", responseStatus, body, status, code)
	}
}

func TestAdminSkillReadFailureBoundaries(t *testing.T) {
	t.Run("list query", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skills" ORDER BY id LIMIT \$1`).WithArgs(51).
			WillReturnError(errors.New("skill list unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/skills", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("list versions", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		now := time.Now().UTC()
		mock.ExpectQuery(`SELECT \* FROM "skills" ORDER BY id LIMIT \$1`).WithArgs(51).
			WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
		mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).WithArgs("writer").
			WillReturnError(errors.New("skill versions unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/skills", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("get missing", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("missing", 1).
			WillReturnRows(sqlmock.NewRows(skillColumns()))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/skills/missing", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("get query", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
			WillReturnError(errors.New("skill unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/skills/writer", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("get versions", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		now := time.Now().UTC()
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
			WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
		mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).WithArgs("writer").
			WillReturnError(errors.New("skill versions unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/skills/writer", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func TestAdminSkillCreateAndMutationFailureBoundaries(t *testing.T) {
	t.Run("invalid create body", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skills", `{`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
	})

	t.Run("duplicate create", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO "skills"`).WillReturnError(&pgconn.PgError{Code: "23505"})
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skills", `{"id":"writer","name":"Writer"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusConflict, "SKILL_ALREADY_EXISTS")
	})

	t.Run("create database error", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO "skills"`).WillReturnError(errors.New("insert unavailable"))
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skills", `{"id":"writer","name":"Writer","enabled":false}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("invalid update body", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/skills/writer", `{`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
	})

	t.Run("invalid update state", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/skills/writer", `{"state":"retired"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_STATE")
	})

	t.Run("missing update", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "skills" SET`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/skills/missing", `{"name":"Missing"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("update database error", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "skills" SET`).WillReturnError(errors.New("update unavailable"))
		mock.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/skills/writer", `{"name":"Writer 2"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("updated versions unavailable", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		now := time.Now().UTC()
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "skills" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
			WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer 2", "", true, now, now))
		mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).WithArgs("writer").
			WillReturnError(errors.New("versions unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodPatch, "/aep/v1/admin/skills/writer", `{"name":"Writer 2"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	for _, test := range []struct {
		name        string
		result      driver.Result
		err         error
		status      int
		problemCode string
	}{
		{name: "missing delete", result: sqlmock.NewResult(0, 0), status: http.StatusNotFound, problemCode: "RESOURCE_NOT_FOUND"},
		{name: "delete database error", err: errors.New("delete unavailable"), status: http.StatusInternalServerError, problemCode: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, _, token := newUserHTTPApplication(t)
			mock.ExpectBegin()
			expectation := mock.ExpectExec(`DELETE FROM "skills" WHERE id = \$1`).WithArgs("writer")
			if test.err != nil {
				expectation.WillReturnError(test.err)
				mock.ExpectRollback()
			} else {
				expectation.WillReturnResult(test.result)
				mock.ExpectCommit()
			}
			response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skills/writer", "")
			requireSkillProblem(t, response.Code, response.Body.String(), test.status, test.problemCode)
		})
	}
}

func TestAdminSkillVersionFailureBoundaries(t *testing.T) {
	t.Run("invalid multipart", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skills/writer/versions", "not multipart")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
	})

	t.Run("missing package", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := uploadSkillRequest(t, New(application).Handler(), token, "/aep/v1/admin/skills/writer/versions", "", nil)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
	})

	t.Run("blob upload", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		application.Blobs = &memorySkillBlobStore{objects: make(map[string][]byte), putErr: errors.New("blob unavailable")}
		response := uploadSkillRequest(t, New(application).Handler(), token, "/aep/v1/admin/skills/writer/versions", "1.0.0", []byte("archive"))
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("version upsert", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		application.Blobs = newMemorySkillBlobStore()
		mock.ExpectExec(`INSERT INTO skill_versions`).WillReturnError(errors.New("version unavailable"))
		response := uploadSkillRequest(t, New(application).Handler(), token, "/aep/v1/admin/skills/writer/versions", "1.0.0", []byte("archive"))
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	for _, test := range []struct {
		name        string
		err         error
		result      driver.Result
		status      int
		problemCode string
	}{
		{name: "publish missing", result: sqlmock.NewResult(0, 0), status: http.StatusNotFound, problemCode: "RESOURCE_NOT_FOUND"},
		{name: "publish database error", err: errors.New("publish unavailable"), status: http.StatusInternalServerError, problemCode: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, _, token := newUserHTTPApplication(t)
			mock.ExpectBegin()
			expectation := mock.ExpectExec(`UPDATE "skill_versions" SET`)
			if test.err != nil {
				expectation.WillReturnError(test.err)
				mock.ExpectRollback()
			} else {
				expectation.WillReturnResult(test.result)
				mock.ExpectCommit()
			}
			response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skills/writer/versions/1.0.0/publish", "")
			requireSkillProblem(t, response.Code, response.Body.String(), test.status, test.problemCode)
		})
	}

	t.Run("delete missing", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2 LIMIT \$3`).WithArgs("writer", "1.0.0", 1).
			WillReturnRows(sqlmock.NewRows(skillVersionColumns()))
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skills/writer/versions/1.0.0", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("delete database error", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2 LIMIT \$3`).WithArgs("writer", "1.0.0", 1).
			WillReturnError(errors.New("version unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skills/writer/versions/1.0.0", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("blob delete", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		now := time.Now().UTC()
		blobs := newMemorySkillBlobStore()
		blobs.deleteErr = errors.New("blob unavailable")
		application.Blobs = blobs
		mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2 LIMIT \$3`).WithArgs("writer", "1.0.0", 1).
			WillReturnRows(sqlmock.NewRows(skillVersionColumns()).AddRow("writer", "1.0.0", "skills/writer/1.0.0/package.zip", "digest", int64(7), true, now, now))
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2`).WithArgs("writer", "1.0.0").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skills/writer/versions/1.0.0", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func TestAdminSkillAssignmentFailureBoundaries(t *testing.T) {
	t.Run("list database error", func(t *testing.T) {
		application, mock, _, token := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 ORDER BY id`).WithArgs("deployment-a").
			WillReturnError(errors.New("assignments unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/skill-assignments", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("invalid subject", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"device","id":"device-a"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SUBJECT")
	})

	t.Run("create begin", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"user","id":"user-a"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("create insert", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "user", "user-a").WillReturnError(errors.New("insert unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"user","id":"user-a"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("create event", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "role", "member").WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnError(errors.New("event unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"role","id":"member"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("create supersede event", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "role", "member").WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectExec(`WITH old AS \(UPDATE control_events SET state='superseded'`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnError(errors.New("supersede unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"role","id":"member"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("create delivery event", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "role", "member").WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectExec(`WITH old AS \(UPDATE control_events SET state='superseded'`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnError(errors.New("delivery unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"role","id":"member"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("create commit", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "team", "engineering").WillReturnResult(pgxmock.NewResult("INSERT", 1))
		expectSkillAssignmentEvent(pool)
		pool.ExpectCommit().WillReturnError(errors.New("commit unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"team","id":"engineering"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("delete begin", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin unavailable"))
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skill-assignments/assignment-a", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("delete missing", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`DELETE FROM skill_assignments`).WithArgs("assignment-a", "deployment-a").WillReturnError(pgx.ErrNoRows)
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skill-assignments/assignment-a", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusNotFound, "RESOURCE_NOT_FOUND")
	})

	t.Run("delete query", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`DELETE FROM skill_assignments`).WithArgs("assignment-a", "deployment-a").WillReturnError(errors.New("delete unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skill-assignments/assignment-a", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("delete event", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`DELETE FROM skill_assignments`).WithArgs("assignment-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"skill_id", "subject_type", "subject_id"}).AddRow("writer", "team", "engineering"))
		pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).WillReturnError(errors.New("event unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skill-assignments/assignment-a", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("delete commit", func(t *testing.T) {
		application, _, pool, token := newUserHTTPApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`DELETE FROM skill_assignments`).WithArgs("assignment-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"skill_id", "subject_type", "subject_id"}).AddRow("writer", "team", "engineering"))
		expectSkillAssignmentEvent(pool)
		pool.ExpectCommit().WillReturnError(errors.New("commit unavailable"))
		pool.ExpectRollback()
		response := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/skill-assignments/assignment-a", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func TestUserSkillFailureBoundaries(t *testing.T) {
	t.Run("manifest query", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`WITH authorized AS`).WithArgs("deployment-a", "user-a").WillReturnError(errors.New("manifest unavailable"))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/manifest", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("manifest scan", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`WITH authorized AS`).WithArgs("deployment-a", "user-a").
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("writer"))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/manifest", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("download query", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`SELECT sv\.object_key`).WithArgs("deployment-a", "user-a", "writer", "1.0.0").WillReturnError(errors.New("download unavailable"))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/writer/versions/1.0.0/package", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("download blob", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		application.Blobs = &memorySkillBlobStore{objects: make(map[string][]byte), getErr: errors.New("blob unavailable")}
		pool.ExpectQuery(`SELECT sv\.object_key`).WithArgs("deployment-a", "user-a", "writer", "1.0.0").
			WillReturnRows(pgxmock.NewRows([]string{"object_key"}).AddRow("skills/writer/1.0.0/package.zip"))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/writer/versions/1.0.0/package", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("invalid sync body", func(t *testing.T) {
		application, _, _, token := newRuntimeHTTPApplication(t)
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/skills/sync-results", `{`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_REQUEST")
	})

	t.Run("sync begin", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin unavailable"))
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-1","status":"completed"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("session required", func(t *testing.T) {
		application, pool, _, _ := newRuntimeHTTPApplication(t)
		token, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "", false, false, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		pool.ExpectBegin()
		pool.ExpectRollback()
		response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-1","status":"completed"}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusUnauthorized, "SESSION_REQUIRED")
	})

	for _, test := range []struct {
		name      string
		commitErr bool
	}{
		{name: "sync insert"},
		{name: "sync commit", commitErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, pool, _, token := newRuntimeHTTPApplication(t)
			pool.ExpectBegin()
			exec := pool.ExpectExec(`INSERT INTO skill_sync_results`).WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a", "session-user", "revision-1", "completed", pgxmock.AnyArg(), pgxmock.AnyArg())
			if test.commitErr {
				exec.WillReturnResult(pgxmock.NewResult("INSERT", 1))
				pool.ExpectCommit().WillReturnError(errors.New("commit unavailable"))
			} else {
				exec.WillReturnError(errors.New("insert unavailable"))
			}
			pool.ExpectRollback()
			response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-1","status":"completed"}`)
			requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
		})
	}
}
