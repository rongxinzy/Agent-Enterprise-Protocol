package httpapi

import (
	"net/http"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// expectSubjectExists queues the existence probe every polymorphic assignment
// and data-scope write performs before it stores anything.
func expectSubjectExists(pool pgxmock.PgxPoolIface, id string) {
	pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM (users|roles|teams) WHERE deployment_id=\$1 AND id=\$2\)`).
		WithArgs("deployment-a", id).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
}

// A grant may only name a subject that exists: ids carry no foreign key, so a
// write that races a delete (or simply names a stale id) would otherwise leave a
// grant behind for a later subject that reuses the identifier.
func TestAdminAssignmentRejectsUnknownSubject(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "skill assignment",
			path: "/aep/v1/admin/skill-assignments",
			body: `{"skillId":"writer","subject":{"type":"role","id":"member"}}`,
		},
		{
			name: "model assignment",
			path: "/aep/v1/admin/model-assignments",
			body: `{"modelId":"chat","subject":{"type":"team","id":"engineering"}}`,
		},
		{
			name: "credential assignment",
			path: "/aep/v1/admin/credential-assignments",
			body: `{"credentialId":"credential-a","subject":{"type":"user","id":"user-a"}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, _, pool, adminToken := newUserHTTPApplication(t)
			expectSubject := "member"
			if test.path == "/aep/v1/admin/credential-assignments" {
				expectSubject = "user-a"
			} else if strings.Contains(test.path, "model") {
				expectSubject = "engineering"
			}
			pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM (users|roles|teams) WHERE deployment_id=\$1 AND id=\$2\)`).
				WithArgs("deployment-a", expectSubject).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

			response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, test.path, test.body)
			// No INSERT expectation is queued: a rejected subject must not be
			// stored.
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_SUBJECT"`) {
				t.Fatalf("unknown subject = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAdminDataScopeRuleRejectsUnknownSubject(t *testing.T) {
	application, _, pool, adminToken, _ := newDirectoryHTTPApplication(t)
	pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM (users|roles|teams) WHERE deployment_id=\$1 AND id=\$2\)`).
		WithArgs("deployment-a", "member").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/data-scope-rules", `{"ruleKind":"exception_grant","subjectType":"role","subjectId":"member","resourceKind":"model","resourceId":"chat"}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_SUBJECT"`) {
		t.Fatalf("unknown data-scope subject = %d %s", response.Code, response.Body.String())
	}
}
