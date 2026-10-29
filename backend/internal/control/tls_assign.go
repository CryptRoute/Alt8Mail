package control

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type tlsTargetState struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	CertPath   string `json:"certPath"`
	KeyPath    string `json:"keyPath"`
	ConfigPath string `json:"configPath,omitempty"`
	Available  bool   `json:"available"`
	Detail     string `json:"detail"`
}

func (s *Server) handleTLSAssignments(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	targets := s.tlsTargetStates(r.Context())
	rows, err := s.db.Query(r.Context(), `
		SELECT certificate_name,target,cert_path,key_path,previous_cert_path,previous_key_path,status,detail,applied_at
		FROM control.tls_assignments ORDER BY applied_at DESC
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	assignments := []map[string]any{}
	for rows.Next() {
		var certName, target, certPath, keyPath, prevCert, prevKey, status, detail string
		var appliedAt time.Time
		if rows.Scan(&certName, &target, &certPath, &keyPath, &prevCert, &prevKey, &status, &detail, &appliedAt) == nil {
			assignments = append(assignments, map[string]any{
				"certificate": certName, "target": target, "certPath": certPath, "keyPath": keyPath,
				"previousCertPath": prevCert, "previousKeyPath": prevKey, "status": status,
				"detail": detail, "appliedAt": appliedAt.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets, "assignments": assignments, "checkedAt": nowRFC3339()})
}

func (s *Server) tlsTargetStates(ctx context.Context) []tlsTargetState {
	web := tlsTargetState{Key: "web", Label: "Web server (nginx)"}
	if configPath, content, err := s.findNginxConfig(); err == nil {
		web.ConfigPath = configPath
		web.CertPath, web.KeyPath = nginxTLSPaths(content)
		web.Available = web.CertPath != "" && web.KeyPath != ""
		if !web.Available {
			web.Detail = "No ssl_certificate/ssl_certificate_key directives found in " + configPath
		}
	} else {
		web.Detail = err.Error()
	}
	postfix := tlsTargetState{Key: "postfix", Label: "Postfix SMTP", Available: true}
	postfix.CertPath = postconfValue(ctx, "smtpd_tls_cert_file")
	postfix.KeyPath = postconfValue(ctx, "smtpd_tls_key_file")
	if postfix.CertPath == "" {
		postfix.Detail = "Postfix has no explicit smtpd_tls_cert_file"
	}
	dovecot := tlsTargetState{Key: "dovecot", Label: "Dovecot IMAP/POP3", Available: true}
	dovecot.CertPath, dovecot.KeyPath = s.dovecotTLSPaths(ctx)
	dovecot.ConfigPath = s.dovecotTLSManagedPath()
	if dovecot.CertPath == "" {
		dovecot.Detail = "Dovecot ssl_cert is not configured"
	}
	return []tlsTargetState{web, postfix, dovecot}
}

func (s *Server) handleTLSAssign(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Certificate string   `json:"certificate"`
		Targets     []string `json:"targets"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	name := strings.TrimSpace(body.Certificate)
	if !certNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "CERTIFICATE", "Choose a saved certificate")
		return
	}
	var certPath, keyPath string
	var notAfter time.Time
	err := s.db.QueryRow(r.Context(), `SELECT cert_path,key_path,not_after FROM control.ssl_certificates WHERE name=$1`, name).Scan(&certPath, &keyPath, &notAfter)
	if err != nil {
		writeError(w, http.StatusNotFound, "CERTIFICATE", "That certificate is not in the inventory")
		return
	}
	if !pathExists(certPath) || !pathExists(keyPath) {
		writeError(w, http.StatusBadRequest, "CERTIFICATE", "The certificate or private key file is missing on disk")
		return
	}
	info, err := readCertificate(certPath, keyPath, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CERTIFICATE", "The certificate could not be read: "+err.Error())
		return
	}
	if info.DaysLeft < 0 {
		writeError(w, http.StatusBadRequest, "EXPIRED", "That certificate has expired")
		return
	}
	requested := map[string]bool{}
	for _, target := range body.Targets {
		target = strings.ToLower(strings.TrimSpace(target))
		if target != "web" && target != "postfix" && target != "dovecot" {
			writeError(w, http.StatusBadRequest, "TARGET", "Targets must be web, postfix or dovecot")
			return
		}
		requested[target] = true
	}
	if len(requested) == 0 {
		writeError(w, http.StatusBadRequest, "TARGET", "Select at least one service")
		return
	}
	targets := []string{}
	for target := range requested {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	mailCertPath, mailKeyPath := certPath, keyPath
	if requested["postfix"] || requested["dovecot"] {
		var materialErr error
		mailCertPath, mailKeyPath, materialErr = s.prepareMailTLSMaterial(r.Context(), certPath, keyPath, requested)
		if materialErr != nil {
			writeError(w, http.StatusBadGateway, "KEY_ACCESS", "Could not make the private key readable by the mail services: "+materialErr.Error())
			return
		}
	}
	previous := map[string]tlsTargetState{}
	for _, state := range s.tlsTargetStates(r.Context()) {
		previous[state.Key] = state
	}
	applied := []string{}
	rollbacks := []struct {
		target string
		fn     func() error
	}{}
	for _, target := range targets {
		var rollback func() error
		var applyErr error
		switch target {
		case "web":
			rollback, applyErr = s.applyWebCertificate(r.Context(), certPath, keyPath)
		case "postfix":
			rollback, applyErr = s.applyPostfixCertificate(r.Context(), mailCertPath, mailKeyPath, previous["postfix"])
		case "dovecot":
			rollback, applyErr = s.applyDovecotCertificate(r.Context(), mailCertPath, mailKeyPath)
		}
		if applyErr != nil {
			for i := len(rollbacks) - 1; i >= 0; i-- {
				if err := rollbacks[i].fn(); err != nil {
					s.logger.Warn("TLS assignment rollback failed", "target", rollbacks[i].target, "err", err)
				}
			}
			_, _ = s.db.Exec(r.Context(), `
				UPDATE control.tls_assignments SET status='rolled_back', detail=$3
				WHERE certificate_name=$1 AND target = ANY($2)
			`, name, targets, truncateText(applyErr.Error(), 500))
			s.actorAudit(r, sess, "tls.assign_failed", name, "Certificate assignment failed and prior targets were rolled back",
				map[string]any{"targets": targets, "error": applyErr.Error(), "applied": applied})
			writeError(w, http.StatusBadGateway, "ASSIGN", "Could not assign the certificate: "+applyErr.Error())
			return
		}
		rollbacks = append(rollbacks, struct {
			target string
			fn     func() error
		}{target: target, fn: rollback})
		applied = append(applied, target)
		_, _ = s.db.Exec(r.Context(), `
			INSERT INTO control.tls_assignments
			  (certificate_name,target,cert_path,key_path,previous_cert_path,previous_key_path,status,detail,applied_by,applied_at)
			VALUES ($1,$2,$3,$4,$5,$6,'applied','', $7, now())
			ON CONFLICT (certificate_name,target) DO UPDATE SET
			  cert_path=EXCLUDED.cert_path,key_path=EXCLUDED.key_path,previous_cert_path=EXCLUDED.previous_cert_path,
			  previous_key_path=EXCLUDED.previous_key_path,status='applied',detail='',applied_by=EXCLUDED.applied_by,applied_at=now()
		`, name, target, certPath, keyPath, previous[target].CertPath, previous[target].KeyPath, sess.User.ID)
	}
	// Any other stored assignment for these targets is now stale.
	_, _ = s.db.Exec(r.Context(), `
		UPDATE control.tls_assignments SET status='rolled_back', detail='superseded by '||$1
		WHERE target = ANY($2) AND certificate_name <> $1 AND status='applied'
	`, name, targets)
	_ = s.probe.serviceStatuses(r.Context(), true)
	s.actorAudit(r, sess, "tls.assigned", name, "Assigned a certificate to services",
		map[string]any{"targets": applied, "certPath": certPath, "issuer": info.Issuer, "notAfter": info.NotAfter})
	s.notifyEvent(r.Context(), "ssl.assigned", map[string]any{"certificate": name, "targets": applied, "notAfter": info.NotAfter})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "certificate": info, "targets": applied,
		"message": "Certificate paths updated and the affected services reloaded.",
	})
}

