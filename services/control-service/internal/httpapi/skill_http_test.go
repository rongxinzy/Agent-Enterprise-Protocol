package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

type memorySkillBlobStore struct {
	objects   map[string][]byte
	deleted   []string
	putErr    error
	getErr    error
	deleteErr error
}

func newMemorySkillBlobStore() *memorySkillBlobStore {
	return &memorySkillBlobStore{objects: make(map[string][]byte)}
}

func (s *memorySkillBlobStore) Put(_ context.Context, key string, content []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.objects[key] = append([]byte(nil), content...)
	return nil
}

func (s *memorySkillBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	content, ok := s.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (s *memorySkillBlobStore) Delete(_ context.Context, key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.objects[key]; !ok {
		return errors.New("object not found")
	}
	delete(s.objects, key)
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *memorySkillBlobStore) Ready(context.Context) error { return nil }

func uploadSkillRequest(t *testing.T, handler http.Handler, token, path, version string, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("version", version); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("package", "skill.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func skillColumns() []string {
	return []string{"id", "name", "description", "enabled", "created_at", "updated_at"}
}

func skillVersionColumns() []string {
	return []string{"skill_id", "version", "object_key", "sha256", "size_bytes", "published", "created_at", "published_at"}
}

// skillManifestQueryPattern and skillPackageQueryPattern match the queries the
// two handlers run with the effectiveness rules spelled out. The manifest and
// the download must answer the same question - Skill enabled, version
// published, grant live and its subject resolvable - so a query that loses one
// of those rules stops matching here and the mock refuses to answer instead of
// quietly serving a package the manifest would not list.
const skillManifestQueryPattern = `WITH effective AS \( SELECT DISTINCT sk\.id AS skill_id,.*SELECT skill_id,name,version,sha256,size_bytes FROM latest ORDER BY skill_id`

const skillPackageQueryPattern = `WITH effective AS \( SELECT DISTINCT sk\.id AS skill_id,.*sk\.enabled=true.*sv\.published=true.*sa\.deployment_id=\$1.*\(sa\.expires_at IS NULL OR sa\.expires_at>now\(\)\).*sa\.subject_type='user' AND sa\.subject_id=\$2.*sa\.subject_type='role'.*r\.enabled=true.*sa\.subject_type='team'.*t\.enabled=true.*\) SELECT object_key FROM effective WHERE skill_id=\$3 AND version=\$4 LIMIT 1`

func expectSkillAdminAudit(pool pgxmock.PgxPoolIface, action, skillID string) {
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", action, "skill", nullableString(skillID), nil, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

func TestAdminSkillResourceAndVersionLifecycle(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	blobs := newMemorySkillBlobStore()
	application.Blobs = blobs
	handler := New(application).Handler()
	now := time.Now().UTC()

	// Every mutation below must leave one entry in the console's operation log
	// (telemetry_events), attributed to the acting administrator.
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.created", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// The skill identifier is generated from the name; the global-store
	// existence probe runs before the insert transaction.
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "skills"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	expectSkillAdminAudit(pool, "create", "")
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/skills", `{"name":"Writer","description":"Draft content","enabled":true}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"writer"`) || !strings.Contains(created.Body.String(), `"state":"active"`) {
		t.Fatalf("create Skill = %d %s", created.Code, created.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "Draft content", true, now, now))
	mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).WithArgs("writer").
		WillReturnRows(sqlmock.NewRows(skillVersionColumns()).AddRow("writer", "1.0.0", "skills/writer/1.0.0/old.zip", "old-digest", 12, true, now, now))
	got := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skills/writer", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"version":"1.0.0"`) || !strings.Contains(got.Body.String(), `"state":"published"`) {
		t.Fatalf("get Skill = %d %s", got.Code, got.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "skills" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer 2", "Updated", false, now, now))
	mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).WithArgs("writer").
		WillReturnRows(sqlmock.NewRows(skillVersionColumns()))
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.updated", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAdminAudit(pool, "update", "writer")
	updated := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/skills/writer", `{"name":"Writer 2","description":"Updated","state":"withdrawn"}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"state":"withdrawn"`) || !strings.Contains(updated.Body.String(), `"enabled":false`) {
		t.Fatalf("update Skill = %d %s", updated.Code, updated.Body.String())
	}

	archive := skillArchive(t)
	digest := sha256.Sum256(archive)
	sha := hex.EncodeToString(digest[:])
	objectKey := "skills/writer/2.0.0/" + sha + ".zip"
	mock.ExpectQuery(`INSERT INTO skill_versions`).WithArgs("writer", "2.0.0", objectKey, sha, int64(len(archive))).
		WillReturnRows(sqlmock.NewRows([]string{"published"}).AddRow(false))
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.version.uploaded", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAdminAudit(pool, "create", "writer")
	uploaded := uploadSkillRequest(t, handler, adminToken, "/aep/v1/admin/skills/writer/versions", "2.0.0", archive)
	if uploaded.Code != http.StatusCreated || !strings.Contains(uploaded.Body.String(), `"sha256":"`+sha+`"`) || !strings.Contains(uploaded.Body.String(), `"state":"draft"`) || !bytes.Equal(blobs.objects[objectKey], archive) {
		t.Fatalf("upload Skill version = %d %s, stored = %q", uploaded.Code, uploaded.Body.String(), blobs.objects[objectKey])
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "skill_versions" SET "published"=\$1,"published_at"=\$2 WHERE skill_id = \$3 AND version = \$4`).
		WithArgs(true, sqlmock.AnyArg(), "writer", "2.0.0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.version.published", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAdminAudit(pool, "publish", "writer")
	published := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/skills/writer/versions/2.0.0/publish", "")
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"published":true`) {
		t.Fatalf("publish Skill version = %d %s", published.Code, published.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2 LIMIT \$3`).WithArgs("writer", "2.0.0", 1).
		WillReturnRows(sqlmock.NewRows(skillVersionColumns()).AddRow("writer", "2.0.0", objectKey, sha, int64(len(archive)), true, now, now))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2`).WithArgs("writer", "2.0.0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.version.withdrawn", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAdminAudit(pool, "delete", "writer")
	deletedVersion := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/skills/writer/versions/2.0.0", "")
	if deletedVersion.Code != http.StatusNoContent || len(blobs.deleted) != 1 || blobs.deleted[0] != objectKey {
		t.Fatalf("delete Skill version = %d %s, deleted = %#v", deletedVersion.Code, deletedVersion.Body.String(), blobs.deleted)
	}

	expectSkillDeleteLock(mock, "writer")
	expectSkillReferenceCounts(mock, "writer", 0, 0)
	mock.ExpectExec(`DELETE FROM "skills" WHERE id = \$1`).WithArgs("writer").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.deleted", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAdminAudit(pool, "delete", "writer")
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/skills/writer", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete Skill = %d %s", deleted.Code, deleted.Body.String())
	}
}

