package assetprobe

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// ExecResult is the outcome of one governed command execution.
type ExecResult struct {
	ExitCode  int
	Output    string
	Truncated bool
	Duration  time.Duration
}

// cappedBuffer collects combined stdout and stderr up to ExecOutputLimit
// bytes and discards the rest, so a runaway command cannot exhaust the
// control plane's memory or the response body.
type cappedBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := ExecOutputLimit - b.buffer.Len()
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		b.buffer.Write(p[:room])
		b.truncated = true
		return len(p), nil
	}
	b.buffer.Write(p)
	return len(p), nil
}

// ExecSSH runs one command on the asset's endpoint and returns its exit
// status and bounded combined output. ctx bounds the whole operation; the
// caller enforces MaxExecTimeout.
func ExecSSH(ctx context.Context, host string, port float64, auth SSHAuth, command string) (ExecResult, error) {
	started := time.Now()
	client, err := connectSSH(ctx, host, port, auth)
	if err != nil {
		return ExecResult{}, err
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return ExecResult{Duration: time.Since(started)}, fmt.Errorf("open session: %w", err)
	}
	defer func() { _ = session.Close() }()

	output := &cappedBuffer{}
	session.Stdout = output
	session.Stderr = output
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case <-ctx.Done():
		// Best effort: signal the remote side and reclaim local resources.
		_ = session.Signal(ssh.SIGKILL)
		return ExecResult{ExitCode: -1, Output: output.buffer.String(), Truncated: output.truncated, Duration: time.Since(started)}, fmt.Errorf("execution deadline: %w", ctx.Err())
	case runErr := <-done:
		result := ExecResult{ExitCode: 0, Output: output.buffer.String(), Truncated: output.truncated, Duration: time.Since(started)}
		if runErr != nil {
			var exitErr *ssh.ExitError
			if asExitError(runErr, &exitErr) {
				result.ExitCode = exitErr.ExitStatus()
			} else {
				result.ExitCode = -1
				return result, fmt.Errorf("run command: %w", runErr)
			}
		}
		return result, nil
	}
}

func asExitError(err error, target **ssh.ExitError) bool {
	exitErr, ok := err.(*ssh.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}
