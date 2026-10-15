package httpapi

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

var (
	errSkillPackageNotZip     = errors.New("the Skill package is not a ZIP archive")
	errSkillPackageNoRootFile = errors.New("the Skill package does not contain a SKILL.md file")
)

// normalizeSkillPackage enforces the package contract of section 8.3 before the
// bytes are stored and returns the bytes that must actually be persisted. The
// contract stays "a ZIP archive with SKILL.md at its root" - so every Agent,
// including third-party ones, installs it unchanged - but the control plane
// locates the Skill root by the shallowest SKILL.md, moves that directory to the
// archive root, and drops everything outside it before storage. A package that
// no Agent could install is refused once, at upload, instead of failing at every
// install.
//
// When the descriptor already sits at the archive root and no deeper duplicate
// has to be discarded, the bytes are returned verbatim so the checksum covers
// exactly what the caller sent; the archive is only rewritten when it moves or
// drops an entry.
func normalizeSkillPackage(archive []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, errSkillPackageNotZip
	}
	root, err := skillPackageRoot(reader.File)
	if err != nil {
		return nil, err
	}
	if len(root) == 0 && !skillPackageHasDiscardedSkillMD(reader.File, root) {
		return archive, nil
	}
	return skillPackageAtRoot(reader.File, root)
}

// skillPackageRoot returns the directory that holds the SKILL.md descriptor: the
// shallowest directory depth at which a SKILL.md appears, so a descriptor buried
// at any depth - "skill-creator/skill-creator/SKILL.md" or "a/b/c/SKILL.md" -
// still identifies the Skill. When several SKILL.md files share that shallowest
// depth the package is ambiguous and refused, naming the offending paths.
func skillPackageRoot(entries []*zip.File) ([]string, error) {
	shallowest := -1
	var root []string
	var ambiguous []string
	for _, entry := range entries {
		if entry.FileInfo().IsDir() {
			continue
		}
		segments := skillPackageSegments(entry.Name)
		if len(segments) == 0 || segments[len(segments)-1] != "SKILL.md" {
			continue
		}
		depth := len(segments) - 1
		switch {
		case shallowest == -1 || depth < shallowest:
			shallowest = depth
			root = segments[:depth]
			ambiguous = []string{strings.Join(segments, "/")}
		case depth == shallowest:
			ambiguous = append(ambiguous, strings.Join(segments, "/"))
		}
	}
	switch {
	case shallowest == -1:
		return nil, errSkillPackageNoRootFile
	case len(ambiguous) > 1:
		sort.Strings(ambiguous)
		return nil, fmt.Errorf("the Skill package contains multiple SKILL.md files at the same depth: %s", strings.Join(ambiguous, ", "))
	default:
		return root, nil
	}
}

// skillPackageHasDiscardedSkillMD reports whether a non-shallowest SKILL.md
// would be dropped. Only the chosen descriptor survives; a deeper duplicate is
// discarded along with anything else outside the Skill root.
func skillPackageHasDiscardedSkillMD(entries []*zip.File, root []string) bool {
	for _, entry := range entries {
		if entry.FileInfo().IsDir() {
			continue
		}
		segments := skillPackageSegments(entry.Name)
		if len(segments) == 0 || segments[len(segments)-1] != "SKILL.md" {
			continue
		}
		if !slices.Equal(segments[:len(segments)-1], root) {
			return true
		}
	}
	return false
}

// skillPackageAtRoot rewrites the archive relative to the Skill root: entries
// below root are lifted to the archive root, entries outside root - including
// duplicate SKILL.md files below the chosen one - are dropped. Every surviving
// entry keeps its content and its file mode (executable bits included), and
// entries keep their input order, so the same upload always yields the same
// bytes.
func skillPackageAtRoot(entries []*zip.File, root []string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		segments := skillPackageSegments(entry.Name)
		if len(segments) < len(root) || !slices.Equal(segments[:len(root)], root) {
			// Outside the Skill root: not part of the package.
			continue
		}
		remaining := segments[len(root):]
		if len(remaining) == 0 || (remaining[len(remaining)-1] == "SKILL.md" && !slices.Equal(remaining, []string{"SKILL.md"})) {
			// The Skill root directory entry itself, or a duplicate descriptor
			// below the chosen one.
			continue
		}
		name := strings.Join(remaining, "/")
		if entry.FileInfo().IsDir() {
			name += "/"
		}
		header := entry.FileHeader
		header.Name = name
		header.SetMode(entry.Mode())
		target, err := writer.CreateHeader(&header)
		if err != nil {
			return nil, err
		}
		source, err := entry.Open()
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(target, source); err != nil {
			_ = source.Close()
			return nil, err
		}
		if err := source.Close(); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// skillPackageSegments splits a ZIP entry name the way the installing Agent does
// before it looks for SKILL.md: Windows separators become "/" and empty or "."
// segments are dropped, so "./SKILL.md" is a root SKILL.md while
// "nested/SKILL.md" is not.
func skillPackageSegments(name string) []string {
	segments := strings.Split(strings.ReplaceAll(name, `\`, "/"), "/")
	trimmed := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		trimmed = append(trimmed, segment)
	}
	return trimmed
}
