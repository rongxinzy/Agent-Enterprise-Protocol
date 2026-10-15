package httpapi

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// An unmapped admin route is gated only by the admin flag, so a role holding the
// matching fine-grained permission is refused while the console - which gates its
// controls on permissions - offers controls that can never work. Walk the real
// route table, not a hand-written list: that is how the assignment routes stayed
// unmapped (the mapping tested synthetic paths like /skills/x/assignment).
func TestAdminRoutesHavePermissionMappings(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`admin\.(Get|Post|Patch|Put|Delete)\("([^"]+)"`).FindAllStringSubmatch(string(source), -1)
	if len(routes) == 0 {
		t.Fatal("no admin routes found in server.go; has the route table moved?")
	}
	for _, route := range routes {
		method, path := route[1], route[2]
		if len(requiredAdminPermission(method, path)) == 0 {
			t.Errorf("%s %s has no permission mapping", method, path)
		}
	}
}

func TestAssignmentRoutesMapToAssignPermissions(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		want   string
	}{
		{method: http.MethodPost, path: "/aep/v1/admin/skill-assignments", want: "skills.assign"},
		{method: http.MethodDelete, path: "/aep/v1/admin/skill-assignments/assignment-a", want: "skills.assign"},
		{method: http.MethodGet, path: "/aep/v1/admin/skill-assignments", want: "skills.read"},
		{method: http.MethodPost, path: "/aep/v1/admin/model-assignments", want: "models.assign"},
		{method: http.MethodGet, path: "/aep/v1/admin/model-assignments", want: "models.read"},
		{method: http.MethodPost, path: "/aep/v1/admin/credential-assignments", want: "credentials.assign"},
		{method: http.MethodDelete, path: "/aep/v1/admin/credential-assignments/assignment-a", want: "credentials.assign"},
	} {
		permissions := requiredAdminPermission(test.method, test.path)
		if len(permissions) != 1 || permissions[0] != test.want {
			t.Errorf("%s %s = %v, want [%s]", test.method, test.path, permissions, test.want)
		}
	}
}

// The permission is only worth having if it works end to end: a non-admin whose
// role carries skills.assign must be able to create a grant.
func TestAdminAssignmentPermissionAllowsDelegatedWrite(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "skills.assign").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectBegin()
	expectSubjectGuard(pool, "user", "user-a", true)
	pool.ExpectExec(`INSERT INTO skill_assignments`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "writer", "user", "user-a", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSkillAssignmentEvent(pool)
	pool.ExpectCommit()

	response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"writer","subject":{"type":"user","id":"user-a"}}`)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"skillId":"writer"`) {
		t.Fatalf("delegated assignment = %d %s", response.Code, response.Body.String())
	}
}
