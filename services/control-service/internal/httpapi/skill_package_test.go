package httpapi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
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

// skillPackageWithModes builds a ZIP archive honouring explicit file modes and
// directory entries, so tests can assert that normalization preserves the
// executable bit.
func skillPackageWithModes(t *testing.T, entries []struct {
	name string
	body string
	mode fs.FileMode
}) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		header.SetMode(item.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(item.body)); err != nil {
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

// skillArchiveEntries returns the sorted entry names of a stored package, so
// tests can assert the shape written to object storage.
func skillArchiveEntries(t *testing.T, archive []byte) []string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, entry := range reader.File {
		names = append(names, entry.Name)
	}
	sort.Strings(names)
	return names
}

func TestNormalizeSkillPackage(t *testing.T) {
	for _, test := range []struct {
		name        string
		archive     []byte
		valid       bool
		identity    bool
		wantEntries []string
	}{
		{name: "root SKILL.md", archive: skillArchive(t), valid: true, identity: true},
		{name: "dot-prefixed root SKILL.md", archive: skillPackage(t, map[string]string{"./SKILL.md": "body"}), valid: true, identity: true},
		{name: "with references", archive: skillPackage(t, map[string]string{"SKILL.md": "body", "references/info.txt": "x"}), valid: true, identity: true},
		{name: "root SKILL.md with a directory entry", archive: skillPackage(t, map[string]string{"SKILL.md": "body", "agents/": "", "agents/tool.txt": "tool"}), valid: true, identity: true},
		{name: "empty archive", archive: skillPackage(t, nil)},
		{name: "truncated archive", archive: []byte("PK\x03\x04test-skill-archive")},
		{name: "plain text", archive: []byte("skill-version-package")},
		{name: "wrong case", archive: skillPackage(t, map[string]string{"skill.md": "body"})},
		{name: "directory named SKILL.md", archive: skillPackage(t, map[string]string{"SKILL.md/": ""})},
		{
			name:        "one-level wrapper",
			archive:     skillPackage(t, map[string]string{"outer/SKILL.md": "body", "outer/references/x.txt": "x"}),
			valid:       true,
			wantEntries: []string{"SKILL.md", "references/x.txt"},
		},
		{
			name: "two-level wrapper of the real package shape",
			archive: skillPackage(t, map[string]string{
				"skill-creator/skill-creator/SKILL.md":         "body",
				"skill-creator/skill-creator/LICENSE":          "license",
				"skill-creator/skill-creator/agents/tool.txt":  "tool",
				"skill-creator/skill-creator/references/x.txt": "ref",
			}),
			valid:       true,
			wantEntries: []string{"LICENSE", "SKILL.md", "agents/tool.txt", "references/x.txt"},
		},
		{
			name:        "deeply buried descriptor",
			archive:     skillPackage(t, map[string]string{"a/b/c/SKILL.md": "body", "a/b/c/agents/tool.txt": "tool"}),
			valid:       true,
			wantEntries: []string{"SKILL.md", "agents/tool.txt"},
		},
		{
			name:        "root README plus a wrapped Skill",
			archive:     skillPackage(t, map[string]string{"README.md": "readme", "skill/SKILL.md": "body", "skill/agents/tool.txt": "tool"}),
			valid:       true,
			wantEntries: []string{"SKILL.md", "agents/tool.txt"},
		},
		{
			name:        "shallow descriptor wins over a deeper duplicate",
			archive:     skillPackage(t, map[string]string{"SKILL.md": "body", "nested/SKILL.md": "deeper"}),
			valid:       true,
			wantEntries: []string{"SKILL.md"},
		},
		{
			name:        "backslash wrapper",
			archive:     skillPackage(t, map[string]string{`outer\SKILL.md`: "body", `outer\references\x.txt`: "x"}),
			valid:       true,
			wantEntries: []string{"SKILL.md", "references/x.txt"},
		},
		{name: "same-depth duplicate", archive: skillPackage(t, map[string]string{"x/SKILL.md": "body", "y/SKILL.md": "body"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizeSkillPackage(test.archive)
			if test.valid && err != nil {
				t.Fatalf("normalizeSkillPackage() = %v, want nil", err)
			}
			if !test.valid {
				if err == nil {
					t.Fatal("normalizeSkillPackage() = nil, want an error")
				}
				return
			}
			if test.identity {
				if !bytes.Equal(normalized, test.archive) {
					t.Fatal("normalizeSkillPackage() rewrote a package that was already at the root")
				}
				return
			}
			entries := skillArchiveEntries(t, normalized)
			if strings.Join(entries, ",") != strings.Join(test.wantEntries, ",") {
				t.Fatalf("normalized entries = %v, want %v", entries, test.wantEntries)
			}
			if !slices.Contains(entries, "SKILL.md") {
				t.Fatalf("normalized package has no root SKILL.md: %v", entries)
			}
		})
	}
}

// A package with several equally shallow SKILL.md files is ambiguous, and the
// refusal names the paths so the uploader knows what to fix.
func TestNormalizeSkillPackageRejectsAmbiguousDescriptor(t *testing.T) {
	archive := skillPackage(t, map[string]string{"alpha/SKILL.md": "body", "beta/SKILL.md": "body"})
	_, err := normalizeSkillPackage(archive)
	if err == nil {
		t.Fatal("normalizeSkillPackage() accepted an ambiguous package")
	}
	if !strings.Contains(err.Error(), "alpha/SKILL.md") || !strings.Contains(err.Error(), "beta/SKILL.md") {
		t.Fatalf("normalizeSkillPackage() error = %q, want both paths named", err)
	}
}

// The same upload must always yield the same stored bytes, so the manifest
// digest is reproducible across retries.
func TestNormalizeSkillPackageIsDeterministic(t *testing.T) {
	archive := skillPackage(t, map[string]string{
		"skill-creator/skill-creator/SKILL.md":         "body",
		"skill-creator/skill-creator/agents/tool.txt":  "tool",
		"skill-creator/skill-creator/references/x.txt": "ref",
	})
	first, err := normalizeSkillPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeSkillPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("normalizeSkillPackage() is not deterministic")
	}
}

