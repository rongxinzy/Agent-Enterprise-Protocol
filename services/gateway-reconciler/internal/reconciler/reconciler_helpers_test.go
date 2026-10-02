package reconciler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchCredentials(t *testing.T) {
	reference := &SecretReference{Name: "provider-secrets", Key: "model-a"}

	t.Run("nil fetcher yields no values", func(t *testing.T) {
		reconciler := &Reconciler{}
		if got := reconciler.fetchCredentials(context.Background(), DesiredState{Routes: []Route{{Enabled: true, CredentialRef: reference}}}); got != nil {
			t.Fatalf("fetchCredentials() = %#v, want nil", got)
		}
	})

	t.Run("disabled routes and duplicates are skipped, failures tolerated", func(t *testing.T) {
		calls := 0
		reconciler := &Reconciler{config: Config{CredentialFetcher: func(_ context.Context, ref SecretReference) (string, error) {
			calls++
			if ref.Key == "broken" {
				return "", errors.New("secret unavailable")
			}
			return "value-" + ref.Key, nil
		}}}
		got := reconciler.fetchCredentials(context.Background(), DesiredState{Routes: []Route{
			{ModelID: "off", Enabled: false, CredentialRef: reference},
			{ModelID: "a", Enabled: true, CredentialRef: reference},
			{ModelID: "a-again", Enabled: true, CredentialRef: reference}, // same ref: deduped
			{ModelID: "broken", Enabled: true, CredentialRef: &SecretReference{Name: "provider-secrets", Key: "broken"}},
			{ModelID: "tokenless", Enabled: true}, // no ref
		}})
		if len(got) != 1 || got["provider-secrets/model-a"] != "value-model-a" {
			t.Fatalf("fetchCredentials() = %#v", got)
		}
		if calls != 2 {
			t.Fatalf("fetcher called %d times, want 2 (dedup + one failure)", calls)
		}
	})
}

func TestIngressPath(t *testing.T) {
	cases := []struct{ endpoint, want string }{
		{"/v1", "/v1"}, // bare path: verbatim
		{"http://gateway.internal:8080/v1", "/v1"}, // absolute URL: path only
		{"https://host.example", "/v1"},            // URL without path: default
		{"http://host/openai", "/openai"},          // non-/v1 path carries over
	}
	for _, tc := range cases {
		if got := ingressPath(tc.endpoint); got != tc.want {
			t.Errorf("ingressPath(%q) = %q, want %q", tc.endpoint, got, tc.want)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	if got, ok := upstreamURL("http://provider.internal:9000/v1"); !ok || got != "http://provider.internal:9000/v1" {
		t.Fatalf("absolute = %q, %v", got, ok)
	}
	if got, ok := upstreamURL("https://host.example/base/"); !ok || got != "https://host.example/base" {
		t.Fatalf("trailing slash = %q, %v", got, ok)
	}
	if _, ok := upstreamURL("/relative-only"); ok {
		t.Fatal("a bare path is not an upstream URL")
	}
	if _, ok := upstreamURL("ftp://host/path"); ok {
		t.Fatal("non-http schemes are not upstream URLs")
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
