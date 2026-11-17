package control

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var certNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$`)

type certificateInfo struct {
	Name       string   `json:"name"`
	CertPath   string   `json:"certPath"`
	KeyPath    string   `json:"keyPath"`
	Subject    string   `json:"subject"`
	Issuer     string   `json:"issuer"`
	SANs       []string `json:"sans"`
	NotBefore  string   `json:"notBefore"`
	NotAfter   string   `json:"notAfter"`
	DaysLeft   int      `json:"daysLeft"`
	SelfSigned bool     `json:"selfSigned"`
	HasKey     bool     `json:"hasKey"`
}

func readCertificate(certPath, keyPath, name string) (certificateInfo, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return certificateInfo{}, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return certificateInfo{}, fmt.Errorf("not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return certificateInfo{}, err
	}
	sans := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		sans = append(sans, ip.String())
	}
	// A panel-generated server certificate is self-issued but not a CA, so
	// CheckSignatureFrom rejects it. Verify the TBS signature directly to
	// distinguish a self-signed leaf from a CA-issued certificate.
	selfSigned := false
	if cert.Subject.String() == cert.Issuer.String() {
		selfSigned = cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
	}
	_, keyErr := os.Stat(keyPath)
	return certificateInfo{
		Name: name, CertPath: certPath, KeyPath: keyPath,
		Subject: cert.Subject.String(), Issuer: cert.Issuer.String(), SANs: sans,
		NotBefore: cert.NotBefore.UTC().Format(time.RFC3339), NotAfter: cert.NotAfter.UTC().Format(time.RFC3339),
		DaysLeft: int(time.Until(cert.NotAfter).Hours() / 24), SelfSigned: selfSigned, HasKey: keyErr == nil,
	}, nil
}

func (s *Server) scanCertificates(ctx context.Context) ([]certificateInfo, error) {
	type candidate struct {
		name, cert, key string
	}
	candidates := []candidate{}
	live, _ := filepath.Glob(filepath.Join(s.cfg.CertbotDir, "*", "fullchain.pem"))
	for _, certPath := range live {
		dir := filepath.Dir(certPath)
		candidates = append(candidates, candidate{name: filepath.Base(dir), cert: certPath, key: filepath.Join(dir, "privkey.pem")})
	}
	current, _ := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "main.cf"))
	for _, line := range strings.Split(string(current), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "smtpd_tls_cert_file") {
			if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
				certPath := strings.TrimSpace(parts[1])
				keyPath := ""
				if keyLine := postconfValue(ctx, "smtpd_tls_key_file"); keyLine != "" {
					keyPath = keyLine
				}
				if certPath != "" {
					candidates = append(candidates, candidate{name: "postfix-mail", cert: certPath, key: keyPath})
				}
			}
		}
	}
	if pathExists(s.cfg.TLSCertFile) {
		candidates = append(candidates, candidate{name: "alt8mail-control", cert: s.cfg.TLSCertFile, key: s.cfg.TLSKeyFile})
	}
	if dovecotCert, dovecotKey := s.dovecotTLSPaths(ctx); dovecotCert != "" && pathExists(dovecotCert) {
		candidates = append(candidates, candidate{name: "dovecot-mail", cert: dovecotCert, key: dovecotKey})
	}
	// Self-signed certificates created by the panel live beside the control
	// certificate; include them in the inventory so operators can see expiry.
	manualCerts, _ := filepath.Glob(filepath.Join(s.cfg.SSLDir, "*.crt"))
	importedCerts, _ := filepath.Glob(filepath.Join(s.cfg.SSLDir, "imported", "*.crt"))
	manualCerts = append(manualCerts, importedCerts...)
	for _, certPath := range manualCerts {
		if samePath(certPath, s.cfg.TLSCertFile) {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(certPath), ".crt")
		if !certNamePattern.MatchString(name) {
			continue
		}
		candidates = append(candidates, candidate{name: name, cert: certPath, key: strings.TrimSuffix(certPath, ".crt") + ".key"})
	}
	seen := map[string]bool{}
	items := []certificateInfo{}
	for _, candidate := range candidates {
		key := candidate.name
		if seen[key] {
			continue
		}
		seen[key] = true
		info, err := readCertificate(candidate.cert, candidate.key, candidate.name)
		if err != nil {
			continue
		}
		items = append(items, info)
		_, _ = s.db.Exec(ctx, `
			INSERT INTO control.ssl_certificates (name, cert_path, key_path, subject, issuer, sans,
			  not_before, not_after, self_signed, checked_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
			ON CONFLICT (name) DO UPDATE SET cert_path=EXCLUDED.cert_path, key_path=EXCLUDED.key_path,
			  subject=EXCLUDED.subject, issuer=EXCLUDED.issuer, sans=EXCLUDED.sans,
			  not_before=EXCLUDED.not_before, not_after=EXCLUDED.not_after,
			  self_signed=EXCLUDED.self_signed, checked_at=now()
		`, info.Name, info.CertPath, info.KeyPath, info.Subject, info.Issuer, info.SANs,
			info.NotBefore, info.NotAfter, info.SelfSigned)
	}
	// A file removed outside the panel should disappear from the dashboard's
	// cached certificate inventory as soon as the next scan runs.
	rows, err := s.db.Query(ctx, `SELECT name, cert_path FROM control.ssl_certificates`)
	if err == nil {
		stale := []string{}
		for rows.Next() {
			var name, certPath string
			if rows.Scan(&name, &certPath) == nil && !pathExists(certPath) {
				stale = append(stale, name)
			}
		}
		rows.Close()
		for _, name := range stale {
			_, _ = s.db.Exec(ctx, `DELETE FROM control.ssl_certificates WHERE name=$1`, name)
		}
	}
	return items, nil
}

func postconfValue(ctx context.Context, key string) string {
	result, err := runCommand(ctx, 10*time.Second, "postconf", "-h", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func (s *Server) registerTLSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/tls/certificates", s.require("tls.read", s.handleCertificates))
	mux.HandleFunc("GET /control/api/tls/certificates/{name}", s.require("tls.read", s.handleCertificateDetail))
	mux.HandleFunc("POST /control/api/tls/issue", s.require("tls.manage", s.handleCertificateIssue))
	mux.HandleFunc("POST /control/api/tls/renew", s.require("tls.manage", s.handleCertificateRenew))
	mux.HandleFunc("POST /control/api/tls/self-signed", s.require("tls.manage", s.handleSelfSigned))
	mux.HandleFunc("POST /control/api/tls/import", s.require("tls.manage", s.handleCertificateImport))
	mux.HandleFunc("GET /control/api/tls/assignments", s.require("tls.read", s.handleTLSAssignments))
	mux.HandleFunc("POST /control/api/tls/assign", s.require("tls.manage", s.handleTLSAssign))
	mux.HandleFunc("GET /control/api/tls/settings", s.require("tls.read", s.handleTLSSettings))
	mux.HandleFunc("PUT /control/api/tls/settings", s.require("tls.manage", s.handleTLSSettingsSave))
	mux.HandleFunc("POST /control/api/tls/test-email", s.require("tls.manage", s.handleTLSTestEmail))
	mux.HandleFunc("GET /control/api/tls/renewal-wrapper", s.require("tls.read", s.handleRenewalWrapperStatus))
	mux.HandleFunc("POST /control/api/tls/renewal-wrapper", s.require("tls.manage", s.handleRenewalWrapperInstall))
}

func (s *Server) handleCertificates(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	items, err := s.scanCertificates(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, _ := s.db.Query(r.Context(), `SELECT name, not_after, self_signed, last_error FROM control.ssl_certificates ORDER BY not_after`)
	errors := map[string]string{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var name, lastError string
			var notAfter time.Time
			var self bool
			if rows.Scan(&name, &notAfter, &self, &lastError) == nil && lastError != "" {
				errors[name] = lastError
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"certificates": items, "errors": errors, "checkedAt": nowRFC3339()})
}

func (s *Server) handleCertificateDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	// The route is registered as /{name}; pathID reads {id} and previously
	// produced an empty lookup, which surfaced as "Could not read certificate /
	// That record no longer exists" for every row.
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		name = pathID(r)
	}
	if !certNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "CERTIFICATE", "Invalid certificate name")
		return
	}
	var certPath, keyPath, subject, issuer string
	var sans []string
	var notBefore, notAfter time.Time
	var self bool
	err := s.db.QueryRow(r.Context(), `
		SELECT cert_path,key_path,subject,issuer,sans,not_before,not_after,self_signed
		FROM control.ssl_certificates WHERE name=$1
	`, name).Scan(&certPath, &keyPath, &subject, &issuer, &sans, &notBefore, &notAfter, &self)
	if err != nil {
		// The scan is authoritative for files on disk. If the cached row was
		// removed after a rescan, refresh and retry before returning 404.
		items, scanErr := s.scanCertificates(r.Context())
		if scanErr == nil {
			for _, item := range items {
				if item.Name == name {
					writeJSON(w, http.StatusOK, item)
					return
				}
			}
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", "That certificate is no longer present on this server")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "certPath": certPath, "keyPath": keyPath, "subject": subject, "issuer": issuer,
		"sans": sans, "notBefore": notBefore.UTC().Format(time.RFC3339), "notAfter": notAfter.UTC().Format(time.RFC3339),
		"daysLeft": int(time.Until(notAfter).Hours() / 24), "selfSigned": self,
	})
}

func (s *Server) handleCertificateIssue(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Domains string `json:"domains"`
		Email   string `json:"email"`
		Staging bool   `json:"staging"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	domains := []string{}
	for _, domain := range strings.Split(body.Domains, ",") {
		domain = normalizeDomain(domain)
		if !validDomain(domain) {
			writeError(w, http.StatusBadRequest, "DOMAIN", "Invalid domain: "+domain)
			return
		}
		domains = append(domains, domain)
	}
	if len(domains) == 0 {
		writeError(w, http.StatusBadRequest, "DOMAIN", "Enter at least one domain")
		return
	}
	if body.Email == "" {
		body.Email = s.settingString(r.Context(), "ssl.admin_email", "")
	}
	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, http.StatusBadRequest, "EMAIL", "Enter a valid Let's Encrypt account email")
		return
	}
	args := []string{"certonly", "--webroot", "-w", s.cfg.CertbotWebroot, "--non-interactive", "--agree-tos", "--email", body.Email}
	if body.Staging {
		args = append(args, "--staging")
	}
	for _, domain := range domains {
		args = append(args, "-d", domain)
	}
	result, err := runCommand(r.Context(), 180*time.Second, "certbot", args...)
	combined := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if err != nil {
		s.notifyEvent(r.Context(), "ssl.failed", map[string]any{"domains": domains, "error": truncateText(combined, 1500)})
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "CERTBOT", "message": "certbot failed", "details": combined}})
		return
	}
	_, _ = s.scanCertificates(r.Context())
	s.actorAudit(r, sess, "tls.issued", strings.Join(domains, ", "), "Issued a certificate with certbot", map[string]any{"output": truncateText(combined, 2000)})
	s.notifyEvent(r.Context(), "ssl.renewed", map[string]any{"domains": domains, "action": "issued"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": combined})
}

func (s *Server) handleCertificateRenew(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name   string `json:"name"`
		DryRun bool   `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !certNamePattern.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "CERTIFICATE", "Invalid certificate name")
		return
	}
	if _, err := os.Stat(filepath.Join(s.cfg.CertbotDir, body.Name)); err != nil {
		writeError(w, http.StatusNotFound, "CERTIFICATE", "That Let's Encrypt certificate is not installed")
		return
	}
	args := []string{"renew", "--cert-name", body.Name, "--non-interactive", "--no-random-sleep-on-renew"}
	if body.DryRun {
		args = append(args, "--dry-run")
	}
	result, err := runCommand(r.Context(), 180*time.Second, "certbot", args...)
	combined := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if err != nil {
		_, _ = s.db.Exec(r.Context(), `UPDATE control.ssl_certificates SET last_error=$2 WHERE name=$1`, body.Name, truncateText(combined, 2000))
		s.notifyEvent(r.Context(), "ssl.failed", map[string]any{"certificate": body.Name, "dryRun": body.DryRun, "error": truncateText(combined, 1500)})
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "CERTBOT", "message": "Certificate renewal failed", "details": combined}})
		return
	}
	_, _ = s.scanCertificates(r.Context())
	s.actorAudit(r, sess, "tls.renewed", body.Name, "Renewed a certificate with certbot", map[string]any{"dryRun": body.DryRun})
	if !body.DryRun {
		s.notifyEvent(r.Context(), "ssl.renewed", map[string]any{"certificate": body.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dryRun": body.DryRun, "output": combined})
}

func (s *Server) handleSelfSigned(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		CommonName string `json:"commonName"`
		SANs       string `json:"sans"`
		Force      bool   `json:"force"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	commonName := normalizeDomain(body.CommonName)
	if commonName == "" || (net.ParseIP(commonName) == nil && !validDomain(commonName)) {
		writeError(w, http.StatusBadRequest, "COMMON_NAME", "Enter a valid common name")
		return
	}
	sans := []string{}
	seen := map[string]bool{}
	appendSAN := func(value string) error {
		value = normalizeDomain(value)
		if value == "" || seen[value] {
			return nil
		}
		if net.ParseIP(value) == nil && !validDomain(value) {
			return fmt.Errorf("invalid SAN %q", value)
		}
		seen[value] = true
		sans = append(sans, value)
		return nil
	}
	if err := appendSAN(commonName); err != nil {
		writeError(w, http.StatusBadRequest, "SAN", err.Error())
		return
	}
	for _, value := range strings.Split(body.SANs, ",") {
		if err := appendSAN(value); err != nil {
			writeError(w, http.StatusBadRequest, "SAN", err.Error())
			return
		}
	}
	if err := os.MkdirAll(s.cfg.SSLDir, 0o700); err != nil {
		writeFromError(w, err)
		return
	}
	name := "selfsigned-" + strings.NewReplacer(".", "-", ":", "-").Replace(commonName)
	certPath := filepath.Join(s.cfg.SSLDir, name+".crt")
	keyPath := filepath.Join(s.cfg.SSLDir, name+".key")
	if !body.Force && (pathExists(certPath) || pathExists(keyPath)) {
		writeError(w, http.StatusConflict, "EXISTS", "That self-signed certificate already exists; confirm regeneration")
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		writeFromError(w, err)
		return
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Alt8Mail System Control"}},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		writeFromError(w, err)
		return
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		writeFromError(w, err)
		return
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		writeFromError(w, err)
		return
	}
	info, _ := readCertificate(certPath, keyPath, name)
	s.actorAudit(r, sess, "tls.self_signed_created", commonName, "Generated a self-signed certificate", map[string]any{"sans": sans, "cert": certPath})
	_, _ = s.scanCertificates(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "certificate": info, "certPath": certPath, "keyPath": keyPath})
}

