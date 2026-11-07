package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type officeSettings struct {
	ID                  string     `json:"id"`
	Enabled             bool       `json:"enabled"`
	DeploymentMode      string     `json:"deploymentMode"`
	PublicURL           string     `json:"publicUrl"`
	Status              string     `json:"status"`
	ContainerStatus     string     `json:"containerStatus"`
	Version             string     `json:"version"`
	Image               string     `json:"image"`
	GatewayStatus       string     `json:"gatewayStatus"`
	GatewayHost         string     `json:"gatewayHost"`
	LastError           string     `json:"lastError"`
	LastHealthAt        *time.Time `json:"lastHealthAt"`
	RemoteHost          string     `json:"remoteHost"`
	RemotePort          int        `json:"remotePort"`
	RemoteUser          string     `json:"remoteUser"`
	RemoteAuth          string     `json:"remoteAuth"`
	RemoteSecret        string     `json:"-"`
	RemotePassphrase    string     `json:"-"`
	RemoteHostKey       string     `json:"remoteHostKey"`
	RemotePublicIP      string     `json:"remotePublicIp"`
	RemoteAgentToken    string     `json:"-"`
	RemoteAgentPath     string     `json:"remoteAgentPath"`
	RemoteHasSecret     bool       `json:"remoteHasSecret"`
	RemoteHasAgentToken bool       `json:"remoteHasAgentToken"`
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
	Name       string   `json:"name"`
	CertPath   string   `json:"certPath"`
	KeyPath    string   `json:"keyPath"`
	SANs       []string `json:"sans"`
	NotAfter   string   `json:"notAfter"`
	SelfSigned bool     `json:"selfSigned"`
}

