package httpapi

import "testing"

func TestUserMembershipProblem(t *testing.T) {
	tests := []struct {
		name  string
		roles []string
		teams []string
		code  string
	}{
		{name: "missing role", roles: nil, teams: []string{"all-users"}, code: "USER_RBAC_REQUIRED"},
		{name: "missing team", roles: []string{"admin"}, teams: nil, code: "USER_RBAC_REQUIRED"},
		{name: "invalid role", roles: []string{"bad role"}, teams: []string{"all-users"}, code: "INVALID_ROLE"},
		{name: "invalid team", roles: []string{"admin"}, teams: []string{"bad team"}, code: "INVALID_TEAM"},
		{name: "valid", roles: []string{"admin"}, teams: []string{"all-users"}, code: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, _ := userMembershipProblem(test.roles, test.teams)
			if code != test.code {
				t.Fatalf("userMembershipProblem() code = %q, want %q", code, test.code)
			}
		})
	}
}
