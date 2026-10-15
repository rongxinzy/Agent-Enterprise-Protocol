package httpapi

import (
	"net/http"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

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
