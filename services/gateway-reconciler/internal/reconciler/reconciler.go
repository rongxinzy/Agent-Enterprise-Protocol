package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// aiProxyPluginVersion pins the Higress built-in ai-proxy wasm build the
// rendered WasmPlugin references (oci:// tag).
const aiProxyPluginVersion = "2.0.1"

// Pin the official ai-statistics 2.0.1 OCI artifact, independently of the
// gateway image and mutable plugin tags.
const aiStatisticsPluginURL = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-statistics@sha256:9bebfc803f6ea92c0805670bd9a6e8a5bb727f2e1a86b133f20bb7002e71511e"

type Config struct {
	NativeGateway NativeGatewayConfig
	ControlURL    string
	Token         string
	OutputDir     string
	Tenants       []string
	HTTPClient    *http.Client
	Applier       Applier
	// CredentialFetcher resolves credentialRef values for the rendered
	// WasmPlugin (ai-proxy requires inline apiTokens). When nil, routes with
	// credentialRefs render without tokens — ai-proxy then rejects requests
	// to those providers.
	CredentialFetcher func(ctx context.Context, ref SecretReference) (string, error)
}

type SecretReference struct {
	Name      string  `json:"name"`
	Key       string  `json:"key"`
	Namespace *string `json:"namespace"`
}

type Route struct {
	ModelID       string           `json:"modelId"`
	Enabled       bool             `json:"enabled"`
	Endpoint      string           `json:"endpoint"`
	UpstreamModel string           `json:"upstreamModel"`
	Protocol      string           `json:"protocol"`
	ProviderType  string           `json:"providerType,omitempty"`
	CredentialRef *SecretReference `json:"credentialRef,omitempty"`
	// endpoint is the parsed absolute upstream URL; Render
	// populates it during validation and it never serializes.
	endpoint *url.URL `json:"-"`
}

type DesiredState struct {
	DeploymentID string  `json:"deploymentId"`
	Revision     string  `json:"revision"`
	PublishedAt  string  `json:"publishedAt"`
	ContentHash  string  `json:"contentHash"`
	Routes       []Route `json:"routes"`
}

func (d DesiredState) TenantID() string {
	return d.DeploymentID
}

type Status struct {
	State            string  `json:"state"`
	ObservedRevision *string `json:"observedRevision"`
	ContentHash      *string `json:"contentHash"`
	LastAppliedAt    *string `json:"lastAppliedAt"`
	ErrorCode        *string `json:"errorCode"`
	Message          *string `json:"message"`
	ResourceCount    int     `json:"resourceCount"`
}

type Reconciler struct {
	config Config
}

