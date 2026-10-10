package httpapi

import (
	"archive/zip"
	"bytes"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// skillPackage builds a ZIP archive with the given entries, matching the
// package contract the upload path enforces.
func skillPackage(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(entries[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// skillArchive is a minimal conforming package: a ZIP archive with SKILL.md at
// its root.
func skillArchive(t *testing.T) []byte {
	t.Helper()
	return skillPackage(t, map[string]string{"SKILL.md": "---\nname: Test\n---\n"})
}

func TestValidateSkillPackage(t *testing.T) {
	for _, test := range []struct {
		name    string
		archive []byte
		valid   bool
	}{
		{name: "root SKILL.md", archive: skillArchive(t), valid: true},
		{name: "dot-prefixed root SKILL.md", archive: skillPackage(t, map[string]string{"./SKILL.md": "body"}), valid: true},
		{name: "with references", archive: skillPackage(t, map[string]string{"SKILL.md": "body", "references/info.txt": "x"}), valid: true},
		{name: "empty archive", archive: skillPackage(t, nil)},
		{name: "truncated archive", archive: []byte("PK\x03\x04test-skill-archive")},
		{name: "plain text", archive: []byte("skill-version-package")},
		{name: "nested SKILL.md", archive: skillPackage(t, map[string]string{"nested/SKILL.md": "body"})},
		{name: "wrapped in a directory", archive: skillPackage(t, map[string]string{"demo/SKILL.md": "body"})},
		{name: "wrong case", archive: skillPackage(t, map[string]string{"skill.md": "body"})},
		{name: "directory named SKILL.md", archive: skillPackage(t, map[string]string{"SKILL.md/": ""})},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateSkillPackage(test.archive)
			if test.valid && err != nil {
				t.Fatalf("validateSkillPackage() = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("validateSkillPackage() = nil, want an error")
			}
		})
	}
}

// A package that no Agent could install must be refused once, at upload, rather
// than accepted here and refused at every install.
func TestAdminSkillUploadRejectsInvalidPackage(t *testing.T) {
	for _, test := range []struct {
		name    string
		archive []byte
	}{
		{name: "not a ZIP", archive: []byte("skill-version-package")},
		{name: "no root SKILL.md", archive: skillPackage(t, map[string]string{"nested/SKILL.md": "body"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, _, _, adminToken := newUserHTTPApplication(t)
			blobs := newMemorySkillBlobStore()
			application.Blobs = blobs
			// No database expectation is queued: a rejected package must not
			// reach object storage or the Skill version table.
			response := uploadSkillRequest(t, New(application).Handler(), adminToken, "/aep/v1/admin/skills/writer/versions", "1.0.0", test.archive)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_SKILL_PACKAGE"`) {
				t.Fatalf("upload of an invalid package = %d %s", response.Code, response.Body.String())
			}
			if len(blobs.objects) != 0 {
				t.Fatalf("rejected package was stored: %#v", blobs.objects)
			}
		})
	}
}
