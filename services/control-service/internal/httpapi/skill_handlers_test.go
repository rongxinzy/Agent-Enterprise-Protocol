package httpapi

import (
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func TestSkillVersionsJSONIncludesAdminStateAndDigest(t *testing.T) {
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	items := skillVersionsJSON([]repository.SkillVersion{
		{SkillID: "writing", Version: "1.0.0", SHA256: "abc", SizeBytes: 42, CreatedAt: createdAt},
		{SkillID: "writing", Version: "0.9.0", SHA256: "def", SizeBytes: 24, Published: true, CreatedAt: createdAt},
	})
	if len(items) != 2 {
		t.Fatalf("version count = %d", len(items))
	}
	if items[0]["state"] != "draft" || items[0]["sha256"] != "abc" || items[0]["size"] != int64(42) {
		t.Fatalf("draft version = %#v", items[0])
	}
	if items[1]["state"] != "published" {
		t.Fatalf("published version = %#v", items[1])
	}
}

func TestSkillJSONUsesCanonicalStateAndLegacyEnabled(t *testing.T) {
	active := skillJSON(repository.Skill{ID: "writing", Name: "写作", Enabled: true}, nil)
	if active["state"] != "active" || active["enabled"] != true {
		t.Fatalf("active Skill = %#v", active)
	}
	withdrawn := skillJSON(repository.Skill{ID: "writing", Name: "写作", Enabled: false}, nil)
	if withdrawn["state"] != "withdrawn" || withdrawn["enabled"] != false {
		t.Fatalf("withdrawn Skill = %#v", withdrawn)
	}
}

func TestSkillEnabledFromPatchMapsStateAndRejectsConflict(t *testing.T) {
	active, err := skillEnabledFromPatch(stringPointer("active"), nil)
	if err != nil || active == nil || !*active {
		t.Fatalf("active state = %v, %v", active, err)
	}
	withdrawn, err := skillEnabledFromPatch(stringPointer("withdrawn"), nil)
	if err != nil || withdrawn == nil || *withdrawn {
		t.Fatalf("withdrawn state = %v, %v", withdrawn, err)
	}
	if _, err := skillEnabledFromPatch(stringPointer("invalid"), nil); err == nil {
		t.Fatal("invalid state was accepted")
	}
	if _, err := skillEnabledFromPatch(stringPointer("active"), boolPointer(false)); err == nil {
		t.Fatal("conflicting state and enabled fields were accepted")
	}
}

func stringPointer(value string) *string { return &value }

func boolPointer(value bool) *bool { return &value }
