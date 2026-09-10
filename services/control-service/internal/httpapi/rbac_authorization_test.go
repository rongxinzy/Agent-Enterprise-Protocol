package httpapi

import (
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func TestPermissionSubset(t *testing.T) {
	tests := []struct {
		name      string
		grantor   []string
		requested []string
		want      bool
	}{
		{name: "empty role", grantor: []string{"users.write"}, requested: nil, want: true},
		{name: "same permission", grantor: []string{"users.write"}, requested: []string{"users.write"}, want: true},
		{name: "strict subset", grantor: []string{"users.read", "users.write"}, requested: []string{"users.read"}, want: true},
		{name: "permission escalation", grantor: []string{"users.write"}, requested: []string{"roles.write"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := permissionSubset(test.grantor, test.requested); got != test.want {
				t.Fatalf("permissionSubset() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRoleGrantAllowed(t *testing.T) {
	custom := repository.RoleRecord{
		Role:        repository.Role{ID: "user-manager"},
		Permissions: []string{"users.read", "users.write"},
	}
	builtIn := repository.RoleRecord{
		Role:        repository.Role{ID: "admin", BuiltIn: true},
		Permissions: []string{"users.read", "users.write", "roles.write"},
	}

	if !roleGrantAllowed(false, []string{"users.read", "users.write"}, []repository.RoleRecord{custom}) {
		t.Fatal("delegated administrator could not grant a permission subset")
	}
	if roleGrantAllowed(false, []string{"users.write"}, []repository.RoleRecord{custom}) {
		t.Fatal("delegated administrator granted a role containing an unheld permission")
	}
	if roleGrantAllowed(false, builtIn.Permissions, []repository.RoleRecord{builtIn}) {
		t.Fatal("delegated administrator granted a built-in role")
	}
	if !roleGrantAllowed(true, nil, []repository.RoleRecord{builtIn}) {
		t.Fatal("system administrator could not grant the built-in administrator role")
	}
}
