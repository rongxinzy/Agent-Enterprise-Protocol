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

var assignmentNow = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

// The three assignment lists used to return every row in the deployment with no
// bound and no way to continue. They now take limit/cursor like the other list
// endpoints. The cursor carries (created_at, id): assignment ids are random
// UUIDs, so paging by the id alone would lose the chronological order the
// console shows and skip rows that a concurrent write placed behind the cursor.
func TestAdminSkillAssignmentListPaginates(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id LIMIT \$2`).
		WithArgs("deployment-a", 2).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("skill_id")).
			AddRow("assignment-a", "deployment-a", "writer", "user", "user-a", nil, assignmentNow).
			AddRow("assignment-b", "deployment-a", "writer", "user", "user-b", nil, assignmentNow.Add(time.Minute)))
	first := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skill-assignments?limit=1", "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"nextCursor":"2026-10-14T08:00:00Z|assignment-a"`) || strings.Contains(first.Body.String(), "assignment-b") {
		t.Fatalf("first page = %d %s", first.Code, first.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 AND \(created_at, id\) > \(\$2, \$3\) ORDER BY created_at, id LIMIT \$4`).
		WithArgs("deployment-a", assignmentNow, "assignment-a", 2).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("skill_id")).
			AddRow("assignment-b", "deployment-a", "writer", "user", "user-b", nil, assignmentNow.Add(time.Minute)))
	second := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/skill-assignments?limit=1&cursor=2026-10-14T08:00:00Z%7Cassignment-a", "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"nextCursor":null`) || !strings.Contains(second.Body.String(), `"id":"assignment-b"`) {
		t.Fatalf("second page = %d %s", second.Code, second.Body.String())
	}
}

func TestAdminModelAndCredentialAssignmentListsReportCursor(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()

	mock.ExpectQuery(`SELECT \* FROM "model_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id LIMIT \$2`).
		WithArgs("deployment-a", 51).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("model_id")).
			AddRow("model-assignment-a", "deployment-a", "model-a", "user", "user-a", nil, assignmentNow))
	models := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/model-assignments", "")
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"nextCursor":null`) || !strings.Contains(models.Body.String(), `"resourceId":"model-a"`) {
		t.Fatalf("model list = %d %s", models.Code, models.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "credential_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id LIMIT \$2`).
		WithArgs("deployment-a", 51).
		WillReturnRows(sqlmock.NewRows(assignmentColumns("credential_id")).
			AddRow("credential-assignment-a", "deployment-a", "credential-a", "user", "user-a", nil, assignmentNow))
	credentials := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/credential-assignments", "")
	if credentials.Code != http.StatusOK || !strings.Contains(credentials.Body.String(), `"nextCursor":null`) || !strings.Contains(credentials.Body.String(), `"resourceId":"credential-a"`) {
		t.Fatalf("credential list = %d %s", credentials.Code, credentials.Body.String())
	}
}

// A cursor this server did not issue cannot be positioned in the list: say so
// instead of running a query on a guessed position.
func TestAdminAssignmentListRejectsAnUnusableCursor(t *testing.T) {
	application, _, _, adminToken := newUserHTTPApplication(t)
	response := userRequest(New(application).Handler(), adminToken, http.MethodGet, "/aep/v1/admin/skill-assignments?cursor=not-a-cursor", "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("unusable cursor = %d %s", response.Code, response.Body.String())
	}
}
