package reconciler

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const fieldManager = "aep-gateway-reconciler"

type Applier interface {
	Apply(context.Context, DesiredState, []RenderedResource) error
}

type KubernetesConfig struct {
	URL   string
	Token string
	// TokenFile is read by the client-go transport, which re-reads the file
	// through its cached file token source (one-minute cache tuned to the
	// kubelet's projected-token refresh cadence, last good value kept on a
	// failed read). Projected service-account tokens rotate in place, so a
	// token captured once at startup silently 401s after its first TTL.
	TokenFile string
	CAFile    string
}

type KubernetesApplier struct {
	config *rest.Config
	client kubernetes.Interface
}

func NewKubernetesApplier(config KubernetesConfig) (*KubernetesApplier, error) {
	return newKubernetesApplier(config, nil)
}

// newKubernetesApplier builds the applier from the Kubernetes connection
// configuration; an injected client replaces the connection in tests.
func newKubernetesApplier(config KubernetesConfig, client kubernetes.Interface) (*KubernetesApplier, error) {
	baseURL := strings.TrimRight(config.URL, "/")
	if baseURL == "" || (config.Token == "" && config.TokenFile == "") {
		return nil, errors.New("kubernetes URL and service-account token are required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("kubernetes URL must be an absolute HTTP(S) URL")
	}
	restConfig := &rest.Config{
		Host:            baseURL,
		Timeout:         10 * time.Second,
		BearerToken:     config.Token,
		BearerTokenFile: config.TokenFile,
	}
	if config.CAFile != "" {
		certificate, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read Kubernetes CA: %w", err)
		}
		// Validate the PEM eagerly, before any request fails mid-reconcile.
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(certificate) {
			return nil, errors.New("kubernetes CA file contains no certificates")
		}
		restConfig.TLSClientConfig = rest.TLSClientConfig{CAData: certificate}
	}
	if client == nil {
		client, err = kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, err
		}
	}
	return &KubernetesApplier{config: restConfig, client: client}, nil
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
	return a.delete(ctx, openAIIngressAPIPath(resourceSuffix(desired.TenantID())))
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
		err := a.client.CoreV1().RESTClient().Patch(types.ApplyPatchType).
			AbsPath(resource.APIPath).
			Param("fieldManager", fieldManager).
			// force=true keeps takeover parity with the previous raw-HTTP
			// client; reporting SSA conflicts instead of stealing ownership is
			// a deliberate follow-up.
			Param("force", "true").
			Body([]byte(resource.Body)).
			Do(ctx).Error()
		if err != nil {
			return fmt.Errorf("kubernetes apply %s returned %s", resource.APIPath, kubernetesErrorStatus(err))
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
	err := a.client.CoreV1().RESTClient().Delete().AbsPath(path).Do(ctx).Error()
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("kubernetes delete %s returned %s", path, kubernetesErrorStatus(err))
}

// kubernetesErrorStatus renders a client-go failure with its HTTP status code
// when the API server produced one, keeping the historical log shape.
func kubernetesErrorStatus(err error) string {
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Code != 0 {
		return fmt.Sprintf("%d: %s", status.Status().Code, status.Status().Message)
	}
	return err.Error()
}

// ReadSecret reads one key from a Kubernetes Secret in higress-system and
// returns the value. Serves as the CredentialFetcher for routes whose
// credentialRef names a Secret there. The typed client decodes the base64
// data field itself; a corrupt entry fails the whole read.
func (a *KubernetesApplier) ReadSecret(ctx context.Context, name, key string) (string, error) {
	secret, err := a.client.CoreV1().Secrets("higress-system").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		var status apierrors.APIStatus
		if errors.As(err, &status) && status.Status().Code != 0 {
			return "", fmt.Errorf("read secret %s returned %d", name, status.Status().Code)
		}
		return "", fmt.Errorf("read secret %s: %w", name, err)
	}
	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s has no key %s", name, key)
	}
	return string(value), nil
}
