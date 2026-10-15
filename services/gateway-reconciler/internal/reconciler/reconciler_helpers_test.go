package reconciler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func TestFetchCredentialsPinnedNamespace(t *testing.T) {
	t.Run("nil fetcher", func(t *testing.T) {
		worker := &Reconciler{}
		got, err := worker.fetchCredentials(t.Context(), DesiredState{})
		if got != nil || err != nil {
			t.Fatalf("fetchCredentials() = %v, %v; want nil, nil", got, err)
		}
	})

	for _, name := range []string{"", "higress-system", "other-namespace"} {
		t.Run("stored namespace "+name, func(t *testing.T) {
			var namespace *string
			if name != "" {
				namespace = new(name)
			}
			ref := &gatewayrender.SecretReference{Name: "aep-credential-c1", Key: "api-key", Namespace: namespace}
			calls := 0
			worker := &Reconciler{config: Config{CredentialFetcher: func(_ context.Context, got gatewayrender.SecretReference) (string, error) {
				calls++
				if got.Namespace == nil || *got.Namespace != CredentialSecretNamespace || got.Name != ref.Name || got.Key != ref.Key {
					t.Fatalf("reference = %+v, want pinned namespace and original name/key", got)
				}
				return "value", nil
			}}}
			got, err := worker.fetchCredentials(t.Context(), DesiredState{Routes: []gatewayrender.Route{
				{Enabled: true, CredentialRef: ref},
				{Enabled: true, CredentialRef: ref},
				{Enabled: false, CredentialRef: &gatewayrender.SecretReference{Name: "disabled", Key: "api-key"}},
			}})
			if err != nil || got["aep-credential-c1/api-key"] != "value" || calls != 1 {
				t.Fatalf("credentials = %v, error = %v, calls = %d", got, err, calls)
			}
			if ref.Namespace != namespace {
				t.Fatal("fetch mutated the desired-state reference and its canonical hash")
			}
		})
	}
}

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
