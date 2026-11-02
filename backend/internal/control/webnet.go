package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	webNetMaxHosts      = 40
	webNetMaxCIDR       = 64
	webNetTitleLimit    = 160
	webNetMessageLimit  = 2000
	webNetHealthTimeout = 30 * time.Second
)

type webNetSettings struct {
	MailHostMode       string     `json:"mailHostMode"`
	WebmailMaintenance bool       `json:"webmailMaintenance"`
	AdminMaintenance   bool       `json:"adminMaintenance"`
	MaintenanceTitle   string     `json:"maintenanceTitle"`
	MaintenanceMessage string     `json:"maintenanceMessage"`
	BlockedTitle       string     `json:"blockedTitle"`
	BlockedMessage     string     `json:"blockedMessage"`
	WebmailAllow       []string   `json:"webmailAllow"`
	WebmailDeny        []string   `json:"webmailDeny"`
	AdminAllow         []string   `json:"adminAllow"`
	AdminDeny          []string   `json:"adminDeny"`
	AppliedRevision    int        `json:"appliedRevision"`
	AppliedAt          *time.Time `json:"appliedAt,omitempty"`
	LastError          string     `json:"lastError"`
	UpdatedAt          *time.Time `json:"updatedAt,omitempty"`
}

type webNetRoute struct {
	Service     string `json:"service"`
	Hostname    string `json:"hostname"`
	Certificate string `json:"certificate"`
	IPMode      string `json:"ipMode"`
	IPAddress   string `json:"ipAddress"`
	Primary     bool   `json:"primary"`
	Enabled     bool   `json:"enabled"`
}

type webNetIP struct {
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Family    string `json:"family"`
	Scope     string `json:"scope"`
	Local     bool   `json:"local"`
	Public    bool   `json:"public"`
	Label     string `json:"label,omitempty"`
}

type webNetCertificate struct {
	Name       string   `json:"name"`
	CertPath   string   `json:"certPath"`
	KeyPath    string   `json:"keyPath"`
	SANs       []string `json:"sans"`
	NotAfter   string   `json:"notAfter,omitempty"`
	DaysLeft   int      `json:"daysLeft"`
	SelfSigned bool     `json:"selfSigned"`
	HasKey     bool     `json:"hasKey"`
}

type webNetHostCheck struct {
	Service      string   `json:"service"`
	Hostname     string   `json:"hostname"`
	Primary      bool     `json:"primary"`
	DNSOK        bool     `json:"dnsOk"`
	Resolved     []string `json:"resolved"`
	Expected     []string `json:"expected"`
	DNSDetail    string   `json:"dnsDetail"`
	Certificate  string   `json:"certificate,omitempty"`
	CertPath     string   `json:"certPath,omitempty"`
	KeyPath      string   `json:"keyPath,omitempty"`
	CertDaysLeft int      `json:"certDaysLeft"`
	CertOK       bool     `json:"certOk"`
	CertDetail   string   `json:"certDetail"`
	OK           bool     `json:"ok"`
}

type webNetIssuedCertificate struct {
	Service     string   `json:"service"`
	Certificate string   `json:"certificate"`
	Domains     []string `json:"domains"`
}

type webNetIssuanceError struct {
	Service string
	Domains []string
	Output  string
}

func (e *webNetIssuanceError) Error() string {
	if strings.TrimSpace(e.Output) != "" {
		return "automatic Let's Encrypt issuance failed for " + strings.Join(e.Domains, ", ") + ": " + truncateText(e.Output, 1200)
	}
	return "automatic Let's Encrypt issuance failed for " + strings.Join(e.Domains, ", ")
}

type webNetSaveRequest struct {
	MailHostMode       *string       `json:"mailHostMode"`
	WebmailMaintenance *bool         `json:"webmailMaintenance"`
	AdminMaintenance   *bool         `json:"adminMaintenance"`
	MaintenanceTitle   *string       `json:"maintenanceTitle"`
	MaintenanceMessage *string       `json:"maintenanceMessage"`
	BlockedTitle       *string       `json:"blockedTitle"`
	BlockedMessage     *string       `json:"blockedMessage"`
	LetsEncryptEmail   *string       `json:"letsencryptEmail"`
	WebmailAllow       []string      `json:"webmailAllow"`
	WebmailDeny        []string      `json:"webmailDeny"`
	AdminAllow         []string      `json:"adminAllow"`
	AdminDeny          []string      `json:"adminDeny"`
	Routes             []webNetRoute `json:"routes"`
}

func (s *Server) registerWebNetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/webnet", s.require("server.read", s.handleWebNetGet))
	mux.HandleFunc("PUT /control/api/webnet", s.require("server.manage", s.handleWebNetSave))
	mux.HandleFunc("POST /control/api/webnet/verify", s.require("server.manage", s.handleWebNetVerify))
	mux.HandleFunc("POST /control/api/webnet/apply", s.require("server.manage", s.handleWebNetApply))
	mux.HandleFunc("GET /control/api/webnet/metrics", s.require("server.read", s.handleWebNetMetrics))
}

