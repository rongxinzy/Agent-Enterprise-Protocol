package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-reconciler/internal/reconciler"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if len(os.Args) < 3 {
			os.Exit(2)
		}
		response, err := (&http.Client{Timeout: 2 * time.Second}).Get(os.Args[2])
		if err != nil || response.StatusCode != http.StatusOK {
			if response != nil {
				_ = response.Body.Close()
			}
			os.Exit(1)
		}
		_ = response.Body.Close()
		return
	}
	if err := run(); err != nil {
		slog.Error("gateway reconciler stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	worker, err := reconciler.New(config.worker)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	readiness := newReadinessState(config.worker.Tenants)
	go serveHealth(ctx, config.address, readiness)

	var workers sync.WaitGroup
	startWorkers := func(workCtx context.Context) {
		for _, tenant := range config.worker.Tenants {
			workers.Add(1)
			go func() {
				defer workers.Done()
				runTenant(workCtx, worker, tenant, config.interval, readiness)
			}()
		}
	}

	// Leader election: when multiple replicas run, only the lease holder
	// executes the reconcile loop; followers idle (but stay ready) for
	// fast failover. Disabled (nil) when no KubernetesApplier is configured.
	// client-go never re-elects after a lost lease, so an ended election exits
	// the process non-zero and Kubernetes restarts the pod.
	var elector *reconciler.LeaderElector
	if applier, ok := config.worker.Applier.(*reconciler.KubernetesApplier); ok {
		identity := os.Getenv("POD_NAME")
		if identity == "" {
			identity = fmt.Sprintf("reconciler-%d", os.Getpid())
		}
		elector = reconciler.NewLeaderElector(applier, "aep-system", "gateway-reconciler-leader", identity)
		elector.StartWork = startWorkers
		// Followers never start workers; they stay ready exactly like the old
		// idle loop did, until they lead or the process exits after a lost lease.
		readiness.markReady()
		slog.Info("leader election enabled", "identity", identity)
	} else {
		slog.Info("leader election disabled (no Kubernetes applier)")
		startWorkers(ctx)
		<-ctx.Done()
		return nil
	}

	electionDone := make(chan error, 1)
	go func() { electionDone <- elector.Run(ctx) }()
	select {
	case <-ctx.Done():
		// SIGTERM/SIGINT: orderly shutdown; wait for the guarded lease release.
		if err := <-electionDone; err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	case err := <-electionDone:
		if ctx.Err() != nil {
			// The signal path won the race with the elector returning.
			return nil
		}
		stop()
		// The lease-scoped work context is already cancelled; drain the
		// unwind before exiting so no reconcile survives the leadership loss.
		drained := make(chan struct{})
		go func() {
			workers.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
			slog.Warn("reconcile workers did not drain before exit")
		}
		return fmt.Errorf("leader election ended: %w", err)
	}
}

type serverConfig struct {
	worker   reconciler.Config
	address  string
	interval time.Duration
}

func loadConfig() (serverConfig, error) {
	tenants := split(os.Getenv("AEP_RECONCILER_TENANTS"))
	interval, err := time.ParseDuration(value("AEP_RECONCILER_INTERVAL", "15s"))
	if err != nil || interval <= 0 {
		return serverConfig{}, errors.New("AEP_RECONCILER_INTERVAL must be positive")
	}
	token, err := secret("AEP_DATA_PLANE_RECONCILER_TOKEN")
	if err != nil {
		return serverConfig{}, err
	}
	workerConfig := reconciler.Config{ControlURL: os.Getenv("AEP_RECONCILER_CONTROL_URL"), Token: token, OutputDir: value("AEP_RECONCILER_OUTPUT_DIR", "/var/lib/aep-reconciler"), Tenants: tenants}
	if path := os.Getenv("AEP_RECONCILER_NATIVE_GATEWAY_CONFIG_FILE"); path != "" {
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) > 64<<10 {
			return serverConfig{}, errors.New("native gateway configuration file unavailable")
		}
		if json.Unmarshal(data, &workerConfig.NativeGateway) != nil {
			return serverConfig{}, errors.New("invalid native gateway configuration")
		}
	}
	if kubernetesURL := os.Getenv("AEP_RECONCILER_KUBERNETES_URL"); kubernetesURL != "" {
		kubernetesToken, err := secret("AEP_RECONCILER_KUBERNETES_TOKEN")
		if err != nil {
			return serverConfig{}, err
		}
		workerConfig.Applier, err = reconciler.NewKubernetesApplier(reconciler.KubernetesConfig{URL: kubernetesURL, Token: kubernetesToken, TokenFile: os.Getenv("AEP_RECONCILER_KUBERNETES_TOKEN_FILE"), CAFile: os.Getenv("AEP_RECONCILER_KUBERNETES_CA_FILE"), StateDir: workerConfig.OutputDir})
		if err != nil {
			return serverConfig{}, err
		}
		if applier, ok := workerConfig.Applier.(*reconciler.KubernetesApplier); ok {
			workerConfig.CredentialFetcher = func(ctx context.Context, ref gatewayrender.SecretReference) (string, error) {
				// Route references carry the pinned credential namespace; an
				// operator-configured reference leaves it unset and ReadSecret
				// falls back to its default namespace.
				namespace := ""
				if ref.Namespace != nil {
					namespace = *ref.Namespace
				}
				return applier.ReadSecret(ctx, namespace, ref.Name, ref.Key)
			}
		}
	}
	return serverConfig{worker: workerConfig, address: value("AEP_RECONCILER_ADDRESS", ":8091"), interval: interval}, nil
}