func New(config Config) (*Reconciler, error) {
	if native := config.NativeGateway; native.Enabled && (native.RedisService == "" || native.RedisPort < 1 || native.RedisPort > 65535 || native.RedisDatabase < 0) {
		return nil, errors.New("native gateway requires valid Redis configuration")
	}
	if strings.TrimRight(config.ControlURL, "/") == "" || config.Token == "" || config.OutputDir == "" || len(config.Tenants) == 0 {
		return nil, errors.New("control URL, token, output directory, and at least one tenant are required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	config.ControlURL = strings.TrimRight(config.ControlURL, "/")
	sort.Strings(config.Tenants)
	return &Reconciler{config: config}, nil
}

func (r *Reconciler) Sync(ctx context.Context, tenant string) error {
	desired, err := r.fetchDesired(ctx, tenant)
	if err != nil {
		return r.writeFailure(ctx, tenant, "CONTROL_PLANE_UNAVAILABLE", err)
	}
	if desired.Revision == "" {
		return r.writeStatus(ctx, tenant, Status{State: "pending", ResourceCount: 0})
	}
	if err := r.writeStatus(ctx, tenant, Status{State: "applying", ObservedRevision: &desired.Revision, ContentHash: &desired.ContentHash, ResourceCount: len(desired.Routes)}); err != nil {
		return err
	}
	credentials := r.fetchCredentials(ctx, desired)
	document, resources, _, err := Render(desired, credentials)
	if err != nil {
		return r.writeFailure(ctx, tenant, "RENDER_FAILED", err)
	}
	if canonicalHash(desired) != desired.ContentHash {
		return r.writeFailure(ctx, tenant, "DESIRED_HASH_MISMATCH", errors.New("desired state content hash does not match canonical routes"))
	}
	if err := writeAtomic(filepath.Join(r.config.OutputDir, tenant+".yaml"), []byte(document)); err != nil {
		return r.writeFailure(ctx, tenant, "OUTPUT_WRITE_FAILED", err)
	}
	if r.config.Applier != nil {
		if err := r.config.Applier.Apply(ctx, desired, resources); err != nil {
			return r.writeFailure(ctx, tenant, "KUBERNETES_APPLY_FAILED", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if r.config.NativeGateway.Enabled {
		if err := r.syncGatewayLimits(ctx, desired); err != nil {
			return r.writeFailure(ctx, tenant, "GATEWAY_LIMITS_FAILED", errors.New("native gateway limits could not be applied"))
		}
	}
	return r.writeStatus(ctx, tenant, Status{State: "ready", ObservedRevision: &desired.Revision, ContentHash: &desired.ContentHash, LastAppliedAt: &now, ResourceCount: len(desired.Routes)})
}

func (r *Reconciler) fetchDesired(ctx context.Context, tenant string) (DesiredState, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.config.ControlURL+"/internal/data-plane/desired-state", nil)
	if err != nil {
		return DesiredState{}, err
	}
	r.addHeaders(request, tenant)
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return DesiredState{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return DesiredState{}, fmt.Errorf("desired state request returned %d", response.StatusCode)
	}
	var desired DesiredState
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&desired); err != nil {
		return DesiredState{}, err
	}
	return desired, nil
}

func (r *Reconciler) writeStatus(ctx context.Context, tenant string, status Status) error {
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, r.config.ControlURL+"/internal/data-plane/status", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	r.addHeaders(request, tenant)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status update returned %d", response.StatusCode)
	}
	return nil
}

func (r *Reconciler) writeFailure(ctx context.Context, tenant, code string, cause error) error {
	message := cause.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	status := Status{State: "error", ErrorCode: &code, Message: &message}
	if err := r.writeStatus(ctx, tenant, status); err != nil {
		return fmt.Errorf("%s: %w; status update: %v", code, cause, err)
	}
	return fmt.Errorf("%s: %w", code, cause)
}

// fetchCredentials resolves every route's credentialRef to its value via the
// configured CredentialFetcher. Unresolvable refs are logged and skipped —
// the route renders without an apiToken and ai-proxy rejects its requests.
func (r *Reconciler) fetchCredentials(ctx context.Context, desired DesiredState) map[string]string {
	if r.config.CredentialFetcher == nil {
		return nil
	}
	out := make(map[string]string)
	for _, route := range desired.Routes {
		if route.CredentialRef == nil || !route.Enabled {
			continue
		}
		key := route.CredentialRef.Name + "/" + route.CredentialRef.Key
		if _, done := out[key]; done {
			continue
		}
		value, err := r.config.CredentialFetcher(ctx, *route.CredentialRef)
		if err != nil {
			continue
		}
		out[key] = value
	}
	return out
}

func (r *Reconciler) addHeaders(request *http.Request, tenant string) {
	request.Header.Set("X-AEP-Data-Plane-Token", r.config.Token)
	request.Header.Set("X-AEP-Deployment-ID", tenant)
	request.Header.Set("X-AEP-Protocol-Version", "1.0")
}

// ResourceKind tags each rendered Kubernetes resource so the applier can
// apply or delete them by name without sniffing API paths.
type ResourceKind string

const (
	ResourceOpenAIIngress    ResourceKind = "openaiIngress"
	ResourceAnthropicIngress ResourceKind = "anthropicIngress"
	ResourceEnvoyFilter      ResourceKind = "envoyFilter"
	ResourceWasmPlugin       ResourceKind = "wasmPlugin"
	ResourceAIStatistics     ResourceKind = "aiStatistics"
	ResourceGatewayNative    ResourceKind = "gatewayNative"
)

// RenderedResource is one YAML document plus the Kubernetes API path it
// server-side-applies to.
type RenderedResource struct {
	Kind    ResourceKind
	APIPath string
	Body    string
}

// Render projects the desired state into Kubernetes/Higress resources in a
// pinned order: per-model OpenAI ingress/upstream pairs, then per anthropic route (sorted
// by model ID) its Ingress + EnvoyFilter pair, then the ai-proxy WasmPlugin
// (always present so disabling every openai route only empties its
// matchRules), then the tenant-scoped ai-statistics WasmPlugin. Both plugins
// remain present with empty matchRules when idle. Anthropic routes never touch
// shared resources: the EnvoyFilter
// declares its own upstream cluster and redirects the route to it, so nothing
// outside the tenant's own names is written.
func Render(desired DesiredState, credentialValues map[string]string) (string, []RenderedResource, string, error) {
	routes := append([]Route(nil), desired.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].ModelID < routes[j].ModelID })
	if desired.Revision == "" {
		return "", nil, "", errors.New("desired revision is required")
	}
	if strings.TrimSpace(desired.TenantID()) == "" {
		return "", nil, "", errors.New("deployment ID is required")
	}
	suffix := resourceSuffix(desired.TenantID())
	enabledOpenAI := make([]Route, 0, len(routes))
	anthropic := make([]Route, 0)
	slugOwners := make(map[string]string)
	for index := range routes {
		route := routes[index]
		if route.Protocol == "anthropic" {
			if route.ProviderType != "" {
				return "", nil, "", fmt.Errorf("provider type %q is not supported for anthropic model %q", route.ProviderType, route.ModelID)
			}
			endpoint, err := parseAbsoluteEndpoint(route.Endpoint)
			if err != nil {
				return "", nil, "", fmt.Errorf("anthropic model %q: %w", route.ModelID, err)
			}
			slug := anthropicSlug(route.ModelID)
			if owner, taken := slugOwners[slug]; taken {
				return "", nil, "", fmt.Errorf("anthropic models %q and %q share path prefix /%s", owner, route.ModelID, slug)
			}
			slugOwners[slug] = route.ModelID
			route.endpoint = endpoint
			if route.Enabled {
				anthropic = append(anthropic, route)
			}
			continue
		}
		if route.ProviderType == "" {
			route.ProviderType = "openai"
		}
		if route.ProviderType != "openai" && route.ProviderType != "deepseek" {
			return "", nil, "", fmt.Errorf("unsupported provider type %q for model %q", route.ProviderType, route.ModelID)
		}
		// Relative endpoints retain the deployment's preconfigured backend.
		// An absolute URL must also select an actual upstream: ai-proxy only
		// rewrites HTTP headers/body, it does not create an Envoy cluster.
		if raw := strings.TrimSpace(route.Endpoint); raw != "" && !strings.HasPrefix(raw, "/") {
			endpoint, err := parseAbsoluteEndpoint(raw)
			if err != nil {
				return "", nil, "", fmt.Errorf("openai model %q: %w", route.ModelID, err)
			}
			route.endpoint = endpoint
		}
		if strings.HasPrefix(strings.TrimSpace(route.Endpoint), "//") {
			return "", nil, "", errors.New("endpoint must not be a scheme-relative URL")
		}
		if route.Enabled {
			enabledOpenAI = append(enabledOpenAI, route)
		}
	}
	resources := make([]RenderedResource, 0, 4)
	for _, route := range enabledOpenAI {
		name := openAIResourceName(desired.TenantID(), route.ModelID)
		resources = append(resources, RenderedResource{Kind: ResourceOpenAIIngress, APIPath: ingressAPIPath(name), Body: renderOpenAIIngress(name, route)})
		if route.endpoint != nil {
			resources = append(resources, RenderedResource{Kind: ResourceEnvoyFilter, APIPath: envoyFilterAPIPath(name), Body: renderEnvoyFilter(name, route, nil)})
		}
	}
	for _, route := range anthropic {
		name := anthropicResourceName(route.ModelID)
		resources = append(resources,
			RenderedResource{Kind: ResourceAnthropicIngress, APIPath: ingressAPIPath(name), Body: renderAnthropicIngress(name, route)},
			RenderedResource{Kind: ResourceEnvoyFilter, APIPath: envoyFilterAPIPath(name), Body: renderEnvoyFilter(name, route, credentialValues)},
		)
	}
	resources = append(resources, RenderedResource{Kind: ResourceWasmPlugin, APIPath: wasmPluginAPIPath(suffix), Body: renderWasmPlugin(desired.TenantID(), enabledOpenAI, credentialValues)})
	resources = append(resources, RenderedResource{Kind: ResourceAIStatistics, APIPath: aiStatisticsAPIPath(suffix), Body: renderAIStatistics(desired.TenantID(), enabledOpenAI, anthropic)})
	var document strings.Builder
	for index, resource := range resources {
		if index > 0 {
			document.WriteString("---\n")
		}
		document.WriteString(resource.Body)
	}
	canonical := strings.TrimSpace(document.String()) + "\n"
	digest := sha256.Sum256([]byte(canonical))
	return canonical, resources, hex.EncodeToString(digest[:]), nil
}

