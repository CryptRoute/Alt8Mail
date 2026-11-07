package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type officeSettings struct {
	ID              string     `json:"id"`
	Enabled         bool       `json:"enabled"`
	DeploymentMode  string     `json:"deploymentMode"`
	PublicURL       string     `json:"publicUrl"`
	Status          string     `json:"status"`
	ContainerStatus string     `json:"containerStatus"`
	Version         string     `json:"version"`
	Image           string     `json:"image"`
	GatewayStatus   string     `json:"gatewayStatus"`
	GatewayHost     string     `json:"gatewayHost"`
	LastError       string     `json:"lastError"`
	LastHealthAt    *time.Time `json:"lastHealthAt"`
}

type officeAgentStatus struct {
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
	Error             string `json:"error"`
	CheckedAt         string `json:"checkedAt"`
}

type officeCertificate struct {
	Name       string `json:"name"`
	CertPath   string `json:"certPath"`
	KeyPath    string `json:"keyPath"`
	NotAfter   string `json:"notAfter"`
	SelfSigned bool   `json:"selfSigned"`
}

func (s *Server) registerOfficeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/office", s.require("server.read", s.handleOfficeGet))
	mux.HandleFunc("PATCH /control/api/office", s.require("server.manage", s.handleOfficePatch))
	mux.HandleFunc("POST /control/api/office/install", s.require("server.manage", s.handleOfficeInstall))
	mux.HandleFunc("POST /control/api/office/start", s.require("server.manage", s.handleOfficeAction("start")))
	mux.HandleFunc("POST /control/api/office/stop", s.require("server.manage", s.handleOfficeAction("stop")))
	mux.HandleFunc("POST /control/api/office/restart", s.require("server.manage", s.handleOfficeAction("restart")))
	mux.HandleFunc("POST /control/api/office/update", s.require("server.manage", s.handleOfficeAction("update")))
	mux.HandleFunc("POST /control/api/office/rotate-jwt", s.require("server.manage", s.handleOfficeAction("rotate-jwt")))
	mux.HandleFunc("POST /control/api/office/gateway", s.require("server.manage", s.handleOfficeGateway))
	mux.HandleFunc("GET /control/api/office/logs", s.require("server.read", s.handleOfficeLogs))
}

func (s *Server) officeSettings(ctx context.Context) (officeSettings, error) {
	var settings officeSettings
	err := s.db.QueryRow(ctx, `
		SELECT id, enabled, deployment_mode, public_url, status, container_status, version,
		       image, gateway_status, gateway_host, last_error, last_health_at
		FROM public.office_settings WHERE id = 'default'
	`).Scan(&settings.ID, &settings.Enabled, &settings.DeploymentMode, &settings.PublicURL,
		&settings.Status, &settings.ContainerStatus, &settings.Version, &settings.Image,
		&settings.GatewayStatus, &settings.GatewayHost, &settings.LastError, &settings.LastHealthAt)
	return settings, err
}

