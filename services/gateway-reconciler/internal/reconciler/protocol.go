package reconciler

import (
	"fmt"
	"net/url"
	"strings"
)

// protocolStrategy encapsulates the protocol-specific knowledge about a
// route: which data-plane bucket it renders into, how its endpoints
// validate, and how its per-model resources are named. Render's
// classification loop, the applier's deletion pass, and the quota/native
// ingress collectors all consume this one abstraction, so protocol knowledge
// lives in exactly one place per protocol instead of being re-derived at
// each call site.
//
// One wire format, one strategy. OpenAI has shipped two generations of wire
// API (Chat Completions and the newer Responses API): when the platform
// models the newer one, it lands here as its own strategy — a new case in
// strategyFor with its own type — never as format sniffing or a
// compatibility branch inside an existing strategy.
type protocolStrategy interface {
	// bucket reports which render bucket the route occupies.
	bucket() renderBucket
	// validateEndpoint checks the route's endpoint for this protocol. It
	// returns the parsed absolute endpoint when the route should own an
	// EnvoyFilter cluster, or nil when the protocol's shape does not need
	// one (a relative openai endpoint keeps the deployment's preconfigured
	// backend; disabled routes render nothing).
	validateEndpoint(route Route) (*url.URL, error)
}

// renderBucket is the render pass's classification of a route.
type renderBucket int

const (
	// bucketAnthropic: per-model Ingress + EnvoyFilter pair with server-side
	// credential injection (x-api-key + authorization Bearer) and the
	// slug-strip path rewrite.
	bucketAnthropic renderBucket = iota
	// bucketOpenAI: per-model Ingress plus a per-model ai-proxy matchRule
	// bound to that ingress (isolated provider configuration and apiTokens —
	// a shared matchRule list would resolve only the first entry). Routes
	// with an absolute endpoint additionally own an EnvoyFilter routing
	// their ingress to a dedicated upstream cluster.
	bucketOpenAI
)

// anthropicStrategy: the per-model passthrough under the aep-anthropic-
// prefix. Deployed objects keep their identity across upgrades.
type anthropicStrategy struct{}

func (anthropicStrategy) bucket() renderBucket { return bucketAnthropic }

func (anthropicStrategy) validateEndpoint(route Route) (*url.URL, error) {
	endpoint, err := parseAbsoluteEndpoint(route.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("anthropic model %q: %w", route.ModelID, err)
	}
	return endpoint, nil
}

// openaiStrategy: openai-compatible routes (openai and deepseek provider
// types share the shape; the provider type only selects the ai-proxy
// provider block).
type openaiStrategy struct{}

func (openaiStrategy) bucket() renderBucket { return bucketOpenAI }

// validateEndpoint keeps #237's semantics: a relative endpoint retains the
// deployment's preconfigured backend (no EnvoyFilter cluster — ai-proxy only
// rewrites headers/body, it does not create an Envoy cluster); an absolute
// one must select an actual upstream and must not be scheme-relative. HTTPS
// IP-literal endpoints additionally render an IP SAN check in the filter
// itself (see renderEnvoyFilter).
func (openaiStrategy) validateEndpoint(route Route) (*url.URL, error) {
	raw := strings.TrimSpace(route.Endpoint)
	if strings.HasPrefix(raw, "//") {
		return nil, fmt.Errorf("openai model %q: endpoint must not be a scheme-relative URL", route.ModelID)
	}
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil, nil
	}
	endpoint, err := parseAbsoluteEndpoint(raw)
	if err != nil {
		return nil, fmt.Errorf("openai model %q: %w", route.ModelID, err)
	}
	return endpoint, nil
}

// strategyFor resolves a route's protocol strategy. openai-compatible routes
// default to the openai provider when none is set; an anthropic route with a
// provider type, or an unknown protocol, is rejected.
func strategyFor(route Route) (protocolStrategy, error) {
	switch route.Protocol {
	case "anthropic":
		if route.ProviderType != "" {
			return nil, fmt.Errorf("provider type %q is not supported for anthropic model %q", route.ProviderType, route.ModelID)
		}
		return anthropicStrategy{}, nil
	case "openai-compatible", "":
		// An empty protocol rides the openai-compatible bucket, matching the
		// historical classification loop that treated everything non-anthropic
		// as openai.
		provider := route.ProviderType
		if provider == "" {
			provider = "openai"
		}
		if provider != "openai" && provider != "deepseek" {
			return nil, fmt.Errorf("unsupported provider type %q for model %q", provider, route.ModelID)
		}
		return openaiStrategy{}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q for model %q", route.Protocol, route.ModelID)
	}
}