func renderOpenAIIngress(name string, route Route) string {
	var document strings.Builder
	// The authorizer strips caller-supplied identity headers and stamps the
	// validated catalog model. Native Higress header matching isolates each
	// ai-proxy provider configuration even when models share a request path.
	document.WriteString("apiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n  name: " + yamlScalar(name) + "\n  namespace: higress-system\n  annotations:\n    higress.io/exact-match-header-x-aep-model-id: " + yamlScalar(route.ModelID) + "\nspec:\n  ingressClassName: higress\n")
	document.WriteString("  rules:\n    - http:\n        paths:\n")
	path := ingressPath(route.Endpoint)
	if route.endpoint != nil {
		// Client URLs stay OpenAI-compatible regardless of provider base path;
		// ai-proxy rehomes /v1/... under openaiCustomUrl.
		path = "/v1"
	}
	document.WriteString("          - path: " + yamlScalar(path) + "\n            pathType: Prefix\n            backend:\n              service:\n                name: aep-model-gateway\n                port:\n                  number: 80\n")
	return document.String()
}

func renderWasmPlugin(deployment string, enabled []Route, credentialValues map[string]string) string {
	suffix := resourceSuffix(deployment)
	var document strings.Builder
	document.WriteString("apiVersion: extensions.higress.io/v1alpha1\nkind: WasmPlugin\nmetadata:\n  name: " + yamlScalar("aep-ai-proxy-"+suffix) + "\n  namespace: higress-system\nspec:\n  url: " + yamlScalar("oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-proxy:"+aiProxyPluginVersion) + "\n  failStrategy: FAIL_CLOSE\n  defaultConfigDisable: true\n")
	if len(enabled) == 0 {
		document.WriteString("  matchRules: []\n")
		return document.String()
	}
	document.WriteString("  matchRules:\n")
	for _, route := range enabled {
		document.WriteString("    - config:\n        provider:\n          type: " + yamlScalar(route.ProviderType) + "\n")
		if custom, ok := upstreamURL(route.Endpoint); ok {
			document.WriteString("          openaiCustomUrl: " + yamlScalar(custom) + "\n")
		}
		document.WriteString("          modelMapping:\n            " + yamlScalar(route.ModelID) + ": " + yamlScalar(route.UpstreamModel) + "\n")
		if route.CredentialRef != nil {
			if value, ok := credentialValues[route.CredentialRef.Name+"/"+route.CredentialRef.Key]; ok && value != "" {
				document.WriteString("          apiTokens:\n            - " + yamlScalar(value) + "\n")
			}
		}
		document.WriteString("      ingress:\n        - " + yamlScalar(openAIResourceName(deployment, route.ModelID)) + "\n")
	}
	return document.String()
}

