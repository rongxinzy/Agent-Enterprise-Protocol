package httpapi

import "testing"

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
