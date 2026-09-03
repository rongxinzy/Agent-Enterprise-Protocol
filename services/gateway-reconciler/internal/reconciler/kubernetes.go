package reconciler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const fieldManager = "aep-gateway-reconciler"

type Applier interface {
	Apply(context.Context, DesiredState, string) error
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
		return nil, errors.New("Kubernetes URL and service-account token are required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Kubernetes URL must be an absolute HTTP(S) URL")
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
				return nil, errors.New("Kubernetes CA file contains no certificates")
			}
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		}
		client = &http.Client{Timeout: 10 * time.Second, Transport: transport}
	}
	return &KubernetesApplier{baseURL: baseURL, token: config.Token, client: client}, nil
}

func (a *KubernetesApplier) Apply(ctx context.Context, desired DesiredState, document string) error {
	documents := strings.Split(document, "---\n")
	if len(documents) != 2 {
		return errors.New("rendered data plane must contain exactly two resources")
	}
	suffix := resourceSuffix(desired.DeploymentID)
	resources := []struct {
		path string
		body string
	}{
		{path: "/apis/networking.k8s.io/v1/namespaces/higress-system/ingresses/aep-model-gateway-" + suffix, body: documents[0]},
		{path: "/apis/extensions.higress.io/v1alpha1/namespaces/higress-system/wasmplugins/aep-ai-proxy-" + suffix, body: documents[1]},
	}
	hasEnabledRoute := false
	for _, route := range desired.Routes {
		if route.Enabled {
			hasEnabledRoute = true
			break
		}
	}
	if !hasEnabledRoute {
		if err := a.delete(ctx, resources[0].path); err != nil {
			return err
		}
		resources = resources[1:]
	}
	for _, resource := range resources {
		query := url.Values{"fieldManager": {fieldManager}, "force": {"true"}}
		request, err := http.NewRequestWithContext(ctx, http.MethodPatch, a.baseURL+resource.path+"?"+query.Encode(), strings.NewReader(resource.body))
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
			return fmt.Errorf("Kubernetes apply %s returned %d: %s", resource.path, response.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return nil
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
		return fmt.Errorf("Kubernetes delete %s returned %d: %s", path, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
