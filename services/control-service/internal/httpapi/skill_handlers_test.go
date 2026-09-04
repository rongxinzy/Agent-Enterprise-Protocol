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
