package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// storedPayload matches the JSON the server writes into
// `skill_sync_results.payload`. It compares the value as data, so key order and
// spacing do not decide the outcome.
type storedPayload struct{ want string }

func (m storedPayload) Match(value interface{}) bool {
	var encoded []byte
	switch typed := value.(type) {
	case []byte:
		encoded = typed
	case string:
		encoded = []byte(typed)
	default:
		return false
	}
	var actual, expected interface{}
	if json.Unmarshal(encoded, &actual) != nil || json.Unmarshal([]byte(m.want), &expected) != nil {
		return false
	}
	return reflect.DeepEqual(actual, expected)
}

// The guide used to document `manifestRevision` while the server has always read
// `revision`: a report built against the old guide must be refused, not stored
// without its revision.
func TestSkillSyncResultRejectsManifestRevision(t *testing.T) {
	application, _, _, userToken := newRuntimeHTTPApplication(t)
	// No insert expectation is queued: a refused report must not be stored.
	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"manifestRevision":"43","status":"completed","items":[{"skillId":"writer","status":"installed"}]}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("manifestRevision report = %d %s", response.Code, response.Body.String())
	}
}

func TestSkillSyncResultAcceptsRevision(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_sync_results`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a", "session-user", "revision-9", "completed", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-9","status":"completed","items":[{"skillId":"writer","status":"installed"}]}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("revision report = %d %s", response.Code, response.Body.String())
	}
}

// A pass that fails one Skill reports `errorCode` and `message` on that item.
// SkillSyncItem declares both, and decoding is strict, so naming them in the
// decoded item is what keeps the whole report - including the Skills that did
// sync - from being refused with a 400.
func TestSkillSyncResultKeepsFailedItemError(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_sync_results`).
		WithArgs(
			pgxmock.AnyArg(),
			"deployment-a", "user-a", "session-user",
			"revision-11", "partial",
			// Only the Skill that counts as installed is named; the failed one
			// is not.
			[]string{"writer"},
			// The payload is the decoded report re-marshalled, so the absent
			// top-level `manifestRevision` lands as null while the per-item
			// `errorCode` and `message` are kept.
			storedPayload{want: `{"revision":"revision-11","manifestRevision":null,"status":"partial","items":[{"skillId":"writer","status":"installed"},{"skillId":"xlsx","status":"failed","errorCode":"SKILL_INSTALL_FAILED","message":"object storage unavailable"}]}`},
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-11","status":"partial","items":[{"skillId":"writer","status":"installed"},{"skillId":"xlsx","status":"failed","errorCode":"SKILL_INSTALL_FAILED","message":"object storage unavailable"}]}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("failed Skill report = %d %s", response.Code, response.Body.String())
	}
}

// `version` and `action` are declared on SkillSyncItem. They are advisory, so
// they only have to survive into the stored payload; which Skills count as
// installed still comes from `status`.
func TestSkillSyncResultKeepsItemVersionAndAction(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_sync_results`).
		WithArgs(
			pgxmock.AnyArg(),
			"deployment-a", "user-a", "session-user",
			"revision-12", "completed",
			[]string{"writer"},
			storedPayload{want: `{"revision":"revision-12","manifestRevision":null,"status":"completed","items":[{"skillId":"writer","version":"2.0.0","action":"update","status":"updated"}]}`},
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-12","status":"completed","items":[{"skillId":"writer","version":"2.0.0","action":"update","status":"updated"}]}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("versioned Skill report = %d %s", response.Code, response.Body.String())
	}
}

// The contract declares `startedAt` and `completedAt` on the report itself;
// they are kept as reported and read by nothing.
func TestSkillSyncResultKeepsReportTimestamps(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO skill_sync_results`).
		WithArgs(
			pgxmock.AnyArg(),
			"deployment-a", "user-a", "session-user",
			"revision-13", "completed",
			[]string{"writer"},
			storedPayload{want: `{"revision":"revision-13","manifestRevision":null,"startedAt":"2026-08-19T08:01:00Z","completedAt":"2026-08-19T08:02:03Z","status":"completed","items":[{"skillId":"writer","status":"unchanged"}]}`},
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-13","startedAt":"2026-08-19T08:01:00Z","completedAt":"2026-08-19T08:02:03Z","status":"completed","items":[{"skillId":"writer","status":"unchanged"}]}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("timestamped report = %d %s", response.Code, response.Body.String())
	}
}

// Accepting the two documented error fields must not relax the rest: a field
// the server does not read is still refused instead of being dropped.
func TestSkillSyncResultRejectsUnknownItemField(t *testing.T) {
	application, _, _, userToken := newRuntimeHTTPApplication(t)
	// No insert expectation is queued: a refused report must not be stored.
	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/skills/sync-results", `{"revision":"revision-9","status":"completed","items":[{"skillId":"writer","status":"installed","bogus":1}]}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("unknown item field = %d %s", response.Code, response.Body.String())
	}
}
