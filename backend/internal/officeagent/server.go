// Package officeagent is the narrow lifecycle agent for the Alt8Mail Office
// service. It owns one Docker Compose project and the local-only JWT secret.
// It deliberately does not expose a general-purpose shell or Docker API.
package officeagent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Runner func(ctx context.Context, timeout time.Duration, name string, args ...string) (stdout, stderr string, err error)

type Config struct {
	Listen        string
	TokenFile     string
	ComposeFile   string
	SecretFile    string
	StateFile     string
	DefaultImage  string
	ContainerName string
	HTTPPort      int
	NginxSite     string
	NginxEnabled  string
	CertRoots     []string
	DockerBin     string
	Runner        Runner
	Health        func(ctx context.Context, address string) error
}

type Server struct {
	cfg   Config
	token string
	mu    sync.Mutex
}

type state struct {
	PublicURL string `json:"publicUrl"`
	Image     string `json:"image"`
	TLSCert   string `json:"tlsCert,omitempty"`
	TLSKey    string `json:"tlsKey,omitempty"`
}

type Status struct {
	Installed         bool   `json:"installed"`
	Running           bool   `json:"running"`
	Status            string `json:"status"`
	ContainerStatus   string `json:"containerStatus"`
	Image             string `json:"image"`
	Version           string `json:"version"`
	PublicURL         string `json:"publicUrl"`
	Container         string `json:"container"`
	SecretPresent     bool   `json:"secretPresent"`
	GatewayConfigured bool   `json:"gatewayConfigured"`
	GatewayStatus     string `json:"gatewayStatus"`
	Health            string `json:"health"`
	Error             string `json:"error,omitempty"`
	CheckedAt         string `json:"checkedAt"`
}

func New(cfg Config) (*Server, error) {
	cfg.Listen = strings.TrimSpace(cfg.Listen)
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8098"
	}
	cfg.TokenFile = strings.TrimSpace(cfg.TokenFile)
	if cfg.TokenFile == "" {
		cfg.TokenFile = "/etc/a8m/office-agent-token"
	}
	cfg.ComposeFile = strings.TrimSpace(cfg.ComposeFile)
	if cfg.ComposeFile == "" {
		cfg.ComposeFile = "/etc/a8m/office/docker-compose.yml"
	}
	cfg.SecretFile = strings.TrimSpace(cfg.SecretFile)
	if cfg.SecretFile == "" {
		cfg.SecretFile = "/etc/a8m/office-jwt-secret"
	}
	cfg.StateFile = strings.TrimSpace(cfg.StateFile)
	if cfg.StateFile == "" {
		cfg.StateFile = "/var/lib/a8m-office/state.json"
	}
	cfg.DefaultImage = strings.TrimSpace(cfg.DefaultImage)
	if cfg.DefaultImage == "" {
		cfg.DefaultImage = "onlyoffice/documentserver:9.4.0.1"
	}
	cfg.ContainerName = strings.TrimSpace(cfg.ContainerName)
	if cfg.ContainerName == "" {
		cfg.ContainerName = "a8m-office"
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8082
	}
	cfg.NginxSite = strings.TrimSpace(cfg.NginxSite)
	if cfg.NginxSite == "" {
		cfg.NginxSite = "/etc/nginx/sites-available/a8m-office"
	}
	cfg.NginxEnabled = strings.TrimSpace(cfg.NginxEnabled)
	if cfg.NginxEnabled == "" {
		cfg.NginxEnabled = "/etc/nginx/sites-enabled/a8m-office"
	}
	if len(cfg.CertRoots) == 0 {
		cfg.CertRoots = []string{"/etc/letsencrypt/live", "/etc/ssl"}
	}
	cfg.DockerBin = strings.TrimSpace(cfg.DockerBin)
	if cfg.DockerBin == "" {
		cfg.DockerBin = "docker"
	}
	if cfg.Runner == nil {
		cfg.Runner = execRunner
	}
	if cfg.Health == nil {
		cfg.Health = httpHealth
	}
	token, err := ensureToken(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, token: token}, nil
}

func ensureToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		if len(token) >= 32 {
			return token, nil
		}
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := writeSecretFile(path, token); err != nil {
		return "", err
	}
	return token, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func writeSecretFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(value+"\n"), 0o640); err != nil {
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

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /status", s.auth(s.handleStatus))
	mux.HandleFunc("POST /install", s.auth(s.handleInstall))
	mux.HandleFunc("POST /start", s.auth(s.handleStart))
	mux.HandleFunc("POST /stop", s.auth(s.handleStop))
	mux.HandleFunc("POST /restart", s.auth(s.handleRestart))
	mux.HandleFunc("POST /update", s.auth(s.handleUpdate))
	mux.HandleFunc("POST /rotate-jwt", s.auth(s.handleRotateJWT))
	mux.HandleFunc("POST /gateway", s.auth(s.handleGateway))
	mux.HandleFunc("GET /logs", s.auth(s.handleLogs))
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if raw == "" || subtle.ConstantTimeCompare([]byte(raw), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid office agent credentials")
			return
		}
		next(w, r)
	}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "a8m-office-agent"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.status(r.Context())
	if err != nil {
		status.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PublicURL string `json:"publicUrl"`
		Image     string `json:"image"`
		TLSCert   string `json:"tlsCert"`
		TLSKey    string `json:"tlsKey"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", "Invalid install request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status, err := s.install(r.Context(), body.PublicURL, body.Image, body.TLSCert, body.TLSKey)
	if err != nil {
		status.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, status)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) install(ctx context.Context, publicURL, image, tlsCert, tlsKey string) (Status, error) {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if err := validPublicURL(publicURL); err != nil {
		return Status{}, err
	}
	image = strings.TrimSpace(image)
	if image == "" {
		image = s.cfg.DefaultImage
	}
	if err := validImage(image); err != nil {
		return Status{}, err
	}
	tlsCert = strings.TrimSpace(tlsCert)
	tlsKey = strings.TrimSpace(tlsKey)
	if (tlsCert == "") != (tlsKey == "") {
		return Status{}, fmt.Errorf("choose both a TLS certificate and key, or neither")
	}
	// A reinstall without new TLS fields keeps the gateway that was configured
	// previously, so changing the public URL also updates that gateway.
	previous, _ := s.loadState()
	if tlsCert == "" && tlsKey == "" {
		tlsCert, tlsKey = previous.TLSCert, previous.TLSKey
	}
	secret, err := s.ensureSecret()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.ComposeFile), 0o750); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(s.cfg.ComposeFile, []byte(composeContents(image, secret, s.cfg.ContainerName, s.cfg.HTTPPort)), 0o640); err != nil {
		return Status{}, err
	}
	st := state{PublicURL: publicURL, Image: image, TLSCert: tlsCert, TLSKey: tlsKey}
	if err := s.saveState(st); err != nil {
		return Status{}, err
	}
	if _, stderr, err := s.run(ctx, 10*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "pull"); err != nil {
		return Status{Error: commandFailure("Pulling the OnlyOffice image", err, stderr)}, fmt.Errorf("pull OnlyOffice image: %w", err)
	}
	if _, stderr, err := s.run(ctx, 5*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "up", "-d"); err != nil {
		return Status{Error: commandFailure("Starting OnlyOffice", err, stderr)}, fmt.Errorf("start OnlyOffice: %w", err)
	}
	if st.TLSCert != "" || st.TLSKey != "" {
		if err := s.configureGateway(st); err != nil {
			status, _ := s.status(ctx)
			status.Error = err.Error()
			return status, err
		}
	}
	if err := s.waitHealthy(ctx); err != nil {
		status, _ := s.status(ctx)
		status.Error = err.Error()
		return status, err
	}
	return s.status(ctx)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stderr, err := s.run(r.Context(), 2*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "up", "-d"); err != nil {
		writeError(w, http.StatusBadGateway, "START", commandFailure("Starting OnlyOffice", err, stderr))
		return
	}
	if err := s.waitHealthy(r.Context()); err != nil {
		status, _ := s.status(r.Context())
		status.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, status)
		return
	}
	status, _ := s.status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stderr, err := s.run(r.Context(), time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "stop"); err != nil {
		writeError(w, http.StatusBadGateway, "STOP", commandFailure("Stopping OnlyOffice", err, stderr))
		return
	}
	status, _ := s.status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stderr, err := s.run(r.Context(), 2*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "restart"); err != nil {
		writeError(w, http.StatusBadGateway, "RESTART", commandFailure("Restarting OnlyOffice", err, stderr))
		return
	}
	if err := s.waitHealthy(r.Context()); err != nil {
		status, _ := s.status(r.Context())
		status.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, status)
		return
	}
	status, _ := s.status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image string `json:"image"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.loadState()
	image := strings.TrimSpace(body.Image)
	if image == "" {
		image = st.Image
	}
	if image == "" {
		image = s.cfg.DefaultImage
	}
	if err := validImage(image); err != nil {
		writeError(w, http.StatusBadRequest, "IMAGE", err.Error())
		return
	}
	secret, err := s.ensureSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SECRET", err.Error())
		return
	}
	if err := os.WriteFile(s.cfg.ComposeFile, []byte(composeContents(image, secret, s.cfg.ContainerName, s.cfg.HTTPPort)), 0o640); err != nil {
		writeError(w, http.StatusInternalServerError, "COMPOSE", err.Error())
		return
	}
	st.Image = image
	_ = s.saveState(st)
	if _, stderr, err := s.run(r.Context(), 10*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "pull"); err != nil {
		writeError(w, http.StatusBadGateway, "PULL", commandFailure("Pulling the OnlyOffice image", err, stderr))
		return
	}
	if _, stderr, err := s.run(r.Context(), 5*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "up", "-d", "--force-recreate"); err != nil {
		writeError(w, http.StatusBadGateway, "UPDATE", commandFailure("Updating OnlyOffice", err, stderr))
		return
	}
	if err := s.waitHealthy(r.Context()); err != nil {
		status, _ := s.status(r.Context())
		status.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, status)
		return
	}
	status, _ := s.status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRotateJWT(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SECRET", "Could not create a JWT secret")
		return
	}
	if err := writeSecretFile(s.cfg.SecretFile, secret); err != nil {
		writeError(w, http.StatusInternalServerError, "SECRET", err.Error())
		return
	}
	st, _ := s.loadState()
	if st.Image == "" {
		st.Image = s.cfg.DefaultImage
	}
	if err := os.WriteFile(s.cfg.ComposeFile, []byte(composeContents(st.Image, secret, s.cfg.ContainerName, s.cfg.HTTPPort)), 0o640); err != nil {
		writeError(w, http.StatusInternalServerError, "COMPOSE", err.Error())
		return
	}
	if _, stderr, err := s.run(r.Context(), 3*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "up", "-d", "--force-recreate"); err != nil {
		writeError(w, http.StatusBadGateway, "ROTATE", commandFailure("Restarting OnlyOffice with the new JWT secret", err, stderr))
		return
	}
	if err := s.waitHealthy(r.Context()); err != nil {
		status, _ := s.status(r.Context())
		status.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, status)
		return
	}
	status, _ := s.status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleGateway(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PublicURL string `json:"publicUrl"`
		TLSCert   string `json:"tlsCert"`
		TLSKey    string `json:"tlsKey"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", "Invalid gateway request")
		return
	}
	if (strings.TrimSpace(body.TLSCert) == "") != (strings.TrimSpace(body.TLSKey) == "") {
		writeError(w, http.StatusBadRequest, "TLS", "Choose both a TLS certificate and key, or neither")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.loadState()
	if strings.TrimSpace(body.PublicURL) != "" {
		st.PublicURL = strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	}
	if strings.TrimSpace(body.TLSCert) != "" {
		st.TLSCert = strings.TrimSpace(body.TLSCert)
	}
	if strings.TrimSpace(body.TLSKey) != "" {
		st.TLSKey = strings.TrimSpace(body.TLSKey)
	}
	if err := s.configureGateway(st); err != nil {
		writeError(w, http.StatusBadGateway, "GATEWAY", err.Error())
		return
	}
	_ = s.saveState(st)
	status, _ := s.status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			lines = parsed
		}
	}
	out, stderr, err := s.run(r.Context(), 30*time.Second, s.cfg.DockerBin, "logs", "--tail", strconv.Itoa(lines), s.cfg.ContainerName)
	if err != nil {
		writeError(w, http.StatusBadGateway, "LOGS", commandFailure("Reading OnlyOffice logs", err, stderr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": redactLogs(out)})
}

func (s *Server) status(ctx context.Context) (Status, error) {
	st, _ := s.loadState()
	status := Status{
		Container:     s.cfg.ContainerName,
		Image:         st.Image,
		PublicURL:     st.PublicURL,
		SecretPresent: s.secretPresent(),
		GatewayStatus: s.gatewayStatus(),
		CheckedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if status.Image == "" {
		status.Image = s.cfg.DefaultImage
	}
	status.Version = imageVersion(status.Image)
	status.GatewayConfigured = status.GatewayStatus == "configured"
	status.Installed = fileExists(s.cfg.ComposeFile)
	if !status.Installed {
		status.Status = "not_installed"
		return status, nil
	}
	inspect := func(format string) (string, error) {
		out, _, err := s.run(ctx, 20*time.Second, s.cfg.DockerBin, "inspect", "--format", format, s.cfg.ContainerName)
		return strings.TrimSpace(out), err
	}
	containerStatus, err := inspect("{{.State.Status}}")
	if err != nil {
		status.Status = "stopped"
		status.ContainerStatus = "missing"
		return status, nil
	}
	status.ContainerStatus = containerStatus
	status.Running = containerStatus == "running"
	if image, err := inspect("{{.Config.Image}}"); err == nil && image != "" {
		status.Image = image
		status.Version = imageVersion(image)
	}
	if status.Running {
		status.Status = "running"
		if err := s.cfg.Health(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthcheck", s.cfg.HTTPPort)); err != nil {
			status.Health = "unhealthy"
		} else {
			status.Health = "ok"
		}
	} else {
		status.Status = "stopped"
	}
	return status, nil
}

func (s *Server) configureGateway(st state) error {
	if err := validPublicURL(st.PublicURL); err != nil {
		return err
	}
	host, err := publicHost(st.PublicURL)
	if err != nil {
		return err
	}
	if !validGatewayPath(st.TLSCert, s.cfg.CertRoots) || !validGatewayPath(st.TLSKey, s.cfg.CertRoots) {
		return fmt.Errorf("TLS certificate and key must be absolute paths under an allowed certificate root")
	}
	if !fileExists(st.TLSCert) || !fileExists(st.TLSKey) {
		return fmt.Errorf("TLS certificate or key file is missing")
	}
	config := nginxContents(host, st.TLSCert, st.TLSKey, s.cfg.HTTPPort)
	if err := os.MkdirAll(filepath.Dir(s.cfg.NginxSite), 0o755); err != nil {
		return err
	}
	previous, previousErr := os.ReadFile(s.cfg.NginxSite)
	hadPrevious := previousErr == nil
	hadEnabled := fileExists(s.cfg.NginxEnabled)
	if err := os.WriteFile(s.cfg.NginxSite, []byte(config), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.NginxEnabled), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.cfg.NginxEnabled)
	if err := os.Symlink(s.cfg.NginxSite, s.cfg.NginxEnabled); err != nil {
		return err
	}
	if _, stderr, err := s.run(context.Background(), 30*time.Second, "nginx", "-t"); err != nil {
		// Do not leave a rejected site enabled for the next nginx restart.
		if hadPrevious {
			_ = os.WriteFile(s.cfg.NginxSite, previous, 0o644)
		} else {
			_ = os.Remove(s.cfg.NginxSite)
		}
		if !hadEnabled {
			_ = os.Remove(s.cfg.NginxEnabled)
		}
		return fmt.Errorf("nginx rejected the office gateway: %s", trim(stderr))
	}
	if _, stderr, err := s.run(context.Background(), 30*time.Second, "systemctl", "reload", "nginx"); err != nil {
		return fmt.Errorf("nginx reload failed: %s", trim(stderr))
	}
	return nil
}

func (s *Server) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(180 * time.Second)
	for {
		if err := s.cfg.Health(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthcheck", s.cfg.HTTPPort)); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("OnlyOffice did not become healthy within 180 seconds")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *Server) ensureSecret() (string, error) {
	if data, err := os.ReadFile(s.cfg.SecretFile); err == nil {
		secret := strings.TrimSpace(string(data))
		if len(secret) >= 32 {
			return secret, nil
		}
	}
	secret, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := writeSecretFile(s.cfg.SecretFile, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Server) secretPresent() bool {
	data, err := os.ReadFile(s.cfg.SecretFile)
	return err == nil && len(strings.TrimSpace(string(data))) >= 32
}

func (s *Server) gatewayStatus() string {
	if fileExists(s.cfg.NginxSite) && fileExists(s.cfg.NginxEnabled) {
		return "configured"
	}
	return "not_configured"
}

func (s *Server) loadState() (state, error) {
	var st state
	data, err := os.ReadFile(s.cfg.StateFile)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func (s *Server) saveState(st state) error {
	if err := os.MkdirAll(filepath.Dir(s.cfg.StateFile), 0o750); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(s.cfg.StateFile, data, 0o640)
}

func (s *Server) run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, string, error) {
	return s.cfg.Runner(ctx, timeout, name, args...)
}

func execRunner(ctx context.Context, timeout time.Duration, name string, args ...string) (string, string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func httpHealth(ctx context.Context, address string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("healthcheck returned %d", response.StatusCode)
	}
	return nil
}

func validPublicURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("public URL must be an https origin without a path, query or fragment")
	}
	if !validHostname(strings.ToLower(parsed.Hostname())) {
		return fmt.Errorf("public URL hostname is invalid")
	}
	return nil
}

func publicHost(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || !validHostname(strings.ToLower(parsed.Hostname())) {
		return "", fmt.Errorf("public URL hostname is invalid")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func validHostname(value string) bool {
	if len(value) < 3 || len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func validImage(value string) error {
	if value == "" || len(value) > 255 {
		return fmt.Errorf("image is invalid")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._/:@-", char) {
			continue
		}
		return fmt.Errorf("image contains unsupported characters")
	}
	return nil
}

func validGatewayPath(path string, roots []string) bool {
	if !filepath.IsAbs(path) || strings.Contains(path, "..") {
		return false
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func imageVersion(image string) string {
	if i := strings.LastIndex(image, "@"); i >= 0 {
		return image[i+1:]
	}
	if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		return image[i+1:]
	}
	return "latest"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func trim(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

// commandFailure turns a failed Docker command into an operator-facing message.
// Docker's stderr is usually the useful part, but when the binary itself is
// missing stderr is empty, so fall back to the Go error and name the likely fix.
func commandFailure(action string, err error, stderr string) string {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return action + " failed: Docker Engine or the Docker Compose plugin is not installed on the Office host"
	}
	if message := trim(stderr); message != "" {
		return message
	}
	if err != nil {
		return action + " failed: " + trim(err.Error())
	}
	return action + " failed"
}

func redactLogs(value string) []string {
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "jwt_secret") || strings.Contains(lower, "authorization:") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func composeContents(image, secret, container string, port int) string {
	return fmt.Sprintf(`services:
  documentserver:
    image: %s
    container_name: %s
    restart: unless-stopped
    ports:
      - "127.0.0.1:%d:80"
    environment:
      JWT_ENABLED: "true"
      JWT_SECRET: "%s"
      JWT_HEADER: "Authorization"
      JWT_IN_BODY: "true"
    volumes:
      - /var/lib/a8m-office/data:/var/www/onlyoffice/Data
      - /var/lib/a8m-office/logs:/var/log/onlyoffice
`, image, container, port, secret)
}

func nginxContents(host, cert, key string, port int) string {
	return fmt.Sprintf(`# Managed by the Alt8Mail Office Agent.
server {
    listen 80;
    listen [::]:80;
    server_name %s;
    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://$host$request_uri; }
}
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name %s;
    server_tokens off;
    ssl_certificate %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Content-Type-Options nosniff always;
    client_max_body_size 100m;
    location / {
        proxy_pass http://127.0.0.1:%d;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
        proxy_set_header X-Forwarded-Port $server_port;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
`, host, host, cert, key, port)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