func renderAIStatistics(deployment string, openAI, anthropic []Route) string {
	suffix := resourceSuffix(deployment)
	var document strings.Builder
	document.WriteString("apiVersion: extensions.higress.io/v1alpha1\nkind: WasmPlugin\nmetadata:\n  name: " + yamlScalar("aep-ai-statistics-"+suffix) + "\n  namespace: higress-system\nspec:\n  url: " + yamlScalar(aiStatisticsPluginURL) + "\n  failStrategy: FAIL_OPEN\n  phase: UNSPECIFIED_PHASE\n  priority: 200\n  defaultConfigDisable: true\n")
	if len(openAI)+len(anthropic) == 0 {
		document.WriteString("  matchRules: []\n")
		return document.String()
	}
	// Never enable full default attributes: they include prompts, answers,
	// tool arguments and reasoning. Observation must not capture model content.
	document.WriteString("  matchRules:\n    - config:\n        use_default_attributes: false\n        use_default_response_attributes: true\n        enable_path_suffixes:\n          - /chat/completions\n          - /completions\n          - /responses\n          - /messages\n      configDisable: false\n      ingress:\n")
	for _, route := range openAI {
		document.WriteString("        - " + yamlScalar(openAIResourceName(deployment, route.ModelID)) + "\n")
	}
	for _, route := range anthropic {
		document.WriteString("        - " + yamlScalar(anthropicResourceName(route.ModelID)) + "\n")
	}
	return document.String()
}

