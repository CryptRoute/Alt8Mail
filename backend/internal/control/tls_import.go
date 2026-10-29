package control

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleCertificateImport accepts a CA-issued certificate and its private key
// from an external provider. The pair is validated against each other before
// anything is written, then stored under the panel's private certificate
// directory. Imported certificates appear in the normal inventory and can be
// mapped to nginx, Postfix or Dovecot with POST /control/api/tls/assign.
func (s *Server) handleCertificateImport(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name        string `json:"name"`
		Certificate string `json:"certificate"`
		PrivateKey  string `json:"privateKey"`
		Passphrase  string `json:"passphrase"`
		Overwrite   bool   `json:"overwrite"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if !certNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "NAME", "Use a certificate name with letters, digits, dots, underscores or hyphens")
		return
	}
	storedName := "import-" + strings.TrimPrefix(name, "import-")
	if len(storedName) > 200 || !certNamePattern.MatchString(storedName) {
		writeError(w, http.StatusBadRequest, "NAME", "Certificate name is too long")
		return
	}
	certs, certPEM, err := parseCertificateChain(body.Certificate)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CERTIFICATE", err.Error())
		return
	}
	key, keyPEM, err := parseTLSPrivateKey(body.PrivateKey, body.Passphrase)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PRIVATE_KEY", err.Error())
		return
	}
	leaf := certs[0]
	if !publicKeysEqual(leaf.PublicKey, key.Public()) {
		writeError(w, http.StatusBadRequest, "KEY_MISMATCH", "The private key does not match the leaf certificate")
		return
	}
	now := time.Now()
	if !leaf.NotAfter.After(now) {
		writeError(w, http.StatusBadRequest, "EXPIRED", "The certificate has already expired and cannot be assigned to a service")
		return
	}
	// Put the leaf first even if the provider supplied the chain in another
	// order. nginx and Postfix both expect the server certificate first.
	ordered := []*x509.Certificate{leaf}
	for _, cert := range certs[1:] {
		if cert.Equal(leaf) {
			continue
		}
		ordered = append(ordered, cert)
	}
	var chainPEM []byte
	for _, cert := range ordered {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
	}
	_ = certPEM
	dir := filepath.Join(s.cfg.SSLDir, "imported")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeFromError(w, err)
		return
	}
	certPath := filepath.Join(dir, storedName+".crt")
	keyPath := filepath.Join(dir, storedName+".key")
	if !body.Overwrite {
		if pathExists(certPath) || pathExists(keyPath) {
			writeError(w, http.StatusConflict, "EXISTS", "A certificate with that name is already imported; confirm replacement to overwrite it")
			return
		}
	} else {
		backupDir := filepath.Join(s.cfg.BackupDir, "tls-imports")
		if err := os.MkdirAll(backupDir, 0o700); err == nil {
			stamp := time.Now().UTC().Format("20060102-150405")
			for _, source := range []string{certPath, keyPath} {
				if raw, readErr := os.ReadFile(source); readErr == nil {
					_ = os.WriteFile(filepath.Join(backupDir, filepath.Base(source)+"."+stamp), raw, 0o600)
				}
			}
		}
	}
	if _, err := writeManagedFile(certPath, string(chainPEM), 0o644, 0, 0); err != nil {
		writeFromError(w, err)
		return
	}
	if _, err := writeManagedFile(keyPath, string(keyPEM), 0o600, 0, 0); err != nil {
		_ = os.Remove(certPath)
		writeFromError(w, err)
		return
	}
	info, infoErr := readCertificate(certPath, keyPath, storedName)
	if infoErr != nil {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
		writeError(w, http.StatusBadRequest, "CERTIFICATE", "The stored certificate could not be read: "+infoErr.Error())
		return
	}
	_, _ = s.scanCertificates(r.Context())
	s.actorAudit(r, sess, "tls.imported", storedName, "Imported a certificate and matching private key",
		map[string]any{"cert": certPath, "issuer": info.Issuer, "sans": info.SANs, "notAfter": info.NotAfter})
	s.notifyEvent(r.Context(), "ssl.imported", map[string]any{"certificate": storedName, "notAfter": info.NotAfter, "sans": info.SANs})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "certificate": info, "certPath": certPath, "keyPath": keyPath,
		"message": "Certificate imported. Assign it to a service when you are ready to reload TLS."})
}

func parseCertificateChain(raw string) ([]*x509.Certificate, []byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, fmt.Errorf("paste the PEM certificate (including any intermediate chain)")
	}
	rest := []byte(raw)
	certs := []*x509.Certificate{}
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, nil, fmt.Errorf("no PEM certificate block was found")
	}
	return certs, rest, nil
}

func parseTLSPrivateKey(raw, passphrase string) (crypto.Signer, []byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, fmt.Errorf("paste the PEM private key that matches the certificate")
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, nil, fmt.Errorf("no PEM private key block was found")
	}
	keyBytes := block.Bytes
	if x509.IsEncryptedPEMBlock(block) {
		if passphrase == "" {
			return nil, nil, fmt.Errorf("this private key is encrypted; enter its passphrase")
		}
		decrypted, err := x509.DecryptPEMBlock(block, []byte(passphrase))
		if err != nil {
			return nil, nil, fmt.Errorf("could not decrypt the private key: %w", err)
		}
		keyBytes = decrypted
	}
	var parsed any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(keyBytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(keyBytes)
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(keyBytes)
	case "ENCRYPTED PRIVATE KEY":
		return nil, nil, fmt.Errorf("PKCS#8 encrypted keys are not supported; decrypt the key to PKCS#8 PEM and try again")
	default:
		return nil, nil, fmt.Errorf("unsupported private key type %q; use RSA, ECDSA or PKCS#8 PEM", block.Type)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid private key: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("the private key type cannot sign TLS handshakes")
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(parsed)
	if err != nil {
		return nil, nil, fmt.Errorf("could not normalize the private key: %w", err)
	}
	return signer, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), nil
}

func publicKeysEqual(left, right any) bool {
	leftDER, leftErr := x509.MarshalPKIXPublicKey(left)
	rightDER, rightErr := x509.MarshalPKIXPublicKey(right)
	return leftErr == nil && rightErr == nil && string(leftDER) == string(rightDER)
}

// certificateMatchesHost is used by the assignment UI to warn about name
// mismatches without refusing a deliberate internal deployment.
func certificateMatchesHost(cert *x509.Certificate, host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	for _, name := range cert.DNSNames {
		if strings.EqualFold(name, host) {
			return true
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, candidate := range cert.IPAddresses {
			if candidate.Equal(ip) {
				return true
			}
		}
	}
	return false
}
