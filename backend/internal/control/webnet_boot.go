package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// reconcileWebNetAtStartup restores runtime-only addresses and the managed
// nginx configuration after a reboot or an installer/update that replaced the
// site file. It never restarts the webmail or admin applications; nginx is
// reloaded (or started when a boot-time bind failure left it inactive).
func (s *Server) reconcileWebNetAtStartup(ctx context.Context) {
	s.webNetMu.Lock()
	defer s.webNetMu.Unlock()
	s.restoreManagedNetworkAddresses(ctx)

	settings, err := s.loadWebNetSettings(ctx)
	if err != nil || settings.AppliedRevision <= 0 {
		return
	}
	routes, err := s.loadWebNetRoutes(ctx)
	if err != nil || len(routes) == 0 {
		return
	}
	webmail, ok := webNetPrimaryRoute(routes, "webmail")
	if !ok {
		s.logger.Warn("web net startup reconcile skipped", "reason", "no webmail primary route")
		return
	}
	_, _ = s.scanCertificates(ctx)
	certs, _ := s.webNetCertificates(ctx)
	local, err := s.webNetLocalIPs(ctx)
	if err != nil {
		s.logger.Warn("web net startup reconcile skipped", "reason", err)
		return
	}
	certByRoute := map[string]webNetCertificate{}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		cert, certErr := s.webNetPickCertificate(route, certs)
		if certErr != nil {
			s.logger.Warn("web net startup reconcile skipped", "hostname", route.Hostname, "reason", certErr)
			return
		}
		certByRoute[route.Hostname] = cert
	}
	fallback := certByRoute[webmail.Hostname]
	if mailCert, mailErr := s.webNetMailCertificate(s.mailHostname(ctx), certs); mailErr == nil {
		fallback = mailCert
	}
	config, err := s.renderWebNetNginx(settings, routes, certByRoute, fallback, local, s.mailHostname(ctx))
	if err != nil {
		s.logger.Warn("web net startup reconcile skipped", "reason", err)
		return
	}
	pages := s.renderWebNetPages(settings, "https://"+webmail.Hostname)
	oldConfig, exists := s.readOptionalFile(s.cfg.NginxSitePath)
	oldLogFormat, logFormatExists := s.readOptionalFile(s.cfg.NginxLogFormat)
	oldPages := map[string]*string{}
	for name := range pages {
		existing, pageExists := s.readOptionalFile(filepath.Join(s.cfg.WebNetRoot, name))
		if pageExists {
			oldPages[name] = &existing
		} else {
			oldPages[name] = nil
		}
	}
	if _, err := s.applyNginxWebNet(config, oldConfig, exists, oldLogFormat, logFormatExists, pages, oldPages); err != nil {
		s.logger.Warn("web net startup reconcile failed", "err", err)
		return
	}
	s.logger.Info("reconciled the Web Net managed nginx configuration")
	if err := s.verifyServedCertificatesWithRetry(ctx, routes, certByRoute, local); err != nil {
		s.logger.Warn("web net served-certificate check failed", "err", err)
		_, _ = s.db.Exec(ctx, `UPDATE control.webnet_settings SET last_error=$1 WHERE id=true`,
			truncateText("startup certificate check: "+err.Error(), 1000))
	}
}

func (s *Server) restoreManagedNetworkAddresses(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
		SELECT interface_name,address_cidr
		FROM control.network_addresses WHERE active ORDER BY created_at
	`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var iface, cidr string
		if rows.Scan(&iface, &cidr) != nil {
			continue
		}
		if !interfacePattern.MatchString(iface) {
			continue
		}
		if _, _, parseErr := net.ParseCIDR(cidr); parseErr != nil {
			continue
		}
		result, err := runCommand(ctx, 20*time.Second, "ip", "addr", "replace", cidr, "dev", iface)
		if err != nil {
			s.logger.Warn("could not restore managed network address", "interface", iface, "address", cidr,
				"detail", strings.TrimSpace(result.Stderr))
		}
	}
}

func (s *Server) reloadOrStartNginx(ctx context.Context) error {
	if _, err := runCommand(ctx, 20*time.Second, "systemctl", "is-active", "--quiet", "nginx"); err == nil {
		result, err := runCommand(ctx, 30*time.Second, "systemctl", "reload", "nginx")
		if err != nil {
			return fmt.Errorf("nginx reload failed: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
		}
		return nil
	}
	result, err := runCommand(ctx, 30*time.Second, "systemctl", "restart", "nginx")
	if err != nil {
		return fmt.Errorf("nginx start failed: %s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	return nil
}

func (s *Server) verifyServedCertificatesWithRetry(ctx context.Context, routes []webNetRoute, certByRoute map[string]webNetCertificate, local []webNetIP) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = s.verifyWebNetServedCertificates(ctx, routes, certByRoute, local)
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return lastErr
}

func (s *Server) verifyWebNetServedCertificates(ctx context.Context, routes []webNetRoute, certByRoute map[string]webNetCertificate, local []webNetIP) error {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		cert, ok := certByRoute[route.Hostname]
		if !ok {
			return fmt.Errorf("no certificate is assigned to %s", route.Hostname)
		}
		expected, err := readLeafCertificate(cert.CertPath)
		if err != nil {
			return fmt.Errorf("read %s certificate: %w", route.Hostname, err)
		}
		addresses, err := s.webNetRouteAddresses(route, routes, local)
		if err != nil {
			return err
		}
		matched := false
		var lastErr error
		for _, address := range addresses {
			rawConnection, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, "443"))
			if dialErr != nil {
				lastErr = dialErr
				continue
			}
			connection := tls.Client(rawConnection, &tls.Config{
				ServerName: route.Hostname, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
			})
			if handshakeErr := connection.HandshakeContext(ctx); handshakeErr != nil {
				lastErr = handshakeErr
				_ = connection.Close()
				continue
			}
			state := connection.ConnectionState()
			_ = connection.Close()
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("nginx served no certificate for %s on %s", route.Hostname, address)
			}
			if !bytes.Equal(state.PeerCertificates[0].Raw, expected.Raw) {
				return fmt.Errorf("nginx served the wrong certificate for %s on %s (expected %s)", route.Hostname, address, cert.Name)
			}
			matched = true
		}
		if !matched {
			if lastErr == nil {
				lastErr = fmt.Errorf("no nginx listener accepted a TLS connection for %s", route.Hostname)
			}
			return lastErr
		}
	}
	return nil
}

func readLeafCertificate(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
