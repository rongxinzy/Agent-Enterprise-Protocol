package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
)

func TestAgentControlMainRunsSuccessfulHealthcheck(t *testing.T) {
	originalArguments := os.Args
	originalDependencies := productionServerDependencies
	t.Cleanup(func() {
		os.Args = originalArguments
		productionServerDependencies = originalDependencies
	})

	os.Args = []string{"aep-agent-control", "healthcheck", "http://agent:8080/readyz"}
	probeCalled := false
	productionServerDependencies.probe = func(url string, timeout time.Duration) error {
		probeCalled = true
		if url != "http://agent:8080/readyz" || timeout != 2*time.Second {
			t.Fatalf("probe(%q, %s)", url, timeout)
		}
		return nil
	}
	main()
	if !probeCalled {
		t.Fatal("main() did not invoke the healthcheck probe")
	}
}

func TestAgentControlRunUsesProductionDependencies(t *testing.T) {
	originalDependencies := productionServerDependencies
	t.Cleanup(func() { productionServerDependencies = originalDependencies })
	productionServerDependencies.loadConfig = func() (config.Config, error) {
		return config.Config{}, errors.New("test configuration failure")
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "test configuration failure") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestAgentControlRunHealthcheck(t *testing.T) {
	tests := []struct {
		name       string
		arguments  []string
		probeError error
		wantCode   int
		wantURL    string
		wantStderr string
	}{
		{name: "success with url", arguments: []string{"http://agent:9000/readyz"}, wantURL: "http://agent:9000/readyz"},
		{name: "success without url uses default", wantURL: "http://127.0.0.1:8080/readyz"},
		{name: "probe failure", probeError: errors.New("probe failed"), wantCode: 1, wantURL: "http://127.0.0.1:8080/readyz", wantStderr: "probe failed"},
		{name: "too many arguments", arguments: []string{"a", "b"}, wantCode: 2, wantStderr: "usage: aep-agent-control healthcheck"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			probeURL := ""
			exitCode := runHealthcheck(test.arguments, &stderr, func(url string, _ time.Duration) error {
				probeURL = url
				return test.probeError
			})
			if exitCode != test.wantCode || (test.wantURL != "" && probeURL != test.wantURL) {
				t.Fatalf("runHealthcheck(%v) = %d, probe %q; want %d, %q", test.arguments, exitCode, probeURL, test.wantCode, test.wantURL)
			}
			if test.wantStderr != "" && !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.wantStderr)
			}
		})
	}
}

func agentControlTestConfig() config.Config {
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
	}
}

func TestAgentControlRunLogsAdvertisementState(t *testing.T) {
	tests := []struct {
		name        string
		advertised  string
		wantMessage string
	}{
		{name: "advertised", advertised: "https://agents.example.com", wantMessage: `base_url=https://agents.example.com`},
		{name: "unset", wantMessage: "without advertising a split endpoint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			cfg := agentControlTestConfig()
			cfg.AgentControlBaseURL = test.advertised
			dependencies := serverDependencies{
				loadConfig:      func() (config.Config, error) { return cfg, nil },
				configureLogger: func(string, string, string, string) error { return nil },
				openApplication: func(_ context.Context, received config.Config) (*app.App, error) {
					return &app.App{Config: received}, nil
				},
				listenAndServe: func(*http.Server) error { return http.ErrServerClosed },
				shutdown:       func(*http.Server, context.Context) error { return nil },
			}
			if err := runWithDependencies(dependencies); err != nil {
				t.Fatalf("runWithDependencies() error = %v", err)
			}
			if !strings.Contains(logs.String(), test.wantMessage) {
				t.Fatalf("startup logs = %q, want %q", logs.String(), test.wantMessage)
			}
		})
	}
}

func TestAgentControlServerLifecycleAndSurface(t *testing.T) {
	cfg := agentControlTestConfig()
	opened := false
	dependencies := serverDependencies{
		loadConfig:      func() (config.Config, error) { return cfg, nil },
		configureLogger: func(string, string, string, string) error { return nil },
		openApplication: func(_ context.Context, _ config.Config) (*app.App, error) {
			opened = true
			return &app.App{}, nil
		},
		listenAndServe: func(server *http.Server) error {
			// The agent surface must serve auth and the user runtime, never
			// the admin API, and must expose metrics for the agent_control
			// subsystem.
			metadata := httptest.NewRequest(http.MethodGet, "/aep/v1/metadata", nil)
			metadata.Header.Set("X-AEP-Protocol-Version", "1.0")
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, metadata)
			if response.Code != http.StatusOK {
				t.Fatalf("metadata on agent surface = %d", response.Code)
			}
			admin := httptest.NewRequest(http.MethodGet, "/aep/v1/admin/models", nil)
			admin.Header.Set("X-AEP-Protocol-Version", "1.0")
			response = httptest.NewRecorder()
			server.Handler.ServeHTTP(response, admin)
			if response.Code != http.StatusNotFound {
				t.Fatalf("admin API leaked onto the agent surface: %d", response.Code)
			}
			metrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			response = httptest.NewRecorder()
			server.Handler.ServeHTTP(response, metrics)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "agent_control") {
				t.Fatalf("metrics response = %d %q", response.Code, response.Body.String())
			}
			return http.ErrServerClosed
		},
		shutdown: func(*http.Server, context.Context) error { return nil },
	}
	if err := runWithDependencies(dependencies); err != nil || !opened {
		t.Fatalf("runWithDependencies() = %v, opened = %v", err, opened)
	}
}

func TestAgentControlInitializationFailure(t *testing.T) {
	dependencies := serverDependencies{
		loadConfig:      func() (config.Config, error) { return agentControlTestConfig(), nil },
		configureLogger: func(string, string, string, string) error { return nil },
		openApplication: func(context.Context, config.Config) (*app.App, error) {
			return nil, errors.New("database unavailable")
		},
	}
	if err := runWithDependencies(dependencies); err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("runWithDependencies() error = %v", err)
	}
}