// Re-uploading an existing version replaces its package; the version number is
// the key, so its publication state must survive (it must not take the Skill
// offline for every agent).
func TestAdminSkillReuploadKeepsPublished(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	application.Blobs = newMemorySkillBlobStore()

	archive := skillArchive(t)
	digest := sha256.Sum256(archive)
	sha := hex.EncodeToString(digest[:])
	objectKey := "skills/writer/1.0.0/" + sha + ".zip"
	mock.ExpectQuery(`INSERT INTO skill_versions`).WithArgs("writer", "1.0.0", objectKey, sha, int64(len(archive))).
		WillReturnRows(sqlmock.NewRows([]string{"published"}).AddRow(true))
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.version.uploaded", "skill", "writer", "success", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAdminAudit(pool, "create", "writer")
	response := uploadSkillRequest(t, handler, adminToken, "/aep/v1/admin/skills/writer/versions", "1.0.0", archive)
	if response.Code != http.StatusCreated ||
		!strings.Contains(response.Body.String(), `"state":"published"`) ||
		!strings.Contains(response.Body.String(), `"published":true`) {
		t.Fatalf("re-upload of a published version = %d %s", response.Code, response.Body.String())
	}
}

// expectSkillDeleteLock queues the first step of the guarded Skill delete: the
// transaction locks the Skill row before it is allowed to check references.
func expectSkillDeleteLock(mock sqlmock.Sqlmock, skillID string) {
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1`).WithArgs(skillID, 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow(skillID, "Skill", "", true, now, now))
}

// expectSkillReferenceCounts queues the two reference counts the non-forced
// delete runs under that lock.
func expectSkillReferenceCounts(mock sqlmock.Sqlmock, skillID string, promptBindings, assignments int64) {
	mock.ExpectQuery(`SELECT count\(\*\) FROM "agent_profiles" WHERE prompt_skill_id = \$1`).
		WithArgs(skillID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(promptBindings))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "skill_assignments" WHERE skill_id = \$1`).
		WithArgs(skillID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(assignments))
}

