package assetprobe

import (
	"context"
	"strings"
	"testing"
	"time"
)

func execAuth(host string, port float64, password string) (string, float64, SSHAuth) {
	return host, port, SSHAuth{Method: "password", Username: "probe-user", Password: password}
}

func TestExecSSHSuccess(t *testing.T) {
	host, port, _ := startTestSSHServer(t)
	result, err := ExecSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "probe-secret"}, "uptime")
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "up 42 days") || result.Truncated {
		t.Fatalf("ExecSSH(uptime) = %#v, %v", result, err)
	}
	if result.Duration <= 0 {
		t.Fatalf("duration not recorded: %#v", result)
	}
}

func TestExecSSHNonZeroExit(t *testing.T) {
	host, port, _ := startTestSSHServer(t)
	result, err := ExecSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "probe-secret"}, "fail")
	if err != nil || result.ExitCode != 7 || !strings.Contains(result.Output, "boom") {
		t.Fatalf("ExecSSH(fail) = %#v, %v", result, err)
	}
}

func TestExecSSHOutputTruncation(t *testing.T) {
	host, port, _ := startTestSSHServer(t)
	result, err := ExecSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "probe-secret"}, "big")
	if err != nil || !result.Truncated || len(result.Output) != ExecOutputLimit {
		t.Fatalf("ExecSSH(big) = truncated=%v len=%d err=%v", result.Truncated, len(result.Output), err)
	}
}

func TestExecSSHDeadline(t *testing.T) {
	host, port, _ := startTestSSHServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err := ExecSSH(ctx, host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "probe-secret"}, "sleep")
	if err == nil || result.ExitCode != -1 {
		t.Fatalf("ExecSSH(sleep, 500ms) = %#v, %v — want deadline error", result, err)
	}
}

func TestExecSSHAuthFailureClassified(t *testing.T) {
	host, port, _ := startTestSSHServer(t)
	_, err := ExecSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "wrong"}, "uptime")
	if err == nil || !strings.Contains(err.Error(), "ssh authentication") {
		t.Fatalf("ExecSSH(wrong password) = %v, want classified auth failure", err)
	}
}
