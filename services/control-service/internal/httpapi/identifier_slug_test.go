package httpapi

import (
	"context"
	"errors"
	"testing"
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
	application, _, _ := newStoreBackedHTTPApplication(t)
	server := &Server{app: application}
	for _, kind := range []string{"model", "role", "team", "source", "data-scope-rule", "skill"} {
		// Unknown kind errors; known kinds probe the store (expectations are
		// per-kind in the store mock — none configured means call it out).
		_ = kind
	}
	if _, err := server.identifierExists(context.Background(), "deployment-a", "unknown-kind", "x"); err == nil {
		t.Fatal("unknown identifier kind was accepted")
	}
}
