package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestMainHealthcheckSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("unexpected healthcheck path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	previousArgs := os.Args
	os.Args = []string{"gateway-authorizer", "healthcheck", server.URL + "/readyz"}
	defer func() { os.Args = previousArgs }()
	main()
}

func TestMainHealthcheckFailureExitCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, mode string
		exitCode   int
	}{
		{"unready service", "unready", 1},
		{"invalid arguments", "arguments", 2},
		{"invalid startup config", "startup", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(executable, "-test.run=^TestMainHealthcheckHelper$")
			command.Env = append(os.Environ(), "AEP_TEST_HEALTHCHECK_HELPER=1", "AEP_TEST_HEALTHCHECK_MODE="+test.mode, "AEP_TEST_HEALTHCHECK_URL="+server.URL, "AEP_ENVIRONMENT=invalid")
			output, err := command.CombinedOutput()
			var exitCode int
			if err != nil {
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("start helper: %v", err)
				}
				exitCode = exitErr.ExitCode()
			}
			if exitCode != test.exitCode {
				t.Fatalf("exit code = %d, want %d; output = %q", exitCode, test.exitCode, output)
			}
		})
	}
}

func TestMainHealthcheckHelper(t *testing.T) {
	if os.Getenv("AEP_TEST_HEALTHCHECK_HELPER") != "1" {
		return
	}
	switch os.Getenv("AEP_TEST_HEALTHCHECK_MODE") {
	case "unready":
		os.Args = []string{"gateway-authorizer", "healthcheck", os.Getenv("AEP_TEST_HEALTHCHECK_URL")}
	case "arguments":
		os.Args = []string{"gateway-authorizer", "healthcheck", "http://127.0.0.1:1/readyz", "unexpected"}
	case "startup":
		os.Args = []string{"gateway-authorizer"}
	default:
		t.Fatal("unexpected helper mode")
	}
	main()
	t.Fatal("main returned unexpectedly")
}
