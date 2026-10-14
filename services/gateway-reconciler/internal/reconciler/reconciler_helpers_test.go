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

	t.Run("nil fetcher yields no values and no error", func(t *testing.T) {
		reconciler := &Reconciler{}
		got, err := reconciler.fetchCredentials(context.Background(), DesiredState{Routes: []Route{{Enabled: true, CredentialRef: reference}}})
		if got != nil || err != nil {
			t.Fatalf("fetchCredentials() = %#v, %v; want nil, nil", got, err)
		}
	})

	t.Run("disabled routes, duplicates, and tokenless routes are skipped", func(t *testing.T) {
		calls := 0
		reconciler := &Reconciler{config: Config{CredentialFetcher: func(_ context.Context, ref SecretReference) (string, error) {
			calls++
			return "value-" + ref.Key, nil
		}}}
		got, err := reconciler.fetchCredentials(context.Background(), DesiredState{Routes: []Route{
			{ModelID: "off", Enabled: false, CredentialRef: reference},
			{ModelID: "a", Enabled: true, CredentialRef: reference},
			{ModelID: "a-again", Enabled: true, CredentialRef: reference}, // same ref: deduped
			{ModelID: "tokenless", Enabled: true},                         // no ref
		}})
		if err != nil || len(got) != 1 || got["provider-secrets/model-a"] != "value-model-a" {
			t.Fatalf("fetchCredentials() = %#v, %v", got, err)
		}
		if calls != 1 {
			t.Fatalf("fetcher called %d times, want 1 (dedup)", calls)
		}
	})

	t.Run("a failing ref aborts resolution and names the ref", func(t *testing.T) {
		reconciler := &Reconciler{config: Config{CredentialFetcher: func(_ context.Context, ref SecretReference) (string, error) {
			if ref.Key == "broken" {
				return "", errors.New("secret unavailable")
			}
			return "value-" + ref.Key, nil
		}}}
		got, err := reconciler.fetchCredentials(context.Background(), DesiredState{Routes: []Route{
			{ModelID: "a", Enabled: true, CredentialRef: reference},
			{ModelID: "broken", Enabled: true, CredentialRef: &SecretReference{Name: "provider-secrets", Key: "broken"}},
		}})
		if err == nil || !strings.Contains(err.Error(), "provider-secrets/broken") || !strings.Contains(err.Error(), "secret unavailable") {
			t.Fatalf("fetchCredentials() error = %v, want credentialRef name/key and cause", err)
		}
		if got != nil {
			t.Fatalf("fetchCredentials() = %#v, want nil on failure (no partial values)", got)
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