func runTenant(ctx context.Context, worker *reconciler.Reconciler, tenant string, interval time.Duration, readiness *readinessState) {
	backoff := time.Second
	for {
		err := worker.Sync(ctx, tenant)
		readiness.record(tenant, err)
		if err != nil {
			slog.Error("data-plane reconciliation failed", "tenant", tenant, "error", err)
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(interval)
		if err != nil {
			timer.Reset(backoff)
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type readinessState struct {
	mutex   sync.RWMutex
	tenants map[string]bool
}

func newReadinessState(tenants []string) *readinessState {
	state := &readinessState{tenants: make(map[string]bool, len(tenants))}
	for _, tenant := range tenants {
		state.tenants[tenant] = false
	}
	return state
}

func (s *readinessState) record(tenant string, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.tenants[tenant] = err == nil
}

// markReady reports every tenant as ready without reconciling: followers never
// run workers, and this keeps their readiness semantics identical to the old
// idle loop until they either lead (workers take over reporting) or exit.
func (s *readinessState) markReady() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for tenant := range s.tenants {
		s.tenants[tenant] = true
	}
}

func (s *readinessState) ready() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if len(s.tenants) == 0 {
		return false
	}
	for _, ready := range s.tenants {
		if !ready {
			return false
		}
	}
	return true
}

func serveHealth(ctx context.Context, address string, readiness *readinessState) {
	server := &http.Server{Addr: address, Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/livez":
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			response.Header().Set("Content-Type", "application/json")
			if readiness.ready() {
				response.WriteHeader(http.StatusOK)
				_, _ = response.Write([]byte(`{"status":"ok"}`))
				return
			}
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = response.Write([]byte(`{"status":"not_ready"}`))
		default:
			http.NotFound(response, request)
		}
	})}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("gateway reconciler health server stopped", "error", err)
	}
}

func split(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func value(key, fallback string) string {
	if current := os.Getenv(key); current != "" {
		return current
	}
	return fallback
}

func secret(key string) (string, error) {
	direct, file := os.Getenv(key), os.Getenv(key+"_FILE")
	if direct != "" && file != "" {
		return "", errors.New(key + " and " + key + "_FILE are mutually exclusive")
	}
	if file == "" {
		return direct, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