func renderAnthropicIngress(name string, route Route) string {
	var document strings.Builder
	document.WriteString("apiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n  name: " + yamlScalar(name) + "\n  namespace: higress-system\nspec:\n  ingressClassName: higress\n  rules:\n    - http:\n        paths:\n          - path: " + yamlScalar("/"+anthropicSlug(route.ModelID)) + "\n            pathType: Prefix\n            backend:\n              service:\n                name: aep-model-gateway\n                port:\n                  number: 80\n")
	return document.String()
}

// renderEnvoyFilter emits a per-model upstream: one STRICT_DNS
// cluster for the upstream endpoint (with TLS + SNI when the endpoint is
// https) plus a route merge that redirects the ingress route to that cluster,
// rewrites the host. OpenAI retains ai-proxy path and credential handling;
// Anthropic strips its model prefix and injects credentials. request_headers_to_add must
// stay at the Route level — nested under `route` istiod drops it silently.
// Without a resolved credential the headers are omitted and the upstream
// answers 401 itself.
func renderEnvoyFilter(name string, route Route, credentialValues map[string]string) string {
	endpoint := route.endpoint
	host := endpoint.Hostname()
	if host == "" {
		host = endpoint.Host
	}
	port := 443
	if endpoint.Scheme == "http" {
		port = 80
	}
	if raw := endpoint.Port(); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}
	clusterName := name
	var document strings.Builder
	document.WriteString("apiVersion: networking.istio.io/v1alpha3\nkind: EnvoyFilter\nmetadata:\n  name: " + yamlScalar(name) + "\n  namespace: higress-system\nspec:\n  configPatches:\n")
	document.WriteString("    - applyTo: CLUSTER\n      patch:\n        operation: ADD\n        value:\n          name: " + yamlScalar(clusterName) + "\n          type: STRICT_DNS\n          connect_timeout: 10s\n          dns_lookup_family: V4_ONLY\n          load_assignment:\n            cluster_name: " + yamlScalar(clusterName) + "\n            endpoints:\n              - lb_endpoints:\n                  - endpoint:\n                      address:\n                        socket_address:\n                          address: " + yamlScalar(host) + "\n                          port_value: " + strconv.Itoa(port) + "\n")
	if endpoint.Scheme == "https" {
		document.WriteString("          transport_socket:\n            name: envoy.transport_sockets.tls\n            typed_config:\n              '@type': type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext\n              sni: " + yamlScalar(host) + "\n")
		if route.Protocol != "anthropic" {
			sanType := "DNS"
			if net.ParseIP(host) != nil {
				sanType = "IP_ADDRESS"
			}
			document.WriteString("              common_tls_context:\n                validation_context:\n                  trusted_ca:\n                    filename: /etc/ssl/certs/ca-certificates.crt\n                  match_typed_subject_alt_names:\n                    - san_type: " + sanType + "\n                      matcher:\n                        exact: " + yamlScalar(host) + "\n")
		}
	}
	document.WriteString("    - applyTo: HTTP_ROUTE\n      match:\n        context: GATEWAY\n        routeConfiguration:\n          vhost:\n            route:\n              name: " + yamlScalar(name) + "\n      patch:\n        operation: MERGE\n        value:\n")
	if route.CredentialRef != nil {
		if value, ok := credentialValues[route.CredentialRef.Name+"/"+route.CredentialRef.Key]; ok && value != "" {
			document.WriteString("          request_headers_to_add:\n            - header:\n                key: x-api-key\n                value: " + yamlScalar(value) + "\n              append: false\n            - header:\n                key: authorization\n                value: " + yamlScalar("Bearer "+value) + "\n              append: false\n")
		}
	}
	authority := host
	if route.Protocol != "anthropic" {
		authority = endpoint.Host
	}
	document.WriteString("          route:\n            cluster: " + yamlScalar(clusterName) + "\n            host_rewrite_literal: " + yamlScalar(authority) + "\n")
	if route.Protocol != "anthropic" {
		// Keep ai-proxy's protocol-specific path and credential transformation.
		return document.String()
	}
	substitution := anthropicRewrite(endpoint.Path)
	document.WriteString("            regex_rewrite:\n              pattern:\n                google_re2: {}\n                regex: " + yamlScalar("^/"+anthropicSlug(route.ModelID)+"/(.*)$") + "\n              substitution: " + yamlScalar(substitution) + "\n")
	return document.String()
}

func canonicalHash(desired DesiredState) string {
	routes := append([]Route(nil), desired.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].ModelID < routes[j].ModelID })
	encoded, _ := json.Marshal(struct {
		Revision string  `json:"revision"`
		Routes   []Route `json:"routes"`
	}{Revision: desired.Revision, Routes: routes})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func yamlScalar(value string) string {
	value = strings.ReplaceAll(value, "'", "''")
	return "'" + value + "'"
}

