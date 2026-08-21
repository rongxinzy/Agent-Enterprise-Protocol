package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-authorizer/internal/gateway"
)

func main() {
	config := gateway.LoadConfig()
	verifier := gateway.NewVerifier(config.JWKSURL, config.Issuer, config.JWKSTTL, config.RequestTimeout)
	handler, err := gateway.NewHandler(config, verifier)
	if err != nil {
		slog.Error("gateway authorizer configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: config.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("AEP gateway authorizer listening", "address", config.Address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway authorizer stopped unexpectedly", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		slog.Error("gateway authorizer shutdown failed", "error", err)
	}
}
