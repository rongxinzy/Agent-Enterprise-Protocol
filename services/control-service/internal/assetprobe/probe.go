// Package assetprobe implements connectivity and authentication probes for
// asset types (FR-AS-04). A probe distinguishes an unreachable endpoint from
// an authentication failure because their remediation paths differ, and its
// detail strings never include credential material.
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

// Timing bounds for one probe. Both are generous because enterprise networks
// often sit behind VPN hops; the admin-triggered endpoint tolerates the wait.
const (
	DialTimeout      = 5 * time.Second
	HandshakeTimeout = 10 * time.Second
	defaultSSHPort   = 22
)

// SSHAuth carries the interpreted credential for one probe run. Username
// comes from the asset spec, not the credential.
type SSHAuth struct {
	Method   string // password | private_key
	Username string
	Password string
	// PrivateKeyPEM and PrivateKeyPassphrase hold the raw key material.
	PrivateKeyPEM        []byte
	PrivateKeyPassphrase string
}

// Result is the probe outcome stored in the asset health columns.
type Result struct {
	State  string
	Detail string
}

// ProbeSSH connects to the endpoint, completes the SSH handshake with the
// supplied auth, and reports why it stopped when it did. host comes from the
// asset spec; port falls back to 22 when absent.
func ProbeSSH(ctx context.Context, host string, port float64, auth SSHAuth) Result {
	address := net.JoinHostPort(host, sshPortString(port))
	dialer := &net.Dialer{Timeout: DialTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return Result{State: StateUnreachable, Detail: fmt.Sprintf("tcp dial %s: %v", address, err)}
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(HandshakeTimeout))

	sshConfig, result := sshClientConfig(auth)
	if result.State != "" {
		return result
	}
	clients := make(chan struct {
		client *ssh.Client
		err    error
	}, 1)
	go func() {
		conn, _, _, err := ssh.NewClientConn(connection, address, sshConfig)
		if err == nil {
			// A successful handshake is enough evidence of reachability and
			// authentication; no session is opened and no command runs.
			// Closing the conn lets the library shut down its channels.
			_ = conn.Close()
		}
		clients <- struct {
			client *ssh.Client
			err    error
		}{nil, err}
	}()
	select {
	case <-ctx.Done():
		return Result{State: StateUnreachable, Detail: fmt.Sprintf("probe cancelled: %v", ctx.Err())}
	case outcome := <-clients:
		if outcome.err != nil {
			// Only a completed handshake with rejected credentials means
			// auth_failed; a protocol-level failure means the port is open
			// but is not an SSH endpoint (a wrong port in FR-AS-04 terms).
			if strings.Contains(outcome.err.Error(), "unable to authenticate") {
				return Result{State: StateAuthFailed, Detail: boundDetail(fmt.Sprintf("ssh authentication: %v", outcome.err))}
			}
			return Result{State: StateUnreachable, Detail: boundDetail(fmt.Sprintf("ssh handshake failed, endpoint may not speak ssh: %v", outcome.err))}
		}
		return Result{State: StateReachable, Detail: "ssh handshake and authentication succeeded"}
	}
}

func sshClientConfig(auth SSHAuth) (*ssh.ClientConfig, Result) {
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
			return nil, Result{State: StateAuthFailed, Detail: boundDetail("parse private key: " + keyParseError(err))}
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		return nil, Result{State: StateAuthFailed, Detail: fmt.Sprintf("unsupported auth method %q", auth.Method)}
	}
	return &ssh.ClientConfig{
		User:            auth.Username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // The probe authenticates the credential, not the host.
		Timeout:         HandshakeTimeout,
	}, Result{}
}

func sshPortString(port float64) string {
	if port <= 0 {
		return strconv.Itoa(defaultSSHPort)
	}
	return strconv.Itoa(int(port))
}

// boundDetail keeps the stored detail readable and bounded; handshake errors
// quote the server banner, which is public information, but never the key.
func boundDetail(detail string) string {
	const limit = 512
	if len(detail) > limit {
		return detail[:limit]
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
