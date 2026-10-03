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
	"sort"
	"strings"
	"time"
)

const fieldManager = "aep-gateway-reconciler"

type Applier interface {
	Apply(context.Context, DesiredState, []RenderedResource) error
}

type KubernetesConfig struct {
	URL        string
	Token      string
	CAFile     string
	HTTPClient *http.Client
}

type KubernetesApplier struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewKubernetesApplier(config KubernetesConfig) (*KubernetesApplier, error) {
	baseURL := strings.TrimRight(config.URL, "/")
	if baseURL == "" || config.Token == "" {
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
	return &KubernetesApplier{baseURL: baseURL, token: config.Token, client: client}, nil
}

// Apply server-side-applies every rendered resource and first deletes what
// the desired state no longer owns: the tenant's openai Ingress when no
// openai-compatible route is enabled, and the per-route anthropic
// Ingress+EnvoyFilter pair for every disabled anthropic route. The WasmPlugin
// is always present in the render (with empty matchRules when idle) and is
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
	deletions := make([]string, 0, 4)
	hasEnabledOpenAI := false
	for _, route := range desired.Routes {
		if route.Protocol == "anthropic" {
			if !route.Enabled {
				name := anthropicResourceName(route.ModelID)
				deletions = append(deletions, ingressAPIPath(name), envoyFilterAPIPath(name))
			}
			continue
		}
		if route.Enabled {
			hasEnabledOpenAI = true
		}
	}
	if !hasEnabledOpenAI {
		deletions = append(deletions, openAIIngressAPIPath(resourceSuffix(desired.TenantID())))
	}
	sort.Strings(deletions)
	for _, path := range deletions {
		if err := a.delete(ctx, path); err != nil {
			return err
		}
	}
	for _, resource := range resources {
		query := url.Values{"fieldManager": {fieldManager}, "force": {"true"}}
		request, err := http.NewRequestWithContext(ctx, http.MethodPatch, a.baseURL+resource.APIPath+"?"+query.Encode(), strings.NewReader(resource.Body))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+a.token)
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
	request.Header.Set("Authorization", "Bearer "+a.token)
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
	request.Header.Set("Authorization", "Bearer "+a.token)
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
