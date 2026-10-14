package gatewayrender

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFetchCredentials(t *testing.T) {
	reference := &SecretReference{Name: "provider-secrets", Key: "model-a"}

	t.Run("nil fetcher yields no values", func(t *testing.T) {
		if got := FetchCredentials(context.Background(), []Route{{Enabled: true, CredentialRef: reference}}, nil); got != nil {
			t.Fatalf("FetchCredentials() = %#v, want nil", got)
		}
	})

	t.Run("disabled routes and duplicates are skipped, failures tolerated", func(t *testing.T) {
		calls := 0
		fetch := func(_ context.Context, ref SecretReference) (string, error) {
			calls++
			if ref.Key == "broken" {
				return "", errors.New("secret unavailable")
			}
			return "value-" + ref.Key, nil
		}
		got := FetchCredentials(context.Background(), []Route{
			{ModelID: "off", Enabled: false, CredentialRef: reference},
			{ModelID: "a", Enabled: true, CredentialRef: reference},
			{ModelID: "a-again", Enabled: true, CredentialRef: reference}, // same ref: deduped
			{ModelID: "broken", Enabled: true, CredentialRef: &SecretReference{Name: "provider-secrets", Key: "broken"}},
			{ModelID: "tokenless", Enabled: true}, // no ref
		}, fetch)
		if len(got) != 1 || got["provider-secrets/model-a"] != "value-model-a" {
			t.Fatalf("FetchCredentials() = %#v", got)
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

func TestResourceSuffixIsStableAndKubernetesSafe(t *testing.T) {
	t.Parallel()
	first := ResourceSuffix("Customer / North-East 01")
	second := ResourceSuffix("Customer / North-East 01")
	if first != second || len(first) > 49 || strings.ContainsAny(first, " /_") {
		t.Fatalf("resource suffix = %q", first)
	}
}
