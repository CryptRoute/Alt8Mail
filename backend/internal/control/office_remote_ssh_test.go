package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func startOfficeSSHTestServer(t *testing.T) (string, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if metadata.User() == "root" && string(password) == "test-password" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				serverConn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer serverConn.Close()
				go ssh.DiscardRequests(requests)
				for newChannel := range channels {
					if newChannel.ChannelType() == "direct-tcpip" {
						var payload struct {
							DestAddr   string
							DestPort   uint32
							OriginAddr string
							OriginPort uint32
						}
						if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
							_ = newChannel.Reject(ssh.ConnectionFailed, "invalid direct-tcpip payload")
							continue
						}
						target, err := net.Dial("tcp", net.JoinHostPort(payload.DestAddr, strconv.Itoa(int(payload.DestPort))))
						if err != nil {
							_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
							continue
						}
						channel, requests, err := newChannel.Accept()
						if err != nil {
							target.Close()
							continue
						}
						go ssh.DiscardRequests(requests)
						go func() {
							defer channel.Close()
							defer target.Close()
							go io.Copy(channel, target)
							_, _ = io.Copy(target, channel)
						}()
						continue
					}
					if newChannel.ChannelType() != "session" {
						_ = newChannel.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, channelRequests, err := newChannel.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for request := range channelRequests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							_ = ssh.Unmarshal(request.Payload, &payload)
							_ = request.Reply(true, nil)
							output := officeSSHTestOutput(payload.Command)
							if output != "" {
								_, _ = channel.Write([]byte(output + "\n"))
							}
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	return listener.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}

func officeSSHTestOutput(command string) string {
	command = strings.TrimSpace(command)
	switch {
	case command == "id -u":
		return "0"
	case command == "uname -s":
		return "Linux"
	case command == "uname -m":
		return "x86_64"
	case strings.Contains(command, "MemTotal"):
		return "4194304"
	case strings.Contains(command, "MemAvailable"):
		return "2097152"
	case strings.Contains(command, "command -v docker"):
		return "/usr/bin/docker"
	case strings.Contains(command, "docker compose version"):
		return "Docker Compose version v2.39.0"
	case strings.Contains(command, "command -v nginx"):
		return "/usr/sbin/nginx"
	case strings.Contains(command, "command -v certbot"):
		return "/usr/bin/certbot"
	case strings.Contains(command, "command -v systemctl"):
		return "/usr/bin/systemctl"
	case strings.Contains(command, "df -Pk /"):
		return "1048576"
	case command == "hostname -I":
		return "203.0.113.10"
	}
	return ""
}

func TestOfficeRemotePreflightOverSSH(t *testing.T) {
	address, fingerprint := startOfficeSSHTestServer(t)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, err = fmt.Sscanf(portText, "%d", &port)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{}}
	target := officeRemoteTarget{
		Host: host, Port: port, User: "root", Auth: "password",
		Secret: "test-password", HostKey: fingerprint,
	}
	result, err := server.officeRemotePreflight(context.Background(), target, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.HostKeyTrusted || !result.Sudo || !result.MemoryOK || !result.Docker ||
		!result.DockerCompose || !result.Nginx || !result.Certbot || !result.Systemd || !result.DiskOK {
		t.Fatalf("unexpected preflight result: %#v", result)
	}
	if result.MemoryTotalBytes != 4<<30 {
		t.Fatalf("memory = %d", result.MemoryTotalBytes)
	}
}

func TestOfficeRemoteAgentHTTPOverSSHTunnel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:8098")
	if err != nil {
		t.Skipf("loopback port 8098 is unavailable: %v", err)
	}
	agentServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("unexpected agent path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer remote-agent-token" {
			t.Errorf("unexpected agent authorization %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"a8m-office-agent"}`))
	})}
	go func() { _ = agentServer.Serve(listener) }()
	t.Cleanup(func() { _ = agentServer.Close() })

	address, fingerprint := startOfficeSSHTestServer(t)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)
	secrets, err := newSecretBox("test-control-secret-key-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	sealedPassword, err := secrets.Seal("test-password")
	if err != nil {
		t.Fatal(err)
	}
	sealedToken, err := secrets.Seal("remote-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{}, secrets: secrets}
	settings := officeSettings{
		DeploymentMode: "remote", RemoteHost: host, RemotePort: port, RemoteUser: "root",
		RemoteAuth: "password", RemoteSecret: sealedPassword, RemoteAgentToken: sealedToken,
		RemoteHostKey: fingerprint,
	}
	raw, status, err := server.officeRemoteAgentRaw(context.Background(), settings, http.MethodGet, "/healthz", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("remote agent response = %d %s", status, raw)
	}
}

func TestOfficeRemotePreflightRequiresTrustedHostKey(t *testing.T) {
	address, fingerprint := startOfficeSSHTestServer(t)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)
	server := &Server{cfg: Config{}}
	target := officeRemoteTarget{
		Host: host, Port: port, User: "root", Auth: "password",
		Secret: "test-password",
	}
	result, err := server.officeRemotePreflight(context.Background(), target, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.HostKeyTrusted || result.Ready || result.HostKeyFingerprint != fingerprint {
		t.Fatalf("untrusted preflight = %#v, want fingerprint %s", result, fingerprint)
	}
}
