package repository

import (
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
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
		{model: Skill{}, table: "skills"},
		{model: SkillVersion{}, table: "skill_versions"},
		{model: SkillAssignment{}, table: "skill_assignments"},
		{model: Credential{}, table: "credentials"},
		{model: CredentialAssignment{}, table: "credential_assignments"},
		{model: Model{}, table: "models"},
		{model: ModelAssignment{}, table: "model_assignments"},
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
		{model: SkillVersion{}, primaryKeys: []string{"skill_id", "version"}},
		{model: Credential{}, primaryKeys: []string{"deployment_id", "id"}},
		{model: Model{}, primaryKeys: []string{"deployment_id", "id"}},
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

func TestStringArrayScansAndEncodesPostgresText(t *testing.T) {
	var values StringArray
	if err := values.Scan(`{"a,b",reasoning,streaming}`); err != nil {
		t.Fatalf("scan string array: %v", err)
	}
	want := []string{"a,b", "reasoning", "streaming"}
	if len(values) != len(want) {
		t.Fatalf("scanned values = %#v, want %#v", values, want)
	}
	for index := range want {
		if values[index] != want[index] {
			t.Fatalf("scanned values = %#v, want %#v", values, want)
		}
	}
	encoded, err := values.Value()
	if err != nil {
		t.Fatalf("encode string array: %v", err)
	}
	if encoded != `{"a,b",reasoning,streaming}` {
		t.Fatalf("encoded values = %q", encoded)
	}
}

func TestStringArrayScansNativePGXSlice(t *testing.T) {
	values := StringArray{"old"}
	if err := values.Scan([]string{"reasoning", "streaming"}); err != nil {
		t.Fatalf("scan native pgx string array: %v", err)
	}
	if len(values) != 2 || values[0] != "reasoning" || values[1] != "streaming" {
		t.Fatalf("scanned values = %#v", values)
	}
}

func TestStringArrayScansPostgresBinary(t *testing.T) {
	encoded, err := pgtype.NewMap().Encode(
		pgtype.TextArrayOID, pgtype.BinaryFormatCode, []string{"reasoning", "streaming"}, nil,
	)
	if err != nil {
		t.Fatalf("encode binary string array: %v", err)
	}
	var values StringArray
	if err := values.Scan(encoded); err != nil {
		t.Fatalf("scan binary string array: %v", err)
	}
	if len(values) != 2 || values[0] != "reasoning" || values[1] != "streaming" {
		t.Fatalf("scanned values = %#v", values)
	}
}
