package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func assignmentColumns(model string) []string {
	return []string{"id", "deployment_id", model, "subject_type", "subject_id", "expires_at", "created_at"}
}

// The three assignment lists used to return every row in the deployment with no
// bound and no way to continue; a deployment with more grants than one page
// silently showed a truncated list. They now take limit/cursor like the other
// list endpoints and report where to resume.
func TestAdminSkillAssignmentListPaginates(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 2).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("skill_id")).
			AddRow("assignment-a", "deployment-a", "writer", "user", "user-a", nil, now).
			AddRow("assignment-b", "deployment-a", "writer", "user", "user-b", nil, now))
	first := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skill-assignments?limit=1", "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"nextCursor":"assignment-a"`) || strings.Contains(first.Body.String(), "assignment-b") {
		t.Fatalf("first page = %d %s", first.Code, first.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 AND id > \$2 ORDER BY id LIMIT \$3`).
		WithArgs("deployment-a", "assignment-a", 2).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("skill_id")).
			AddRow("assignment-b", "deployment-a", "writer", "user", "user-b", nil, now))
	second := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skill-assignments?limit=1&cursor=assignment-a", "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"nextCursor":null`) || !strings.Contains(second.Body.String(), `"id":"assignment-b"`) {
		t.Fatalf("second page = %d %s", second.Code, second.Body.String())
	}
}

func TestAdminModelAndCredentialAssignmentListsReportCursor(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT \* FROM "model_assignments" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 51).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("model_id")).
			AddRow("model-assignment-a", "deployment-a", "model-a", "user", "user-a", nil, now))
	models := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/model-assignments", "")
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"nextCursor":null`) || !strings.Contains(models.Body.String(), `"resourceId":"model-a"`) {
		t.Fatalf("model list = %d %s", models.Code, models.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "credential_assignments" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 51).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("credential_id")).
			AddRow("credential-assignment-a", "deployment-a", "credential-a", "user", "user-a", nil, now))
	credentials := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/credential-assignments", "")
	if credentials.Code != http.StatusOK || !strings.Contains(credentials.Body.String(), `"nextCursor":null`) || !strings.Contains(credentials.Body.String(), `"resourceId":"credential-a"`) {
		t.Fatalf("credential list = %d %s", credentials.Code, credentials.Body.String())
	}
}