func TestAdminSkillDeleteRefusedWhileReferenced(t *testing.T) {
	for _, test := range []struct {
		name       string
		prompt     int64
		assignment int64
	}{
		{name: "digital employee prompt binding", prompt: 1, assignment: 0},
		{name: "Skill assignment", prompt: 0, assignment: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, adminToken := newStoreBackedHTTPApplication(t)
			expectSkillDeleteLock(mock, "skill-a")
			expectSkillReferenceCounts(mock, "skill-a", test.prompt, test.assignment)
			mock.ExpectRollback()
			response := adminRequest(New(application).Handler(), adminToken, http.MethodDelete, "/aep/v1/admin/skills/skill-a", "")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"SKILL_IN_USE"`) {
				t.Fatalf("referenced Skill delete = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

// A refused mutation is audited as well: the operation log must show the
// attempt and its reason, not only the actions that went through.
func TestAdminSkillDeleteRefusalIsAudited(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	expectSkillDeleteLock(mock, "skill-a")
	expectSkillReferenceCounts(mock, "skill-a", 1, 0)
	mock.ExpectRollback()
	pool.ExpectExec(`INSERT INTO telemetry_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.deleted", "skill", "skill-a", "failure", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	response := adminRequest(New(application).Handler(), adminToken, http.MethodDelete, "/aep/v1/admin/skills/skill-a", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"SKILL_IN_USE"`) {
		t.Fatalf("referenced Skill delete = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminSkillForceDeleteWhileReferenced(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	// force skips the reference counts entirely and deletes under the row lock.
	expectSkillDeleteLock(mock, "skill-a")
	mock.ExpectExec(`DELETE FROM "skills" WHERE id = \$1`).WithArgs("skill-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	response := adminRequest(New(application).Handler(), adminToken, http.MethodDelete, "/aep/v1/admin/skills/skill-a?force=true", "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("forced Skill delete = %d %s", response.Code, response.Body.String())
	}
}

func expectSkillAssignmentEvent(pool pgxmock.PgxPoolIface) {
	pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`WITH old AS \(UPDATE control_events SET state='superseded'`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

func TestAdminSkillAssignmentLifecycle(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	expires := now.Truncate(time.Second).Add(24 * time.Hour)
	mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id LIMIT \$2`).WithArgs("deployment-a", 51).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "skill_id", "subject_type", "subject_id", "created_at", "expires_at"}).
			AddRow("assignment-a", "deployment-a", "writer", "team", "engineering", now, expires).
			AddRow("assignment-b", "deployment-a", "writer", "user", "user-a", now, nil))
	listed := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skill-assignments", "")
	if listed.Code != http.StatusOK ||
		!strings.Contains(listed.Body.String(), `"skillId":"writer"`) ||
		!strings.Contains(listed.Body.String(), `"type":"team"`) ||
		// A temporary grant carries its expiry; a perpetual one is null. The
		// console needs both to tell a live grant from an expired one.
		!strings.Contains(listed.Body.String(), `"expiresAt":"`+expires.Format(time.RFC3339)+`"`) ||
		!strings.Contains(listed.Body.String(), `"expiresAt":null`) {
		t.Fatalf("list Skill assignments = %d %s", listed.Code, listed.Body.String())
	}

	// The existence probe runs before the create transaction opens.
	expectSubjectExists(pool, "engineering")
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "team", "engineering", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAssignmentEvent(pool)
	pool.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"team","id":"engineering"},"expiresAt":"2026-12-31T00:00:00Z"}`)
	if created.Code != http.StatusCreated ||
		!strings.Contains(created.Body.String(), `"skillId":"writer"`) ||
		!strings.Contains(created.Body.String(), `"expiresAt":"2026-12-31T00:00:00Z"`) {
		t.Fatalf("create Skill assignment = %d %s", created.Code, created.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`DELETE FROM skill_assignments`).WithArgs("assignment-a", "deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"skill_id", "subject_type", "subject_id"}).AddRow("writer", "team", "engineering"))
	expectSkillAssignmentEvent(pool)
	expectAuditTx(pool)
	pool.ExpectCommit()
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/skill-assignments/assignment-a", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete Skill assignment = %d %s", deleted.Code, deleted.Body.String())
	}

	// A subject that exists but is already granted still probes first.
	expectSubjectExists(pool, "engineering")
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_assignments`).WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnError(&pgconn.PgError{Code: "23505"})
	pool.ExpectRollback()
	duplicate := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"team","id":"engineering"}}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"ASSIGNMENT_EXISTS"`) {
		t.Fatalf("duplicate Skill assignment = %d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestUserSkillManifestDownloadAndSync(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	blobs := newMemorySkillBlobStore()
	blobs.objects["skills/writer/2.0.0/package.zip"] = []byte("PK\x03\x04authorized-package")
	application.Blobs = blobs
	handler := New(application).Handler()

	manifestRows := func() *pgxmock.Rows {
		return pgxmock.NewRows([]string{"id", "name", "version", "sha256", "size_bytes"}).AddRow("writer", "Writer", "2.0.0", "abc123", int64(24))
	}
	pool.ExpectQuery(skillManifestQueryPattern).WithArgs("deployment-a", "user-a").WillReturnRows(manifestRows())
	manifest := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/skills/manifest", "")
	etag := manifest.Header().Get("ETag")
	if manifest.Code != http.StatusOK || etag == "" || !strings.Contains(manifest.Body.String(), `"id":"writer"`) || !strings.Contains(manifest.Body.String(), `"sha256":"abc123"`) {
		t.Fatalf("Skill manifest = %d %s, ETag = %q", manifest.Code, manifest.Body.String(), etag)
	}

	pool.ExpectQuery(skillManifestQueryPattern).WithArgs("deployment-a", "user-a").WillReturnRows(manifestRows())
	request := httptest.NewRequest(http.MethodGet, "/aep/v1/user/skills/manifest", nil)
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Authorization", "Bearer "+userToken)
	request.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, request)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("conditional Skill manifest = %d %s", notModified.Code, notModified.Body.String())
	}

	pool.ExpectQuery(skillPackageQueryPattern).WithArgs("deployment-a", "user-a", "writer", "2.0.0").
		WillReturnRows(pgxmock.NewRows([]string{"object_key"}).AddRow("skills/writer/2.0.0/package.zip"))
	download := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/skills/writer/versions/2.0.0/package", "")
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/zip" || download.Header().Get("Cache-Control") != "private, no-store" || download.Header().Get("Content-Disposition") != `attachment; filename="writer-2.0.0.zip"` || download.Body.String() != "PK\x03\x04authorized-package" {
		t.Fatalf("download Skill package = %d %q, disposition = %q", download.Code, download.Body.String(), download.Header().Get("Content-Disposition"))
	}

	pool.ExpectQuery(skillPackageQueryPattern).WithArgs("deployment-a", "user-a", "missing", "1.0.0").WillReturnError(pgx.ErrNoRows)
	denied := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/skills/missing/versions/1.0.0/package", "")
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), `"code":"SKILL_NOT_ASSIGNED"`) {
		t.Fatalf("unassigned Skill package = %d %s", denied.Code, denied.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_sync_results`).WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a", "session-user", "revision-2", "completed", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	synced := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-2","status":"completed","items":[{"skillId":"zeta","status":"unchanged"},{"skillId":"writer","status":"installed"},{"skillId":"failed","status":"failed"}]}`)
	if synced.Code != http.StatusAccepted {
		t.Fatalf("Skill sync result = %d %s", synced.Code, synced.Body.String())
	}
}

