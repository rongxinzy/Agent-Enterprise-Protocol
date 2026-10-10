package httpapi

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
)

// validateSkillPackage enforces the package contract of section 8.3 before the
// bytes are stored: a ZIP archive with SKILL.md at its root. The control plane
// is the last hop that can refuse a package before it is fanned out to every
// assigned Agent, where installation - not upload - would otherwise be the
// first place the mistake surfaced.
func validateSkillPackage(archive []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return errors.New("the Skill package is not a ZIP archive")
	}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if packageEntryPath(entry.Name) == "SKILL.md" {
			return nil
		}
	}
	return errors.New("the Skill package does not contain SKILL.md at its root")
}

// packageEntryPath normalizes a ZIP entry name the way the installing Agent
// does before it looks for SKILL.md: Windows separators become "/" and empty
// or "." segments are dropped, so "./SKILL.md" is a root SKILL.md while
// "nested/SKILL.md" is not.
func packageEntryPath(name string) string {
	segments := strings.Split(strings.ReplaceAll(name, `\`, "/"), "/")
	trimmed := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		trimmed = append(trimmed, segment)
	}
	return strings.Join(trimmed, "/")
}
