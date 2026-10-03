package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSlugFor(t *testing.T) {
	t.Parallel()
	for name, expected := range map[string]string{
		"Model Operator":        "model-operator",
		"Platform Team":         "platform-team",
		"WeCom":                 "wecom",
		"  Bench -- Anthropic ": "bench-anthropic",
		"Bench_Anthropic/01":    "bench-anthropic-01",
		"UPPER CASE NAME":       "upper-case-name",
		"企业微信":                  "",
		"---":                   "",
		"":                      "",
	} {
		if got := slugFor(name); got != expected {
			t.Fatalf("slugFor(%q) = %q, want %q", name, got, expected)
		}
	}
	long := slugFor(string(make([]byte, 0)) + repeat("a", 60))
	if len(long) != 40 {
		t.Fatalf("slug length = %d, want capped at 40", len(long))
	}
}

func repeat(character string, count int) string {
	result := make([]byte, 0, count)
	for index := 0; index < count; index++ {
		result = append(result, character[0])
	}
	return string(result)
}

func TestUniqueIdentifierSuffixesAndFallbacks(t *testing.T) {
	t.Parallel()
	taken := map[string]bool{"model": true, "model-2": true, "model-3": true}
	identifier, err := uniqueIdentifier(context.Background(), "model", "model", func(_ context.Context, candidate string) (bool, error) {
		return taken[candidate], nil
	})
	if err != nil || identifier != "model-4" {
		t.Fatalf("uniqueIdentifier() = %q, %v", identifier, err)
	}

	// Empty slug falls back to the resource kind, then suffixes.
	identifier, err = uniqueIdentifier(context.Background(), "", "team", func(_ context.Context, _ string) (bool, error) {
		return false, nil
	})
	if err != nil || identifier != "team" {
		t.Fatalf("fallback identifier = %q, %v", identifier, err)
	}

	// Probe failures propagate.
	if _, err := uniqueIdentifier(context.Background(), "x", "role", func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("database unavailable")
	}); err == nil {
		t.Fatal("probe error was swallowed")
	}
}

func TestIdentifierExistsKinds(t *testing.T) {
	t.Parallel()
	// Every kind probes its store surface; unknown kinds are rejected.
	application, mock, _ := newStoreBackedHTTPApplication(t)
	server := &Server{app: application}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "probe-model").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "probe-role", 1).WillReturnRows(sqlmock.NewRows(roleHTTPColumns()))
	mock.ExpectQuery(`FROM "teams" LEFT JOIN user_team_bindings`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "name", "description", "parent_team_id", "path", "depth", "enabled", "created_at", "updated_at", "member_count"}))
	mock.ExpectQuery(`SELECT \* FROM "identity_sources"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "kind", "display_name", "config", "enabled", "created_at", "updated_at"}))
	mock.ExpectQuery(`SELECT \* FROM "data_scope_rules"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at", "reason", "created_by", "created_at", "updated_at"}))
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).
		WithArgs("probe-skill", 1).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "enabled", "created_at", "updated_at"}))
	for _, kind := range []string{"model", "role", "team", "source", "data-scope-rule", "skill"} {
		if _, err := server.identifierExists(context.Background(), "deployment-a", kind, "probe-"+kind); err != nil {
			t.Fatalf("identifierExists(%q) = %v", kind, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.identifierExists(context.Background(), "deployment-a", "unknown-kind", "x"); err == nil {
		t.Fatal("unknown identifier kind was accepted")
	}
}

func TestUniqueIdentifierExhaustsToRandomTail(t *testing.T) {
	t.Parallel()
	// Beyond -99 every candidate stays taken; the identifier keeps moving and
	// carries a random hex tail.
	calls := 0
	identifier, err := uniqueIdentifier(context.Background(), "model", "model", func(_ context.Context, _ string) (bool, error) {
		calls++
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(identifier, "model-") || len(identifier) <= len("model-99") {
		t.Fatalf("random-tail identifier = %q", identifier)
	}
	if calls < 99 {
		t.Fatalf("probe calls = %d, expected the numeric suffix range exhausted", calls)
	}
}
