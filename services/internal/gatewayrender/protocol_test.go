package gatewayrender

import (
	"strings"
	"testing"
)

func TestStrategyForDispatchesByProtocol(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		route  Route
		bucket Bucket
	}{
		{"anthropic", Route{Protocol: "anthropic", ModelID: "a"}, BucketAnthropic},
		{"openai explicit", Route{Protocol: "openai-compatible", ProviderType: "openai", ModelID: "b"}, BucketOpenAI},
		{"openai default provider", Route{Protocol: "openai-compatible", ModelID: "c"}, BucketOpenAI},
		{"empty protocol keeps historical openai default", Route{ModelID: "d"}, BucketOpenAI},
		{"deepseek shares the openai shape", Route{Protocol: "openai-compatible", ProviderType: "deepseek", ModelID: "e"}, BucketOpenAI},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			strategy, err := StrategyFor(test.route)
			if err != nil {
				t.Fatal(err)
			}
			if strategy.Bucket() != test.bucket {
				t.Fatalf("bucket = %v", strategy.Bucket())
			}
		})
	}
	for name, route := range map[string]Route{
		"anthropic with provider": {Protocol: "anthropic", ProviderType: "openai", ModelID: "m"},
		"unknown protocol":        {Protocol: "grpc", ModelID: "m"},
		"unknown provider":        {Protocol: "openai-compatible", ProviderType: "gemini", ModelID: "m"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := StrategyFor(route); err == nil {
				t.Fatal("invalid combination was accepted")
			}
		})
	}
}

func TestOpenAIEndpointSemantics(t *testing.T) {
	t.Parallel()
	strategy, err := StrategyFor(Route{Protocol: "openai-compatible"})
	if err != nil {
		t.Fatal(err)
	}
	// Relative endpoints keep the preconfigured backend: no EnvoyFilter.
	endpoint, err := strategy.validateEndpoint(Route{ModelID: "m", Endpoint: "/v1"})
	if err != nil || endpoint != nil {
		t.Fatalf("relative endpoint = %v, %v", endpoint, err)
	}
	endpoint, err = strategy.validateEndpoint(Route{ModelID: "m", Endpoint: "  "})
	if err != nil || endpoint != nil {
		t.Fatalf("empty endpoint = %v, %v", endpoint, err)
	}
	// Scheme-relative URLs are rejected outright.
	if _, err := strategy.validateEndpoint(Route{ModelID: "m", Endpoint: "//upstream.example/v1"}); err == nil || !strings.Contains(err.Error(), "scheme-relative") {
		t.Fatalf("scheme-relative error = %v", err)
	}
	// Absolute endpoints parse and name the model in errors.
	endpoint, err = strategy.validateEndpoint(Route{ModelID: "glm", Endpoint: "http://172.18.5.188:3000/v1"})
	if err != nil || endpoint == nil || endpoint.Host != "172.18.5.188:3000" {
		t.Fatalf("absolute endpoint = %v, %v", endpoint, err)
	}
	if _, err := strategy.validateEndpoint(Route{ModelID: "glm", Endpoint: "not a url"}); err == nil || !strings.Contains(err.Error(), `"glm"`) {
		t.Fatalf("invalid endpoint error = %v", err)
	}
}

func TestAnthropicEndpointSemantics(t *testing.T) {
	t.Parallel()
	strategy, err := StrategyFor(Route{Protocol: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strategy.validateEndpoint(Route{ModelID: "m", Endpoint: "/v1"}); err == nil || !strings.Contains(err.Error(), "anthropic model") {
		t.Fatalf("relative anthropic endpoint error = %v", err)
	}
	endpoint, err := strategy.validateEndpoint(Route{ModelID: "m", Endpoint: "https://open.bigmodel.cn/api/anthropic"})
	if err != nil || endpoint == nil || endpoint.Host != "open.bigmodel.cn" {
		t.Fatalf("absolute endpoint = %v, %v", endpoint, err)
	}
}