func (s *Server) handleOfficeGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	settings, err := s.officeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not load office settings")
		return
	}
	agent, agentErr := s.officeAgentStatus(r.Context())
	if agentErr == nil {
		s.persistOfficeStatus(r.Context(), agent)
		if refreshed, err := s.officeSettings(r.Context()); err == nil {
			settings = refreshed
		}
	}
	certificates := []officeCertificate{}
	rows, err := s.db.Query(r.Context(), `
		SELECT name, cert_path, key_path, to_char(not_after, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), self_signed
		FROM control.ssl_certificates ORDER BY name
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cert officeCertificate
			if rows.Scan(&cert.Name, &cert.CertPath, &cert.KeyPath, &cert.NotAfter, &cert.SelfSigned) == nil {
				certificates = append(certificates, cert)
			}
		}
	}
	agentError := ""
	if agentErr != nil {
		agentError = agentErr.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings, "agent": agent, "agentError": agentError,
		"certificates": certificates, "defaultImage": s.cfg.OfficeImage,
	})
}

func (s *Server) handleOfficePatch(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Enabled   *bool   `json:"enabled"`
		PublicURL *string `json:"publicUrl"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Enabled == nil && body.PublicURL == nil {
		writeError(w, http.StatusBadRequest, "OFFICE", "Choose an office setting to change")
		return
	}
	settings, err := s.officeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not load office settings")
		return
	}
	publicURL := settings.PublicURL
	if body.PublicURL != nil {
		publicURL = strings.TrimRight(strings.TrimSpace(*body.PublicURL), "/")
		if err := validOfficeURL(publicURL); err != nil {
			writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
			return
		}
	}
	enabled := settings.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if enabled {
		agent, err := s.officeAgentStatus(r.Context())
		if err != nil {
			writeError(w, http.StatusConflict, "OFFICE_AGENT", "The Office agent is not reachable; install and start Office first")
			return
		}
		if !agent.Running || agent.Health != "ok" || !agent.SecretPresent {
			writeError(w, http.StatusConflict, "OFFICE_NOT_READY", "Office must be running, healthy and have a JWT secret before it can be enabled")
			return
		}
		if publicURL == "" {
			writeError(w, http.StatusBadRequest, "OFFICE_URL", "Set the public Office URL before enabling Office")
			return
		}
		// Record this health check now so webmail sees a fresh timestamp even
		// when Office was enabled through the API and the Control page was not
		// reopened afterward.
		s.persistOfficeStatus(r.Context(), agent)
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE public.office_settings
		SET enabled = $1, public_url = $2, updated_at = now()
		WHERE id = 'default'
	`, enabled, publicURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not update office settings")
		return
	}
	s.actorAudit(r, sess, "office.policy", "office", fmt.Sprintf("Office %s for webmail", map[bool]string{true: "enabled", false: "disabled"}[enabled]), map[string]any{"enabled": enabled, "publicUrl": publicURL})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "publicUrl": publicURL})
}

func (s *Server) handleOfficeInstall(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		PublicURL string `json:"publicUrl"`
		Image     string `json:"image"`
		TLSCert   string `json:"tlsCert"`
		TLSKey    string `json:"tlsKey"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.PublicURL = strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	body.Image = strings.TrimSpace(body.Image)
	body.TLSCert = strings.TrimSpace(body.TLSCert)
	body.TLSKey = strings.TrimSpace(body.TLSKey)
	if err := validOfficeURL(body.PublicURL); err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
		return
	}
	if (body.TLSCert == "") != (body.TLSKey == "") {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose both a TLS certificate and key, or neither")
		return
	}
	agent, err := s.officeAgentRequest(r.Context(), http.MethodPost, "/install", body, 15*time.Minute)
	if err != nil {
		s.persistOfficeStatus(r.Context(), agent)
		writeError(w, http.StatusBadGateway, "OFFICE_INSTALL", err.Error())
		return
	}
	s.persistOfficeStatus(r.Context(), agent)
	_, _ = s.db.Exec(r.Context(), `
		UPDATE public.office_settings SET public_url=$1, image=$2, updated_at=now() WHERE id='default'
	`, body.PublicURL, firstNonEmpty(body.Image, s.cfg.OfficeImage))
	host := ""
	if parsed, parseErr := url.Parse(body.PublicURL); parseErr == nil {
		host = parsed.Hostname()
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE public.office_settings SET gateway_host=$1 WHERE id='default'`, host)
	s.actorAudit(r, sess, "office.install", "office", "Installed or reconfigured OnlyOffice", map[string]any{"publicUrl": body.PublicURL, "image": body.Image})
	settings, _ := s.officeSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "agent": agent})
}

func (s *Server) handleOfficeAction(action string) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
		var body map[string]any
		if action == "update" {
			var update struct {
				Image string `json:"image"`
			}
			if err := decodeJSON(w, r, &update, 16<<10); err != nil {
				writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
				return
			}
			body = map[string]any{"image": update.Image}
		}
		agent, err := s.officeAgentRequest(r.Context(), http.MethodPost, "/"+action, body, 15*time.Minute)
		if err != nil {
			s.persistOfficeStatus(r.Context(), agent)
			writeError(w, http.StatusBadGateway, "OFFICE_ACTION", err.Error())
			return
		}
		s.persistOfficeStatus(r.Context(), agent)
		s.actorAudit(r, sess, "office."+action, "office", "Office action: "+action, map[string]any{"status": agent.Status})
		writeJSON(w, http.StatusOK, map[string]any{"agent": agent})
	}
}

func (s *Server) handleOfficeGateway(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		PublicURL string `json:"publicUrl"`
		TLSCert   string `json:"tlsCert"`
		TLSKey    string `json:"tlsKey"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.PublicURL = strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	body.TLSCert = strings.TrimSpace(body.TLSCert)
	body.TLSKey = strings.TrimSpace(body.TLSKey)
	if err := validOfficeURL(body.PublicURL); err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
		return
	}
	if (body.TLSCert == "") != (body.TLSKey == "") {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose both a TLS certificate and key, or neither")
		return
	}
	agent, err := s.officeAgentRequest(r.Context(), http.MethodPost, "/gateway", body, 2*time.Minute)
	if err != nil {
		s.persistOfficeStatus(r.Context(), agent)
		writeError(w, http.StatusBadGateway, "OFFICE_GATEWAY", err.Error())
		return
	}
	s.persistOfficeStatus(r.Context(), agent)
	host := ""
	if parsed, parseErr := url.Parse(body.PublicURL); parseErr == nil {
		host = parsed.Hostname()
	}
	_, _ = s.db.Exec(r.Context(), `
		UPDATE public.office_settings SET public_url=$1, gateway_host=$2, updated_at=now() WHERE id='default'
	`, body.PublicURL, host)
	s.actorAudit(r, sess, "office.gateway", "office", "Configured the Office gateway", map[string]any{"host": host})
	settings, _ := s.officeSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "agent": agent})
}

