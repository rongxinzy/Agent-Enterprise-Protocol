package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestSkillIdentifierValidation(t *testing.T) {
	for _, value := range []string{"a", "review", "Writer_2", "office.docx-skill"} {
		if !validSkillIdentifier(value) {
			t.Errorf("validSkillIdentifier(%q) = false", value)
		}
	}
	for _, value := range []string{"", ".hidden", "trailing-", "../escape", "skill/version", `skill\version`, strings.Repeat("a", 65)} {
		if validSkillIdentifier(value) {
			t.Errorf("validSkillIdentifier(%q) = true", value)
		}
	}
}

func TestSkillVersionIdentifierValidation(t *testing.T) {
	for _, value := range []string{"1", "1.0.0", "2026.09-rc.1", "build+20260912"} {
		if !validSkillVersionIdentifier(value) {
			t.Errorf("validSkillVersionIdentifier(%q) = false", value)
		}
	}
	for _, value := range []string{"", ".1.0", "1.0.", "../1.0", "1/2", `1\2`, strings.Repeat("a", 129)} {
		if validSkillVersionIdentifier(value) {
			t.Errorf("validSkillVersionIdentifier(%q) = true", value)
		}
	}
}

func TestSkillObjectKeyRequiresValidatedSegments(t *testing.T) {
	key, ok := skillObjectKey("review", "1.0.0", "abc123")
	if !ok || key != "skills/review/1.0.0/abc123.zip" {
		t.Fatalf("skillObjectKey() = %q, %t", key, ok)
	}
	for _, input := range [][2]string{{"../review", "1.0.0"}, {"review", "../1.0.0"}} {
		if key, ok := skillObjectKey(input[0], input[1], "abc123"); ok || key != "" {
			t.Errorf("skillObjectKey(%q, %q) = %q, %t", input[0], input[1], key, ok)
		}
	}
}

func TestSkillHTTPRejectsUnsafeIdentifiersBeforePersistence(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skills", `{"id":"../writer","name":"Writer","description":""}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_ID")
	})

	t.Run("upload version", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		blobs := newMemorySkillBlobStore()
		application.Blobs = blobs
		response := uploadSkillRequest(t, New(application).Handler(), token, "/aep/v1/admin/skills/writer/versions", "../1.0.0", []byte("archive"))
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_VERSION")
		if len(blobs.objects) != 0 {
			t.Fatalf("unsafe version stored objects: %#v", blobs.objects)
		}
	})

	t.Run("assignment", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := adminRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/skill-assignments", `{"skillId":"../writer","subject":{"type":"user","id":"user-a"}}`)
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_ID")
	})

	t.Run("download", func(t *testing.T) {
		application, _, _, token := newUserHTTPApplication(t)
		response := userRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/user/skills/writer/versions/%2E%2E/package", "")
		requireSkillProblem(t, response.Code, response.Body.String(), http.StatusBadRequest, "INVALID_SKILL_VERSION")
	})
}
