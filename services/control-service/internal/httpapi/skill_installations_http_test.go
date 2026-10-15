package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const skillInstallationsQuery = `WITH latest AS`

func TestSkillInstallationState(t *testing.T) {
	for _, test := range []struct {
		name      string
		reported  bool
		installed []string
		want      string
	}{
		{name: "no report", reported: false, installed: nil, want: "unreported"},
		{name: "listed as installed", reported: true, installed: []string{"writer", "reader"}, want: "installed"},
		{name: "reported without the Skill", reported: true, installed: []string{"reader"}, want: "missing"},
		{name: "reported an empty list", reported: true, installed: []string{}, want: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := skillInstallationState(test.reported, test.installed, "writer"); got != test.want {
				t.Fatalf("skillInstallationState() = %q, want %q", got, test.want)
			}
		})
	}
}

// The per-item status is passed through as reported; `"items": null` is a shape
// real reports can carry and must not break the view.
func TestSkillSyncItemStatus(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{name: "item status", payload: `{"items":[{"skillId":"writer","status":"removed"}]}`, want: "removed"},
		{name: "other skill only", payload: `{"items":[{"skillId":"reader","status":"failed"}]}`, want: ""},
		{name: "null items", payload: `{"items":null}`, want: ""},
		{name: "missing items", payload: `{}`, want: ""},
		{name: "items is not an array", payload: `{"items":{"skillId":"writer"}}`, want: ""},
		{name: "empty payload", payload: ``, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := skillSyncItemStatus([]byte(test.payload), "writer"); got != test.want {
				t.Fatalf("skillSyncItemStatus(%s) = %q, want %q", test.payload, got, test.want)
			}
		})
	}
}

// The view is scoped to the users the Skill is granted to: the summary counts
// them all, and only those whose report does not show the Skill installed need
// attention.
func TestAdminSkillInstallationsSummary(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
	pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "installed_skill_ids", "payload"}).
			AddRow("user-a", "revision-1", now, []string{"writer"}, []byte(`{"items":[{"skillId":"writer","status":"installed"}]}`)).
			AddRow("user-b", "revision-2", now.Add(-time.Minute), []string{}, []byte(`{"items":[{"skillId":"writer","status":"removed"}]}`)).
			AddRow("user-c", nil, nil, []string{}, []byte(`{}`)))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
	if response.Code != http.StatusOK {
		t.Fatalf("Skill installations = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`"skillId":"writer"`,
		`"granted":3`, `"reported":2`, `"installed":1`, `"missing":1`, `"unreported":1`,
		`"userId":"user-b"`, `"state":"missing"`, `"revision":"revision-2"`,
		`"itemStatus":"removed"`,
		`"userId":"user-c"`, `"state":"unreported"`,
		`"truncated":false`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Skill installations missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, `"userId":"user-a"`) {
		t.Fatalf("an installed user must not be listed as needing attention: %s", body)
	}
	if strings.Index(body, "user-b") > strings.Index(body, "user-c") {
		t.Fatalf("a reported-missing user must be listed before one that never reported: %s", body)
	}
}

// A user whose report shows the Skill missing outranks one that never reported,
// so the limit cannot hide the more actionable row.
func TestAdminSkillInstallationsTruncates(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
	pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "installed_skill_ids", "payload"}).
			AddRow("user-a", nil, nil, []string{}, []byte(`{}`)).
			AddRow("user-b", "revision-1", now, []string{}, []byte(`{}`)))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations?limit=1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("truncated Skill installations = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"truncated":true`) || !strings.Contains(body, `"reported":1`) || !strings.Contains(body, `"unreported":1`) {
		t.Fatalf("truncated Skill installations = %s", body)
	}
	if !strings.Contains(body, `"userId":"user-b"`) || strings.Contains(body, `"userId":"user-a"`) {
		t.Fatalf("the reported-missing user must survive the limit: %s", body)
	}
}

// `"items": null` in a real report must not fail the whole view.
func TestAdminSkillInstallationsToleratesNullItems(t *testing.T) {
	application, mock, pool, adminToken := newUserHTTPApplication(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
	pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "installed_skill_ids", "payload"}).
			AddRow("user-a", "revision-1", now, []string{}, []byte(`{"items":null}`)))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
	if response.Code != http.StatusOK {
		t.Fatalf("null-items Skill installations = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"state":"missing"`) || strings.Contains(response.Body.String(), `"itemStatus"`) {
		t.Fatalf("null-items Skill installations = %s", response.Body.String())
	}
}

// Nobody granted the Skill (or nobody reported on it): an empty view, and the
// unknown Skill is a 404 rather than a silently empty page.
func TestAdminSkillInstallationsEmptyAndUnknownSkill(t *testing.T) {
	t.Run("no grants", func(t *testing.T) {
		application, mock, pool, adminToken := newUserHTTPApplication(t)
		now := time.Now().UTC()
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
			WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
		pool.ExpectQuery(skillInstallationsQuery).WithArgs("deployment-a", "writer").
			WillReturnRows(pgxmock.NewRows([]string{"user_id", "revision", "created_at", "installed_skill_ids", "payload"}))
		response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
		if response.Code != http.StatusOK {
			t.Fatalf("empty Skill installations = %d %s", response.Code, response.Body.String())
		}
		body := response.Body.String()
		if !strings.Contains(body, `"granted":0`) || !strings.Contains(body, `"latestReportedAt":null`) || !strings.Contains(body, `"items":[]`) {
			t.Fatalf("empty Skill installations = %s", body)
		}
	})

	t.Run("unknown skill", func(t *testing.T) {
		application, mock, _, adminToken := newUserHTTPApplication(t)
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
			WillReturnRows(sqlmock.NewRows(skillColumns()))
		response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/writer/installations", "")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
			t.Fatalf("unknown Skill installations = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestAdminSkillInstallationsBoundaries(t *testing.T) {
	t.Run("invalid skill id", func(t *testing.T) {
		application, _, _, adminToken := newUserHTTPApplication(t)
		// No expectations are queued: an invalid identifier is refused before
		// either the store or the report query is touched.
		response := adminRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skills/-bad-/installations", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_ID")
	})

	t.Run("database failure", func(t *testing.T) {
		application, mock, pool, adminToken := newUserHTTPApplication(t)
		now := time.Now().UTC()
		mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
			WillReturnRows(sqlmock.NewRows(skillColumns()).AddRow("writer", "Writer", "", true, now, now))
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