func (s *Server) registerOfficeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/office", s.require("server.read", s.handleOfficeGet))
	mux.HandleFunc("GET /control/api/office/preflight", s.require("server.read", s.handleOfficePreflight))
	mux.HandleFunc("POST /control/api/office/remote/preflight", s.require("server.manage", s.handleOfficeRemotePreflight))
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
		       image, gateway_status, gateway_host, last_error, last_health_at,
		       COALESCE(remote_host,''), COALESCE(remote_port,22), COALESCE(remote_user,'root'),
		       COALESCE(remote_auth,'key'), COALESCE(remote_secret,''), COALESCE(remote_passphrase,''),
		       COALESCE(remote_host_key,''), COALESCE(remote_public_ip,''), COALESCE(remote_agent_token,''),
		       COALESCE(remote_agent_path,'')
		FROM public.office_settings WHERE id = 'default'
	`).Scan(&settings.ID, &settings.Enabled, &settings.DeploymentMode, &settings.PublicURL,
		&settings.Status, &settings.ContainerStatus, &settings.Version, &settings.Image,
		&settings.GatewayStatus, &settings.GatewayHost, &settings.LastError, &settings.LastHealthAt,
		&settings.RemoteHost, &settings.RemotePort, &settings.RemoteUser, &settings.RemoteAuth,
		&settings.RemoteSecret, &settings.RemotePassphrase, &settings.RemoteHostKey,
		&settings.RemotePublicIP, &settings.RemoteAgentToken, &settings.RemoteAgentPath)
	settings.RemoteHasSecret = strings.TrimSpace(settings.RemoteSecret) != ""
	settings.RemoteHasAgentToken = strings.TrimSpace(settings.RemoteAgentToken) != ""
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
		SELECT name, cert_path, key_path, sans, to_char(not_after, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), self_signed
		FROM control.ssl_certificates ORDER BY name
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cert officeCertificate
			if rows.Scan(&cert.Name, &cert.CertPath, &cert.KeyPath, &cert.SANs, &cert.NotAfter, &cert.SelfSigned) == nil {
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
		"host":     s.probe.hostMetrics(r.Context(), false),
		"tlsEmail": s.settingString(r.Context(), "ssl.admin_email", ""),
	})
}

type officePreflightResult struct {
	PublicURL     string   `json:"publicUrl"`
	Hostname      string   `json:"hostname"`
	DNSOK         bool     `json:"dnsOk"`
	Resolved      []string `json:"resolved"`
	Expected      []string `json:"expected"`
	DNSDetail     string   `json:"dnsDetail"`
	MemoryTotal   uint64   `json:"memoryTotalBytes"`
	MemoryUsed    uint64   `json:"memoryUsedBytes"`
	MemoryPercent float64  `json:"memoryPercent"`
	MemoryOK      bool     `json:"memoryOk"`
	MemoryDetail  string   `json:"memoryDetail"`
	Ready         bool     `json:"ready"`
}

func (s *Server) officePreflight(ctx context.Context, publicURL string) officePreflightResult {
	result := officePreflightResult{PublicURL: publicURL, Resolved: []string{}, Expected: []string{}}
	parsed, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || parsed.Hostname() == "" {
		result.DNSDetail = "Enter a valid Office URL first"
	} else {
		result.Hostname = strings.ToLower(parsed.Hostname())
	}
	expected := map[string]bool{}
	if addresses, addressErr := s.webNetLocalIPs(ctx); addressErr == nil {
		for _, address := range addresses {
			if address.Address != "" {
				expected[address.Address] = true
			}
		}
	}
	for _, key := range []string{"server.public_ipv4", "server.public_ipv6"} {
		if ip := net.ParseIP(strings.TrimSpace(s.settingString(ctx, key, ""))); ip != nil {
			expected[ip.String()] = true
		}
	}
	for address := range expected {
		result.Expected = append(result.Expected, address)
	}
	sort.Strings(result.Expected)
	if result.Hostname == "" {
		// validation message already set
	} else if len(result.Expected) == 0 {
		result.DNSDetail = "No local public address is known; set the server public IPv4/IPv6 under Server settings"
	} else {
		resolver := s.dnsResolver(ctx)
		lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		hosts, lookupErr := resolver.LookupHost(lookupCtx, result.Hostname)
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
		sort.Strings(result.Resolved)
		if lookupErr != nil {
			result.DNSDetail = "DNS lookup failed: " + lookupErr.Error()
		} else if len(result.Resolved) == 0 {
			result.DNSDetail = "No A or AAAA record is published for " + result.Hostname
		} else {
			for _, address := range result.Resolved {
				if expected[address] {
					result.DNSOK = true
					break
				}
			}
			if result.DNSOK {
				result.DNSDetail = "DNS points at this server"
			} else {
				result.DNSDetail = "DNS resolves to " + strings.Join(result.Resolved, ", ") + ", not this server (" + strings.Join(result.Expected, ", ") + ")"
			}
		}
	}
	host := s.probe.hostMetrics(ctx, false)
	result.MemoryTotal = host.MemoryTotalBytes
	result.MemoryUsed = host.MemoryUsedBytes
	result.MemoryPercent = host.MemoryPercent
	result.MemoryOK = host.MemoryTotalBytes >= 2<<30
	if host.MemoryTotalBytes == 0 {
		result.MemoryDetail = "Could not read host memory"
	} else if result.MemoryOK {
		result.MemoryDetail = "Enough memory for the local OnlyOffice container"
	} else {
		result.MemoryDetail = "OnlyOffice needs at least 2 GB; 4 GB or more is recommended"
	}
	result.Ready = result.DNSOK && result.MemoryOK
	return result
}

func (s *Server) handleOfficePreflight(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	publicURL := strings.TrimRight(strings.TrimSpace(r.URL.Query().Get("publicUrl")), "/")
	if publicURL == "" {
		publicURL = ""
	}
	if publicURL != "" {
		if err := validOfficeURL(publicURL); err != nil {
			writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, s.officePreflight(r.Context(), publicURL))
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
		if !agent.GatewayConfigured {
			writeError(w, http.StatusConflict, "OFFICE_GATEWAY", "Configure the public gateway and TLS certificate before enabling Office")
			return
		}
		if publicURL == "" {
			writeError(w, http.StatusBadRequest, "OFFICE_URL", "Set the public Office URL before enabling Office")
			return
		}
		if settings.DeploymentMode == "remote" {
			target, targetErr := s.officeRemoteTargetFromSettings(settings)
			if targetErr != nil {
				writeError(w, http.StatusConflict, "OFFICE_REMOTE", targetErr.Error())
				return
			}
			preflight, preflightErr := s.officeRemotePreflight(r.Context(), target, publicURL)
			if preflightErr != nil || !preflight.Ready {
				detail := "The remote Office host is not ready"
				if preflightErr != nil {
					detail = preflightErr.Error()
				} else if preflight.Detail != "" {
					detail = preflight.Detail
				}
				writeError(w, http.StatusConflict, "OFFICE_REMOTE_PREFLIGHT", detail)
				return
			}
		} else {
			preflight := s.officePreflight(r.Context(), publicURL)
			if !preflight.DNSOK {
				writeError(w, http.StatusConflict, "OFFICE_DNS", preflight.DNSDetail)
				return
			}
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
		PublicURL      string              `json:"publicUrl"`
		Image          string              `json:"image"`
		TLSMode        string              `json:"tlsMode"`
		TLSEmail       string              `json:"tlsEmail"`
		TLSCert        string              `json:"tlsCert"`
		TLSKey         string              `json:"tlsKey"`
		DeploymentMode string              `json:"deploymentMode"`
		Remote         officeRemoteRequest `json:"remote"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.PublicURL = strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	body.Image = strings.TrimSpace(body.Image)
	body.TLSCert = strings.TrimSpace(body.TLSCert)
	body.TLSKey = strings.TrimSpace(body.TLSKey)
	body.TLSMode = strings.ToLower(strings.TrimSpace(body.TLSMode))
	body.TLSEmail = strings.TrimSpace(body.TLSEmail)
	body.DeploymentMode = strings.ToLower(strings.TrimSpace(body.DeploymentMode))
	if body.DeploymentMode == "" {
		body.DeploymentMode = "local"
	}
	if body.TLSMode == "" {
		body.TLSMode = "keep"
	}
	image := firstNonEmpty(body.Image, s.cfg.OfficeImage)
	if err := validOfficeImage(image); err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_IMAGE", err.Error())
		return
	}
	if err := validOfficeURL(body.PublicURL); err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
		return
	}
	if (body.TLSCert == "") != (body.TLSKey == "") {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose both a TLS certificate and key, or neither")
		return
	}
	if body.TLSMode != "keep" && body.TLSMode != "none" && body.TLSMode != "auto" && body.TLSMode != "existing" {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose a supported TLS mode")
		return
	}
	if body.TLSMode == "existing" && (body.TLSCert == "" || body.TLSKey == "") {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose an existing certificate and key")
		return
	}
	if body.TLSMode == "auto" {
		if body.TLSEmail == "" {
			body.TLSEmail = s.settingString(r.Context(), "ssl.admin_email", "")
		}
		if body.TLSEmail == "" {
			writeError(w, http.StatusBadRequest, "OFFICE_TLS_EMAIL", "Enter a Let's Encrypt account email")
			return
		}
		body.TLSCert, body.TLSKey = "", ""
	}
	if body.TLSMode == "existing" {
		parsed, _ := url.Parse(body.PublicURL)
		cert, certErr := s.officeCertificateForPaths(r.Context(), body.TLSCert, body.TLSKey)
		if certErr != nil {
			writeError(w, http.StatusBadRequest, "OFFICE_TLS", "The selected certificate is not available in System Control TLS")
			return
		}
		if !officeCertificateCovers(cert, parsed.Hostname()) {
			writeError(w, http.StatusBadRequest, "OFFICE_TLS_NAME", "The selected certificate does not cover "+parsed.Hostname()+"; use automatic Let's Encrypt or choose a matching certificate")
			return
		}
	}
	if body.DeploymentMode != "local" && body.DeploymentMode != "remote" {
		writeError(w, http.StatusBadRequest, "OFFICE_DEPLOYMENT", "Choose local or remote deployment")
		return
	}
	if body.DeploymentMode == "remote" && body.TLSMode == "existing" {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Existing certificates are local to this server. Use automatic Let's Encrypt or install without a public gateway on a remote host")
		return
	}
	var remoteTarget officeRemoteTarget
	var previousSettings officeSettings
	if body.DeploymentMode == "remote" {
		body.Remote.PublicURL = body.PublicURL
		var targetErr error
		remoteTarget, previousSettings, targetErr = s.officeRemoteTargetForRequest(r.Context(), body.Remote)
		if targetErr != nil {
			writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE", targetErr.Error())
			return
		}
		preflight, preflightErr := s.officeRemotePreflight(r.Context(), remoteTarget, body.PublicURL)
		if preflightErr != nil {
			writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_PREFLIGHT", preflightErr.Error())
			return
		}
		if !preflight.Ready {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":     map[string]any{"code": "OFFICE_REMOTE_PREFLIGHT", "message": preflight.Detail},
				"preflight": preflight,
			})
			return
		}
		if strings.TrimSpace(remoteTarget.HostKey) == "" {
			remoteTarget.HostKey = preflight.HostKeyFingerprint
		}
	} else {
		preflight := s.officePreflight(r.Context(), body.PublicURL)
		if !preflight.DNSOK {
			writeError(w, http.StatusConflict, "OFFICE_DNS", preflight.DNSDetail)
			return
		}
		if !preflight.MemoryOK {
			writeError(w, http.StatusConflict, "OFFICE_MEMORY", preflight.MemoryDetail)
			return
		}
	}
	if body.DeploymentMode == "remote" {
		remoteSecret := previousSettings.RemoteSecret
		if body.Remote.Secret != "" {
			sealed, sealErr := s.secrets.Seal(body.Remote.Secret)
			if sealErr != nil {
				writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE_SECRET", "Could not protect the SSH credential")
				return
			}
			remoteSecret = sealed
		}
		remotePassphrase := previousSettings.RemotePassphrase
		if body.Remote.Passphrase != "" {
			sealed, sealErr := s.secrets.Seal(body.Remote.Passphrase)
			if sealErr != nil {
				writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE_SECRET", "Could not protect the SSH passphrase")
				return
			}
			remotePassphrase = sealed
		}
		client, _, dialErr := s.dialOfficeRemote(r.Context(), remoteTarget)
		if dialErr != nil {
			writeError(w, http.StatusBadGateway, "OFFICE_REMOTE", dialErr.Error())
			return
		}
		agentToken, agentPath, bootstrapErr := s.bootstrapRemoteAgent(r.Context(), client, image)
		client.Close()
		if bootstrapErr != nil {
			writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_AGENT", bootstrapErr.Error())
			return
		}
		if _, dbErr := s.db.Exec(r.Context(), `
			UPDATE public.office_settings SET
				deployment_mode='remote', remote_host=$1, remote_port=$2, remote_user=$3,
				remote_auth=$4, remote_secret=$5, remote_passphrase=$6, remote_host_key=$7,
				remote_public_ip=$8, remote_agent_token=$9, remote_agent_path=$10,
				public_url=$11, image=$12, updated_at=now()
			WHERE id='default'
		`, remoteTarget.Host, remoteTarget.Port, remoteTarget.User, remoteTarget.Auth,
			remoteSecret, remotePassphrase, remoteTarget.HostKey, remoteTarget.PublicIP,
			agentToken, agentPath, body.PublicURL, image); dbErr != nil {
			writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE", "Could not save the remote Office deployment target")
			return
		}
	}
	if body.DeploymentMode == "local" {
		// Switch the agent transport before the request so a previous remote
		// installation cannot capture this local reconfigure.
		_, _ = s.db.Exec(r.Context(), `UPDATE public.office_settings SET deployment_mode='local', updated_at=now() WHERE id='default'`)
	}
	agentBody := map[string]any{
		"publicUrl": body.PublicURL, "image": image,
		"tlsCert": body.TLSCert, "tlsKey": body.TLSKey,
		"autoTls": body.TLSMode == "auto", "tlsEmail": body.TLSEmail,
		"disableGateway": body.TLSMode == "none",
	}
	agent, err := s.officeAgentRequest(r.Context(), http.MethodPost, "/install", agentBody, 30*time.Minute)
	if err != nil {
		s.persistOfficeStatus(r.Context(), agent)
		writeError(w, http.StatusBadGateway, "OFFICE_INSTALL", err.Error())
		return
	}
	s.persistOfficeStatus(r.Context(), agent)
	if body.DeploymentMode == "remote" {
		client, _, syncDialErr := s.dialOfficeRemote(r.Context(), remoteTarget)
		if syncDialErr != nil {
			writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_SECRET", "Office installed but the JWT secret could not be synchronized: "+syncDialErr.Error())
			return
		}
		secret, secretErr := readRemoteJWTSecret(r.Context(), client)
		client.Close()
		if secretErr != nil {
			writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_SECRET", "Office installed but the JWT secret could not be synchronized: "+secretErr.Error())
			return
		}
		if writeErr := s.writeLocalOfficeJWTSecret(secret); writeErr != nil {
			writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE_SECRET", "Office installed but the local JWT secret could not be written: "+writeErr.Error())
			return
		}
	}
	if body.DeploymentMode == "local" {
		_, _ = s.scanCertificates(r.Context())
		_, _ = s.db.Exec(r.Context(), `
			UPDATE public.office_settings SET public_url=$1, image=$2, deployment_mode='local', updated_at=now() WHERE id='default'
		`, body.PublicURL, image)
	} else {
		_, _ = s.db.Exec(r.Context(), `
			UPDATE public.office_settings SET public_url=$1, image=$2, updated_at=now() WHERE id='default'
		`, body.PublicURL, image)
	}
	host := ""
	if parsed, parseErr := url.Parse(body.PublicURL); parseErr == nil {
		host = parsed.Hostname()
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE public.office_settings SET gateway_host=$1 WHERE id='default'`, host)
	auditDetails := map[string]any{"publicUrl": body.PublicURL, "image": image, "deploymentMode": body.DeploymentMode}
	if body.DeploymentMode == "remote" {
		auditDetails["remoteHost"] = remoteTarget.Host
		auditDetails["remoteUser"] = remoteTarget.User
	}
	s.actorAudit(r, sess, "office.install", "office", "Installed or reconfigured OnlyOffice", auditDetails)
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
		if action == "rotate-jwt" {
			if settings, settingsErr := s.officeSettings(r.Context()); settingsErr == nil && settings.DeploymentMode == "remote" {
				target, targetErr := s.officeRemoteTargetFromSettings(settings)
				if targetErr != nil {
					writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_SECRET", "Office rotated the remote JWT secret but it could not be loaded: "+targetErr.Error())
					return
				}
				client, _, dialErr := s.dialOfficeRemote(r.Context(), target)
				if dialErr != nil {
					writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_SECRET", "Office rotated the remote JWT secret but it could not be synchronized: "+dialErr.Error())
					return
				}
				secret, secretErr := readRemoteJWTSecret(r.Context(), client)
				client.Close()
				if secretErr != nil {
					writeError(w, http.StatusBadGateway, "OFFICE_REMOTE_SECRET", "Office rotated the remote JWT secret but it could not be synchronized: "+secretErr.Error())
					return
				}
				if writeErr := s.writeLocalOfficeJWTSecret(secret); writeErr != nil {
					writeError(w, http.StatusInternalServerError, "OFFICE_REMOTE_SECRET", "Could not write the synchronized Office JWT secret: "+writeErr.Error())
					return
				}
			}
		}
		s.actorAudit(r, sess, "office."+action, "office", "Office action: "+action, map[string]any{"status": agent.Status})
		writeJSON(w, http.StatusOK, map[string]any{"agent": agent})
	}
}

func (s *Server) handleOfficeGateway(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		PublicURL string `json:"publicUrl"`
		TLSMode   string `json:"tlsMode"`
		TLSEmail  string `json:"tlsEmail"`
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
	body.TLSMode = strings.ToLower(strings.TrimSpace(body.TLSMode))
	body.TLSEmail = strings.TrimSpace(body.TLSEmail)
	if err := validOfficeURL(body.PublicURL); err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_URL", err.Error())
		return
	}
	settings, settingsErr := s.officeSettings(r.Context())
	if settingsErr != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not load office settings")
		return
	}
	if settings.DeploymentMode == "remote" && body.TLSMode == "existing" {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Existing certificates are local to this server; use automatic Let's Encrypt for the remote host")
		return
	}
	if (body.TLSCert == "") != (body.TLSKey == "") {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose both a TLS certificate and key, or neither")
		return
	}
	if body.TLSMode != "" && body.TLSMode != "keep" && body.TLSMode != "auto" && body.TLSMode != "existing" {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Configure a certificate or keep the current gateway")
		return
	}
	if body.TLSMode == "existing" && (body.TLSCert == "" || body.TLSKey == "") {
		writeError(w, http.StatusBadRequest, "OFFICE_TLS", "Choose an existing certificate and key")
		return
	}
	if body.TLSMode == "auto" {
		if body.TLSEmail == "" {
			body.TLSEmail = s.settingString(r.Context(), "ssl.admin_email", "")
		}
		if body.TLSEmail == "" {
			writeError(w, http.StatusBadRequest, "OFFICE_TLS_EMAIL", "Enter a Let's Encrypt account email")
			return
		}
		body.TLSCert, body.TLSKey = "", ""
	}
	if body.TLSMode == "existing" {
		parsed, _ := url.Parse(body.PublicURL)
		cert, certErr := s.officeCertificateForPaths(r.Context(), body.TLSCert, body.TLSKey)
		if certErr != nil {
			writeError(w, http.StatusBadRequest, "OFFICE_TLS", "The selected certificate is not available in System Control TLS")
			return
		}
		if !officeCertificateCovers(cert, parsed.Hostname()) {
			writeError(w, http.StatusBadRequest, "OFFICE_TLS_NAME", "The selected certificate does not cover "+parsed.Hostname()+"; use automatic Let's Encrypt or choose a matching certificate")
			return
		}
	}
	if settings.DeploymentMode == "remote" {
		target, targetErr := s.officeRemoteTargetFromSettings(settings)
		if targetErr != nil {
			writeError(w, http.StatusConflict, "OFFICE_REMOTE", targetErr.Error())
			return
		}
		preflight, preflightErr := s.officeRemotePreflight(r.Context(), target, body.PublicURL)
		if preflightErr != nil || !preflight.Ready {
			detail := "The remote Office host is not ready"
			if preflightErr != nil {
				detail = preflightErr.Error()
			} else if preflight.Detail != "" {
				detail = preflight.Detail
			}
			writeError(w, http.StatusConflict, "OFFICE_REMOTE_PREFLIGHT", detail)
			return
		}
	} else {
		preflight := s.officePreflight(r.Context(), body.PublicURL)
		if !preflight.DNSOK {
			writeError(w, http.StatusConflict, "OFFICE_DNS", preflight.DNSDetail)
			return
		}
	}
	agentBody := map[string]any{
		"publicUrl": body.PublicURL, "tlsCert": body.TLSCert, "tlsKey": body.TLSKey,
		"autoTls": body.TLSMode == "auto", "tlsEmail": body.TLSEmail,
	}
	agent, err := s.officeAgentRequest(r.Context(), http.MethodPost, "/gateway", agentBody, 5*time.Minute)
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
	settings, _ = s.officeSettings(r.Context())
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
	if s.db != nil {
		settings, settingsErr := s.officeSettings(ctx)
		if settingsErr != nil {
			return nil, 0, fmt.Errorf("could not load the Office deployment target: %w", settingsErr)
		}
		if settings.DeploymentMode == "remote" {
			return s.officeRemoteAgentRaw(ctx, settings, method, path, body, timeout)
		}
	}
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

func (s *Server) officeCertificateForPaths(ctx context.Context, certPath, keyPath string) (officeCertificate, error) {
	var cert officeCertificate
	err := s.db.QueryRow(ctx, `
		SELECT name, cert_path, key_path, sans, to_char(not_after, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), self_signed
		FROM control.ssl_certificates WHERE cert_path=$1 AND key_path=$2
	`, certPath, keyPath).Scan(&cert.Name, &cert.CertPath, &cert.KeyPath, &cert.SANs, &cert.NotAfter, &cert.SelfSigned)
	return cert, err
}

func officeCertificateCovers(cert officeCertificate, hostname string) bool {
	hostname = normalizeDomain(hostname)
	for _, raw := range cert.SANs {
		san := normalizeDomain(raw)
		if san == hostname {
			return true
		}
		if strings.HasPrefix(san, "*.") {
			suffix := strings.TrimPrefix(san, "*")
			if strings.HasSuffix(hostname, suffix) {
				prefix := strings.TrimSuffix(hostname, suffix)
				if prefix != "" && !strings.Contains(prefix, ".") {
					return true
				}
			}
		}
	}
	return false
}

func validOfficeImage(value string) error {
	if value == "" || len(value) > 255 {
		return fmt.Errorf("OnlyOffice image is invalid")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._/:@-", char) {
			continue
		}
		return fmt.Errorf("OnlyOffice image contains unsupported characters")
	}
	return nil
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