// Moving the Skill to the archive root must not cost a script its executable
// bit, and directory entries inside the Skill must survive.
func TestNormalizeSkillPackagePreservesModesAndDirectories(t *testing.T) {
	archive := skillPackageWithModes(t, []struct {
		name string
		body string
		mode fs.FileMode
	}{
		{name: "outer/", mode: fs.ModeDir | 0o755},
		{name: "outer/scripts/run.sh", body: "echo hi", mode: 0o755},
		{name: "outer/agents/", mode: fs.ModeDir | 0o755},
		{name: "outer/SKILL.md", body: "body", mode: 0o644},
	})
	normalized, err := normalizeSkillPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(normalized), int64(len(normalized)))
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]fs.FileMode{}
	for _, entry := range reader.File {
		modes[entry.Name] = entry.Mode()
	}
	if modes["SKILL.md"]&0o777 != 0o644 {
		t.Fatalf("SKILL.md mode = %v, want 0644", modes["SKILL.md"])
	}
	if modes["scripts/run.sh"]&0o777 != 0o755 {
		t.Fatalf("scripts/run.sh mode = %v, want 0755", modes["scripts/run.sh"])
	}
	if _, ok := modes["agents/"]; !ok {
		t.Fatalf("inner directory entry was dropped: %v", modes)
	}
}

