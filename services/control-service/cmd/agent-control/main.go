// Command aep-agent-control serves the agent control protocol — the
// desktop-agent runtime surface (/aep/v1/user/*: heartbeat, control-event
// inbox, skill delivery, telemetry, model connection, credential
// resolution) plus the shared session and discovery endpoints. The AEP
// participant model (docs/aep-v1.md) names this the Control, Event, and
// Asset service; this binary actualizes it as its own process, sharing the
// control plane's PostgreSQL, MinIO, and signing key. All-in-one
// deployments keep using the control-service server, which mounts the same
// routes.
//
// The binary lives beside cmd/server rather than in its own service tree
// because it deliberately shares the control-service's internal packages
// (app, auth, httpapi handlers): the architectural split is the process,
// image, and network boundary — not a code fork.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/httpapi"
	runtime "github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/runtime"
)

type serverDependencies struct {
	loadConfig      func() (config.Config, error)
	configureLogger func(format, level, service, environment string) error
	openApplication func(context.Context, config.Config) (*app.App, error)
	listenAndServe  func(*http.Server) error
	shutdown        func(*http.Server, context.Context) error
	probe           func(string, time.Duration) error
}

var productionServerDependencies = serverDependencies{
	loadConfig:      config.Load,
	configureLogger: runtime.ConfigureLogger,
	openApplication: app.Open,
	listenAndServe:  (*http.Server).ListenAndServe,
	shutdown:        (*http.Server).Shutdown,
	probe:           runtime.Probe,
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if exitCode := runHealthcheck(os.Args[2:], os.Stderr, productionServerDependencies.probe); exitCode != 0 {
			os.Exit(exitCode)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("agent control service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	return runWithDependencies(productionServerDependencies)
}

func runHealthcheck(arguments []string, stderr io.Writer, probe func(string, time.Duration) error) int {
	url := "http://127.0.0.1:8080/readyz"
	if len(arguments) == 1 {
		url = arguments[0]
	} else if len(arguments) != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: aep-agent-control healthcheck [url]")
		return 2
	}
	if err := probe(url, 2*time.Second); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runWithDependencies(dependencies serverDependencies) error {
	cfg, err := dependencies.loadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := dependencies.configureLogger(cfg.LogFormat, cfg.LogLevel, "aep-agent-control", cfg.Environment); err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}
	// Split deployments advertise this service's own address through the
	// control-service metadata (agentControl.baseUrl) so desktop agents can
	// discover the redirect; all-in-one deployments leave it unset.
	if cfg.AgentControlBaseURL == "" {
		slog.Info("AEP_AGENT_CONTROL_BASE_URL is not set; this deployment serves the agent surface without advertising a split endpoint")
	} else {
		slog.Info("agent control endpoint advertised", "base_url", cfg.AgentControlBaseURL)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := dependencies.openApplication(ctx, cfg)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	defer application.Close()
	// Retention and bootstrap stay with the enterprise control-service; this
	// process only serves the agent protocol surface. app.Open remains
	// idempotent (advisory-locked migrations) when both processes boot.

	metrics := runtime.NewHTTPMetrics("agent_control")
	api := httpapi.NewAgentControlAPI(application, metrics.Middleware).Handler()
	handler := http.NewServeMux()
	handler.Handle("/metrics", metrics.Handler())
	handler.Handle("/", api)
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.HTTPMaxHeaderBytes,
	}
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("agent control service listening", "address", cfg.Address, "environment", cfg.Environment)
		serverErr <- dependencies.listenAndServe(server)
	}()
	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := dependencies.shutdown(server, shutdownContext); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
