package httpapi

import (
	"errors"
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

func TestAdminModelAndCredentialAssignmentListsPaginate(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	for _, test := range []struct{ table, column, path, resourceType, resourceID string }{
		{"model_assignments", "model_id", "/aep/v1/admin/model-assignments", "model", "model-a"},
		{"credential_assignments", "credential_id", "/aep/v1/admin/credential-assignments", "credential", "credential-a"},
	} {
		mock.ExpectQuery(`SELECT \* FROM "`+test.table+`" WHERE deployment_id = \$1 ORDER BY created_at, id LIMIT \$2`).
			WithArgs("deployment-a", 2).
			WillReturnRows(sqlmock.NewRows(assignmentColumns(test.column)).
				AddRow(test.table+"-1", "deployment-a", test.resourceID, "user", "user-a", nil, assignmentNow).
				AddRow(test.table+"-2", "deployment-a", test.resourceID, "user", "user-b", nil, assignmentNow.Add(time.Minute)))
		first := userRequest(handler, adminToken, http.MethodGet, test.path+"?limit=1", "")
		if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"nextCursor":"2026-10-14T08:00:00Z|`+test.table+`-1"`) || !strings.Contains(first.Body.String(), `"resourceType":"`+test.resourceType+`"`) {
			t.Fatalf("%s first page = %d %s", test.path, first.Code, first.Body.String())
		}

		mock.ExpectQuery(`SELECT \* FROM "`+test.table+`" WHERE deployment_id = \$1 AND \(created_at, id\) > \(\$2, \$3\) ORDER BY created_at, id LIMIT \$4`).
			WithArgs("deployment-a", assignmentNow, test.table+"-1", 2).
			WillReturnRows(sqlmock.NewRows(assignmentColumns(test.column)).
				AddRow(test.table+"-2", "deployment-a", test.resourceID, "user", "user-b", nil, assignmentNow.Add(time.Minute)))
		second := userRequest(handler, adminToken, http.MethodGet, test.path+"?limit=1&cursor=2026-10-14T08:00:00Z%7C"+test.table+"-1", "")
		if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"nextCursor":null`) || !strings.Contains(second.Body.String(), `"id":"`+test.table+`-2"`) {
			t.Fatalf("%s second page = %d %s", test.path, second.Code, second.Body.String())
		}
	}
}

// A cursor this server did not issue cannot be positioned in the list: say so
// instead of running a query on a guessed position.
func TestAdminAssignmentListsRejectAnUnusableCursor(t *testing.T) {
	application, _, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	for _, path := range []string{
		"/aep/v1/admin/skill-assignments?cursor=not-a-cursor",
		"/aep/v1/admin/model-assignments?cursor=not-a-cursor",
		"/aep/v1/admin/credential-assignments?cursor=not-a-cursor",
	} {
		response := userRequest(handler, adminToken, http.MethodGet, path, "")
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
			t.Fatalf("%s = %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestAdminAssignmentListsReportDatabaseFailures(t *testing.T) {
	application, mock, _, adminToken := newUserHTTPApplication(t)
	handler := New(application).Handler()
	for _, test := range []struct{ table, path string }{
		{"model_assignments", "/aep/v1/admin/model-assignments"},
		{"credential_assignments", "/aep/v1/admin/credential-assignments"},
	} {
		mock.ExpectQuery(`SELECT \* FROM "`+test.table+`" WHERE deployment_id = \$1 ORDER BY created_at, id LIMIT \$2`).
			WithArgs("deployment-a", 51).
			WillReturnError(errors.New("assignments unavailable"))
		response := userRequest(handler, adminToken, http.MethodGet, test.path, "")
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) {
			t.Fatalf("%s = %d %s", test.path, response.Code, response.Body.String())
		}
	}
}
