package gatewayrender

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFetchCredentials(t *testing.T) {
	reference := &SecretReference{Name: "provider-secrets", Key: "model-a"}

	t.Run("nil fetcher yields no values and no error", func(t *testing.T) {
		got, err := FetchCredentials(context.Background(), []Route{{Enabled: true, CredentialRef: reference}}, nil)
		if got != nil || err != nil {
			t.Fatalf("FetchCredentials() = %#v, %v; want nil, nil", got, err)
		}
	})

	t.Run("disabled routes, duplicates, and tokenless routes are skipped", func(t *testing.T) {
		calls := 0
		fetch := func(_ context.Context, ref SecretReference) (string, error) {
			calls++
			return "value-" + ref.Key, nil
		}
		got, err := FetchCredentials(context.Background(), []Route{
			{ModelID: "off", Enabled: false, CredentialRef: reference},
			{ModelID: "a", Enabled: true, CredentialRef: reference},
			{ModelID: "a-again", Enabled: true, CredentialRef: reference}, // same ref: deduped
			{ModelID: "tokenless", Enabled: true},                         // no ref
		}, fetch)
		if err != nil || len(got) != 1 || got["provider-secrets/model-a"] != "value-model-a" {
			t.Fatalf("FetchCredentials() = %#v, %v", got, err)
		}
		if calls != 1 {
			t.Fatalf("fetcher called %d times, want 1 (dedup)", calls)
		}
	})

	t.Run("a failing ref aborts resolution and names the ref", func(t *testing.T) {
		fetch := func(_ context.Context, ref SecretReference) (string, error) {
			if ref.Key == "broken" {
				return "", errors.New("secret unavailable")
			}
			return "value-" + ref.Key, nil
		}
		got, err := FetchCredentials(context.Background(), []Route{
			{ModelID: "a", Enabled: true, CredentialRef: reference},
			{ModelID: "broken", Enabled: true, CredentialRef: &SecretReference{Name: "provider-secrets", Key: "broken"}},
		}, fetch)
		if err == nil || !strings.Contains(err.Error(), "provider-secrets/broken") || !strings.Contains(err.Error(), "secret unavailable") {
			t.Fatalf("FetchCredentials() error = %v, want credentialRef name/key and cause", err)
		}
		if got != nil {
			t.Fatalf("FetchCredentials() = %#v, want nil on failure (no partial values)", got)
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

func TestResourceSuffixIsStableAndKubernetesSafe(t *testing.T) {
	t.Parallel()
	first := ResourceSuffix("Customer / North-East 01")
	second := ResourceSuffix("Customer / North-East 01")
	if first != second || len(first) > 49 || strings.ContainsAny(first, " /_") {
		t.Fatalf("resource suffix = %q", first)
	}
}
