package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestReplaceUserRBACValidationBoundaries(t *testing.T) {
	application, adminToken, _ := testHTTPApplication(t)
	handler := New(application).Handler()
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "missing role and team", body: `{}`, code: "USER_RBAC_REQUIRED"},
		{name: "invalid role", body: `{"roleIds":["bad/id"],"teamIds":["engineering"]}`, code: "INVALID_ROLE"},
		{name: "invalid team", body: `{"roleIds":["member"],"teamIds":["bad/id"]}`, code: "INVALID_TEAM"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/users/user-a/rbac", test.body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("RBAC validation = %d %s", response.Code, response.Body.String())
			}
		})
	}

	tooMany := make([]string, 65)
	for index := range tooMany {
		tooMany[index] = "role-" + string(rune('a'+index%26)) + string(rune('0'+index/26))
	}
	roles, err := json.Marshal(map[string]any{"roleIds": tooMany, "teamIds": []string{"engineering"}})
	if err != nil {
		t.Fatal(err)
	}
	response := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/users/user-a/rbac", string(roles))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"USER_RBAC_REQUIRED"`) {
		t.Fatalf("too many roles = %d %s", response.Code, response.Body.String())
	}
}