// parseAbsoluteEndpoint validates an upstream URL without exposing its
// contents in validation errors (malformed URLs may contain credentials).
func parseAbsoluteEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("endpoint must be an absolute http(s) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Hostname() == "" {
		return nil, errors.New("endpoint must not contain credentials, query, or fragment")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("endpoint port must be between 1 and 65535")
		}
	}
	return parsed, nil
}

// anthropicSlug sanitizes a model ID into the client-facing path prefix
// (baseURL = <gateway>/<slug>; anthropic SDKs append /v1/... to it).
func anthropicSlug(modelID string) string {
	var result strings.Builder
	for _, character := range strings.ToLower(modelID) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			result.WriteRune(character)
		} else {
			result.WriteByte('-')
		}
	}
	clean := strings.Trim(result.String(), "-")
	// The ingress path must equal the wire model id verbatim — runtimes
	// build their gateway base URL as <gateway>/<model-id> — so the slug
	// cap only guards against runaway names. AEP caps model ids at 40
	// characters (identifier_slug.go); capping at 30 truncated longer ids
	// into paths nothing could ever route to (silent 404).
	if len(clean) > 40 {
		clean = strings.Trim(clean[:40], "-")
	}
	if clean == "" {
		clean = "model"
	}
	return clean
}

// anthropicRewrite strips the model path prefix and rehomes the remainder
// under the endpoint's own path ("" keeps the client path minus the slug,
// e.g. /api/anthropic rewrites /<slug>/v1/x to /api/anthropic/v1/x).
func anthropicRewrite(endpointPath string) string {
	path := strings.Trim(endpointPath, "/")
	if path == "" {
		return "/\\1"
	}
	return "/" + path + "/\\1"
}

// anthropicResourceName derives the Ingress/EnvoyFilter/cluster name for an
// anthropic route. resourceSuffix is capped at 49 chars, so the full name
// stays within the 63-character object name limit.
func anthropicResourceName(modelID string) string {
	return "aep-anthropic-" + resourceSuffix(modelID)
}

func openAIResourceName(deployment, modelID string) string {
	return "aep-openai-" + resourceSuffix(deployment+"/"+modelID)
}

func openAIIngressAPIPath(suffix string) string {
	return "/apis/networking.k8s.io/v1/namespaces/higress-system/ingresses/aep-model-gateway-" + suffix
}

func ingressAPIPath(name string) string {
	return "/apis/networking.k8s.io/v1/namespaces/higress-system/ingresses/" + name
}

func envoyFilterAPIPath(name string) string {
	return "/apis/networking.istio.io/v1alpha3/namespaces/higress-system/envoyfilters/" + name
}

func wasmPluginAPIPath(suffix string) string {
	return "/apis/extensions.higress.io/v1alpha1/namespaces/higress-system/wasmplugins/aep-ai-proxy-" + suffix
}

func aiStatisticsAPIPath(suffix string) string {
	return "/apis/extensions.higress.io/v1alpha1/namespaces/higress-system/wasmplugins/aep-ai-statistics-" + suffix
}

// ingressPath maps a route endpoint to the Ingress path prefix. Endpoints
// carry either an absolute URL (http://host/v1) or a bare path (/v1): the
// URL form contributes its path (defaulting to /v1), the bare form is used
// verbatim.
func ingressPath(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return endpoint
	}
	if parsed.Path != "" {
		return parsed.Path
	}
	return "/v1"
}

// upstreamURL returns the endpoint's absolute base URL (scheme://host plus
// path — ai-proxy's openaiCustomUrl appends the API route to it) when the
// endpoint carries one.
func upstreamURL(endpoint string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}
	base := parsed.Scheme + "://" + parsed.Host + parsed.Path
	return strings.TrimSuffix(base, "/"), true
}

func resourceSuffix(value string) string {
	var result strings.Builder
	for _, character := range strings.ToLower(value) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			result.WriteRune(character)
		} else {
			result.WriteByte('-')
		}
	}
	clean := strings.Trim(result.String(), "-")
	if clean == "" {
		clean = "tenant"
	}
	digest := sha256.Sum256([]byte(value))
	if len(clean) > 40 {
		clean = strings.Trim(clean[:40], "-")
	}
	return clean + "-" + hex.EncodeToString(digest[:4])
}

func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".reconcile-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
