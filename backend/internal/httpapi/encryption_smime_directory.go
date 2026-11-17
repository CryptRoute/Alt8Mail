package httpapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
	"github.com/go-ldap/ldap/v3"
)

func (s *Server) smimeDirectoryConfigured() bool {
	return strings.TrimSpace(s.cfg.SMIMELDAPURL) != "" && strings.TrimSpace(s.cfg.SMIMELDAPBaseDN) != ""
}

func (s *Server) discoverSMIMECertificate(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, ok := s.requireMailCryptoProtocol(w, r, "smime"); !ok {
		return
	}
	email := strings.TrimSpace(r.PathValue("email"))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed == nil {
		writeError(w, http.StatusBadRequest, "RECIPIENT", "Choose a valid recipient email")
		return
	}
	email = parsed.Address
	certs, sourceURL, err := s.fetchSMIMECertificatesFromDirectory(r.Context(), email)
	if err != nil {
		status := http.StatusBadGateway
		code := "SMIME_DIRECTORY"
		message := "The S/MIME directory could not return a certificate"
		if errors.Is(err, errSMIMEDirectoryDisabled) {
			status = http.StatusConflict
			message = "S/MIME directory discovery is not configured"
		} else if errors.Is(err, errSMIMECertificateNotFound) {
			status = http.StatusNotFound
			code = "SMIME_NOT_FOUND"
			message = "No S/MIME certificate was found for this address"
		}
		writeError(w, status, code, message)
		return
	}
	leaf, chain := selectSMIMELeaf(certs, nil, email)
	if leaf == nil {
		writeError(w, http.StatusNotFound, "SMIME_NOT_FOUND", "The directory returned no certificate for this address")
		return
	}
	info, err := mailcrypto.CertificateInfoFor(leaf, chain)
	if err != nil {
		writeError(w, http.StatusBadGateway, "SMIME_DIRECTORY", "The directory returned an invalid certificate")
		return
	}
	key, keyChanged, err := s.storeContactSMIMECertificate(r.Context(), sess, email,
		string(info.CertificatePEM), string(info.ChainPEM), "enterprise", "ldap")
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", err.Error())
		return
	}
	warning := ""
	if keyChanged {
		warning = "The directory returned a different certificate. Verify the new fingerprint before encrypting."
	}
	s.audit(r, sess.User, "encryption.smime_certificate_discovered", email,
		"Discovered an S/MIME certificate through the directory · "+truncate(sourceURL, 160))
	writeJSON(w, http.StatusOK, contactDiscoveryResult{
		Email: email, KeyID: key.KeyID, Fingerprint: key.Fingerprint, Trust: key.Trust,
		KeyChanged: keyChanged, Warning: warning,
	})
}

var (
	errSMIMEDirectoryDisabled   = errors.New("S/MIME directory is not configured")
	errSMIMECertificateNotFound = errors.New("S/MIME certificate not found")
)

func (s *Server) fetchSMIMECertificatesFromDirectory(ctx context.Context, email string) ([]*x509.Certificate, string, error) {
	if !s.smimeDirectoryConfigured() {
		return nil, "", errSMIMEDirectoryDisabled
	}
	rawURL := strings.TrimSpace(s.cfg.SMIMELDAPURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return nil, "", fmt.Errorf("invalid S/MIME LDAP URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "ldap" && scheme != "ldaps" {
		return nil, "", fmt.Errorf("S/MIME LDAP URL must use ldap or ldaps")
	}
	if scheme == "ldap" && s.cfg.SMIMELDAPInsecure && s.cfg.Env == "production" {
		return nil, "", fmt.Errorf("insecure S/MIME LDAP is not allowed in production; use StartTLS or ldaps")
	}
	tlsConfig := &tls.Config{ServerName: parsed.Hostname(), MinVersion: tls.VersionTLS12}
	options := []ldap.DialOpt{
		ldap.DialWithDialer(&net.Dialer{Timeout: 6 * time.Second}),
		ldap.DialWithTLSConfig(tlsConfig),
	}
	conn, err := ldap.DialURL(rawURL, options...)
	if err != nil {
		return nil, "", fmt.Errorf("connect to S/MIME directory: %w", err)
	}
	defer conn.Close()
	conn.SetTimeout(8 * time.Second)
	if scheme == "ldap" && !s.cfg.SMIMELDAPInsecure {
		if err := conn.StartTLS(tlsConfig); err != nil {
			return nil, "", fmt.Errorf("start S/MIME directory TLS: %w", err)
		}
	}
	if strings.TrimSpace(s.cfg.SMIMELDAPBindDN) != "" {
		if err := conn.Bind(s.cfg.SMIMELDAPBindDN, s.cfg.SMIMELDAPBindPassword); err != nil {
			return nil, "", fmt.Errorf("bind to S/MIME directory: %w", err)
		}
	}
	filter := strings.TrimSpace(s.cfg.SMIMELDAPFilter)
	if filter == "" {
		filter = "(mail=%s)"
	}
	filter = strings.ReplaceAll(filter, "%s", ldap.EscapeFilter(strings.ToLower(email)))
	attribute := strings.TrimSpace(s.cfg.SMIMELDAPAttribute)
	if attribute == "" {
		attribute = "userCertificate;binary"
	}
	request := ldap.NewSearchRequest(
		s.cfg.SMIMELDAPBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 20, 8, false,
		filter, []string{attribute, "userSMIMECertificate;binary"}, nil,
	)
	result, err := conn.Search(request)
	if err != nil {
		return nil, "", fmt.Errorf("search S/MIME directory: %w", err)
	}
	if len(result.Entries) == 0 {
		return nil, "", errSMIMECertificateNotFound
	}
	certs := []*x509.Certificate{}
	for _, entry := range result.Entries {
		for _, name := range []string{attribute, "userSMIMECertificate;binary"} {
			for _, value := range entry.GetRawAttributeValues(name) {
				parsedCerts, parseErr := mailcrypto.ParseSMIMECertificates(value)
				if parseErr != nil {
					continue
				}
				for _, cert := range parsedCerts {
					certs = append(certs, cert)
				}
			}
		}
	}
	if len(certs) == 0 {
		return nil, "", errSMIMECertificateNotFound
	}
	return certs, parsed.Redacted(), nil
}
