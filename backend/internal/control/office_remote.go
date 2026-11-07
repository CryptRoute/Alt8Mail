package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type officeRemoteTarget struct {
	Host       string
	Port       int
	User       string
	Auth       string
	Secret     string
	Passphrase string
	HostKey    string
	PublicIP   string
	AgentPath  string
}

type officeRemoteRequest struct {
	PublicURL           string `json:"publicUrl"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	User                string `json:"user"`
	Auth                string `json:"auth"`
	Secret              string `json:"secret"`
	Passphrase          string `json:"passphrase"`
	HostKey             string `json:"hostKey"`
	PublicIP            string `json:"publicIp"`
	ClearHostKey        bool   `json:"clearHostKey"`
	InstallDependencies *bool  `json:"installDependencies"`
}

type officeRemoteHostKey struct {
	Fingerprint string
	Type        string
}

type officeRemotePreflight struct {
	Host                 string   `json:"host"`
	HostKeyFingerprint   string   `json:"hostKeyFingerprint"`
	HostKeyType          string   `json:"hostKeyType"`
	HostKeyTrusted       bool     `json:"hostKeyTrusted"`
	MemoryTotalBytes     uint64   `json:"memoryTotalBytes"`
	MemoryUsedBytes      uint64   `json:"memoryUsedBytes"`
	MemoryPercent        float64  `json:"memoryPercent"`
	MemoryOK             bool     `json:"memoryOk"`
	MemoryDetail         string   `json:"memoryDetail"`
	Docker               bool     `json:"docker"`
	DockerCompose        bool     `json:"dockerCompose"`
	Nginx                bool     `json:"nginx"`
	Certbot              bool     `json:"certbot"`
	Systemd              bool     `json:"systemd"`
	Architecture         string   `json:"architecture"`
	Sudo                 bool     `json:"sudo"`
	DiskFreeBytes        uint64   `json:"diskFreeBytes"`
	DiskOK               bool     `json:"diskOk"`
	DNSOK                bool     `json:"dnsOk"`
	Resolved             []string `json:"resolved"`
	Expected             []string `json:"expected"`
	DNSDetail            string   `json:"dnsDetail"`
	PackageManager       string   `json:"packageManager"`
	MissingPackages      []string `json:"missingPackages"`
	InstallDependencies  bool     `json:"installDependencies"`
	AutoInstallSupported bool     `json:"autoInstallSupported"`
	Blockers             []string `json:"blockers"`
	Detail               string   `json:"detail"`
	Ready                bool     `json:"ready"`
}

type officeHostKeyError struct {
	fingerprint string
	keyType     string
	mismatch    bool
}

func (e *officeHostKeyError) Error() string {
	if e.mismatch {
		return "the remote SSH host key does not match the pinned fingerprint"
	}
	return "confirm the remote SSH host key fingerprint"
}

func (s *Server) officeRemoteTargetFromSettings(settings officeSettings) (officeRemoteTarget, error) {
	target := officeRemoteTarget{
		Host: settings.RemoteHost, Port: settings.RemotePort, User: settings.RemoteUser,
		Auth: settings.RemoteAuth, HostKey: settings.RemoteHostKey, PublicIP: settings.RemotePublicIP,
		AgentPath: settings.RemoteAgentPath,
	}
	var err error
	if strings.TrimSpace(settings.RemoteSecret) != "" {
		target.Secret, err = s.secrets.Open(settings.RemoteSecret)
		if err != nil {
			return target, fmt.Errorf("could not decrypt the remote SSH credential")
		}
	}
	if strings.TrimSpace(settings.RemotePassphrase) != "" {
		target.Passphrase, err = s.secrets.Open(settings.RemotePassphrase)
		if err != nil {
			return target, fmt.Errorf("could not decrypt the remote SSH passphrase")
		}
	}
	return target, nil
}

func (s *Server) dialOfficeRemote(ctx context.Context, target officeRemoteTarget) (*ssh.Client, officeRemoteHostKey, error) {
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return nil, officeRemoteHostKey{}, fmt.Errorf("enter the remote Office host")
	}
	host = strings.Trim(host, "[]")
	if strings.ContainsAny(host, " \t\r\n/") {
		return nil, officeRemoteHostKey{}, fmt.Errorf("the remote SSH host is invalid")
	}
	port := target.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, officeRemoteHostKey{}, fmt.Errorf("choose a valid SSH port")
	}
	user := strings.TrimSpace(target.User)
	if user == "" || !systemAccountPattern.MatchString(user) {
		return nil, officeRemoteHostKey{}, fmt.Errorf("enter a valid SSH system account")
	}
	var auth ssh.AuthMethod
	switch strings.ToLower(strings.TrimSpace(target.Auth)) {
	case "password":
		if target.Secret == "" {
			return nil, officeRemoteHostKey{}, fmt.Errorf("enter the SSH password")
		}
		auth = ssh.Password(target.Secret)
	case "key", "":
		signer, err := parseTerminalKey(target.Secret, target.Passphrase)
		if err != nil {
			return nil, officeRemoteHostKey{}, err
		}
		auth = ssh.PublicKeys(signer)
	default:
		return nil, officeRemoteHostKey{}, fmt.Errorf("SSH authentication must be password or key")
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 12 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, officeRemoteHostKey{}, fmt.Errorf("SSH connection to %s failed: %w", addr, err)
	}
	var observed officeRemoteHostKey
	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		observed = officeRemoteHostKey{Fingerprint: ssh.FingerprintSHA256(key), Type: key.Type()}
		pinned := strings.TrimSpace(target.HostKey)
		if pinned == "" {
			return &officeHostKeyError{fingerprint: observed.Fingerprint, keyType: observed.Type}
		}
		if !strings.EqualFold(pinned, observed.Fingerprint) {
			return &officeHostKeyError{fingerprint: observed.Fingerprint, keyType: observed.Type, mismatch: true}
		}
		return nil
	}
	config := &ssh.ClientConfig{
		User: user, Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: hostKeyCallback, Timeout: 12 * time.Second,
		ClientVersion: "SSH-2.0-Alt8Mail-Control",
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		var hostKeyErr *officeHostKeyError
		if errors.As(err, &hostKeyErr) {
			return nil, observed, hostKeyErr
		}
		return nil, observed, fmt.Errorf("SSH authentication failed: %w", err)
	}
	return ssh.NewClient(sshConn, chans, reqs), observed, nil
}

func remotePrivilege(ctx context.Context, client *ssh.Client) (root bool, canSudo bool) {
	if output, err := runRemote(ctx, client, "id -u", 10*time.Second, false); err == nil && strings.TrimSpace(output) == "0" {
		root = true
		return root, false
	}
	if _, err := runRemote(ctx, client, "sudo -n true", 10*time.Second, false); err == nil {
		canSudo = true
	}
	return root, canSudo
}

func remoteQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func runRemote(ctx context.Context, client *ssh.Client, command string, timeout time.Duration, sudo bool) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("could not open an SSH session: %w", err)
	}
	defer session.Close()
	if sudo {
		command = "sudo -n sh -c " + remoteQuote(command)
	}
	type outcome struct {
		output string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- outcome{output: string(output), err: runErr}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	case <-timer.C:
		_ = session.Close()
		return "", fmt.Errorf("remote command timed out: %s", command)
	case result := <-done:
		return strings.TrimSpace(result.output), result.err
	}
}

func uploadRemoteFile(ctx context.Context, client *ssh.Client, remotePath string, data []byte, mode string, sudo bool) error {
	tempPath := "/tmp/a8m-office-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("could not open an upload session: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return fmt.Errorf("could not open the upload stream: %w", err)
	}
	if err := session.Start("umask 077; cat > " + remoteQuote(tempPath)); err != nil {
		session.Close()
		return fmt.Errorf("could not start the upload: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		if _, writeErr := stdin.Write(data); writeErr != nil {
			_ = stdin.Close()
			done <- fmt.Errorf("could not upload the file: %w", writeErr)
			return
		}
		_ = stdin.Close()
		done <- session.Wait()
	}()
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		session.Close()
		return ctx.Err()
	case <-timer.C:
		session.Close()
		return fmt.Errorf("timed out uploading %s", remotePath)
	case err := <-done:
		session.Close()
		if err != nil {
			return fmt.Errorf("could not finish the upload: %w", err)
		}
	}
	installCommand := fmt.Sprintf("install -m %s %s %s && rm -f %s", remoteQuote(mode), remoteQuote(tempPath), remoteQuote(remotePath), remoteQuote(tempPath))
	if _, err := runRemote(ctx, client, installCommand, 60*time.Second, sudo); err != nil {
		return fmt.Errorf("could not install %s: %w", remotePath, err)
	}
	return nil
}

func remoteMemoryBytes(output string) (uint64, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return 0, fmt.Errorf("empty memory output")
	}
	kilobytes, err := strconv.ParseUint(output, 10, 64)
	if err != nil {
		return 0, err
	}
	return kilobytes * 1024, nil
}

func mergeOfficeRemoteTarget(target officeRemoteTarget, body officeRemoteRequest, settings officeSettings) officeRemoteTarget {
	if target.Host == "" {
		target.Host = settings.RemoteHost
	}
	if target.Port == 0 {
		target.Port = settings.RemotePort
	}
	if target.User == "" {
		target.User = settings.RemoteUser
	}
	if target.Auth == "" {
		target.Auth = settings.RemoteAuth
	}
	if target.HostKey == "" && !body.ClearHostKey {
		target.HostKey = settings.RemoteHostKey
	}
	if target.PublicIP == "" {
		target.PublicIP = settings.RemotePublicIP
	}
	if target.AgentPath == "" {
		target.AgentPath = strings.TrimSpace(settings.RemoteAgentPath)
	}
	if target.AgentPath == "" {
		target.AgentPath = "/opt/a8m-office/a8m-office-agent"
	}
	return target
}

func (s *Server) officeRemoteTargetForRequest(ctx context.Context, body officeRemoteRequest) (officeRemoteTarget, officeSettings, error) {
	settings, err := s.officeSettings(ctx)
	if err != nil {
		return officeRemoteTarget{}, settings, err
	}
	target := mergeOfficeRemoteTarget(officeRemoteTarget{
		Host: body.Host, Port: body.Port, User: body.User, Auth: body.Auth,
		Secret: body.Secret, Passphrase: body.Passphrase, HostKey: body.HostKey,
		PublicIP: body.PublicIP, AgentPath: settings.RemoteAgentPath,
	}, body, settings)
	if target.Secret == "" && strings.TrimSpace(settings.RemoteSecret) != "" {
		target.Secret, err = s.secrets.Open(settings.RemoteSecret)
		if err != nil {
			return target, settings, fmt.Errorf("could not decrypt the saved SSH credential")
		}
	}
	if target.Passphrase == "" && strings.TrimSpace(settings.RemotePassphrase) != "" {
		target.Passphrase, err = s.secrets.Open(settings.RemotePassphrase)
		if err != nil {
			return target, settings, fmt.Errorf("could not decrypt the saved SSH passphrase")
		}
	}
	return target, settings, nil
}

func (s *Server) handleOfficeRemotePreflight(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body officeRemoteRequest
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	target, settings, err := s.officeRemoteTargetForRequest(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE", err.Error())
		return
	}
	publicURL := strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	if publicURL == "" {
		publicURL = settings.PublicURL
	}
	if publicURL != "" {
		if err := validOfficeURL(publicURL); err != nil {
			writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
			return
		}
	}
	installDependencies := true
	if body.InstallDependencies != nil {
		installDependencies = *body.InstallDependencies
	}
	result, err := s.officeRemotePreflight(r.Context(), target, publicURL, installDependencies)
	if err != nil {
		var hostKeyErr *officeHostKeyError
		if errors.As(err, &hostKeyErr) && hostKeyErr.mismatch {
			writeError(w, http.StatusConflict, "SSH_HOST_KEY", "SSH host key mismatch. Observed "+hostKeyErr.fingerprint+". Clear the pinned fingerprint to review the new key.")
			return
		}
		writeError(w, http.StatusBadGateway, "SSH_PREFLIGHT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func remotePrepareCommand() string {
	return `set -eu
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
if ! command -v apt-get >/dev/null 2>&1; then
  echo "This host is not Debian/Ubuntu; install Docker Engine, the Compose plugin, nginx and certbot manually." >&2
  exit 1
fi
apt_opts="-o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
packages=""
if ! command -v docker >/dev/null 2>&1; then
  packages="$packages docker.io"
fi
if ! docker compose version >/dev/null 2>&1; then
  if apt-cache show docker-compose-v2 >/dev/null 2>&1; then
    packages="$packages docker-compose-v2"
  elif apt-cache show docker-compose-plugin >/dev/null 2>&1; then
    packages="$packages docker-compose-plugin"
  else
    echo "No Docker Compose v2 package is available in apt." >&2
    exit 1
  fi
fi
if ! command -v nginx >/dev/null 2>&1; then
  packages="$packages nginx"
fi
if ! command -v certbot >/dev/null 2>&1; then
  packages="$packages certbot"
fi
if [ -n "$packages" ]; then
  dpkg --configure -a >/dev/null 2>&1 || true
  tries=0
  until apt-get $apt_opts update; do
    tries=$((tries + 1))
    if [ "$tries" -ge 3 ]; then
      echo "apt-get update failed after three attempts" >&2
      exit 1
    fi
    sleep 5
  done
  apt-get $apt_opts install -y --no-install-recommends $packages
fi
if command -v docker >/dev/null 2>&1; then
  systemctl enable --now docker >/dev/null 2>&1 || true
  tries=0
  until docker info >/dev/null 2>&1; do
    tries=$((tries + 1))
    if [ "$tries" -ge 30 ]; then
      echo "Docker daemon did not become ready" >&2
      exit 1
    fi
    sleep 2
  done
fi
if command -v nginx >/dev/null 2>&1; then
  systemctl enable --now nginx >/dev/null 2>&1 || true
  nginx -t >/dev/null
  systemctl is-active --quiet nginx || {
    echo "nginx is installed but not running" >&2
    exit 1
  }
fi
command -v certbot >/dev/null 2>&1
docker compose version >/dev/null 2>&1
`
}

func missingRemotePackages(docker, compose, nginx, certbot bool) []string {
	missing := []string{}
	if !docker {
		missing = append(missing, "Docker Engine")
	}
	if !compose {
		missing = append(missing, "Docker Compose plugin")
	}
	if !nginx {
		missing = append(missing, "nginx")
	}
	if !certbot {
		missing = append(missing, "certbot")
	}
	return missing
}

func remotePackagesReady(result officeRemotePreflight, installDependencies bool) bool {
	packagesOK := result.Docker && result.DockerCompose && result.Nginx && result.Certbot
	canInstallMissing := len(result.MissingPackages) > 0 && installDependencies && result.AutoInstallSupported
	return packagesOK || canInstallMissing
}

func prepareOfficeRemote(ctx context.Context, client *ssh.Client) (string, error) {
	root, canSudo := remotePrivilege(ctx, client)
	if !root && !canSudo {
		return "", fmt.Errorf("the SSH account needs root or passwordless sudo")
	}
	output, err := runRemote(ctx, client, remotePrepareCommand(), 20*time.Minute, !root)
	if err != nil {
		return output, fmt.Errorf("could not install remote prerequisites: %s", remoteCommandDetail(output))
	}
	return output, nil
}

func remoteCommandDetail(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return "the remote command failed"
	}
	lines := strings.Split(output, "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, " | ")
}

func (s *Server) handleOfficeRemotePrepare(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body officeRemoteRequest
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	target, settings, err := s.officeRemoteTargetForRequest(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE", err.Error())
		return
	}
	if strings.TrimSpace(target.HostKey) == "" {
		writeError(w, http.StatusConflict, "SSH_HOST_KEY", "Trust the SSH host key before preparing the remote host")
		return
	}
	client, _, err := s.dialOfficeRemote(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusBadGateway, "SSH_PREPARE", err.Error())
		return
	}
	defer client.Close()
	if _, prepareErr := prepareOfficeRemote(r.Context(), client); prepareErr != nil {
		writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_PREPARE", prepareErr.Error())
		return
	}
	publicURL := strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	if publicURL == "" {
		publicURL = settings.PublicURL
	}
	preflight, preflightErr := s.officeRemotePreflight(r.Context(), target, publicURL, false)
	if preflightErr != nil {
		writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_PREFLIGHT", "Prerequisites were installed, but verification failed: "+preflightErr.Error())
		return
	}
	s.actorAudit(r, sess, "office.remote.prepare", "office", "Installed remote Office prerequisites", map[string]any{
		"remoteHost": target.Host, "remoteUser": target.User,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preflight": preflight})
}

func (s *Server) officeRemotePreflight(ctx context.Context, target officeRemoteTarget, publicURL string, installDependencies bool) (officeRemotePreflight, error) {
	result := officeRemotePreflight{
		Resolved: []string{}, Expected: []string{}, Blockers: []string{}, MissingPackages: []string{},
		InstallDependencies: installDependencies,
	}
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return result, fmt.Errorf("enter the remote Office host")
	}
	result.Host = host
	client, hostKey, err := s.dialOfficeRemote(ctx, target)
	if err != nil {
		var hostKeyErr *officeHostKeyError
		if errors.As(err, &hostKeyErr) {
			result.HostKeyFingerprint = hostKeyErr.fingerprint
			result.HostKeyType = hostKeyErr.keyType
			if hostKeyErr.mismatch {
				return result, hostKeyErr
			}
			result.Detail = "The SSH host key is not pinned yet. Confirm the fingerprint before running remote checks."
			return result, nil
		}
		return result, err
	}
	defer client.Close()
	result.HostKeyFingerprint = hostKey.Fingerprint
	result.HostKeyType = hostKey.Type
	result.HostKeyTrusted = true

	root, canSudo := remotePrivilege(ctx, client)
	elevate := !root && canSudo
	result.Sudo = root || canSudo
	if !result.Sudo {
		result.Blockers = append(result.Blockers, "SSH user must be root or have passwordless sudo")
	}
	read := func(command string, timeout time.Duration) string {
		output, err := runRemote(ctx, client, command, timeout, elevate)
		if err != nil {
			return ""
		}
		return output
	}
	uname := read("uname -s", 10*time.Second)
	if !strings.EqualFold(strings.TrimSpace(uname), "Linux") {
		result.Blockers = append(result.Blockers, "The remote host must be Linux")
	}
	result.Architecture = strings.ToLower(strings.TrimSpace(read("uname -m", 10*time.Second)))
	remoteArch := result.Architecture
	archOK := (runtime.GOARCH == "amd64" && (remoteArch == "x86_64" || remoteArch == "amd64")) ||
		(runtime.GOARCH == "arm64" && (remoteArch == "aarch64" || remoteArch == "arm64"))
	if !archOK {
		result.Blockers = append(result.Blockers, "Remote CPU architecture "+firstNonEmpty(remoteArch, "unknown")+" does not match this Control Plane build ("+runtime.GOARCH+")")
	}
	memoryKB := read("awk '/MemTotal/ {print $2}' /proc/meminfo", 10*time.Second)
	if total, err := remoteMemoryBytes(memoryKB); err == nil {
		result.MemoryTotalBytes = total
		result.MemoryOK = total >= 2<<30
		result.MemoryPercent = 0
		if availableKB := read("awk '/MemAvailable/ {print $2}' /proc/meminfo", 10*time.Second); availableKB != "" {
			if available, parseErr := strconv.ParseUint(availableKB, 10, 64); parseErr == nil && total >= available*1024 {
				result.MemoryUsedBytes = total - available*1024
				result.MemoryPercent = float64(result.MemoryUsedBytes) / float64(total) * 100
			}
		}
		result.MemoryDetail = "Remote host memory is sufficient"
		if !result.MemoryOK {
			result.MemoryDetail = "OnlyOffice needs at least 2 GB; 4 GB or more is recommended"
			result.Blockers = append(result.Blockers, "Remote host has less than 2 GB RAM")
		}
	} else {
		result.MemoryDetail = "Could not read remote host memory"
		result.Blockers = append(result.Blockers, "Could not read remote host memory")
	}
	result.Docker = read("command -v docker", 10*time.Second) != ""
	result.DockerCompose = result.Docker && read("docker compose version", 20*time.Second) != ""
	result.Nginx = read("command -v nginx", 10*time.Second) != ""
	result.Certbot = read("command -v certbot", 10*time.Second) != ""
	result.Systemd = read("command -v systemctl", 10*time.Second) != ""
	switch {
	case read("command -v apt-get", 10*time.Second) != "":
		result.PackageManager = "apt"
	case read("command -v dnf", 10*time.Second) != "":
		result.PackageManager = "dnf"
	case read("command -v yum", 10*time.Second) != "":
		result.PackageManager = "yum"
	}
	result.AutoInstallSupported = result.PackageManager == "apt"
	result.MissingPackages = missingRemotePackages(result.Docker, result.DockerCompose, result.Nginx, result.Certbot)
	canInstallMissing := len(result.MissingPackages) > 0 && installDependencies && result.AutoInstallSupported
	if len(result.MissingPackages) > 0 && !canInstallMissing {
		if !result.Docker {
			result.Blockers = append(result.Blockers, "Docker Engine is not installed on the remote host")
		} else if !result.DockerCompose {
			result.Blockers = append(result.Blockers, "The Docker Compose plugin is not installed on the remote host")
		}
		if !result.Nginx {
			result.Blockers = append(result.Blockers, "nginx is not installed on the remote host")
		}
		if !result.Certbot {
			result.Blockers = append(result.Blockers, "certbot is not installed on the remote host")
		}
		if installDependencies && !result.AutoInstallSupported {
			result.Blockers = append(result.Blockers, "Automatic dependency installation requires apt-get on a Debian/Ubuntu host")
		}
	}
	if !result.Systemd {
		result.Blockers = append(result.Blockers, "systemd is not available on the remote host")
	}
	if freeKB := read("df -Pk / | awk 'NR==2 {print $4}'", 10*time.Second); freeKB != "" {
		if value, parseErr := strconv.ParseUint(freeKB, 10, 64); parseErr == nil {
			result.DiskFreeBytes = value * 1024
			result.DiskOK = result.DiskFreeBytes >= 1<<30
			if !result.DiskOK {
				result.Blockers = append(result.Blockers, "Less than 1 GB free disk on the remote host")
			}
		}
	}
	remoteIPs := []string{}
	if addresses := read("hostname -I", 10*time.Second); addresses != "" {
		for _, raw := range strings.Fields(addresses) {
			if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
				remoteIPs = append(remoteIPs, ip.String())
			}
		}
	}
	expected := map[string]bool{}
	for _, value := range remoteIPs {
		expected[value] = true
	}
	if ip := net.ParseIP(strings.TrimSpace(target.PublicIP)); ip != nil {
		expected[ip.String()] = true
	}
	for value := range expected {
		result.Expected = append(result.Expected, value)
	}
	sortStrings(result.Expected)
	if parsed, err := urlParseHost(publicURL); err == nil && parsed != "" {
		resolver := s.dnsResolver(ctx)
		lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		hosts, lookupErr := resolver.LookupHost(lookupCtx, parsed)
		cancel()
		seen := map[string]bool{}
		for _, raw := range hosts {
			if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
				value := ip.String()
				if !seen[value] {
					seen[value] = true
					result.Resolved = append(result.Resolved, value)
				}
			}
		}
		sortStrings(result.Resolved)
		if lookupErr != nil {
			result.DNSDetail = "DNS lookup failed: " + lookupErr.Error()
		} else if len(result.Resolved) == 0 {
			result.DNSDetail = "No A or AAAA record is published for " + parsed
		} else {
			for _, address := range result.Resolved {
				if expected[address] {
					result.DNSOK = true
					break
				}
			}
			if result.DNSOK {
				result.DNSDetail = "DNS points at the remote Office host"
			} else {
				result.DNSDetail = "DNS resolves to " + strings.Join(result.Resolved, ", ") + ", not the remote host (" + strings.Join(result.Expected, ", ") + ")"
			}
		}
	} else {
		result.DNSDetail = "Enter a valid public Office URL"
	}
	if !result.DNSOK {
		result.Blockers = append(result.Blockers, result.DNSDetail)
	}
	if len(result.Blockers) == 0 {
		if len(result.MissingPackages) > 0 {
			result.Detail = "Remote host is ready; missing dependencies will be installed during the install"
		} else {
			result.Detail = "Remote host is ready for the Office agent"
		}
	} else {
		result.Detail = strings.Join(result.Blockers, " · ")
	}
	result.Ready = result.HostKeyTrusted && result.Sudo && result.MemoryOK && result.Systemd &&
		archOK && result.DiskOK && result.DNSOK && remotePackagesReady(result, installDependencies)
	return result, nil
}

func urlParseHost(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid URL")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (s *Server) officeRemoteAgentRaw(ctx context.Context, settings officeSettings, method, path string, body any, timeout time.Duration) ([]byte, int, error) {
	target, err := s.officeRemoteTargetFromSettings(settings)
	if err != nil {
		return nil, 0, err
	}
	token, err := s.secrets.Open(settings.RemoteAgentToken)
	if err != nil || strings.TrimSpace(token) == "" {
		return nil, 0, fmt.Errorf("the remote Office agent token is unavailable")
	}
	client, _, err := s.dialOfficeRemote(ctx, target)
	if err != nil {
		return nil, 0, err
	}
	defer client.Close()
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return client.Dial("tcp", "127.0.0.1:8098")
		},
	}
	defer transport.CloseIdleConnections()
	var reader io.Reader
	if body != nil {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:8098"+path, reader)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := &http.Client{Transport: transport, Timeout: timeout}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("remote Office agent is not reachable: %w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	return raw, response.StatusCode, nil
}

func (s *Server) officeAgentBinaryPath() (string, error) {
	executable, _ := os.Executable()
	candidates := []string{
		filepath.Join(filepath.Dir(executable), "a8m-office-agent"),
		"/opt/a8m/current/bin/a8m-office-agent",
		"/usr/local/bin/a8m-office-agent",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("a8m-office-agent binary was not found next to the control plane binary")
}

func remoteOfficeAgentEnv(image string) string {
	return fmt.Sprintf(`OFFICE_AGENT_LISTEN=127.0.0.1:8098
OFFICE_AGENT_TOKEN_FILE=/etc/a8m/office-agent-token
OFFICE_AGENT_COMPOSE_FILE=/etc/a8m/office/docker-compose.yml
OFFICE_AGENT_SECRET_FILE=/etc/a8m/office-jwt-secret
OFFICE_AGENT_STATE_FILE=/var/lib/a8m-office/state.json
OFFICE_AGENT_IMAGE=%s
OFFICE_AGENT_CONTAINER=a8m-office
OFFICE_AGENT_HTTP_PORT=8082
OFFICE_AGENT_NGINX_SITE=/etc/nginx/sites-available/a8m-office
OFFICE_AGENT_NGINX_ENABLED=/etc/nginx/sites-enabled/a8m-office
OFFICE_AGENT_CERTBOT=certbot
OFFICE_AGENT_CERTBOT_DIR=/etc/letsencrypt/live
OFFICE_AGENT_CERTBOT_WEBROOT=/var/www/certbot
OFFICE_AGENT_FONTS_DIR=/var/lib/a8m-office/fonts
`, image)
}

const remoteOfficeAgentUnit = `[Unit]
Description=Alt8Mail Office lifecycle agent
Documentation=https://github.com/CryptRoute/Alt8Mail
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=/opt/a8m-office
EnvironmentFile=-/etc/a8m/office-agent.env
ExecStart=/opt/a8m-office/a8m-office-agent
Restart=on-failure
RestartSec=3
TimeoutStopSec=20
KillSignal=SIGTERM
UMask=0027
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=/etc/a8m /etc/nginx -/etc/letsencrypt -/var/lib/letsencrypt -/var/log/letsencrypt -/var/lib/a8m-office -/var/www/certbot

[Install]
WantedBy=multi-user.target
`

func readRemoteJWTSecret(ctx context.Context, client *ssh.Client) (string, error) {
	root, canSudo := remotePrivilege(ctx, client)
	if !root && !canSudo {
		return "", fmt.Errorf("the SSH user needs root or passwordless sudo")
	}
	output, err := runRemote(ctx, client, "cat /etc/a8m/office-jwt-secret", 15*time.Second, !root)
	if err != nil {
		return "", fmt.Errorf("could not read the remote Office JWT secret: %w", err)
	}
	secret := strings.TrimSpace(output)
	if len(secret) < 32 {
		return "", fmt.Errorf("the remote Office JWT secret is missing or too short")
	}
	return secret, nil
}

func (s *Server) writeLocalOfficeJWTSecret(secret string) error {
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return fmt.Errorf("refusing to write an empty Office JWT secret")
	}
	path := strings.TrimSpace(s.cfg.OfficeSecretFile)
	if path == "" {
		return fmt.Errorf("CONTROL_OFFICE_JWT_SECRET_FILE is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(secret+"\n"), 0o640); err != nil {
		return err
	}
	if group, err := user.LookupGroup("a8m"); err == nil {
		if gid, convErr := strconv.Atoi(group.Gid); convErr == nil {
			_ = os.Chown(temp, 0, gid)
		}
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func remoteAgentHealth(ctx context.Context, client *ssh.Client) error {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return client.Dial("tcp", "127.0.0.1:8098")
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8098/healthz", nil)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("remote agent health returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *Server) bootstrapRemoteAgent(ctx context.Context, client *ssh.Client, image string) (string, string, error) {
	root, canSudo := remotePrivilege(ctx, client)
	if !root && !canSudo {
		return "", "", fmt.Errorf("the SSH user needs root or passwordless sudo")
	}
	elevate := !root
	if _, err := runRemote(ctx, client,
		"install -d -m 0755 /opt/a8m-office /etc/a8m /var/lib/a8m-office /var/www/certbot "+
			"/etc/letsencrypt /var/lib/letsencrypt /var/log/letsencrypt "+
			"/etc/nginx/sites-available /etc/nginx/sites-enabled", 60*time.Second, elevate); err != nil {
		return "", "", fmt.Errorf("could not prepare remote Office directories: %w", err)
	}
	binaryPath, err := s.officeAgentBinaryPath()
	if err != nil {
		return "", "", err
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", "", fmt.Errorf("could not read the Office agent binary: %w", err)
	}
	remoteBinary := "/opt/a8m-office/a8m-office-agent"
	if err := uploadRemoteFile(ctx, client, remoteBinary, binary, "0755", elevate); err != nil {
		return "", "", err
	}
	token, err := randomToken(48)
	if err != nil {
		return "", "", err
	}
	sealedToken, err := s.secrets.Seal(token)
	if err != nil {
		return "", "", err
	}
	if err := uploadRemoteFile(ctx, client, "/etc/a8m/office-agent-token", []byte(token+"\n"), "0600", elevate); err != nil {
		return "", "", err
	}
	if err := uploadRemoteFile(ctx, client, "/etc/a8m/office-agent.env", []byte(remoteOfficeAgentEnv(image)), "0640", elevate); err != nil {
		return "", "", err
	}
	if err := uploadRemoteFile(ctx, client, "/etc/systemd/system/a8m-office-agent.service", []byte(remoteOfficeAgentUnit), "0644", elevate); err != nil {
		return "", "", err
	}
	if _, err := runRemote(ctx, client, "systemctl daemon-reload && systemctl enable a8m-office-agent && systemctl restart a8m-office-agent", 90*time.Second, elevate); err != nil {
		return "", "", fmt.Errorf("could not start the remote Office agent: %w", err)
	}
	// Give systemd and the agent a moment to bind loopback before the first
	// authenticated request.
	deadline := time.Now().Add(20 * time.Second)
	for {
		output, healthErr := runRemote(ctx, client, "systemctl is-active a8m-office-agent", 10*time.Second, elevate)
		if healthErr == nil && strings.TrimSpace(output) == "active" && remoteAgentHealth(ctx, client) == nil {
			break
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("the remote Office agent did not become active and healthy")
		}
		time.Sleep(time.Second)
	}
	return sealedToken, remoteBinary, nil
}
