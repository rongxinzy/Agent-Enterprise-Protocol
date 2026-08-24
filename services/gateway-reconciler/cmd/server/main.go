package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-reconciler/internal/reconciler"
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
	config, err := loadConfig()
	if err != nil {
		slog.Error("gateway reconciler configuration failed", "error", err)
		os.Exit(1)
	}
	worker, err := reconciler.New(config.worker)
	if err != nil {
		slog.Error("gateway reconciler initialization failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go serveHealth(ctx, config.address)
	for _, tenant := range config.worker.Tenants {
		go runTenant(ctx, worker, tenant, config.interval)
	}
	<-ctx.Done()
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
	if kubernetesURL := os.Getenv("AEP_RECONCILER_KUBERNETES_URL"); kubernetesURL != "" {
		kubernetesToken, err := secret("AEP_RECONCILER_KUBERNETES_TOKEN")
		if err != nil {
			return serverConfig{}, err
		}
		workerConfig.Applier, err = reconciler.NewKubernetesApplier(reconciler.KubernetesConfig{URL: kubernetesURL, Token: kubernetesToken, CAFile: os.Getenv("AEP_RECONCILER_KUBERNETES_CA_FILE")})
		if err != nil {
			return serverConfig{}, err
		}
	}
	return serverConfig{worker: workerConfig, address: value("AEP_RECONCILER_ADDRESS", ":8091"), interval: interval}, nil
}

func runTenant(ctx context.Context, worker *reconciler.Reconciler, tenant string, interval time.Duration) {
	backoff := time.Second
	for {
		err := worker.Sync(ctx, tenant)
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

func serveHealth(ctx context.Context, address string) {
	server := &http.Server{Addr: address, Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/livez" || request.URL.Path == "/readyz" {
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(response, request)
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
