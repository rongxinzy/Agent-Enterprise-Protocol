package main

import (
	"net"
	"strings"
	"testing"
)

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("AEP_ENVIRONMENT", "development")
	t.Setenv("AEP_GATEWAY_UPSTREAM_URL", "not-an-http-url")
	if err := run(); err == nil || !strings.Contains(err.Error(), "configuration: AEP_GATEWAY_UPSTREAM_URL") {
		t.Fatalf("run() configuration error = %v", err)
	}
}

func TestRunReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("AEP_ENVIRONMENT", "development")
	t.Setenv("AEP_GATEWAY_REQUIRE_ENTITLEMENT", "false")
	t.Setenv("AEP_GATEWAY_UPSTREAM_URL", "http://127.0.0.1:8080")
	t.Setenv("AEP_GATEWAY_ADDRESS", listener.Addr().String())
	if err := run(); err == nil || !strings.Contains(err.Error(), listener.Addr().String()) {
		t.Fatalf("run() listener error = %v", err)
	}
}
