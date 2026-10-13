package reconciler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const fieldManager = "aep-gateway-reconciler"

type Applier interface {
	Apply(context.Context, DesiredState, []RenderedResource) error
}

type KubernetesConfig struct {
	URL   string
	Token string
	// TokenFile is re-read on every request when set: projected
	// service-account tokens expire hourly and are rotated in place, so a
	// token captured once at startup silently 401s after its first TTL.
	TokenFile  string
	CAFile     string
	HTTPClient *http.Client
	// StateDir remembers resources this applier kept, so a later desired
	// state can delete routes that disappeared instead of only routes it
	// still lists as disabled. Empty keeps that memory in process only.
	StateDir string
}

type KubernetesApplier struct {
	baseURL   string
	token     string
	tokenFile string
	stateDir  string
	client    *http.Client

	mu      sync.Mutex
	applied map[string][]string
}

// bearerToken returns the current credential, re-reading the token file when
// one is configured (projected tokens rotate in place).
func (a *KubernetesApplier) bearerToken() string {
	if a.tokenFile == "" {
		return a.token
	}
	if data, err := os.ReadFile(a.tokenFile); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	return a.token
}

func NewKubernetesApplier(config KubernetesConfig) (*KubernetesApplier, error) {
	baseURL := strings.TrimRight(config.URL, "/")
	if baseURL == "" || (config.Token == "" && config.TokenFile == "") {
		return nil, errors.New("kubernetes URL and service-account token are required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("kubernetes URL must be an absolute HTTP(S) URL")
	}
	client := config.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if config.CAFile != "" {
			certificate, err := os.ReadFile(config.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read Kubernetes CA: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(certificate) {
				return nil, errors.New("kubernetes CA file contains no certificates")
			}
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		}
		client = &http.Client{Timeout: 10 * time.Second, Transport: transport}
	}
	return &KubernetesApplier{baseURL: baseURL, token: config.Token, tokenFile: config.TokenFile, stateDir: config.StateDir, client: client, applied: map[string][]string{}}, nil
}

// Apply server-side-applies every rendered resource and first deletes what
// the desired state no longer owns: disabled per-model OpenAI ingress/upstream
// pairs, obsolete OpenAI absolute upstreams, and the per-route anthropic
// Ingress+EnvoyFilter pair for every disabled anthropic route. Both WasmPlugins
// are always present in the render (with empty matchRules when idle) and are
// therefore always applied, never deleted.
func (a *KubernetesApplier) Apply(ctx context.Context, desired DesiredState, resources []RenderedResource) error {
	if !hasResourceKind(resources, ResourceWasmPlugin) {
		return errors.New("rendered data plane must include the ai-proxy WasmPlugin")
	}
	for _, resource := range resources {
		if strings.TrimSpace(resource.APIPath) == "" || strings.TrimSpace(resource.Body) == "" {
			return fmt.Errorf("rendered %s resource has an empty API path or body", resource.Kind)
		}
	}
	kept := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		kept[resource.APIPath] = struct{}{}
	}
	deletions := make([]string, 0, 4)
	for _, route := range desired.Routes {
		if route.Protocol == "anthropic" {
			if !route.Enabled {
				name := anthropicResourceName(route.ModelID)
				deletions = append(deletions, ingressAPIPath(name), envoyFilterAPIPath(name))
			}
			continue
		}
		name := openAIResourceName(desired.TenantID(), route.ModelID)
		if !route.Enabled {
			deletions = append(deletions, ingressAPIPath(name))
		}
		// Also remove the old absolute upstream when an enabled model switches
		// back to a relative/preconfigured endpoint.
		if !route.Enabled || !hasResourcePath(resources, envoyFilterAPIPath(name)) {
			deletions = append(deletions, envoyFilterAPIPath(name))
		}
	}
	for _, path := range deletions {
		delete(kept, path)
	}
	sort.Strings(deletions)
	for _, path := range deletions {
		if err := a.delete(ctx, path); err != nil {
			return err
		}
	}
	if err := a.ApplyGatewayNative(ctx, resources); err != nil {
		return err
	}
	// Remove the legacy shared ingress only after replacement routes/plugins
	// have been applied. It otherwise remains an unscoped fallback route.
	if err := a.delete(ctx, openAIIngressAPIPath(resourceSuffix(desired.TenantID()))); err != nil {
		return err
	}
	// Routes that left the desired state are not in the deletion list above.
	// A manual route replaced by a catalog republish would otherwise stay on
	// the gateway after the control plane reports the old revision ready.
	return a.pruneAbsent(ctx, desired.TenantID(), kept)
}

