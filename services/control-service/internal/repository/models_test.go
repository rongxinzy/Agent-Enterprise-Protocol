package repository

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestIdentityModelsUseExistingTables(t *testing.T) {
	tests := []struct {
		model any
		table string
	}{
		{model: Deployment{}, table: "deployments"},
		{model: User{}, table: "users"},
		{model: Permission{}, table: "permissions"},
		{model: Role{}, table: "roles"},
		{model: RolePermission{}, table: "role_permissions"},
		{model: Team{}, table: "teams"},
		{model: UserRoleBinding{}, table: "user_role_bindings"},
		{model: UserTeamBinding{}, table: "user_team_bindings"},
	}
	for _, test := range tests {
		parsed, err := schema.Parse(test.model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", test.model, err)
		}
		if parsed.Table != test.table {
			t.Errorf("%T table = %q, want %q", test.model, parsed.Table, test.table)
		}
	}
}

func TestDeploymentOwnedModelsUseCompositePrimaryKeys(t *testing.T) {
	tests := []struct {
		model       any
		primaryKeys []string
	}{
		{model: Role{}, primaryKeys: []string{"deployment_id", "id"}},
		{model: Team{}, primaryKeys: []string{"deployment_id", "id"}},
		{model: RolePermission{}, primaryKeys: []string{"deployment_id", "role_id", "permission_id"}},
		{model: UserRoleBinding{}, primaryKeys: []string{"deployment_id", "user_id", "role_id"}},
		{model: UserTeamBinding{}, primaryKeys: []string{"deployment_id", "user_id", "team_id"}},
	}
	for _, test := range tests {
		parsed, err := schema.Parse(test.model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", test.model, err)
		}
		if len(parsed.PrimaryFields) != len(test.primaryKeys) {
			t.Fatalf("%T primary key count = %d, want %d", test.model, len(parsed.PrimaryFields), len(test.primaryKeys))
		}
		for index, field := range parsed.PrimaryFields {
			if field.DBName != test.primaryKeys[index] {
				t.Errorf("%T primary key %d = %q, want %q", test.model, index, field.DBName, test.primaryKeys[index])
			}
		}
	}
}

func TestUniqueStringsPreservesFirstOccurrence(t *testing.T) {
	actual := uniqueStrings([]string{"role-b", "role-a", "role-b", "role-a"})
	want := []string{"role-b", "role-a"}
	if len(actual) != len(want) {
		t.Fatalf("unique strings = %#v, want %#v", actual, want)
	}
	for index := range want {
		if actual[index] != want[index] {
			t.Fatalf("unique strings = %#v, want %#v", actual, want)
		}
	}
}