// A package whose entry data cannot be read is rejected rather than stored
// truncated: the rewrite must surface the archive reader's error.
func TestNormalizeSkillPackageSurfacesReadErrors(t *testing.T) {
	// A damaged local file header still leaves the central directory readable,
	// so opening the entry - not parsing the archive - is what fails.
	deflated := skillPackage(t, map[string]string{"outer/SKILL.md": strings.Repeat("body", 64)})
	headerIndex := bytes.Index(deflated, []byte("PK\x03\x04"))
	if headerIndex < 0 {
		t.Fatal("test archive has no local file header")
	}
	brokenHeader := bytes.Clone(deflated)
	brokenHeader[headerIndex+2] = 'X'
	if _, err := normalizeSkillPackage(brokenHeader); err == nil {
		t.Fatal("normalizeSkillPackage() accepted an entry with a damaged header")
	}

	// A stored wrapper whose payload no longer matches its recorded checksum
	// fails while it is copied out, not when it is opened.
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "outer/SKILL.md", Method: zip.Store}
	header.SetMode(0o644)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(strings.Repeat("body", 64))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := buffer.Bytes()
	index := bytes.Index(corrupt, []byte("PK\x03\x04"))
	nameLength := int(corrupt[index+26]) | int(corrupt[index+27])<<8
	extraLength := int(corrupt[index+28]) | int(corrupt[index+29])<<8
	corrupt[index+30+nameLength+extraLength] ^= 0xff
	if _, err := normalizeSkillPackage(corrupt); err == nil {
		t.Fatal("normalizeSkillPackage() accepted an entry with a bad checksum")
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
		{name: "no SKILL.md", archive: skillPackage(t, map[string]string{"only.txt": "x"})},
		{name: "ambiguous descriptor", archive: skillPackage(t, map[string]string{"alpha/SKILL.md": "body", "beta/SKILL.md": "body"})},
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

// A wrapped package is accepted and stored at the Skill root: the persisted
// bytes, the checksum, and the size all describe the normalized archive.
func TestAdminSkillUploadUnwrapsPackage(t *testing.T) {
	for _, test := range []struct {
		name        string
		entries     map[string]string
		wantEntries []string
	}{
		{
			name:        "one-level wrapper",
			entries:     map[string]string{"outer/SKILL.md": "body", "outer/references/x.txt": "x"},
			wantEntries: []string{"SKILL.md", "references/x.txt"},
		},
		{
			name:        "real two-level wrapper",
			entries:     map[string]string{"skill-creator/skill-creator/SKILL.md": "body", "skill-creator/skill-creator/agents/tool.txt": "tool", "skill-creator/skill-creator/LICENSE": "license"},
			wantEntries: []string{"LICENSE", "SKILL.md", "agents/tool.txt"},
		},
		{
			name:        "root README plus a wrapped Skill",
			entries:     map[string]string{"README.md": "readme", "skill/SKILL.md": "body", "skill/agents/tool.txt": "tool"},
			wantEntries: []string{"SKILL.md", "agents/tool.txt"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, mock, pool, adminToken := newUserHTTPApplication(t)
			blobs := newMemorySkillBlobStore()
			application.Blobs = blobs
			handler := New(application).Handler()

			archive := skillPackage(t, test.entries)
			normalized, err := normalizeSkillPackage(archive)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(normalized)
			sha := hex.EncodeToString(digest[:])
			objectKey := "skills/writer/1.0.0/" + sha + ".zip"
			mock.ExpectQuery(`INSERT INTO skill_versions`).WithArgs("writer", "1.0.0", objectKey, sha, int64(len(normalized))).
				WillReturnRows(sqlmock.NewRows([]string{"published"}).AddRow(false))
			pool.ExpectExec(`INSERT INTO telemetry_events`).
				WithArgs(pgxmock.AnyArg(), "deployment-a", "admin-user", "session-admin", "skill.version.uploaded", "skill", "writer", "success", pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			expectSkillAdminAudit(pool, "create", "writer")

			response := uploadSkillRequest(t, handler, adminToken, "/aep/v1/admin/skills/writer/versions", "1.0.0", archive)
			if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"sha256":"`+sha+`"`) {
				t.Fatalf("upload of a wrapped package = %d %s", response.Code, response.Body.String())
			}
			if !bytes.Equal(blobs.objects[objectKey], normalized) {
				t.Fatalf("stored bytes = %q, want normalized %q", blobs.objects[objectKey], normalized)
			}
			if entries := skillArchiveEntries(t, blobs.objects[objectKey]); strings.Join(entries, ",") != strings.Join(test.wantEntries, ",") {
				t.Fatalf("stored entries = %v, want %v", entries, test.wantEntries)
			}
		})
	}
}
