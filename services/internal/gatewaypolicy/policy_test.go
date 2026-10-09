package gatewaypolicy

import (
	"regexp"
	"strings"
	"testing"
)

func text(value string) *string { return &value }
func TestNativeRuleKeys(t *testing.T) {
	keys, err := Keys("model-a", "user-a", []string{"team-a", "team-b"}, []string{"role-a", "role-b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ kind, id string }{{"global", ""}, {"model", "model-a"}, {"user", "user-a"}, {"team", "team-b"}, {"role", "role-b"}} {
		c := Configuration{Kind: "tokens", ScopeType: scope.kind, Maximum: 100, Interval: "minute", Enabled: true}
		if scope.id != "" {
			c.ScopeID = text(scope.id)
		}
		if !c.Valid() {
			t.Fatal(c)
		}
		if !regexp.MustCompile(strings.TrimPrefix(Pattern(c), "regexp:")).MatchString(keys) {
			t.Fatal("native selector did not match", c, keys)
		}
		if scope.kind != "model" {
			c.ModelID = text("model-a")
			if !regexp.MustCompile(strings.TrimPrefix(Pattern(c), "regexp:")).MatchString(keys) {
				t.Fatal("model restriction did not match")
			}
			c.ModelID = text("model-b")
			if regexp.MustCompile(strings.TrimPrefix(Pattern(c), "regexp:")).MatchString(keys) {
				t.Fatal("model scope escaped")
			}
		}
	}
	if _, err := Keys(strings.Repeat("m", 256), "u", make([]string, 1000), nil); err == nil {
		t.Fatal("unbounded headers")
	}
	if Revision([]Limit{{ID: "b"}, {ID: "a"}}) != Revision([]Limit{{ID: "a"}, {ID: "b"}}) {
		t.Fatal("unstable publication")
	}
}

func TestConfigurationValidation(t *testing.T) {
	valid := Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "day"}
	for _, mutate := range []func(*Configuration){func(c *Configuration) { c.Kind = "unknown" }, func(c *Configuration) { c.Maximum = 0 }, func(c *Configuration) { c.Maximum = 1e12 + 1 }, func(c *Configuration) { c.ExpectedVersion = -1 }, func(c *Configuration) { c.Interval = "week" }, func(c *Configuration) { c.ModelID = text("") }, func(c *Configuration) { c.ModelID = text(strings.Repeat("m", 257)) }, func(c *Configuration) { c.ScopeID = text("extra") }, func(c *Configuration) { c.ScopeType = "unknown" }, func(c *Configuration) { c.ScopeType = "team"; c.ScopeID = text("") }, func(c *Configuration) { c.ScopeType = "model"; c.ScopeID = text("a"); c.ModelID = text("b") }} {
		c := valid
		mutate(&c)
		if c.Valid() {
			t.Fatal(c)
		}
	}
	if !ValidID("rule-a.1") || ValidID("../bad") || ValidID(strings.Repeat("a", 81)) {
		t.Fatal("rule ID validation")
	}
}
