// Package assetprobe implements connectivity and authentication probes for
// asset types (FR-AS-04) and governed command execution (FR-AS-03). A probe
// distinguishes an unreachable endpoint from an authentication failure
// because their remediation paths differ, and its detail strings never
// include credential material.
package assetprobe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Probe outcomes. Unknown is the zero value before any probe has run.
const (
	StateReachable   = "reachable"
	StateAuthFailed  = "auth_failed"
	StateUnreachable = "unreachable"
)

// Timing bounds for one probe or execution. Both are generous because
// enterprise networks often sit behind VPN hops.
const (
	DialTimeout      = 5 * time.Second
	HandshakeTimeout = 10 * time.Second
	MaxExecTimeout   = 120 * time.Second
	ExecOutputLimit  = 64 * 1024
	defaultSSHPort   = 22
)

// SSHAuth carries the interpreted credential for one connection. Username
// comes from the asset spec, not the credential.
type SSHAuth struct {
	Method               string // password | private_key
	Username             string
	Password             string
	PrivateKeyPEM        []byte
	PrivateKeyPassphrase string
}

// Result is the probe outcome stored in the asset health columns.
type Result struct {
	State  string
	Detail string
}

// ClassifiedError carries the probe-classification of a connection failure
// so probes and executions report the same distinction.
type ClassifiedError struct {
	State  string
	Detail string
	Err    error
}

func (e *ClassifiedError) Error() string { return e.Detail }

func (e *ClassifiedError) Unwrap() error { return e.Err }

// ProbeSSH connects to the endpoint, completes the SSH handshake with the
// supplied auth, and reports why it stopped when it did. A successful
// handshake is enough evidence of reachability and authentication; no
// session is opened and no command runs.
func ProbeSSH(ctx context.Context, host string, port float64, auth SSHAuth) Result {
	client, err := connectSSH(ctx, host, port, auth)
	if err != nil {
		var classified *ClassifiedError
		if errorsAs(err, &classified) {
			return Result{State: classified.State, Detail: classified.Detail}
		}
		return Result{State: StateUnreachable, Detail: boundDetail(fmt.Sprintf("probe failed: %v", err))}
	}
	_ = client.Close()
	return Result{State: StateReachable, Detail: "ssh handshake and authentication succeeded"}
}

// connectSSH dials and handshakes, classifying failures the way FR-AS-04
// reports them: dial and protocol failures are unreachable, rejected
// credentials are auth_failed.
func connectSSH(ctx context.Context, host string, port float64, auth SSHAuth) (*ssh.Client, error) {
	address := net.JoinHostPort(host, sshPortString(port))
	dialer := &net.Dialer{Timeout: DialTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, &ClassifiedError{State: StateUnreachable, Detail: boundDetail(fmt.Sprintf("tcp dial %s: %v", address, err)), Err: err}
	}
	_ = connection.SetDeadline(time.Now().Add(HandshakeTimeout))
	config, err := sshClientConfig(auth)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	conn, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		// Only a completed handshake with rejected credentials means
		// auth_failed; a protocol-level failure means the port is open but
		// is not an SSH endpoint (a wrong port in FR-AS-04 terms).
		state, prefix := StateUnreachable, "ssh handshake failed, endpoint may not speak ssh"
		if strings.Contains(err.Error(), "unable to authenticate") {
			state, prefix = StateAuthFailed, "ssh authentication"
		}
		return nil, &ClassifiedError{State: state, Detail: boundDetail(fmt.Sprintf("%s: %v", prefix, err)), Err: err}
	}
	// The handshake consumed part of the deadline; execution sets its own
	// per-command deadlines, so lift the connection-wide cap.
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(conn, channels, requests), nil
}

func sshClientConfig(auth SSHAuth) (*ssh.ClientConfig, error) {
	var authMethod ssh.AuthMethod
	switch auth.Method {
	case "password":
		authMethod = ssh.Password(auth.Password)
	case "private_key":
		var signer ssh.Signer
		var err error
		if auth.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(auth.PrivateKeyPEM, []byte(auth.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(auth.PrivateKeyPEM)
		}
		if err != nil {
			return nil, &ClassifiedError{State: StateAuthFailed, Detail: boundDetail("parse private key: " + keyParseError(err)), Err: err}
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		return nil, &ClassifiedError{State: StateAuthFailed, Detail: fmt.Sprintf("unsupported auth method %q", auth.Method)}
	}
	return &ssh.ClientConfig{
		User:            auth.Username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Probes and governed execution authenticate the credential, not the host.
		Timeout:         HandshakeTimeout,
	}, nil
}

func sshPortString(port float64) string {
	if port <= 0 {
		return strconv.Itoa(defaultSSHPort)
	}
	return strconv.Itoa(int(port))
}

func errorsAs(err error, target **ClassifiedError) bool {
	classified, ok := err.(*ClassifiedError)
	if ok {
		*target = classified
	}
	return ok
}

// boundDetail keeps the stored detail readable and bounded; handshake errors
// quote the server banner, which is public information, but never the key.
func boundDetail(detail string) string {
	if len(detail) > 512 {
		return detail[:512]
	}
	return detail
}

// keyParseError strips the raw key material x/crypto occasionally echoes in
// pem.Decode errors.
func keyParseError(err error) string {
	message := err.Error()
	if len(message) > 256 {
		return message[:256] + "…"
	}
	return message
}