// A package is only downloadable while its Skill is effective. The grant alone
// was once enough: a disabled Skill stayed on nobody's manifest but still
// served its package. The effectiveness rules the manifest already applied are
// pinned through skillPackageQueryPattern, so a download query that drops one
// of them stops matching and the mock refuses to answer.
func TestUserSkillPackageDownloadRequiresEffectiveSkill(t *testing.T) {
	const (
		path          = "/aep/v1/user/skills/writer/versions/2.0.0/package"
		objectKeyPath = "skills/writer/2.0.0/package.zip"
	)
	t.Run("disabled Skill with a live grant", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		application.Blobs = &memorySkillBlobStore{objects: map[string][]byte{objectKeyPath: []byte("PK\x03\x04disabled-skill-package")}}
		// skills.enabled=false leaves the grant and its expiry in place, so the
		// shared effectiveness query matches nothing: the object key never
		// reaches the handler.
		pool.ExpectQuery(skillPackageQueryPattern).WithArgs("deployment-a", "user-a", "writer", "2.0.0").WillReturnError(pgx.ErrNoRows)
		response := userRequest(New(application).Handler(), userToken, http.MethodGet, path, "")
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"SKILL_NOT_ASSIGNED"`) {
			t.Fatalf("disabled Skill package = %d %s", response.Code, response.Body.String())
		}
		// The refusal is the ordinary "not assigned" one: it must not disclose
		// why the Skill is ineffective.
		if body := response.Body.String(); strings.Contains(body, "enabled") || strings.Contains(body, "disabled") {
			t.Fatalf("disabled Skill refusal discloses state: %s", body)
		}
	})

	t.Run("re-enabled Skill", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		application.Blobs = &memorySkillBlobStore{objects: map[string][]byte{objectKeyPath: []byte("PK\x03\x04re-enabled-package")}}
		pool.ExpectQuery(skillPackageQueryPattern).WithArgs("deployment-a", "user-a", "writer", "2.0.0").
			WillReturnRows(pgxmock.NewRows([]string{"object_key"}).AddRow(objectKeyPath))
		response := userRequest(New(application).Handler(), userToken, http.MethodGet, path, "")
		if response.Code != http.StatusOK || response.Body.String() != "PK\x03\x04re-enabled-package" || response.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("re-enabled Skill package = %d %q", response.Code, response.Body.String())
		}
	})

	t.Run("expired grant", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		application.Blobs = &memorySkillBlobStore{objects: map[string][]byte{objectKeyPath: []byte("PK\x03\x04expired-grant-package")}}
		// The expiry rule the download already had is untouched, and it stays in
		// the shared fragment: an expired grant keeps matching nothing.
		pool.ExpectQuery(skillPackageQueryPattern).WithArgs("deployment-a", "user-a", "writer", "2.0.0").WillReturnError(pgx.ErrNoRows)
		response := userRequest(New(application).Handler(), userToken, http.MethodGet, path, "")
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"SKILL_NOT_ASSIGNED"`) {
			t.Fatalf("expired grant package = %d %s", response.Code, response.Body.String())
		}
	})
}

