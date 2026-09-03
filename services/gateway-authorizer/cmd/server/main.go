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

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-authorizer/internal/gateway"
	runtime "github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/runtime"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		url := "http://127.0.0.1:8090/readyz"
		if len(os.Args) == 3 {
			url = os.Args[2]
		} else if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: aep-gateway-authorizer healthcheck [url]")
			os.Exit(2)
		}
		if err := runtime.Probe(url, 2*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("gateway authorizer stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := gateway.LoadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := runtime.ConfigureLogger(config.LogFormat, config.LogLevel, "aep-gateway-authorizer", config.Environment); err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}
	verifier := gateway.NewVerifier(config.JWKSURL, config.Issuer, config.JWKSTTL, config.RequestTimeout)
	verifier.ConfigureLicenseStatus(config.LicenseStatusURL, config.LicenseStatusToken, config.LicenseStatusTTL)
	gatewayHandler, err := gateway.NewHandler(config, verifier)
	if err != nil {
		return fmt.Errorf("configure handler: %w", err)
	}
	metrics := runtime.NewHTTPMetrics("gateway_authorizer")
	handler := http.NewServeMux()
	handler.Handle("/metrics", metrics.Handler())
	handler.Handle("/", metrics.Middleware(gatewayHandler))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Addr:              config.Address,
		Handler:           handler,
		ReadHeaderTimeout: config.HTTPReadHeaderTimeout,
		ReadTimeout:       config.HTTPReadTimeout,
		WriteTimeout:      config.HTTPWriteTimeout,
		IdleTimeout:       config.HTTPIdleTimeout,
		MaxHeaderBytes:    config.HTTPMaxHeaderBytes,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("gateway authorizer listening", "address", config.Address)
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
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.HTTPShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		if serveErr == nil {
			serveErr = fmt.Errorf("shutdown: %w", err)
		} else {
			slog.Error("gateway authorizer shutdown failed", "error", err)
		}
	}
	return serveErr
}
