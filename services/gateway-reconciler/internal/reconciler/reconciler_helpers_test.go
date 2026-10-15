package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomic(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "dir", "state.yaml")

	if err := writeAtomic(path, []byte("content")); err != nil {
		t.Fatalf("writeAtomic(): %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "content" {
		t.Fatalf("read back = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}

	// A failed write must not clobber the previous content: point the target
	// at a path whose parent is a regular file so MkdirAll fails.
	blocked := filepath.Join(root, "nested", "dir", "state.yaml", "child")
	if err := writeAtomic(blocked, []byte("x")); err == nil {
		t.Fatal("writing under a file must fail")
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "content" {
		t.Fatalf("original content lost: %q, %v", got, err)
	}

	// No temporary files are left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".reconcile-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}
