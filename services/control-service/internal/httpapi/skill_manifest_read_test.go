package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// A manifest read that ends in a terminating error must not be served as a
// complete list: the agent removes the managed Skills the response omits, so a
// truncated page would uninstall Skills that are still granted. The descriptor
// check itself lives in skillManifest; this file keeps the regression isolated
// so the shared-query work in the download path cannot rewrite it away.
func TestUserSkillManifestReadCompleteness(t *testing.T) {
	columns := []string{"id", "name", "version", "sha256", "size_bytes"}

	t.Run("terminating error before the first row", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`WITH (authorized|effective) AS`).WithArgs("deployment-a", "user-a").
			WillReturnRows(pgxmock.NewRows(columns).CloseError(errors.New("manifest read interrupted")))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/manifest", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("terminating error after a row was read", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`WITH (authorized|effective) AS`).WithArgs("deployment-a", "user-a").
			WillReturnRows(pgxmock.NewRows(columns).AddRow("writer", "Writer", "2.0.0", "abc123", int64(24)).CloseError(errors.New("manifest read interrupted")))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/manifest", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("a genuinely empty manifest stays successful", func(t *testing.T) {
		application, pool, _, token := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`WITH (authorized|effective) AS`).WithArgs("deployment-a", "user-a").
			WillReturnRows(pgxmock.NewRows(columns))
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/manifest", "")
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" || !strings.Contains(response.Body.String(), `"skills":[]`) {
			t.Fatalf("empty Skill manifest = %d %s, ETag = %q", response.Code, response.Body.String(), response.Header().Get("ETag"))
		}
	})
}
