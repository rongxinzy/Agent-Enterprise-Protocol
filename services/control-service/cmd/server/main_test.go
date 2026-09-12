package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
)

func TestMainRunsSuccessfulHealthcheck(t *testing.T) {
	originalArguments := os.Args
	originalDependencies := productionServerDependencies
	t.Cleanup(func() {
		os.Args = originalArguments
		productionServerDependencies = originalDependencies
	})

	os.Args = []string{"aep-control", "healthcheck", "http://control:8080/readyz"}
	probeCalled := false
	productionServerDependencies.probe = func(url string, timeout time.Duration) error {
		probeCalled = true
		if url != "http://control:8080/readyz" || timeout != 2*time.Second {
			t.Fatalf("probe(%q, %s)", url, timeout)
		}
		return nil
	}
	main()
	if !probeCalled {
		t.Fatal("main() did not invoke the healthcheck probe")
	}
}

func TestRunUsesProductionDependencies(t *testing.T) {
	originalDependencies := productionServerDependencies
	t.Cleanup(func() { productionServerDependencies = originalDependencies })
	productionServerDependencies.loadConfig = func() (config.Config, error) {
		return config.Config{}, errors.New("test configuration failure")
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "test configuration failure") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunHealthcheck(t *testing.T) {
	tests := []struct {
		name       string
		arguments  []string
		probeError error
		wantCode   int
		wantURL    string
		wantStderr string
	}{
		{name: "default URL", wantURL: "http://127.0.0.1:8080/readyz"},
		{name: "custom URL", arguments: []string{"http://control:8080/healthz"}, wantURL: "http://control:8080/healthz"},
		{name: "probe failure", probeError: errors.New("service unavailable"), wantCode: 1, wantURL: "http://127.0.0.1:8080/readyz", wantStderr: "service unavailable"},
		{name: "usage failure", arguments: []string{"one", "two"}, wantCode: 2, wantStderr: "usage: aep-control healthcheck [url]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			probeCalls := 0
			code := runHealthcheck(test.arguments, &stderr, func(url string, timeout time.Duration) error {
				probeCalls++
				if url != test.wantURL || timeout != 2*time.Second {
					t.Fatalf("probe(%q, %s), want (%q, 2s)", url, timeout, test.wantURL)
				}
				return test.probeError
			})
			if code != test.wantCode || !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("runHealthcheck() = %d, stderr = %q", code, stderr.String())
			}
			if test.wantCode == 2 && probeCalls != 0 {
				t.Fatal("usage failure invoked the probe")
			}
			if test.wantCode != 2 && probeCalls != 1 {
				t.Fatalf("probe calls = %d", probeCalls)
			}
		})
	}
}

func testServerConfig() config.Config {
	return config.Config{
		Address:               "127.0.0.1:0",
		LogFormat:             "json",
		LogLevel:              "info",
		Environment:           "test",
		HTTPReadHeaderTimeout: 2 * time.Second,
		HTTPReadTimeout:       3 * time.Second,
		HTTPWriteTimeout:      4 * time.Second,
		HTTPIdleTimeout:       5 * time.Second,
		HTTPShutdownTimeout:   6 * time.Second,
		HTTPMaxHeaderBytes:    8192,
	}
}

func TestRunWithDependenciesInitializationFailures(t *testing.T) {
	t.Run("configuration", func(t *testing.T) {
		err := runWithDependencies(serverDependencies{
			loadConfig: func() (config.Config, error) { return config.Config{}, errors.New("invalid environment") },
		})
		if err == nil || !strings.Contains(err.Error(), "configuration: invalid environment") {
			t.Fatalf("runWithDependencies() error = %v", err)
		}
	})

	t.Run("logger", func(t *testing.T) {
		err := runWithDependencies(serverDependencies{
			loadConfig: func() (config.Config, error) { return testServerConfig(), nil },
			configureLogger: func(format, level, service, environment string) error {
				if format != "json" || level != "info" || service != "aep-control-service" || environment != "test" {
					t.Fatalf("unexpected logger arguments: %q %q %q %q", format, level, service, environment)
				}
				return errors.New("logger failed")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "configure logger: logger failed") {
			t.Fatalf("runWithDependencies() error = %v", err)
		}
	})

	t.Run("application", func(t *testing.T) {
		err := runWithDependencies(serverDependencies{
			loadConfig:      func() (config.Config, error) { return testServerConfig(), nil },
			configureLogger: func(string, string, string, string) error { return nil },
			openApplication: func(context.Context, config.Config) (*app.App, error) {
				return nil, errors.New("database unavailable")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "initialize: database unavailable") {
			t.Fatalf("runWithDependencies() error = %v", err)
		}
	})
}

func TestRunWithDependenciesServerLifecycle(t *testing.T) {
	tests := []struct {
		name          string
		serveError    error
		shutdownError error
		wantError     string
	}{
		{name: "server closed", serveError: http.ErrServerClosed},
		{name: "listen failure", serveError: errors.New("listen failed"), wantError: "listen failed"},
		{name: "shutdown failure", serveError: http.ErrServerClosed, shutdownError: errors.New("shutdown failed"), wantError: "shutdown: shutdown failed"},
		{name: "listen and shutdown failure", serveError: errors.New("listen failed"), shutdownError: errors.New("shutdown failed"), wantError: "listen failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testServerConfig()
			opened := false
			shutdownCalled := false
			dependencies := serverDependencies{
				loadConfig:      func() (config.Config, error) { return cfg, nil },
				configureLogger: func(string, string, string, string) error { return nil },
				openApplication: func(_ context.Context, received config.Config) (*app.App, error) {
					opened = true
					if received.Address != cfg.Address {
						t.Fatalf("openApplication address = %q", received.Address)
					}
					return &app.App{Config: received}, nil
				},
				listenAndServe: func(server *http.Server) error {
					if server.Addr != cfg.Address || server.ReadHeaderTimeout != cfg.HTTPReadHeaderTimeout ||
						server.ReadTimeout != cfg.HTTPReadTimeout || server.WriteTimeout != cfg.HTTPWriteTimeout ||
						server.IdleTimeout != cfg.HTTPIdleTimeout || server.MaxHeaderBytes != cfg.HTTPMaxHeaderBytes {
						t.Fatalf("server configuration = %#v", server)
					}
					response := httptest.NewRecorder()
					server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
					if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "control_service") {
						t.Fatalf("metrics response = %d %q", response.Code, response.Body.String())
					}
					return test.serveError
				},
				shutdown: func(_ *http.Server, ctx context.Context) error {
					shutdownCalled = true
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > cfg.HTTPShutdownTimeout {
						t.Fatalf("shutdown deadline = %v, present = %v", deadline, ok)
					}
					return test.shutdownError
				},
			}

			err := runWithDependencies(dependencies)
			if !opened || !shutdownCalled {
				t.Fatalf("opened = %v, shutdown = %v", opened, shutdownCalled)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("runWithDependencies() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runWithDependencies() error = %v, want detail %q", err, test.wantError)
			}
		})
	}
}
