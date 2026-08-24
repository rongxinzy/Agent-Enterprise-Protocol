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
	return serverConfig{worker: reconciler.Config{ControlURL: os.Getenv("AEP_RECONCILER_CONTROL_URL"), Token: os.Getenv("AEP_DATA_PLANE_RECONCILER_TOKEN"), OutputDir: value("AEP_RECONCILER_OUTPUT_DIR", "/var/lib/aep-reconciler"), Tenants: tenants}, address: value("AEP_RECONCILER_ADDRESS", ":8091"), interval: interval}, nil
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