// The manifest and the package download must answer the same effectiveness
// question, so both are built from skillEffectivenessQuery and neither may
// re-state one of its rules - a second copy is how the download came to ignore
// `sk.enabled` while the manifest enforced it.
func TestSkillQueriesShareEffectivenessRules(t *testing.T) {
	queries := map[string]string{"manifest": skillManifestQuery, "package download": skillPackageQuery}
	tails := make(map[string]string, len(queries))
	for name, query := range queries {
		if !strings.HasPrefix(query, skillEffectivenessQuery) || strings.Count(query, skillEffectivenessQuery) != 1 {
			t.Fatalf("%s query is not the shared effectiveness query plus its own tail: %s", name, query)
		}
		tails[name] = strings.TrimPrefix(query, skillEffectivenessQuery)
	}

	rules := []string{
		"sk.enabled=true",
		"sv.published=true",
		"sa.deployment_id=$1",
		"(sa.expires_at IS NULL OR sa.expires_at>now())",
		"sa.subject_type='user' AND sa.subject_id=$2",
		"sa.subject_type='role'",
		"sa.subject_type='team'",
		"r.enabled=true",
		"t.enabled=true",
	}
	for _, rule := range rules {
		if !strings.Contains(skillEffectivenessQuery, rule) {
			t.Fatalf("the shared effectiveness query lost the rule %q", rule)
		}
		for name, tail := range tails {
			if strings.Contains(tail, rule) {
				t.Fatalf("%s query re-states the rule %q outside the shared fragment", name, rule)
			}
		}
	}

	// Neither tail may reach back into the tables the shared fragment queries:
	// an extra join there is the other way a second, divergent rule reappears.
	for name, tail := range tails {
		if strings.Contains(tail, "skill_versions sv") || strings.Contains(tail, "skill_assignments sa") {
			t.Fatalf("%s query re-joins the Skill tables outside the shared fragment: %s", name, tail)
		}
	}
	// The download resolves one version of the effective set.
	if !strings.Contains(tails["package download"], "FROM effective WHERE skill_id=$3 AND version=$4") {
		t.Fatalf("package download no longer resolves a version of the effective set: %s", tails["package download"])
	}
}