func (s *Server) handleOfficeLogs(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	lines := strings.TrimSpace(r.URL.Query().Get("lines"))
	if lines == "" {
		lines = "120"
	}
	result, status, err := s.officeAgentRaw(r.Context(), http.MethodGet, "/logs?lines="+url.QueryEscape(lines), nil, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, "OFFICE_LOGS", err.Error())
		return
	}
	if status < 200 || status >= 300 {
		writeError(w, http.StatusBadGateway, "OFFICE_LOGS", "The Office agent could not read logs")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func (s *Server) officeAgentStatus(ctx context.Context) (officeAgentStatus, error) {
	return s.officeAgentRequest(ctx, http.MethodGet, "/status", nil, 15*time.Second)
}

// officeAgentQuickStatus is for the background readiness monitor: the agent is
// on loopback, so waiting the page-load timeout is unnecessary.
func (s *Server) officeAgentQuickStatus(ctx context.Context) (officeAgentStatus, error) {
	return s.officeAgentRequest(ctx, http.MethodGet, "/status", nil, 5*time.Second)
}

func (s *Server) officeAgentRequest(ctx context.Context, method, path string, body any, timeout time.Duration) (officeAgentStatus, error) {
	raw, status, err := s.officeAgentRaw(ctx, method, path, body, timeout)
	var agent officeAgentStatus
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &agent)
	}
	if err != nil {
		if agent.Error != "" {
			return agent, fmt.Errorf("%s", agent.Error)
		}
		return agent, err
	}
	if status < 200 || status >= 300 {
		if agent.Error != "" {
			return agent, fmt.Errorf("%s", agent.Error)
		}
		return agent, fmt.Errorf("office agent returned HTTP %d", status)
	}
	if agent.Error != "" {
		return agent, fmt.Errorf("%s", agent.Error)
	}
	return agent, nil
}

func (s *Server) officeAgentRaw(ctx context.Context, method, path string, body any, timeout time.Duration) ([]byte, int, error) {
	tokenBytes, err := os.ReadFile(s.cfg.OfficeAgentToken)
	if err != nil {
		return nil, 0, fmt.Errorf("office agent token is unavailable: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return nil, 0, fmt.Errorf("office agent token is empty")
	}
	var reader io.Reader
	if body != nil {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.cfg.OfficeAgentURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("office agent is not reachable: %w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	return raw, response.StatusCode, nil
}

func (s *Server) persistOfficeStatus(ctx context.Context, agent officeAgentStatus) {
	if agent.Status == "" && agent.Error == "" {
		return
	}
	status := agent.Status
	if status == "" {
		status = "error"
	}
	lastHealth := any(nil)
	if agent.Health == "ok" {
		lastHealth = time.Now().UTC()
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE public.office_settings SET
			status=$1, container_status=$2, version=$3, image=$4,
			gateway_status=$5, last_error=$6, last_health_at=COALESCE($7, last_health_at),
			updated_at=now()
		WHERE id='default'
	`, status, agent.ContainerStatus, agent.Version, agent.Image, agent.GatewayStatus, agent.Error, lastHealth)
}

func validOfficeURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Office URL must be an https origin without a path, query or fragment")
	}
	if !strings.Contains(parsed.Hostname(), ".") {
		return fmt.Errorf("Office URL must use a public hostname")
	}
	return nil
}
