package gatewayrender

import (
	"strings"
	"testing"
)

func TestRenderIsDeterministicAndDoesNotIncludeSecretValues(t *testing.T) {
	desired := Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{{ModelID: "model-b", Enabled: true, Endpoint: "/b", UpstreamModel: "up-b", Protocol: "openai-compatible", CredentialRef: &SecretReference{Name: "provider-secrets", Key: "model-b"}}, {ModelID: "model-a", Enabled: true, Endpoint: "/a", UpstreamModel: "up-a", Protocol: "openai-compatible"}}}
	first, _, firstHash, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _, secondHash, err := Render(Input{Deployment: "demo", Revision: "rev-1", Routes: []Route{desired.Routes[1], desired.Routes[0]}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstHash != secondHash {
		t.Fatal("render output is not deterministic")
	}
	// With nil credential values the render omits apiTokens entirely; the
	// credentialRef name stays a lookup key and never reaches the document.
	if strings.Contains(first, "provider-secret-value") {
		t.Fatal("secret value leaked into render without credential fetcher")
	}
	// With the value resolved, it inlines as an apiToken for ai-proxy.
	withCredentials, _, _, err := Render(desired, map[string]string{"provider-secrets/model-b": "provider-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withCredentials, "provider-secret-value") {
		t.Fatal("resolved credential was not inlined as an apiToken")
	}
}

func TestRenderRejectsMissingRevision(t *testing.T) {
	if _, _, _, err := Render(Input{Deployment: "demo"}, nil); err == nil {
		t.Fatal("missing revision was accepted")
	}
}

func TestRenderSelectsDeepSeekProviderAndRejectsUnknownProviders(t *testing.T) {
	desired := Input{Deployment: "demo", Revision: "rev-deepseek", Routes: []Route{{
		ModelID: "reasoner", Enabled: true, Endpoint: "/v1/chat", UpstreamModel: "deepseek-reasoner", Protocol: "openai-compatible", ProviderType: "deepseek",
	}}}
	document, _, _, err := Render(desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document, "type: 'deepseek'") {
		t.Fatalf("DeepSeek provider was not rendered: %s", document)
	}
	desired.Routes[0].ProviderType = "unknown"
	if _, _, _, err := Render(desired, nil); err == nil {
		t.Fatal("unsupported provider type was accepted")
	}
}
