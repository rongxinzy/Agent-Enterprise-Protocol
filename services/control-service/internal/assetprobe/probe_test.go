package assetprobe

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer runs an in-process SSH server on 127.0.0.1 that accepts
// exactly one password ("probe-secret") and exactly one client key, returning
// that key's PEM so tests can exercise both success and failure paths.
func startTestSSHServer(t *testing.T) (host string, port float64, clientKeyPEM []byte) {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}

	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	clientKeyPEM = pem.EncodeToMemory(block)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	config := &ssh.ServerConfig{}
	config.AddHostKey(hostSigner)
	config.PasswordCallback = func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() == "probe-user" && string(password) == "probe-secret" {
			return nil, nil
		}
		return nil, fmt.Errorf("password rejected for %q", meta.User())
	}
	config.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() == "probe-user" && bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("key rejected for %q", meta.User())
	}

	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func(connection net.Conn) {
				server, channels, requests, err := ssh.NewServerConn(connection, config)
				if err != nil {
					_ = connection.Close()
					return
				}
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if channel.ChannelType() != "session" {
						_ = channel.Reject(ssh.Prohibited, "probe only")
						continue
					}
					connection, channelRequests, err := channel.Accept()
					if err != nil {
						continue
					}
					go serveSession(connection, channelRequests)
				}
				_ = server.Close()
			}(connection)
		}
	}()

	address := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", float64(address.Port), clientKeyPEM
}

func TestProbeSSHPasswordAuth(t *testing.T) {
	host, port, _ := startTestSSHServer(t)

	result := ProbeSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "probe-secret"})
	if result.State != StateReachable {
		t.Fatalf("correct password = %#v, want reachable", result)
	}

	result = ProbeSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "probe-user", Password: "wrong"})
	if result.State != StateAuthFailed {
		t.Fatalf("wrong password = %#v, want auth_failed", result)
	}
	if strings.Contains(result.Detail, "wrong") {
		t.Fatalf("auth failure detail must not echo the submitted secret: %s", result.Detail)
	}

	result = ProbeSSH(context.Background(), host, port, SSHAuth{Method: "password", Username: "other-user", Password: "probe-secret"})
	if result.State != StateAuthFailed {
		t.Fatalf("wrong username = %#v, want auth_failed", result)
	}
}

func TestProbeSSHPrivateKeyAuth(t *testing.T) {
	host, port, clientKeyPEM := startTestSSHServer(t)

	result := ProbeSSH(context.Background(), host, port, SSHAuth{Method: "private_key", Username: "probe-user", PrivateKeyPEM: clientKeyPEM})
	if result.State != StateReachable {
		t.Fatalf("authorized key = %#v, want reachable", result)
	}

	_, unrelated, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(unrelated, "")
	if err != nil {
		t.Fatal(err)
	}
	result = ProbeSSH(context.Background(), host, port, SSHAuth{Method: "private_key", Username: "probe-user", PrivateKeyPEM: pem.EncodeToMemory(block)})
	if result.State != StateAuthFailed {
		t.Fatalf("unrelated key = %#v, want auth_failed", result)
	}

	result = ProbeSSH(context.Background(), host, port, SSHAuth{Method: "private_key", Username: "probe-user", PrivateKeyPEM: []byte("not a pem key")})
	if result.State != StateAuthFailed {
		t.Fatalf("garbage key = %#v, want auth_failed", result)
	}
	if strings.Contains(result.Detail, "not a pem key") {
		t.Fatalf("key parse detail must not echo key material: %s", result.Detail)
	}
}

func TestProbeSSHUnreachable(t *testing.T) {
	// Bind and close to get a port that is not listening.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()

	result := ProbeSSH(context.Background(), "127.0.0.1", float64(address.Port), SSHAuth{Method: "password", Username: "u", Password: "p"})
	if result.State != StateUnreachable {
		t.Fatalf("closed port = %#v, want unreachable", result)
	}

	result = ProbeSSH(context.Background(), "127.0.0.1", 0, SSHAuth{Method: "password", Username: "u", Password: "p"})
	if result.State != StateUnreachable && result.State != StateAuthFailed {
		t.Fatalf("default port fallback = %#v, want unreachable or auth_failed", result)
	}
}

func TestProbeSSHCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := ProbeSSH(ctx, "127.0.0.1", 1, SSHAuth{Method: "password", Username: "u", Password: "p"})
	if result.State != StateUnreachable {
		t.Fatalf("cancelled probe = %#v, want unreachable", result)
	}
}

// An open port that does not speak SSH is a wrong-port case: unreachable,
// not an authentication failure.
func TestProbeSSHNonSSHPortIsUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func(connection net.Conn) {
				_, _ = connection.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
				_ = connection.Close()
			}(connection)
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	result := ProbeSSH(context.Background(), "127.0.0.1", float64(address.Port), SSHAuth{Method: "password", Username: "u", Password: "p"})
	if result.State != StateUnreachable {
		t.Fatalf("non-ssh port = %#v, want unreachable", result)
	}
}

// serveSession answers exec requests with canned behaviors keyed by the
// command text: "uptime" succeeds, "fail" exits 7, "big" floods output,
// anything else echoes and exits 1.
func serveSession(connection ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = connection.Close() }()
	for request := range requests {
		if request.Type != "exec" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		command := execPayloadCommand(request.Payload)
		var output []byte
		exit := uint32(1)
		switch {
		case strings.Contains(command, "uptime"):
			output, exit = []byte("up 42 days\n"), 0
		case strings.Contains(command, "fail"):
			output, exit = []byte("boom\n"), 7
		case strings.Contains(command, "big"):
			output, exit = bytes.Repeat([]byte("x"), 200*1024), 0
		case strings.Contains(command, "sleep"):
			time.Sleep(5 * time.Second)
			output, exit = []byte("slept\n"), 0
		default:
			output, exit = []byte("echo: "+command+"\n"), 1
		}
		_, _ = connection.Write(output)
		_, _ = connection.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exit}))
		if request.WantReply {
			_ = request.Reply(true, nil)
		}
		return
	}
}

// execPayloadCommand strips the uint32 length prefix of an exec payload.
func execPayloadCommand(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	length := binary.BigEndian.Uint32(payload[:4])
	if int(length) > len(payload)-4 {
		return string(payload[4:])
	}
	return string(payload[4 : 4+length])
}
