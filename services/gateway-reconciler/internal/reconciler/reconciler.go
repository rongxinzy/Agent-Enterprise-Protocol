package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Config struct {
	ControlURL string
	Token      string
	OutputDir  string
	Tenants    []string
	HTTPClient *http.Client
	Applier    Applier
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
}

type DesiredState struct {
	DeploymentID string  `json:"deploymentId"`
	Revision     string  `json:"revision"`
	PublishedAt  string  `json:"publishedAt"`
	ContentHash  string  `json:"contentHash"`
	Routes       []Route `json:"routes"`
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
	document, _, err := Render(desired)
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
		if err := r.config.Applier.Apply(ctx, desired, document); err != nil {
			return r.writeFailure(ctx, tenant, "KUBERNETES_APPLY_FAILED", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
	defer response.Body.Close()
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
	defer response.Body.Close()
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

func (r *Reconciler) addHeaders(request *http.Request, tenant string) {
	request.Header.Set("X-AEP-Data-Plane-Token", r.config.Token)
	request.Header.Set("X-AEP-Tenant-ID", tenant)
	request.Header.Set("X-AEP-Protocol-Version", "1.0")
}

func Render(desired DesiredState) (string, string, error) {
	routes := append([]Route(nil), desired.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].ModelID < routes[j].ModelID })
	if desired.Revision == "" {
		return "", "", errors.New("desired revision is required")
	}
	if strings.TrimSpace(desired.DeploymentID) == "" {
		return "", "", errors.New("enterprise ID is required")
	}
	suffix := resourceSuffix(desired.DeploymentID)
	enabled := make([]Route, 0, len(routes))
	for index := range routes {
		route := routes[index]
		if route.ProviderType == "" {
			route.ProviderType = "openai"
		}
		if route.ProviderType != "openai" && route.ProviderType != "deepseek" {
			return "", "", fmt.Errorf("unsupported provider type %q for model %q", route.ProviderType, route.ModelID)
		}
		if route.Enabled {
			enabled = append(enabled, route)
		}
	}
	var document strings.Builder
	document.WriteString("apiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n  name: " + yamlScalar("aep-model-gateway-"+suffix) + "\n  namespace: higress-system\nspec:\n  ingressClassName: higress\n")
	if len(enabled) > 0 {
		document.WriteString("  rules:\n    - http:\n        paths:\n")
		for _, route := range enabled {
			document.WriteString("          - path: " + yamlScalar(route.Endpoint) + "\n            pathType: Prefix\n            backend:\n              service:\n                name: aep-model-gateway\n                port:\n                  number: 80\n")
		}
	}
	document.WriteString("---\napiVersion: extensions.higress.io/v1alpha1\nkind: WasmPlugin\nmetadata:\n  name: " + yamlScalar("aep-ai-proxy-"+suffix) + "\n  namespace: higress-system\nspec:\n  failStrategy: FAIL_CLOSE\n  defaultConfigDisable: true\n")
	if len(enabled) == 0 {
		document.WriteString("  matchRules: []\n")
	} else {
		document.WriteString("  matchRules:\n")
	}
	for _, route := range enabled {
		document.WriteString("    - config:\n        provider:\n          type: " + yamlScalar(route.ProviderType) + "\n          modelMapping:\n            " + yamlScalar(route.ModelID) + ": " + yamlScalar(route.UpstreamModel) + "\n")
		if route.CredentialRef != nil {
			document.WriteString("        credentialRef:\n          name: " + yamlScalar(route.CredentialRef.Name) + "\n          key: " + yamlScalar(route.CredentialRef.Key) + "\n")
			if route.CredentialRef.Namespace != nil {
				document.WriteString("          namespace: " + yamlScalar(*route.CredentialRef.Namespace) + "\n")
			}
		}
		document.WriteString("      ingress:\n        - " + yamlScalar("aep-model-gateway-"+suffix) + "\n")
	}
	canonical := strings.TrimSpace(document.String()) + "\n"
	digest := sha256.Sum256([]byte(canonical))
	return canonical, hex.EncodeToString(digest[:]), nil
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
	defer os.Remove(temporaryName)
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
