package reconciler

import (
	"context"
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

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

// Rendered resource types and the render functions live in the shared
// services/internal/gatewayrender package so the gateway operator renders the
// same data plane from CRs. This package keeps the
// DB-shaped wire types (DesiredState/Status), the control-plane sync loop,
// and the Kubernetes applier.

type Config struct {
	NativeGateway gatewayrender.NativeGatewayConfig
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
	CredentialFetcher gatewayrender.CredentialFetcher
}

type DesiredState struct {
	DeploymentID string                `json:"deploymentId"`
	Revision     string                `json:"revision"`
	PublishedAt  string                `json:"publishedAt"`
	ContentHash  string                `json:"contentHash"`
	Routes       []gatewayrender.Route `json:"routes"`
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
	credentials := gatewayrender.FetchCredentials(ctx, desired.Routes, r.config.CredentialFetcher)
	document, resources, _, err := gatewayrender.Render(gatewayrender.Input{Deployment: desired.TenantID(), Revision: desired.Revision, Routes: desired.Routes}, credentials)
	if err != nil {
		return r.writeFailure(ctx, tenant, "RENDER_FAILED", err)
	}
	if gatewayrender.CanonicalHash(desired.Revision, desired.Routes) != desired.ContentHash {
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

func (r *Reconciler) addHeaders(request *http.Request, tenant string) {
	request.Header.Set("X-AEP-Data-Plane-Token", r.config.Token)
	request.Header.Set("X-AEP-Deployment-ID", tenant)
	request.Header.Set("X-AEP-Protocol-Version", "1.0")
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
