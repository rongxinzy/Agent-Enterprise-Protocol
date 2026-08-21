package main

import (
	"context"
	"errors"
	"fmt"
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

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		url := "http://127.0.0.1:8080/readyz"
		if len(os.Args) == 3 {
			url = os.Args[2]
		} else if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: aep-control healthcheck [url]")
			os.Exit(2)
		}
		if err := runtime.Probe(url, 2*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("control service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := runtime.ConfigureLogger(cfg.LogFormat, cfg.LogLevel, "aep-control-service", cfg.Environment); err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := app.Open(ctx, cfg)
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
		serverErrors <- server.ListenAndServe()
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
	if err := server.Shutdown(shutdownContext); err != nil {
		if serveErr == nil {
			serveErr = fmt.Errorf("shutdown: %w", err)
		} else {
			slog.Error("control service shutdown failed", "error", err)
		}
	}
	return serveErr
}
