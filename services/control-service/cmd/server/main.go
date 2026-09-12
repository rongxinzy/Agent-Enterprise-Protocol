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
		slog.Error("control service stopped", "error", err)
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
		fmt.Fprintln(stderr, "usage: aep-control healthcheck [url]")
		return 2
	}
	if err := probe(url, 2*time.Second); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runWithDependencies(dependencies serverDependencies) error {
	cfg, err := dependencies.loadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := dependencies.configureLogger(cfg.LogFormat, cfg.LogLevel, "aep-control-service", cfg.Environment); err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := dependencies.openApplication(ctx, cfg)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	defer application.Close()

	metrics := runtime.NewHTTPMetrics("control_service")
	api := httpapi.New(application, metrics.Middleware).Handler()
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
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("control service listening", "address", cfg.Address)
		serverErrors <- dependencies.listenAndServe(server)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	defer cancel()
	if err := dependencies.shutdown(server, shutdownContext); err != nil {
		if serveErr == nil {
			serveErr = fmt.Errorf("shutdown: %w", err)
		} else {
			slog.Error("control service shutdown failed", "error", err)
		}
	}
	return serveErr
}
