package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const skillInstallationsQuery = `WITH latest AS`

// The admin view answers "did the Skill actually reach the machines": the
// summary counts the latest report per user, and only the users whose report
// does not show the Skill installed are listed.
func TestAdminSkillInstallationsSummary(t *testing.T) {
	application, _, pool, adminToken := newUserHTTPApplication(t)
	now := time.Now().UTC()
	pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "state"}).
			AddRow("user-a", "revision-1", now, "installed").
			AddRow("user-b", "revision-2", now.Add(-time.Minute), "failed").
			AddRow("user-c", "revision-3", now.Add(-2*time.Minute), "absent"))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
	if response.Code != http.StatusOK {
		t.Fatalf("Skill installations = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`"skillId":"writer"`,
		`"reported":3`, `"installed":1`, `"failed":1`, `"absent":1`,
		`"userId":"user-b"`, `"state":"failed"`, `"revision":"revision-2"`,
		`"userId":"user-c"`, `"state":"absent"`,
		`"truncated":false`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Skill installations missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, `"userId":"user-a"`) {
		t.Fatalf("an installed user must not be listed as needing attention: %s", body)
	}
}

func TestAdminSkillInstallationsTruncates(t *testing.T) {
	application, _, pool, adminToken := newUserHTTPApplication(t)
	now := time.Now().UTC()
	pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "state"}).
			AddRow("user-a", "revision-1", now, "failed").
			AddRow("user-b", "revision-1", now, "absent"))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations?limit=1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("Skill installations = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"truncated":true`) || !strings.Contains(body, `"reported":2`) || strings.Contains(body, `"userId":"user-b"`) {
		t.Fatalf("truncated Skill installations = %s", body)
	}
}

// A Skill nobody reported on yet is an empty view, not an error.
func TestAdminSkillInstallationsEmpty(t *testing.T) {
	application, _, pool, adminToken := newUserHTTPApplication(t)
	pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "state"}))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
	if response.Code != http.StatusOK {
		t.Fatalf("empty Skill installations = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"reported":0`) || !strings.Contains(body, `"latestReportedAt":null`) || !strings.Contains(body, `"items":[]`) {
		t.Fatalf("empty Skill installations = %s", body)
	}
}

func TestAdminSkillInstallationsBoundaries(t *testing.T) {
	t.Run("invalid skill id", func(t *testing.T) {
		application, _, _, adminToken := newUserHTTPApplication(t)
		// No query expectation is queued: an invalid identifier is refused
		// before the database is touched.
		response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/-bad-/installations", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_ID")
	})

	t.Run("database failure", func(t *testing.T) {
		application, _, pool, adminToken := newUserHTTPApplication(t)
		pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
			WillReturnError(errors.New("installations unavailable"))
		response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusInternalServerError, "INTERNAL_ERROR")
	})

	t.Run("requires admin", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "skills.read").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		response := userRequest(New(application).Handler(), userToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"ACCESS_DENIED"`) {
			t.Fatalf("non-admin Skill installations = %d %s", response.Code, response.Body.String())
		}
	})
}
