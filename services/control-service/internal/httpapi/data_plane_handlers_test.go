package httpapi

import (
	"strings"
	"testing"
)

func TestNormalizeDataPlaneStateDefaultsAndValidatesProviderType(t *testing.T) {
	legacy := dataPlaneDesiredStateWrite{Revision: "rev-1", Routes: []dataPlaneRoute{{
		ModelID: "chat", Enabled: true, Endpoint: "/v1/chat", UpstreamModel: "upstream", Protocol: "openai-compatible",
	}}}
	normalized, ok := normalizeDataPlaneState(legacy)
	if !ok || normalized.Routes[0].ProviderType != "openai" {
		t.Fatalf("legacy route normalization = %#v, %v", normalized, ok)
	}

	deepseek := legacy
	deepseek.Routes = append([]dataPlaneRoute(nil), legacy.Routes...)
	deepseek.Routes[0].ProviderType = "deepseek"
	if normalized, ok := normalizeDataPlaneState(deepseek); !ok || normalized.Routes[0].ProviderType != "deepseek" {
		t.Fatalf("DeepSeek route normalization = %#v, %v", normalized, ok)
	}

	unsupported := deepseek
	unsupported.Routes = append([]dataPlaneRoute(nil), deepseek.Routes...)
	unsupported.Routes[0].ProviderType = "provider-by-model-name"
	if _, ok := normalizeDataPlaneState(unsupported); ok {
		t.Fatal("unsupported provider type was accepted")
	}
}

func TestNormalizeDataPlaneStateKeepsEmptyRoutesAsJsonArray(t *testing.T) {
	normalized, ok := normalizeDataPlaneState(dataPlaneDesiredStateWrite{Revision: "empty", Routes: []dataPlaneRoute{}})
	if !ok {
		t.Fatal("empty route state was rejected")
	}
	if normalized.Routes == nil {
		t.Fatal("empty routes must remain a non-nil slice so JSON encodes []")
	}
}

func TestDataPlaneHashIsStableAfterRouteNormalization(t *testing.T) {
	unsorted := dataPlaneDesiredStateWrite{Revision: "rev-1", Routes: []dataPlaneRoute{
		{ModelID: "z-model", Enabled: true, Endpoint: "/z", UpstreamModel: "z", Protocol: "openai-compatible"},
		{ModelID: "a-model", Enabled: false, Endpoint: "/a", UpstreamModel: "a", Protocol: "openai-compatible"},
	}}
	normalized, ok := normalizeDataPlaneState(unsorted)
	if !ok {
		t.Fatal("valid route state was rejected")
	}
	canonical := dataPlaneDesiredStateWrite{Revision: "rev-1", Routes: []dataPlaneRoute{
		{ModelID: "a-model", Enabled: false, Endpoint: "/a", UpstreamModel: "a", Protocol: "openai-compatible", ProviderType: "openai"},
		{ModelID: "z-model", Enabled: true, Endpoint: "/z", UpstreamModel: "z", Protocol: "openai-compatible", ProviderType: "openai"},
	}}
	if dataPlaneHash(normalized) != dataPlaneHash(canonical) {
		t.Fatalf("normalized state hash is not canonical: %s != %s", dataPlaneHash(normalized), dataPlaneHash(canonical))
	}
	if normalized.Routes[0].ModelID != "a-model" || normalized.Routes[1].ModelID != "z-model" {
		t.Fatalf("routes were not sorted: %#v", normalized.Routes)
	}
}

func TestNormalizeDataPlaneStateRejectsDuplicateAndMalformedRoutes(t *testing.T) {
	base := dataPlaneRoute{ModelID: "chat", Enabled: true, Endpoint: "/v1/chat", UpstreamModel: "chat", Protocol: "openai-compatible"}
	tests := []struct {
		name  string
		state dataPlaneDesiredStateWrite
	}{
		{name: "duplicate model", state: dataPlaneDesiredStateWrite{Revision: "rev", Routes: []dataPlaneRoute{base, base}}},
		{name: "missing credential name", state: dataPlaneDesiredStateWrite{Revision: "rev", Routes: []dataPlaneRoute{{ModelID: "chat", Endpoint: "/v1/chat", UpstreamModel: "chat", Protocol: "openai-compatible", CredentialRef: &dataPlaneSecretReference{Key: "key"}}}}},
		{name: "missing credential key", state: dataPlaneDesiredStateWrite{Revision: "rev", Routes: []dataPlaneRoute{{ModelID: "chat", Endpoint: "/v1/chat", UpstreamModel: "chat", Protocol: "openai-compatible", CredentialRef: &dataPlaneSecretReference{Name: "secret"}}}}},
		{name: "empty revision", state: dataPlaneDesiredStateWrite{Revision: " "}},
		{name: "oversized revision", state: dataPlaneDesiredStateWrite{Revision: strings.Repeat("r", 201)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := normalizeDataPlaneState(test.state); ok {
				t.Fatalf("invalid state was accepted: %#v", test.state)
			}
		})
	}
}

func TestNormalizeDataPlaneStateRejectsTooManyRoutes(t *testing.T) {
	routes := make([]dataPlaneRoute, 501)
	for index := range routes {
		routes[index] = dataPlaneRoute{ModelID: "model-" + strings.Repeat("x", index+1), Endpoint: "/v1/chat", UpstreamModel: "chat", Protocol: "openai-compatible"}
	}
	if _, ok := normalizeDataPlaneState(dataPlaneDesiredStateWrite{Revision: "rev", Routes: routes}); ok {
		t.Fatal("state with more than 500 routes was accepted")
	}
}