func (s *Server) findNginxConfig() (string, string, error) {
	patterns := []string{
		filepath.Join("/etc/nginx/sites-enabled", "*"),
		filepath.Join("/etc/nginx/conf.d", "*.conf"),
	}
	publicHost := ""
	if parsed, err := url.Parse(s.cfg.PublicURL); err == nil {
		publicHost = strings.ToLower(parsed.Hostname())
	}
	type candidate struct {
		path, content string
		score         int
	}
	candidates := []candidate{}
	for _, pattern := range patterns {
		paths, _ := filepath.Glob(pattern)
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			resolved := path
			if target, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
				resolved = target
			}
			raw, err := os.ReadFile(resolved)
			if err != nil {
				continue
			}
			content := string(raw)
			if !strings.Contains(content, "ssl_certificate") {
				continue
			}
			score := 1
			if publicHost != "" && strings.Contains(strings.ToLower(content), publicHost) {
				score = 10
			}
			if strings.Contains(content, "server_name") {
				score++
			}
			candidates = append(candidates, candidate{path: resolved, content: content, score: score})
		}
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("no nginx site with TLS directives was found")
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	return candidates[0].path, candidates[0].content, nil
}

func nginxTLSPaths(content string) (string, string) {
	certRE := regexp.MustCompile(`(?m)^\s*ssl_certificate\s+([^;]+);`)
	keyRE := regexp.MustCompile(`(?m)^\s*ssl_certificate_key\s+([^;]+);`)
	cert := ""
	key := ""
	if match := certRE.FindStringSubmatch(content); len(match) == 2 {
		cert = strings.Trim(strings.TrimSpace(match[1]), `"'`)
	}
	if match := keyRE.FindStringSubmatch(content); len(match) == 2 {
		key = strings.Trim(strings.TrimSpace(match[1]), `"'`)
	}
	return cert, key
}