func (s *Server) handleTLSSettings(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	writeJSON(w, http.StatusOK, map[string]any{
		"adminEmail":       s.settingString(r.Context(), "ssl.admin_email", ""),
		"warnDays":         s.settingInt(r.Context(), "ssl.warn_days", 14),
		"renewalInstalled": s.settingBool(r.Context(), "ssl.renewal_installed", false),
	})
}

func (s *Server) handleTLSSettingsSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		AdminEmail string `json:"adminEmail"`
		WarnDays   int    `json:"warnDays"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if _, err := mail.ParseAddress(body.AdminEmail); err != nil {
		writeError(w, http.StatusBadRequest, "EMAIL", "Enter a valid notification email")
		return
	}
	if body.WarnDays < 1 || body.WarnDays > 90 {
		writeError(w, http.StatusBadRequest, "WARN_DAYS", "Warning days must be between 1 and 90")
		return
	}
	_ = s.setSetting(r.Context(), "ssl.admin_email", body.AdminEmail, sess.User.ID)
	_ = s.setSetting(r.Context(), "ssl.warn_days", body.WarnDays, sess.User.ID)
	s.actorAudit(r, sess, "tls.settings_updated", body.AdminEmail, "Updated SSL notification settings", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTLSTestEmail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Recipient string `json:"recipient"`
	}
	_ = decodeJSON(w, r, &body, 16<<10)
	if body.Recipient == "" {
		body.Recipient = s.settingString(r.Context(), "ssl.admin_email", "")
	}
	if body.Recipient == "" {
		writeError(w, http.StatusBadRequest, "EMAIL", "Configure an SSL admin email or enter a recipient")
		return
	}
	result := s.runSMTPTransaction(r.Context(), s.defaultMailFrom(r.Context()), body.Recipient,
		"Alt8Mail SSL notification test",
		"This is a test of the SSL expiry and renewal notification mail path.")
	if result.Err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": result.Err.Error(), "transcript": result.Transcript})
		return
	}
	s.actorAudit(r, sess, "tls.test_email", body.Recipient, "Sent an SSL notification test email", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "transcript": result.Transcript})
}

func (s *Server) handleRenewalWrapperStatus(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	script := "/usr/local/bin/ssl_renewal.sh"
	cron := "/etc/cron.d/alt8mail-ssl"
	writeJSON(w, http.StatusOK, map[string]any{
		"scriptPath": script, "cronPath": cron,
		"scriptInstalled": pathExists(script), "cronInstalled": pathExists(cron),
		"installed": s.settingBool(r.Context(), "ssl.renewal_installed", false),
	})
}

func (s *Server) handleRenewalWrapperInstall(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	script := `#!/bin/sh
set -eu
LOG=/var/log/alt8mail-ssl-renewal.log
{
  echo "=== $(date -u +%Y-%m-%dT%H:%M:%SZ) ssl renewal start ==="
  if certbot renew --quiet --deploy-hook 'systemctl reload nginx; systemctl reload postfix; systemctl reload dovecot'; then
    echo "ssl renewal completed"
  else
    echo "ssl renewal failed"
    exit 1
  fi
} >> "$LOG" 2>&1
`
	cron := "17 3,15 * * * root /usr/local/bin/ssl_renewal.sh\n"
	if _, err := writeManagedFile("/usr/local/bin/ssl_renewal.sh", script, 0o755, 0, 0); err != nil {
		writeFromError(w, err)
		return
	}
	if _, err := writeManagedFile("/etc/cron.d/alt8mail-ssl", cron, 0o644, 0, 0); err != nil {
		writeFromError(w, err)
		return
	}
	_ = s.setSetting(r.Context(), "ssl.renewal_installed", true, sess.User.ID)
	s.actorAudit(r, sess, "tls.renewal_wrapper_installed", "/usr/local/bin/ssl_renewal.sh", "Installed the SSL renewal wrapper and twice-daily cron", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "script": "/usr/local/bin/ssl_renewal.sh", "cron": "/etc/cron.d/alt8mail-ssl"})
}

func samePath(a, b string) bool {
	cleanA, errA := filepath.Abs(a)
	cleanB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && cleanA == cleanB
}