func (a *KubernetesApplier) pruneAbsent(ctx context.Context, tenant string, kept map[string]struct{}) error {
	for _, path := range a.loadApplied(tenant) {
		if _, ok := kept[path]; ok {
			continue
		}
		if err := a.delete(ctx, path); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(kept))
	for path := range kept {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return a.storeApplied(tenant, paths)
}

func (a *KubernetesApplier) loadApplied(tenant string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if paths, ok := a.applied[tenant]; ok {
		return append([]string(nil), paths...)
	}
	if a.stateDir == "" {
		return nil
	}
	data, err := os.ReadFile(a.pathsFile(tenant))
	if err != nil {
		return nil
	}
	var paths []string
	if json.Unmarshal(data, &paths) != nil {
		return nil
	}
	a.applied[tenant] = append([]string(nil), paths...)
	return append([]string(nil), paths...)
}

func (a *KubernetesApplier) storeApplied(tenant string, paths []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applied[tenant] = append([]string(nil), paths...)
	if a.stateDir == "" {
		return nil
	}
	data, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	return writeAtomic(a.pathsFile(tenant), data)
}

func (a *KubernetesApplier) pathsFile(tenant string) string {
	var name strings.Builder
	for _, r := range tenant {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			name.WriteRune(r)
		default:
			name.WriteByte('_')
		}
	}
	if name.Len() == 0 {
		name.WriteString("tenant")
	}
	return filepath.Join(a.stateDir, name.String()+".applied-paths.json")
}

func hasResourcePath(resources []RenderedResource, path string) bool {
	for _, resource := range resources {
		if resource.APIPath == path {
			return true
		}
	}
	return false
}

// ApplyGatewayNative applies only supplied resources. Disabled/tombstone rules
// render disabled plugins instead of deleting unrelated routes or resources.
func (a *KubernetesApplier) ApplyGatewayNative(ctx context.Context, resources []RenderedResource) error {
	for _, resource := range resources {
		query := url.Values{"fieldManager": {fieldManager}, "force": {"true"}}
		request, err := http.NewRequestWithContext(ctx, http.MethodPatch, a.baseURL+resource.APIPath+"?"+query.Encode(), strings.NewReader(resource.Body))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+a.bearerToken())
		request.Header.Set("Content-Type", "application/apply-patch+yaml")
		request.Header.Set("Accept", "application/json")
		response, err := a.client.Do(request)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("kubernetes apply %s returned %d: %s", resource.APIPath, response.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return nil
}

func hasResourceKind(resources []RenderedResource, kind ResourceKind) bool {
	for _, resource := range resources {
		if resource.Kind == kind {
			return true
		}
	}
	return false
}

func (a *KubernetesApplier) delete(ctx context.Context, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, a.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.bearerToken())
	request.Header.Set("Accept", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if (response.StatusCode < 200 || response.StatusCode >= 300) && response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("kubernetes delete %s returned %d: %s", path, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ReadSecret reads one key from a Kubernetes Secret in higress-system and
// returns the value. Serves as the CredentialFetcher for routes whose
// credentialRef names a Secret there.
func (a *KubernetesApplier) ReadSecret(ctx context.Context, name, key string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+"/api/v1/namespaces/higress-system/secrets/"+name, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+a.bearerToken())
	request.Header.Set("Accept", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read secret %s returned %d", name, response.StatusCode)
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &secret); err != nil {
		return "", err
	}
	encoded, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s has no key %s", name, key)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secret %s key %s is not valid base64: %w", name, key, err)
	}
	return string(decoded), nil
}