func replaceNginxDirective(content, directive, value string) (string, int) {
	re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(directive) + `\s+)([^;]+);`)
	count := 0
	updated := re.ReplaceAllStringFunc(content, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		count++
		return parts[1] + value + ";"
	})
	return updated, count
}

func (s *Server) applyWebCertificate(ctx context.Context, certPath, keyPath string) (func() error, error) {
	configPath, content, err := s.findNginxConfig()
	if err != nil {
		return nil, err
	}
	updated, certCount := replaceNginxDirective(content, "ssl_certificate", certPath)
	updated, keyCount := replaceNginxDirective(updated, "ssl_certificate_key", keyPath)
	if certCount == 0 || keyCount == 0 {
		return nil, fmt.Errorf("nginx configuration has no replaceable TLS directives")
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(configPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if _, err := writeManagedFile(configPath, updated, mode, 0, 0); err != nil {
		return nil, fmt.Errorf("write nginx configuration: %w", err)
	}
	restore := func() error {
		if _, err := writeManagedFile(configPath, content, mode, 0, 0); err != nil {
			return err
		}
		_, _ = runCommand(context.Background(), 30*time.Second, "systemctl", "reload", "nginx")
		return nil
	}
	if result, err := runCommand(ctx, 30*time.Second, "nginx", "-t"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("nginx -t rejected the certificate paths: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	if result, err := runCommand(ctx, 30*time.Second, "systemctl", "reload", "nginx"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("nginx reload failed: %s", strings.TrimSpace(result.Stderr))
	}
	return restore, nil
}

func (s *Server) applyPostfixCertificate(ctx context.Context, certPath, keyPath string, previous tlsTargetState) (func() error, error) {
	if result, err := postconf(ctx, "smtpd_tls_cert_file="+certPath, "smtpd_tls_key_file="+keyPath); err != nil {
		return nil, fmt.Errorf("postconf failed: %s", strings.TrimSpace(result.Stderr))
	}
	restore := func() error {
		args := []string{}
		if previous.CertPath != "" {
			args = append(args, "smtpd_tls_cert_file="+previous.CertPath)
		}
		if previous.KeyPath != "" {
			args = append(args, "smtpd_tls_key_file="+previous.KeyPath)
		}
		if len(args) > 0 {
			_, _ = postconf(context.Background(), args...)
		}
		_, _ = runCommand(context.Background(), 30*time.Second, "postfix", "reload")
		return nil
	}
	if result, err := runCommand(ctx, 30*time.Second, "postfix", "check"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("postfix check rejected the certificate paths: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	if result, err := runCommand(ctx, 30*time.Second, "postfix", "reload"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("postfix reload failed: %s", strings.TrimSpace(result.Stderr))
	}
	return restore, nil
}

// prepareMailTLSMaterial makes the private key readable by the mail daemons.
// It first uses ACLs when the source file is inside a panel-writable config
// directory. If the source is outside the writable sandbox (or ACLs are not
// available), it stages a copy under /etc/ssl/alt8mail/services. The returned
// paths are what Postfix and Dovecot should actually load.
func (s *Server) prepareMailTLSMaterial(ctx context.Context, certPath, keyPath string, targets map[string]bool) (string, string, error) {
	// Dovecot's master process reads TLS material as root before dropping
	// privileges (its private directory is typically root-only), so only
	// Postfix's unprivileged smtpd needs an explicit key access check.
	services := []string{}
	if targets["postfix"] {
		services = append(services, "postfix")
	}
	if len(services) == 0 {
		return certPath, keyPath, nil
	}
	needsAccess := false
	for _, service := range services {
		if !s.tlsKeyReadable(ctx, service, keyPath) {
			needsAccess = true
			break
		}
	}
	if !needsAccess {
		return certPath, keyPath, nil
	}
	if s.tlsKeyWritablePath(keyPath) {
		if pathWithin(s.cfg.CertbotDir, keyPath) {
			if err := s.ensureTLSACLDeployHook(); err != nil {
				s.logger.Warn("could not install certbot ACL deploy hook", "err", err)
			}
		}
		s.grantTLSKeyTraversal(ctx, "postfix", keyPath)
		result, err := runCommand(ctx, 10*time.Second, "setfacl", "-m", "u:postfix:r", "-m", "u:dovecot:r", keyPath)
		if err == nil && s.allTLSKeysReadable(ctx, services, keyPath) {
			return certPath, keyPath, nil
		}
		s.logger.Warn("setfacl could not grant mail key access; staging a service copy",
			"key", keyPath, "err", err, "stderr", strings.TrimSpace(result.Stderr))
	}
	stagedCert, stagedKey, err := s.stageMailTLSMaterial(certPath, keyPath)
	if err != nil {
		return "", "", err
	}
	s.grantTLSKeyTraversal(ctx, "postfix", stagedKey)
	if _, err := runCommand(ctx, 10*time.Second, "setfacl", "-m", "u:postfix:r", "-m", "u:dovecot:r", stagedKey); err != nil {
		// The staged copy lives in a writable directory. As a final fallback,
		// make it readable by the postfix group; Dovecot's master reads TLS
		// material as root on standard Debian installations.
		if group, groupErr := user.LookupGroup("postfix"); groupErr == nil {
			if gid, parseErr := strconv.Atoi(group.Gid); parseErr == nil {
				_ = os.Chown(stagedKey, 0, gid)
			}
		}
	}
	if !s.allTLSKeysReadable(ctx, services, stagedKey) {
		return "", "", fmt.Errorf("the private key could not be made readable by %s", strings.Join(services, " and "))
	}
	return stagedCert, stagedKey, nil
}

// grantTLSKeyTraversal gives the mail user execute (search) permission on each
// parent directory that blocks it. A readable file is not enough when the key
// sits below a root-only 0700 directory such as /etc/ssl/alt8mail/imported.
func (s *Server) grantTLSKeyTraversal(ctx context.Context, username, keyPath string) {
	dir := filepath.Dir(keyPath)
	for dir != "" && dir != "." && dir != string(os.PathSeparator) {
		if !s.directorySearchable(ctx, username, dir) {
			_, _ = runCommand(ctx, 8*time.Second, "setfacl", "-m", "u:"+username+":x", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func (s *Server) directorySearchable(ctx context.Context, username, dir string) bool {
	if _, err := user.Lookup(username); err != nil {
		return false
	}
	_, err := runCommand(ctx, 8*time.Second, "runuser", "-u", username, "--", "test", "-x", dir)
	return err == nil
}

func (s *Server) allTLSKeysReadable(ctx context.Context, services []string, keyPath string) bool {
	for _, service := range services {
		if !s.tlsKeyReadable(ctx, service, keyPath) {
			return false
		}
	}
	return true
}

func (s *Server) tlsKeyReadable(ctx context.Context, service, keyPath string) bool {
	if _, err := user.Lookup(service); err != nil {
		return false
	}
	_, err := runCommand(ctx, 8*time.Second, "runuser", "-u", service, "--", "test", "-r", keyPath)
	return err == nil
}

func (s *Server) tlsKeyWritablePath(keyPath string) bool {
	for _, root := range []string{s.cfg.SSLDir, s.cfg.CertbotDir, s.cfg.DovecotDir, s.cfg.PostfixDir} {
		if root != "" && pathWithin(root, keyPath) {
			return true
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(cleanRoot, cleanPath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func (s *Server) stageMailTLSMaterial(certPath, keyPath string) (string, string, error) {
	dir := filepath.Join(s.cfg.SSLDir, "services")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("create service certificate directory: %w", err)
	}
	certRaw, err := os.ReadFile(certPath)
	if err != nil {
		return "", "", fmt.Errorf("read certificate: %w", err)
	}
	keyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("read private key: %w", err)
	}
	certOut := filepath.Join(dir, "mail-stack.crt")
	keyOut := filepath.Join(dir, "mail-stack.key")
	if _, err := writeManagedFile(certOut, string(certRaw), 0o644, 0, 0); err != nil {
		return "", "", fmt.Errorf("write staged certificate: %w", err)
	}
	if _, err := writeManagedFile(keyOut, string(keyRaw), 0o640, 0, 0); err != nil {
		return "", "", fmt.Errorf("write staged private key: %w", err)
	}
	return certOut, keyOut, nil
}

// ensureTLSACLDeployHook keeps the Postfix/Dovecot read ACLs on certbot
// private keys after renewal replaces the archive file. Certbot runs scripts
// in renewal-hooks/deploy after a successful renewal; the hook is idempotent.
func (s *Server) ensureTLSACLDeployHook() error {
	configRoot := filepath.Dir(filepath.Clean(s.cfg.CertbotDir))
	if configRoot == "." || configRoot == "/" {
		configRoot = "/etc/letsencrypt"
	}
	hookPath := filepath.Join(configRoot, "renewal-hooks", "deploy", "alt8mail-mail-key-acl.sh")
	if pathExists(hookPath) {
		return nil
	}
	script := `#!/bin/sh
# Installed by Alt8Mail System Control. Keeps mail daemons able to read
# certificate private keys after certbot replaces them during renewal.
for key in /etc/letsencrypt/archive/*/privkey*.pem /etc/ssl/alt8mail/imported/*.key; do
  [ -f "$key" ] || continue
  setfacl -m u:postfix:r -m u:dovecot:r "$key" 2>/dev/null || true
done
`
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		return err
	}
	if _, err := writeManagedFile(hookPath, script, 0o755, 0, 0); err != nil {
		return err
	}
	return nil
}

func (s *Server) dovecotTLSManagedPath() string {
	return filepath.Join(s.cfg.DovecotDir, "conf.d", "99-alt8mail-tls.conf")
}

func (s *Server) dovecotTLSPaths(ctx context.Context) (string, string) {
	cert := strings.TrimSpace(doveconfValue(ctx, "ssl_cert"))
	key := strings.TrimSpace(doveconfValue(ctx, "ssl_key"))
	clean := func(value string) string {
		value = strings.TrimPrefix(strings.TrimSpace(value), "<")
		return strings.Trim(value, `"'`)
	}
	return clean(cert), clean(key)
}

func doveconfValue(ctx context.Context, key string) string {
	result, err := runCommand(ctx, 10*time.Second, "doveconf", "-h", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func (s *Server) applyDovecotCertificate(ctx context.Context, certPath, keyPath string) (func() error, error) {
	managedPath := s.dovecotTLSManagedPath()
	previousContent, previousErr := os.ReadFile(managedPath)
	content := "# Managed by Alt8Mail System Control. Do not edit by hand.\nssl = required\n" +
		"ssl_cert = <" + certPath + "\nssl_key = <" + keyPath + "\n"
	mode := os.FileMode(0o644)
	if info, err := os.Stat(managedPath); err == nil {
		mode = info.Mode().Perm()
	}
	if _, err := writeManagedFile(managedPath, content, mode, 0, 0); err != nil {
		return nil, fmt.Errorf("write Dovecot TLS drop-in: %w", err)
	}
	restore := func() error {
		if previousErr == nil {
			if _, err := writeManagedFile(managedPath, string(previousContent), mode, 0, 0); err != nil {
				return err
			}
		} else {
			_ = os.Remove(managedPath)
		}
		_, _ = runCommand(context.Background(), 30*time.Second, "systemctl", "reload", "dovecot")
		return nil
	}
	if result, err := runCommand(ctx, 30*time.Second, "doveconf", "-n"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("doveconf rejected the TLS drop-in: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	if result, err := runCommand(ctx, 30*time.Second, "systemctl", "reload", "dovecot"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("Dovecot reload failed: %s", strings.TrimSpace(result.Stderr))
	}
	return restore, nil
}