func (s *Server) loadWebNetSettings(ctx context.Context) (webNetSettings, error) {
	settings := webNetSettings{}
	err := s.db.QueryRow(ctx, `
		SELECT mail_host_mode,webmail_maintenance,admin_maintenance,maintenance_title,maintenance_message,
		       blocked_title,blocked_message,webmail_allow_cidrs,webmail_deny_cidrs,
		       admin_allow_cidrs,admin_deny_cidrs,applied_revision,applied_at,last_error,updated_at
		FROM control.webnet_settings WHERE id=true
	`).Scan(&settings.MailHostMode, &settings.WebmailMaintenance, &settings.AdminMaintenance,
		&settings.MaintenanceTitle, &settings.MaintenanceMessage, &settings.BlockedTitle, &settings.BlockedMessage,
		&settings.WebmailAllow, &settings.WebmailDeny, &settings.AdminAllow, &settings.AdminDeny,
		&settings.AppliedRevision, &settings.AppliedAt, &settings.LastError, &settings.UpdatedAt)
	if err == nil {
		return settings, nil
	}
	_, insertErr := s.db.Exec(ctx, `INSERT INTO control.webnet_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING`)
	if insertErr != nil {
		return settings, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT mail_host_mode,webmail_maintenance,admin_maintenance,maintenance_title,maintenance_message,
		       blocked_title,blocked_message,webmail_allow_cidrs,webmail_deny_cidrs,
		       admin_allow_cidrs,admin_deny_cidrs,applied_revision,applied_at,last_error,updated_at
		FROM control.webnet_settings WHERE id=true
	`).Scan(&settings.MailHostMode, &settings.WebmailMaintenance, &settings.AdminMaintenance,
		&settings.MaintenanceTitle, &settings.MaintenanceMessage, &settings.BlockedTitle, &settings.BlockedMessage,
		&settings.WebmailAllow, &settings.WebmailDeny, &settings.AdminAllow, &settings.AdminDeny,
		&settings.AppliedRevision, &settings.AppliedAt, &settings.LastError, &settings.UpdatedAt)
	return settings, err
}

func (s *Server) loadWebNetRoutes(ctx context.Context) ([]webNetRoute, error) {
	rows, err := s.db.Query(ctx, `
		SELECT service,hostname::text,certificate_name,ip_mode,COALESCE(host(ip_address),''),primary_route,enabled
		FROM control.webnet_routes ORDER BY service, primary_route DESC, hostname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := []webNetRoute{}
	for rows.Next() {
		var route webNetRoute
		if rows.Scan(&route.Service, &route.Hostname, &route.Certificate, &route.IPMode,
			&route.IPAddress, &route.Primary, &route.Enabled) == nil {
			routes = append(routes, route)
		}
	}
	return routes, rows.Err()
}

func (s *Server) handleWebNetGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	settings, err := s.loadWebNetSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	routes, err := s.loadWebNetRoutes(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	local, _ := s.webNetLocalIPs(r.Context())
	certs, _ := s.webNetCertificates(r.Context())
	if len(certs) == 0 {
		// A fresh install has not opened the TLS page yet. Populate the
		// inventory once so the certificate selectors are useful immediately.
		_, _ = s.scanCertificates(r.Context())
		certs, _ = s.webNetCertificates(r.Context())
	}
	payload := map[string]any{
		"settings": settings, "routes": routes,
		"mailHostname": s.mailHostname(r.Context()), "serverHostname": s.serverHostname(r.Context()),
		"controlHostname":  webNetURLHostname(s.cfg.PublicURL),
		"webmailUrl":       s.settingString(r.Context(), "server.webmail_url", s.cfg.WebmailURL),
		"adminUrl":         s.settingString(r.Context(), "server.admin_url", ""),
		"letsencryptEmail": s.settingString(r.Context(), "ssl.admin_email", ""),
		"ips":              local, "certificates": certs,
		"paths": map[string]string{
			"site": s.cfg.NginxSitePath, "enabled": s.cfg.NginxEnabledPath,
			"webRoot": s.cfg.WebRoot, "maintenanceRoot": s.cfg.WebNetRoot,
			"appEnv": s.cfg.AppEnvFile, "logFormat": s.cfg.NginxLogFormat,
			"logDir": s.cfg.NginxLogDir,
		},
		"checkedAt": nowRFC3339(),
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleWebNetSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	s.webNetMu.Lock()
	defer s.webNetMu.Unlock()
	var body webNetSaveRequest
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current, err := s.loadWebNetSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	settings := current
	if body.MailHostMode != nil {
		settings.MailHostMode = strings.ToLower(strings.TrimSpace(*body.MailHostMode))
	}
	if body.WebmailMaintenance != nil {
		settings.WebmailMaintenance = *body.WebmailMaintenance
	}
	if body.AdminMaintenance != nil {
		settings.AdminMaintenance = *body.AdminMaintenance
	}
	if body.MaintenanceTitle != nil {
		settings.MaintenanceTitle = strings.TrimSpace(*body.MaintenanceTitle)
	}
	if body.MaintenanceMessage != nil {
		settings.MaintenanceMessage = strings.TrimSpace(*body.MaintenanceMessage)
	}
	if body.BlockedTitle != nil {
		settings.BlockedTitle = strings.TrimSpace(*body.BlockedTitle)
	}
	if body.BlockedMessage != nil {
		settings.BlockedMessage = strings.TrimSpace(*body.BlockedMessage)
	}
	if body.WebmailAllow != nil {
		settings.WebmailAllow = body.WebmailAllow
	}
	if body.WebmailDeny != nil {
		settings.WebmailDeny = body.WebmailDeny
	}
	if body.AdminAllow != nil {
		settings.AdminAllow = body.AdminAllow
	}
	if body.AdminDeny != nil {
		settings.AdminDeny = body.AdminDeny
	}
	var letsEncryptEmail *string
	if body.LetsEncryptEmail != nil {
		value := strings.TrimSpace(*body.LetsEncryptEmail)
		if value != "" {
			address, parseErr := mail.ParseAddress(value)
			if parseErr != nil || !strings.EqualFold(strings.TrimSpace(address.Address), value) {
				writeError(w, http.StatusBadRequest, "LETSENCRYPT_EMAIL", "Enter a valid Let's Encrypt account email address")
				return
			}
		}
		letsEncryptEmail = &value
	}
	settings, routes, err := s.validateWebNetConfig(r.Context(), settings, body.Routes, false)
	if err != nil {
		webNetFailure(w, http.StatusBadRequest, err)
		return
	}
	if err := s.saveWebNetConfig(r.Context(), sess, settings, routes); err != nil {
		writeFromError(w, err)
		return
	}
	if letsEncryptEmail != nil {
		if err := s.setSetting(r.Context(), "ssl.admin_email", *letsEncryptEmail, sess.User.ID); err != nil {
			writeFromError(w, err)
			return
		}
	}
	s.actorAudit(r, sess, "webnet.settings_saved", "Web Net", "Saved webmail and administration hostname policy",
		map[string]any{"routes": len(routes), "mailHostMode": settings.MailHostMode})
	payload, _ := s.webNetPublicPayload(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": payload})
}

func (s *Server) webNetPublicPayload(ctx context.Context) (map[string]any, error) {
	settings, err := s.loadWebNetSettings(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.loadWebNetRoutes(ctx)
	if err != nil {
		return nil, err
	}
	local, _ := s.webNetLocalIPs(ctx)
	certs, _ := s.webNetCertificates(ctx)
	return map[string]any{
		"settings": settings, "routes": routes,
		"mailHostname": s.mailHostname(ctx), "serverHostname": s.serverHostname(ctx),
		"controlHostname":  webNetURLHostname(s.cfg.PublicURL),
		"webmailUrl":       s.settingString(ctx, "server.webmail_url", s.cfg.WebmailURL),
		"adminUrl":         s.settingString(ctx, "server.admin_url", ""),
		"letsencryptEmail": s.settingString(ctx, "ssl.admin_email", ""),
		"ips":              local, "certificates": certs,
		"paths": map[string]string{
			"site": s.cfg.NginxSitePath, "enabled": s.cfg.NginxEnabledPath,
			"webRoot": s.cfg.WebRoot, "maintenanceRoot": s.cfg.WebNetRoot,
			"appEnv": s.cfg.AppEnvFile, "logFormat": s.cfg.NginxLogFormat,
			"logDir": s.cfg.NginxLogDir,
		},
	}, nil
}

func (s *Server) saveWebNetConfig(ctx context.Context, sess *sessionRecord, settings webNetSettings, routes []webNetRoute) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		UPDATE control.webnet_settings SET
		  mail_host_mode=$1,webmail_maintenance=$2,admin_maintenance=$3,
		  maintenance_title=$4,maintenance_message=$5,blocked_title=$6,blocked_message=$7,
		  webmail_allow_cidrs=$8,webmail_deny_cidrs=$9,admin_allow_cidrs=$10,admin_deny_cidrs=$11,
		  updated_by=$12,updated_at=now()
		WHERE id=true
	`, settings.MailHostMode, settings.WebmailMaintenance, settings.AdminMaintenance,
		settings.MaintenanceTitle, settings.MaintenanceMessage, settings.BlockedTitle, settings.BlockedMessage,
		settings.WebmailAllow, settings.WebmailDeny, settings.AdminAllow, settings.AdminDeny, sess.User.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM control.webnet_routes`); err != nil {
		return err
	}
	for _, route := range routes {
		var ip any
		if route.IPMode == "dedicated" && route.IPAddress != "" {
			ip = route.IPAddress
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO control.webnet_routes
			  (service,hostname,certificate_name,ip_mode,ip_address,primary_route,enabled,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		`, route.Service, route.Hostname, route.Certificate, route.IPMode, ip, route.Primary, route.Enabled)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) validateWebNetConfig(ctx context.Context, settings webNetSettings, routes []webNetRoute, requirePrimary bool) (webNetSettings, []webNetRoute, error) {
	settings.MailHostMode = strings.ToLower(strings.TrimSpace(settings.MailHostMode))
	if settings.MailHostMode != "default_page" && settings.MailHostMode != "drop" {
		return settings, nil, fmt.Errorf("mail host access must be default_page or drop")
	}
	if settings.MaintenanceTitle == "" {
		settings.MaintenanceTitle = "Alt8Mail is temporarily unavailable"
	}
	if settings.MaintenanceMessage == "" {
		settings.MaintenanceMessage = "We are performing planned maintenance. Please try again shortly."
	}
	if settings.BlockedTitle == "" {
		settings.BlockedTitle = "This address is not a webmail host"
	}
	if settings.BlockedMessage == "" {
		settings.BlockedMessage = "Use the webmail or administration address provided by your organization."
	}
	if len([]rune(settings.MaintenanceTitle)) > webNetTitleLimit || len([]rune(settings.BlockedTitle)) > webNetTitleLimit {
		return settings, nil, fmt.Errorf("page titles must be %d characters or fewer", webNetTitleLimit)
	}
	if len([]rune(settings.MaintenanceMessage)) > webNetMessageLimit || len([]rune(settings.BlockedMessage)) > webNetMessageLimit {
		return settings, nil, fmt.Errorf("page messages must be %d characters or fewer", webNetMessageLimit)
	}
	var err error
	if settings.WebmailAllow, err = normalizeCIDRs(settings.WebmailAllow); err != nil {
		return settings, nil, fmt.Errorf("webmail allow list: %w", err)
	}
	if settings.WebmailDeny, err = normalizeCIDRs(settings.WebmailDeny); err != nil {
		return settings, nil, fmt.Errorf("webmail deny list: %w", err)
	}
	if settings.AdminAllow, err = normalizeCIDRs(settings.AdminAllow); err != nil {
		return settings, nil, fmt.Errorf("admin allow list: %w", err)
	}
	if settings.AdminDeny, err = normalizeCIDRs(settings.AdminDeny); err != nil {
		return settings, nil, fmt.Errorf("admin deny list: %w", err)
	}
	if len(routes) > webNetMaxHosts*2 {
		return settings, nil, fmt.Errorf("at most %d hostnames can be configured", webNetMaxHosts*2)
	}
	mailHost := normalizeDomain(s.mailHostname(ctx))
	serverHost := normalizeDomain(s.serverHostname(ctx))
	controlHost := normalizeDomain(webNetURLHostname(s.cfg.PublicURL))
	forbidden := map[string]string{
		mailHost:    "mail/MX",
		serverHost:  "server",
		controlHost: "control plane",
	}
	certs, err := s.webNetCertificates(ctx)
	if err != nil {
		return settings, nil, err
	}
	certNames := map[string]bool{}
	for _, cert := range certs {
		certNames[cert.Name] = true
	}
	local, _ := s.webNetLocalIPs(ctx)
	localIPs := map[string]bool{}
	for _, ip := range local {
		if ip.Local {
			localIPs[ip.Address] = true
		}
	}
	seenHosts := map[string]bool{}
	servicePrimary := map[string]int{}
	serviceCount := map[string]int{}
	dedicated := map[string]string{}
	normalized := make([]webNetRoute, 0, len(routes))
	for _, route := range routes {
		route.Service = strings.ToLower(strings.TrimSpace(route.Service))
		if route.Service != "webmail" && route.Service != "admin" {
			return settings, nil, fmt.Errorf("routes must belong to webmail or admin")
		}
		route.Hostname = normalizeDomain(route.Hostname)
		if net.ParseIP(route.Hostname) != nil || !strings.Contains(route.Hostname, ".") || !validDomain(route.Hostname) || !validWebHostnameLabels(route.Hostname) {
			return settings, nil, fmt.Errorf("%q is not a valid public hostname", route.Hostname)
		}
		if label, blocked := forbidden[route.Hostname]; blocked {
			return settings, nil, fmt.Errorf("%s is the %s hostname and cannot serve webmail or administration", route.Hostname, label)
		}
		if route.Hostname == "localhost" || strings.HasSuffix(route.Hostname, ".local") {
			return settings, nil, fmt.Errorf("%s is reserved for local use", route.Hostname)
		}
		if seenHosts[route.Hostname] {
			return settings, nil, fmt.Errorf("%s is assigned more than once", route.Hostname)
		}
		seenHosts[route.Hostname] = true
		route.Certificate = strings.TrimSpace(route.Certificate)
		if route.Certificate != "" && !certNames[route.Certificate] {
			return settings, nil, fmt.Errorf("certificate %q is not in the TLS inventory", route.Certificate)
		}
		route.IPMode = strings.ToLower(strings.TrimSpace(route.IPMode))
		if route.IPMode == "" {
			route.IPMode = "shared"
		}
		if route.IPMode != "shared" && route.IPMode != "dedicated" {
			return settings, nil, fmt.Errorf("%s has an invalid IP mode", route.Hostname)
		}
		route.IPAddress = strings.TrimSpace(route.IPAddress)
		if route.IPMode == "dedicated" {
			ip := net.ParseIP(route.IPAddress)
			if ip == nil {
				return settings, nil, fmt.Errorf("%s needs a valid dedicated IP address", route.Hostname)
			}
			route.IPAddress = ip.String()
			if !localIPs[route.IPAddress] {
				return settings, nil, fmt.Errorf("%s is not assigned to this server; add it on Server & network first", route.IPAddress)
			}
			if previous, used := dedicated[route.IPAddress]; used && previous != route.Service {
				return settings, nil, fmt.Errorf("%s cannot be dedicated to both %s and %s", route.IPAddress, previous, route.Service)
			}
			dedicated[route.IPAddress] = route.Service
		} else {
			route.IPAddress = ""
		}
		route.Enabled = true
		serviceCount[route.Service]++
		if route.Primary {
			servicePrimary[route.Service]++
		}
		normalized = append(normalized, route)
	}
	for _, service := range []string{"webmail", "admin"} {
		if servicePrimary[service] > 1 {
			return settings, nil, fmt.Errorf("%s can have only one primary hostname", service)
		}
		if serviceCount[service] > 0 && servicePrimary[service] == 0 {
			return settings, nil, fmt.Errorf("choose a primary hostname for %s", service)
		}
		if requirePrimary && servicePrimary[service] == 0 {
			return settings, nil, fmt.Errorf("add an enabled %s hostname before applying", service)
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Service != normalized[j].Service {
			return normalized[i].Service < normalized[j].Service
		}
		if normalized[i].Primary != normalized[j].Primary {
			return normalized[i].Primary
		}
		return normalized[i].Hostname < normalized[j].Hostname
	})
	return settings, normalized, nil
}

func validWebHostnameLabels(hostname string) bool {
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 || len(labels) > 127 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func normalizeCIDRs(values []string) ([]string, error) {
	if len(values) > webNetMaxCIDR {
		return nil, fmt.Errorf("no more than %d entries are allowed", webNetMaxCIDR)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		for _, piece := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' }) {
			piece = strings.TrimSpace(piece)
			if piece == "" {
				continue
			}
			var network *net.IPNet
			if strings.Contains(piece, "/") {
				_, parsed, err := net.ParseCIDR(piece)
				if err != nil {
					return nil, fmt.Errorf("%q is not a valid CIDR", piece)
				}
				network = parsed
			} else {
				ip := net.ParseIP(piece)
				if ip == nil {
					return nil, fmt.Errorf("%q is not a valid IP address or CIDR", piece)
				}
				bits := 128
				if ip.To4() != nil {
					ip = ip.To4()
					bits = 32
				}
				network = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
			}
			value := network.String()
			if !seen[value] {
				seen[value] = true
				out = append(out, value)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func webNetURLHostname(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return normalizeDomain(parsed.Hostname())
}

func (s *Server) webNetCertificates(ctx context.Context) ([]webNetCertificate, error) {
	rows, err := s.db.Query(ctx, `
		SELECT name,cert_path,key_path,sans,not_after,self_signed
		FROM control.ssl_certificates ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []webNetCertificate{}
	for rows.Next() {
		var cert webNetCertificate
		var notAfter *time.Time
		if rows.Scan(&cert.Name, &cert.CertPath, &cert.KeyPath, &cert.SANs, &notAfter, &cert.SelfSigned) != nil {
			continue
		}
		if notAfter != nil {
			cert.NotAfter = notAfter.UTC().Format(time.RFC3339)
			cert.DaysLeft = int(time.Until(*notAfter).Hours() / 24)
		} else {
			cert.DaysLeft = -1
		}
		cert.HasKey = pathExists(cert.KeyPath)
		out = append(out, cert)
	}
	return out, rows.Err()
}

func certificateCoversHostname(cert webNetCertificate, hostname string) bool {
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

func (s *Server) webNetPickCertificate(route webNetRoute, certs []webNetCertificate) (webNetCertificate, error) {
	if route.Certificate != "" {
		for _, cert := range certs {
			if cert.Name != route.Certificate {
				continue
			}
			if !cert.HasKey {
				return webNetCertificate{}, fmt.Errorf("certificate %s has no readable private key", cert.Name)
			}
			if cert.DaysLeft < 0 {
				return webNetCertificate{}, fmt.Errorf("certificate %s has expired or has no expiry date", cert.Name)
			}
			if !certificateCoversHostname(cert, route.Hostname) {
				return webNetCertificate{}, fmt.Errorf("certificate %s does not cover %s", cert.Name, route.Hostname)
			}
			return cert, nil
		}
		return webNetCertificate{}, fmt.Errorf("certificate %s is not in the TLS inventory", route.Certificate)
	}
	candidates := []webNetCertificate{}
	for _, cert := range certs {
		if cert.HasKey && cert.DaysLeft >= 0 && certificateCoversHostname(cert, route.Hostname) {
			candidates = append(candidates, cert)
		}
	}
	if len(candidates) == 0 {
		return webNetCertificate{}, fmt.Errorf("no valid certificate covers %s; issue or import one under TLS certificates", route.Hostname)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].SelfSigned != candidates[j].SelfSigned {
			return !candidates[i].SelfSigned
		}
		return candidates[i].NotAfter > candidates[j].NotAfter
	})
	return candidates[0], nil
}

func (s *Server) webNetLocalIPs(ctx context.Context) ([]webNetIP, error) {
	result, err := runCommand(ctx, 10*time.Second, "ip", "-j", "addr", "show")
	if err != nil {
		return nil, fmt.Errorf("could not inspect local addresses: %s", strings.TrimSpace(result.Stderr))
	}
	var interfaces []struct {
		IfName    string `json:"ifname"`
		OperState string `json:"operstate"`
		AddrInfo  []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			Prefixlen int    `json:"prefixlen"`
			Scope     string `json:"scope"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &interfaces); err != nil {
		return nil, fmt.Errorf("could not parse local addresses: %w", err)
	}
	seen := map[string]bool{}
	out := []webNetIP{}
	for _, iface := range interfaces {
		if iface.OperState != "" && iface.OperState != "UP" && iface.OperState != "UNKNOWN" {
			continue
		}
		name := strings.ToLower(iface.IfName)
		if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") ||
			strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "virbr") ||
			strings.HasPrefix(name, "lxc") {
			continue
		}
		for _, address := range iface.AddrInfo {
			ip := net.ParseIP(strings.TrimSpace(address.Local))
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			if address.Scope == "host" || address.Scope == "link" {
				continue
			}
			value := ip.String()
			if seen[value] {
				continue
			}
			seen[value] = true
			family := "ipv4"
			if ip.To4() == nil {
				family = "ipv6"
			}
			out = append(out, webNetIP{Address: value, Interface: iface.IfName, Family: family,
				Scope: address.Scope, Local: true, Public: !ip.IsPrivate()})
		}
	}
	for _, key := range []string{"server.public_ipv4", "server.public_ipv6"} {
		value := strings.TrimSpace(s.settingString(ctx, key, ""))
		ip := net.ParseIP(value)
		if ip == nil {
			continue
		}
		value = ip.String()
		if seen[value] {
			for index := range out {
				if out[index].Address == value {
					out[index].Public = true
					out[index].Label = "Configured public address"
				}
			}
			continue
		}
		seen[value] = true
		family := "ipv4"
		if ip.To4() == nil {
			family = "ipv6"
		}
		out = append(out, webNetIP{Address: value, Family: family, Scope: "public", Local: false, Public: true, Label: "Configured public address"})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Address < out[j].Address
	})
	if len(out) == 0 {
		return out, fmt.Errorf("no non-loopback IP addresses were found")
	}
	return out, nil
}

func (s *Server) webNetDNSExpected(route webNetRoute, routes []webNetRoute, local []webNetIP) ([]string, error) {
	addresses, err := s.webNetRouteAddresses(route, routes, local)
	if err != nil {
		return nil, err
	}
	expected := append([]string{}, addresses...)
	if route.IPMode == "shared" {
		reserved := map[string]bool{}
		for _, other := range routes {
			if other.Service != route.Service && other.IPMode == "dedicated" && other.IPAddress != "" {
				reserved[other.IPAddress] = true
			}
		}
		for _, ip := range local {
			if !ip.Local && ip.Public && !reserved[ip.Address] {
				expected = append(expected, ip.Address)
			}
		}
	}
	sort.Strings(expected)
	unique := expected[:0]
	for _, value := range expected {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique, nil
}

func (s *Server) webNetVerifyRoutes(ctx context.Context, routes []webNetRoute, certs []webNetCertificate, local []webNetIP) ([]webNetHostCheck, bool) {
	resolver := s.dnsResolver(ctx)
	checks := []webNetHostCheck{}
	allOK := true
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		expected, expectedErr := s.webNetDNSExpected(route, routes, local)
		expectedSet := map[string]bool{}
		for _, ip := range expected {
			expectedSet[ip] = true
		}
		check := webNetHostCheck{Service: route.Service, Hostname: route.Hostname, Primary: route.Primary,
			Resolved: []string{}, Expected: expected, DNSOK: false, CertOK: false}
		if expectedErr != nil {
			check.DNSDetail = expectedErr.Error()
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		addresses, err := resolver.LookupHost(lookupCtx, route.Hostname)
		cancel()
		resolved := []string{}
		seen := map[string]bool{}
		for _, raw := range addresses {
			if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
				value := ip.String()
				if !seen[value] {
					seen[value] = true
					resolved = append(resolved, value)
				}
			}
		}
		sort.Strings(resolved)
		check.Resolved = resolved
		if expectedErr != nil {
			check.DNSOK = false
		} else if err != nil || len(resolved) == 0 {
			check.DNSDetail = "No A or AAAA record was returned"
			if err != nil {
				check.DNSDetail = "DNS lookup failed: " + err.Error()
			}
		} else {
			matched := false
			for _, ip := range resolved {
				if expectedSet[ip] {
					matched = true
					break
				}
			}
			if matched {
				check.DNSOK = true
				check.DNSDetail = "DNS points at this server"
			} else {
				check.DNSDetail = "DNS points to " + strings.Join(resolved, ", ") + ", which is not an address of this server"
			}
			if route.IPMode == "dedicated" {
				if !seen[route.IPAddress] {
					check.DNSOK = false
					check.DNSDetail = route.IPAddress + " is dedicated to this route but is not present in DNS"
				} else {
					check.DNSOK = true
					check.DNSDetail = "DNS points at the dedicated address " + route.IPAddress
				}
				if !expectedSet[route.IPAddress] {
					check.DNSOK = false
					check.DNSDetail = "Dedicated address " + route.IPAddress + " is not assigned to this server"
				}
			}
		}
		cert, certErr := s.webNetPickCertificate(route, certs)
		if certErr != nil {
			check.CertDetail = certErr.Error()
		} else {
			check.Certificate = cert.Name
			check.CertPath = cert.CertPath
			check.KeyPath = cert.KeyPath
			check.CertDaysLeft = cert.DaysLeft
			check.CertOK = true
			check.CertDetail = fmt.Sprintf("%s · expires in %d days", cert.Name, cert.DaysLeft)
		}
		check.OK = check.DNSOK && check.CertOK
		if !check.OK {
			allOK = false
		}
		checks = append(checks, check)
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].Service != checks[j].Service {
			return checks[i].Service < checks[j].Service
		}
		if checks[i].Primary != checks[j].Primary {
			return checks[i].Primary
		}
		return checks[i].Hostname < checks[j].Hostname
	})
	return checks, allOK
}

func (s *Server) handleWebNetVerify(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	s.webNetMu.Lock()
	defer s.webNetMu.Unlock()
	settings, err := s.loadWebNetSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	routes, err := s.loadWebNetRoutes(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	settings, routes, err = s.validateWebNetConfig(r.Context(), settings, routes, false)
	if err != nil {
		webNetFailure(w, http.StatusBadRequest, err)
		return
	}
	_, _ = s.scanCertificates(r.Context())
	certs, _ := s.webNetCertificates(r.Context())
	local, localErr := s.webNetLocalIPs(r.Context())
	if localErr != nil {
		webNetFailure(w, http.StatusBadGateway, localErr)
		return
	}
	routes, certs, checks, ok, issued, err := s.ensureWebNetCertificates(r.Context(), r, sess, routes, certs, local)
	if err != nil {
		webNetCertificateFailure(w, http.StatusBadGateway, err)
		return
	}
	if len(checks) == 0 {
		ok = false
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": ok, "checks": checks, "issued": issued, "checkedAt": nowRFC3339(),
		"mailHostname": s.mailHostname(r.Context()),
	})
}

func webNetCheckForHost(checks []webNetHostCheck, hostname string) (webNetHostCheck, bool) {
	for _, check := range checks {
		if check.Hostname == hostname {
			return check, true
		}
	}
	return webNetHostCheck{}, false
}

func webNetRouteForHost(routes []webNetRoute, hostname string) (webNetRoute, bool) {
	for _, route := range routes {
		if route.Hostname == hostname {
			return route, true
		}
	}
	return webNetRoute{}, false
}

// webNetMissingAutoServices returns the services that have at least one
// enabled Auto hostname whose DNS is ready but whose certificate check failed.
// Explicit certificate choices are never replaced automatically.
func webNetMissingAutoServices(routes []webNetRoute, checks []webNetHostCheck) []string {
	needed := map[string]bool{}
	for _, check := range checks {
		if check.CertOK || !check.DNSOK {
			continue
		}
		route, ok := webNetRouteForHost(routes, check.Hostname)
		if !ok || !route.Enabled || route.Certificate != "" {
			continue
		}
		needed[route.Service] = true
	}
	services := []string{}
	for _, service := range []string{"webmail", "admin"} {
		if needed[service] {
			services = append(services, service)
		}
	}
	return services
}

func webNetAutoDomains(routes []webNetRoute, service string) []string {
	domains := []string{}
	for _, route := range routes {
		if route.Enabled && route.Service == service && route.Certificate == "" {
			domains = append(domains, route.Hostname)
		}
	}
	return domains
}

func webNetCertificateName(primary string) string {
	name := "webnet-" + normalizeDomain(primary)
	if len(name) > 180 {
		name = name[:180]
	}
	return name
}

func (s *Server) issueWebNetCertificate(ctx context.Context, service string, domains []string, email string) (string, string, error) {
	certName := webNetCertificateName(domains[0])
	args := []string{
		"certonly", "--webroot", "-w", s.cfg.CertbotWebroot,
		"--non-interactive", "--agree-tos", "--no-eff-email",
		"--expand", "--force-renewal", "--cert-name", certName, "--email", email,
	}
	for _, domain := range domains {
		args = append(args, "-d", domain)
	}
	result, err := runCommand(ctx, 240*time.Second, "certbot", args...)
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if err != nil {
		return certName, output, &webNetIssuanceError{Service: service, Domains: domains, Output: output}
	}
	return certName, output, nil
}

func (s *Server) pinWebNetServiceCertificate(ctx context.Context, service, certName string, routes []webNetRoute) ([]webNetRoute, error) {
	_, err := s.db.Exec(ctx, `
		UPDATE control.webnet_routes SET certificate_name=$1, updated_at=now()
		WHERE service=$2 AND certificate_name='' AND enabled
	`, certName, service)
	if err != nil {
		return routes, err
	}
	_, _ = s.db.Exec(ctx, `UPDATE control.webnet_settings SET updated_at=now() WHERE id=true`)
	for index := range routes {
		if routes[index].Service == service && routes[index].Enabled && routes[index].Certificate == "" {
			routes[index].Certificate = certName
		}
	}
	return routes, nil
}

func (s *Server) ensureWebNetCertificates(ctx context.Context, r *http.Request, sess *sessionRecord, routes []webNetRoute, certs []webNetCertificate, local []webNetIP) ([]webNetRoute, []webNetCertificate, []webNetHostCheck, bool, []webNetIssuedCertificate, error) {
	checks, ok := s.webNetVerifyRoutes(ctx, routes, certs, local)
	if ok {
		return routes, certs, checks, true, nil, nil
	}
	issued := []webNetIssuedCertificate{}
	for _, service := range webNetMissingAutoServices(routes, checks) {
		domains := webNetAutoDomains(routes, service)
		if len(domains) == 0 {
			continue
		}
		dnsReady := true
		for _, domain := range domains {
			check, found := webNetCheckForHost(checks, domain)
			if !found || !check.DNSOK {
				dnsReady = false
				break
			}
		}
		if !dnsReady {
			continue
		}
		email := strings.TrimSpace(s.settingString(ctx, "ssl.admin_email", ""))
		if email == "" {
			return routes, certs, checks, false, issued, fmt.Errorf("automatic certificate issuance for %s needs a Let's Encrypt account email; set it on the Web Net page or TLS settings", strings.Join(domains, ", "))
		}
		certName, output, err := s.issueWebNetCertificate(ctx, service, domains, email)
		if err != nil {
			return routes, certs, checks, false, issued, err
		}
		routes, err = s.pinWebNetServiceCertificate(ctx, service, certName, routes)
		if err != nil {
			return routes, certs, checks, false, issued, err
		}
		issued = append(issued, webNetIssuedCertificate{Service: service, Certificate: certName, Domains: domains})
		s.actorAudit(r, sess, "webnet.certificate_issued", strings.Join(domains, ", "),
			"Issued a Let's Encrypt certificate automatically for Web Net",
			map[string]any{"certificate": certName, "service": service, "output": truncateText(output, 2000)})
		s.notifyEvent(ctx, "ssl.renewed", map[string]any{
			"domains": domains, "certificate": certName, "action": "issued", "source": "webnet",
		})
	}
	if len(issued) == 0 {
		return routes, certs, checks, false, issued, nil
	}
	if _, err := s.scanCertificates(ctx); err != nil {
		return routes, certs, checks, false, issued, fmt.Errorf("certificate was issued but the TLS inventory scan failed: %w", err)
	}
	certs, _ = s.webNetCertificates(ctx)
	for _, item := range issued {
		found := false
		for _, cert := range certs {
			if cert.Name == item.Certificate {
				found = true
				break
			}
		}
		if !found {
			return routes, certs, checks, false, issued, fmt.Errorf("certificate %s was issued but is not visible in the TLS inventory", item.Certificate)
		}
	}
	checks, ok = s.webNetVerifyRoutes(ctx, routes, certs, local)
	return routes, certs, checks, ok, issued, nil
}

func webNetCertificateFailure(w http.ResponseWriter, status int, err error) {
	details := map[string]any{}
	var issuance *webNetIssuanceError
	if errors.As(err, &issuance) {
		details["certbot"] = truncateText(issuance.Output, 16000)
		details["domains"] = issuance.Domains
		details["service"] = issuance.Service
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": "WEB_NET_CERT", "message": err.Error(), "details": details,
	}})
}

func webNetFailure(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": "WEB_NET", "message": err.Error(),
	}})
}

func webNetPrimaryRoute(routes []webNetRoute, service string) (webNetRoute, bool) {
	for _, route := range routes {
		if route.Service == service && route.Primary && route.Enabled {
			return route, true
		}
	}
	return webNetRoute{}, false
}

func (s *Server) handleWebNetApply(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	s.webNetMu.Lock()
	defer s.webNetMu.Unlock()
	settings, err := s.loadWebNetSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	routes, err := s.loadWebNetRoutes(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	settings, routes, err = s.validateWebNetConfig(r.Context(), settings, routes, true)
	if err != nil {
		webNetFailure(w, http.StatusBadRequest, err)
		return
	}
	webmail, webmailOK := webNetPrimaryRoute(routes, "webmail")
	admin, adminOK := webNetPrimaryRoute(routes, "admin")
	if !webmailOK || !adminOK {
		webNetFailure(w, http.StatusBadRequest, fmt.Errorf("webmail and admin each need an enabled primary hostname"))
		return
	}
	_, _ = s.scanCertificates(r.Context())
	certs, _ := s.webNetCertificates(r.Context())
	local, err := s.webNetLocalIPs(r.Context())
	if err != nil {
		s.webNetMarkError(r.Context(), err)
		webNetFailure(w, http.StatusBadGateway, err)
		return
	}
	routes, certs, checks, ok, issued, err := s.ensureWebNetCertificates(r.Context(), r, sess, routes, certs, local)
	if err != nil {
		s.webNetMarkError(r.Context(), err)
		webNetCertificateFailure(w, http.StatusBadGateway, err)
		return
	}
	if !ok {
		s.webNetMarkError(r.Context(), fmt.Errorf("DNS or certificate verification failed"))
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code": "WEB_NET_VERIFY", "message": "DNS or certificate verification failed",
			"details": map[string]any{"checks": checks},
		}})
		return
	}
	// Read the selected files fresh so a stale inventory cannot produce a
	// config that nginx will reject at reload time.
	for index := range checks {
		if !checks[index].CertOK {
			continue
		}
		info, readErr := readCertificate(checks[index].CertPath, checks[index].KeyPath, checks[index].Certificate)
		if readErr != nil {
			checks[index].CertOK = false
			checks[index].OK = false
			checks[index].CertDetail = "Could not read the certificate: " + readErr.Error()
			ok = false
			continue
		}
		if info.DaysLeft < 0 {
			checks[index].CertOK = false
			checks[index].OK = false
			checks[index].CertDetail = "The certificate has expired"
			ok = false
		}
	}
	if !ok {
		s.webNetMarkError(r.Context(), fmt.Errorf("certificate files failed validation"))
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code": "WEB_NET_VERIFY", "message": "Certificate files failed validation",
			"details": map[string]any{"checks": checks},
		}})
		return
	}
	certByName := map[string]webNetCertificate{}
	for _, cert := range certs {
		certByName[cert.Name] = cert
	}
	certByRoute := map[string]webNetCertificate{}
	for _, check := range checks {
		certByRoute[check.Hostname] = certByName[check.Certificate]
	}
	fallback := certByRoute[webmail.Hostname]
	if mailCert, mailErr := s.webNetMailCertificate(s.mailHostname(r.Context()), certs); mailErr == nil {
		fallback = mailCert
	}
	config, err := s.renderWebNetNginx(settings, routes, certByRoute, fallback, local, s.mailHostname(r.Context()))
	if err != nil {
		s.webNetMarkError(r.Context(), err)
		webNetFailure(w, http.StatusBadRequest, err)
		return
	}
	pages := s.renderWebNetPages(settings)
	oldConfig, configExists := s.readOptionalFile(s.cfg.NginxSitePath)
	oldLogFormat, logFormatExists := s.readOptionalFile(s.cfg.NginxLogFormat)
	oldPages := map[string]*string{}
	for name := range pages {
		path := filepath.Join(s.cfg.WebNetRoot, name)
		existing, exists := s.readOptionalFile(path)
		if exists {
			oldPages[name] = &existing
		} else {
			oldPages[name] = nil
		}
	}
	restoreNginx, err := s.applyNginxWebNet(config, oldConfig, configExists, oldLogFormat, logFormatExists, pages, oldPages)
	if err != nil {
		s.webNetMarkError(r.Context(), err)
		webNetFailure(w, http.StatusBadGateway, err)
		return
	}
	envConfig := webNetEnvValues(webmail, routes)
	restoreEnv, err := s.applyWebNetEnv(envConfig)
	if err != nil {
		_ = restoreNginx()
		s.webNetMarkError(r.Context(), err)
		webNetFailure(w, http.StatusBadGateway, err)
		return
	}
	if err := s.restartWebmailServices(r.Context()); err != nil {
		_ = restoreEnv()
		_ = s.restartWebmailServices(context.Background())
		_ = restoreNginx()
		s.webNetMarkError(r.Context(), err)
		webNetFailure(w, http.StatusBadGateway, err)
		return
	}
	revision := settings.AppliedRevision + 1
	err = s.db.QueryRow(r.Context(), `
		UPDATE control.webnet_settings
		SET applied_revision=applied_revision+1,applied_at=now(),last_error='',updated_at=now()
		WHERE id=true RETURNING applied_revision
	`).Scan(&revision)
	if err != nil {
		_ = restoreEnv()
		_ = s.restartWebmailServices(context.Background())
		_ = restoreNginx()
		s.webNetMarkError(r.Context(), err)
		writeFromError(w, err)
		return
	}
	_, _ = s.db.Exec(r.Context(), `
		UPDATE control.tls_assignments SET status='superseded', detail='managed by Web Net'
		WHERE target='web' AND status='applied'
	`)
	webmailURL := "https://" + webmail.Hostname
	adminURL := "https://" + admin.Hostname + "/"
	oldWebmailHost := webNetURLHostname(s.settingString(r.Context(), "server.webmail_url", s.cfg.WebmailURL))
	if oldWebmailHost != webmail.Hostname {
		// Push subscriptions and service workers are scoped to an origin. When
		// the webmail origin changes, old endpoints cannot be reused and would
		// keep pointing at the now-blocked mail host.
		_, _ = s.db.Exec(r.Context(), `DELETE FROM public.web_push_subscriptions`)
		_, _ = s.db.Exec(r.Context(), `UPDATE public.web_push_settings SET cursor_at=now(),cursor_id='' WHERE id=true`)
	}
	if settingErr := s.setSetting(r.Context(), "server.webmail_url", webmailURL, sess.User.ID); settingErr != nil {
		s.logger.Warn("web net applied but webmail URL setting failed", "err", settingErr)
	}
	if settingErr := s.setSetting(r.Context(), "server.admin_url", adminURL, sess.User.ID); settingErr != nil {
		s.logger.Warn("web net applied but admin URL setting failed", "err", settingErr)
	}
	s.actorAudit(r, sess, "webnet.applied", "Web Net", "Applied webmail and admin edge configuration",
		map[string]any{"revision": revision, "webmail": webmail.Hostname, "admin": admin.Hostname,
			"mailHostMode": settings.MailHostMode, "routes": len(routes)})
	s.notifyEvent(r.Context(), "webnet.applied", map[string]any{
		"revision": revision, "webmailUrl": webmailURL, "adminUrl": adminURL,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "revision": revision, "appliedAt": nowRFC3339(),
		"webmailUrl": webmailURL, "adminUrl": adminURL, "checks": checks, "issued": issued,
		"message": "Web Net configuration applied and nginx reloaded.",
	})
}

func (s *Server) webNetMailCertificate(mailHost string, certs []webNetCertificate) (webNetCertificate, error) {
	for _, cert := range certs {
		if cert.HasKey && cert.DaysLeft >= 0 && certificateCoversHostname(cert, mailHost) {
			return cert, nil
		}
	}
	return webNetCertificate{}, fmt.Errorf("no certificate covers %s", mailHost)
}

func (s *Server) webNetMarkError(ctx context.Context, err error) {
	_, _ = s.db.Exec(ctx, `UPDATE control.webnet_settings SET last_error=$1,updated_at=now() WHERE id=true`, truncateText(err.Error(), 1000))
}

func (s *Server) readOptionalFile(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func (s *Server) applyNginxWebNet(config, oldConfig string, configExists bool, oldLogFormat string, logFormatExists bool, pages map[string]string, oldPages map[string]*string) (func() error, error) {
	if err := os.MkdirAll(s.cfg.NginxLogDir, 0o755); err != nil {
		return nil, fmt.Errorf("create nginx log directory: %w", err)
	}
	if _, err := writeManagedFile(s.cfg.NginxLogFormat, webNetLogFormat, 0o644, 0, 0); err != nil {
		return nil, fmt.Errorf("write nginx log format: %w", err)
	}
	for name, content := range pages {
		if _, err := writeManagedFile(filepath.Join(s.cfg.WebNetRoot, name), content, 0o644, 0, 0); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
	}
	if _, err := writeManagedFile(s.cfg.NginxSitePath, config, 0o644, 0, 0); err != nil {
		return nil, fmt.Errorf("write nginx site: %w", err)
	}
	restoreFiles := func() {
		for name, content := range oldPages {
			path := filepath.Join(s.cfg.WebNetRoot, name)
			if content == nil {
				_ = os.Remove(path)
				continue
			}
			_, _ = writeManagedFile(path, *content, 0o644, 0, 0)
		}
		if logFormatExists {
			_, _ = writeManagedFile(s.cfg.NginxLogFormat, oldLogFormat, 0o644, 0, 0)
		} else {
			_ = os.Remove(s.cfg.NginxLogFormat)
		}
		if configExists {
			_, _ = writeManagedFile(s.cfg.NginxSitePath, oldConfig, 0o644, 0, 0)
		} else {
			_ = os.Remove(s.cfg.NginxSitePath)
		}
	}
	restoreLink, err := ensureNginxSiteLink(s.cfg.NginxSitePath, s.cfg.NginxEnabledPath)
	if err != nil {
		restoreFiles()
		return nil, err
	}
	restore := func() error {
		_ = restoreLink()
		restoreFiles()
		_, _ = runCommand(context.Background(), 30*time.Second, "systemctl", "reload", "nginx")
		return nil
	}
	if result, err := runCommand(context.Background(), 30*time.Second, "nginx", "-t"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("nginx -t rejected the Web Net configuration: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	if result, err := runCommand(context.Background(), 30*time.Second, "systemctl", "reload", "nginx"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("nginx reload failed: %s", strings.TrimSpace(result.Stderr))
	}
	return restore, nil
}

func ensureNginxSiteLink(target, link string) (func() error, error) {
	oldTarget, readErr := os.Readlink(link)
	existed := readErr == nil
	oldFile := ""
	if !existed {
		if content, err := os.ReadFile(link); err == nil {
			oldFile = string(content)
			existed = true
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return nil, err
	}
	restore := func() error {
		_ = os.Remove(link)
		if !existed {
			return nil
		}
		if oldFile != "" {
			_, err := writeManagedFile(link, oldFile, 0o644, 0, 0)
			return err
		}
		return os.Symlink(oldTarget, link)
	}
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		_ = restore()
		return nil, fmt.Errorf("enable nginx site: %w", err)
	}
	return restore, nil
}

func (s *Server) applyWebNetEnv(values map[string]string) (func() error, error) {
	original, err := os.ReadFile(s.cfg.AppEnvFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.cfg.AppEnvFile, err)
	}
	updated := string(original)
	for key, value := range values {
		updated = setEnvFileValue(updated, key, value)
	}
	group, groupErr := user.LookupGroup("a8m")
	if groupErr != nil {
		return nil, fmt.Errorf("the a8m service group is not present: %w", groupErr)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return nil, fmt.Errorf("invalid a8m group id")
	}
	if _, err := writeManagedFile(s.cfg.AppEnvFile, updated, 0o640, 0, gid); err != nil {
		return nil, fmt.Errorf("write %s: %w", s.cfg.AppEnvFile, err)
	}
	restore := func() error {
		_, err := writeManagedFile(s.cfg.AppEnvFile, string(original), 0o640, 0, gid)
		return err
	}
	return restore, nil
}

func setEnvFileValue(content, key, value string) string {
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "\r", "")
	line := key + "=" + value
	lines := strings.Split(content, "\n")
	replaced := false
	for index, current := range lines {
		trimmed := strings.TrimSpace(current)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, key+"=") {
			if !replaced {
				lines[index] = line
				replaced = true
			} else {
				lines = append(lines[:index], lines[index+1:]...)
				break
			}
		}
	}
	if replaced {
		return strings.Join(lines, "\n")
	}
	trimmed := strings.TrimRight(content, "\n")
	if trimmed != "" {
		trimmed += "\n"
	}
	return trimmed + line + "\n"
}

func (s *Server) restartWebmailServices(ctx context.Context) error {
	result, err := runCommand(ctx, 90*time.Second, "systemctl", "restart", "a8m-server", "a8m-admin")
	if err != nil {
		return fmt.Errorf("restart webmail services: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	deadline := time.Now().Add(webNetHealthTimeout)
	for {
		webErr := webNetHealthCheck(ctx, s.cfg.WebHealthURL)
		adminErr := webNetHealthCheck(ctx, s.cfg.AdminHealthURL)
		if webErr == nil && adminErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			if webErr != nil {
				return fmt.Errorf("webmail health check failed after apply: %w", webErr)
			}
			return fmt.Errorf("admin health check failed after apply: %w", adminErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}
}

func webNetHealthCheck(ctx context.Context, address string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", address, response.StatusCode)
	}
	return nil
}

func webNetEnvValues(webmail webNetRoute, routes []webNetRoute) map[string]string {
	origins := []string{}
	for _, route := range routes {
		if route.Service == "webmail" && route.Enabled {
			origins = append(origins, "https://"+route.Hostname)
		}
	}
	return map[string]string{
		"ALT8MAIL_PUBLIC_URL":       "https://" + webmail.Hostname,
		"ALT8MAIL_WEBAUTHN_RPID":    webmail.Hostname,
		"ALT8MAIL_WEBAUTHN_ORIGINS": strings.Join(origins, ","),
		"ALT8MAIL_ADMIN_URL":        "https://" + webNetPrimaryHost(routes, "admin") + "/",
	}
}

func webNetPrimaryHost(routes []webNetRoute, service string) string {
	for _, route := range routes {
		if route.Service == service && route.Primary && route.Enabled {
			return route.Hostname
		}
	}
	return ""
}

func webNetPage(title, message, eyebrow string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>` + html.EscapeString(title) + `</title>
<style>
:root{color-scheme:light dark;--bg:#f4f6fb;--card:#fff;--ink:#101828;--muted:#5c667a;--line:#dde3ee;--accent:#2563eb}
@media (prefers-color-scheme:dark){:root{--bg:#0b1120;--card:#121a2b;--ink:#eef2ff;--muted:#a2adc2;--line:#253149;--accent:#7aa2ff}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:28px;background:var(--bg);color:var(--ink);font:15px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
.card{width:min(620px,100%);background:var(--card);border:1px solid var(--line);border-radius:20px;padding:38px;box-shadow:0 24px 70px rgba(9,16,32,.10)}
.mark{display:flex;align-items:center;gap:10px;font-weight:700;letter-spacing:-.02em;margin-bottom:30px}.dot{width:10px;height:10px;border-radius:50%;background:var(--accent);box-shadow:0 0 0 5px rgba(37,99,235,.16)}
.eyebrow{font-size:11px;font-weight:700;letter-spacing:.14em;text-transform:uppercase;color:var(--accent);margin:0 0 10px}h1{font-size:clamp(26px,5vw,38px);line-height:1.12;letter-spacing:-.035em;margin:0 0 14px}p{color:var(--muted);margin:0 0 26px;max-width:52ch}.foot{display:flex;justify-content:space-between;gap:14px;border-top:1px solid var(--line);padding-top:18px;font-size:12px;color:var(--muted)}
</style>
</head>
<body><main class="card"><div class="mark"><span class="dot"></span>Alt8Mail</div><p class="eyebrow">` + html.EscapeString(eyebrow) + `</p><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(message) + `</p><div class="foot"><span>Alt8Mail secure mail platform</span><span>HTTP status shown by the edge</span></div></main></body>
</html>
`
}

func (s *Server) renderWebNetPages(settings webNetSettings) map[string]string {
	return map[string]string{
		"maintenance-webmail.html": webNetPage(settings.MaintenanceTitle, settings.MaintenanceMessage, "Scheduled maintenance"),
		"maintenance-admin.html":   webNetPage(settings.MaintenanceTitle, settings.MaintenanceMessage, "Administration unavailable"),
		"blocked.html":             webNetPage(settings.BlockedTitle, settings.BlockedMessage, "Web access restricted"),
	}
}
