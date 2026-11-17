package httpapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type smimeRevocationCacheEntry struct {
	status  string
	reason  string
	expires time.Time
}

const maxRevocationResponseBytes = 8 << 20

// smimeRevocationStatus checks OCSP first and CRL distribution points second.
// It returns good, revoked or unknown; network/configuration failures are
// reported as unavailable. Callers decide whether unknown/unavailable is fatal.
func (s *Server) smimeRevocationStatus(ctx context.Context, leaf *x509.Certificate, chain []*x509.Certificate) (string, string) {
	if leaf == nil {
		return "unavailable", "certificate is missing"
	}
	issuer := smimeIssuerFor(leaf, chain)
	if issuer == nil {
		return "unavailable", "issuer certificate is not available"
	}
	key := fmt.Sprintf("%s|%s", leaf.Issuer.String(), leaf.SerialNumber.String())
	if s.revocationCache != nil {
		s.revocationMu.Lock()
		entry, ok := s.revocationCache[key]
		s.revocationMu.Unlock()
		if ok && time.Now().Before(entry.expires) {
			return entry.status, entry.reason
		}
	}
	status, reason := s.checkOCSP(ctx, leaf, issuer)
	if status == "" {
		status, reason = s.checkCRL(ctx, leaf, issuer)
	}
	if status == "" {
		status, reason = "unknown", "no OCSP responder or CRL distribution point is available"
	}
	cacheFor := s.cfg.SMIMERevocationCache
	if cacheFor <= 0 {
		cacheFor = 5 * time.Minute
	}
	s.revocationMu.Lock()
	if s.revocationCache == nil {
		s.revocationCache = map[string]smimeRevocationCacheEntry{}
	}
	s.revocationCache[key] = smimeRevocationCacheEntry{status: status, reason: reason, expires: time.Now().Add(cacheFor)}
	s.revocationMu.Unlock()
	return status, reason
}

func smimeIssuerFor(leaf *x509.Certificate, chain []*x509.Certificate) *x509.Certificate {
	if leaf == nil {
		return nil
	}
	for _, candidate := range chain {
		if candidate == nil {
			continue
		}
		if !bytes.Equal(candidate.RawSubject, leaf.RawIssuer) {
			continue
		}
		if err := leaf.CheckSignatureFrom(candidate); err == nil {
			return candidate
		}
	}
	return nil
}

func (s *Server) revocationHTTPClient() *http.Client {
	if s.revocationClient != nil {
		return s.revocationClient
	}
	timeout := s.cfg.SMIMERevocationTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("revocation redirects are not followed")
		},
	}
}

func (s *Server) revocationURLAllowed(ctx context.Context, raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if s.cfg.SMIMERevocationAllowPrivate || s.wkdAllowPrivate {
		return true
	}
	return s.wkdHostAllowed(ctx, parsed.Hostname())
}

func (s *Server) fetchRevocation(ctx context.Context, method, raw string, body []byte, contentType string) ([]byte, error) {
	if !s.revocationURLAllowed(ctx, raw) {
		return nil, fmt.Errorf("revocation URL is not permitted")
	}
	request, err := http.NewRequestWithContext(ctx, method, raw, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Accept", "application/ocsp-response, application/pkix-crl, application/x-pkcs7-crl")
	response, err := s.revocationHTTPClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("revocation endpoint returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRevocationResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRevocationResponseBytes {
		return nil, fmt.Errorf("revocation response is too large")
	}
	return data, nil
}

func (s *Server) checkOCSP(ctx context.Context, leaf, issuer *x509.Certificate) (string, string) {
	urls := append(append([]string{}, leaf.OCSPServer...), issuer.OCSPServer...)
	seen := map[string]bool{}
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		requestBody, err := ocsp.CreateRequest(leaf, issuer, &ocsp.RequestOptions{Hash: crypto.SHA256})
		if err != nil {
			continue
		}
		data, err := s.fetchRevocation(ctx, http.MethodPost, raw, requestBody, "application/ocsp-request")
		if err != nil {
			continue
		}
		response, err := ocsp.ParseResponseForCert(data, leaf, issuer)
		if err != nil {
			continue
		}
		if !response.NextUpdate.IsZero() && time.Now().After(response.NextUpdate) {
			continue
		}
		switch response.Status {
		case ocsp.Good:
			return "good", ""
		case ocsp.Revoked:
			return "revoked", "certificate is revoked" + optionalReason(revocationReasonLabel(response.RevocationReason))
		default:
			return "unknown", "OCSP responder does not know the certificate"
		}
	}
	return "", ""
}

func revocationReasonLabel(value int) string {
	labels := map[int]string{
		0: "unspecified", 1: "keyCompromise", 2: "cACompromise", 3: "affiliationChanged",
		4: "superseded", 5: "cessationOfOperation", 6: "certificateHold",
		8: "removeFromCRL", 9: "privilegeWithdrawn", 10: "aACompromise",
	}
	return labels[value]
}

func optionalReason(reason string) string {
	if reason == "" || reason == "Unspecified" {
		return ""
	}
	return " (" + reason + ")"
}

func (s *Server) checkCRL(ctx context.Context, leaf, issuer *x509.Certificate) (string, string) {
	seen := map[string]bool{}
	for _, raw := range append(append([]string{}, leaf.CRLDistributionPoints...), issuer.CRLDistributionPoints...) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		data, err := s.fetchRevocation(ctx, http.MethodGet, raw, nil, "")
		if err != nil {
			continue
		}
		revocationList, err := x509.ParseRevocationList(data)
		if err != nil {
			if block, _ := pem.Decode(data); block != nil {
				revocationList, err = x509.ParseRevocationList(block.Bytes)
			}
		}
		if err != nil {
			continue
		}
		if err := revocationList.CheckSignatureFrom(issuer); err != nil {
			continue
		}
		if !revocationList.ThisUpdate.IsZero() && time.Now().Before(revocationList.ThisUpdate.Add(-5*time.Minute)) {
			continue
		}
		if !revocationList.NextUpdate.IsZero() && time.Now().After(revocationList.NextUpdate) {
			continue
		}
		for _, entry := range revocationList.RevokedCertificateEntries {
			if entry.SerialNumber != nil && entry.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
				return "revoked", "certificate is revoked" + optionalReason(revocationReasonLabel(entry.ReasonCode))
			}
		}
		return "good", ""
	}
	return "", ""
}

func (s *Server) applySMIMERevocation(ctx context.Context, check *mailcrypto.SMIMECertificateCheck, leaf *x509.Certificate, chain []*x509.Certificate) {
	if check == nil || !check.Valid || strings.ToLower(strings.TrimSpace(s.cfg.SMIMERevocationMode)) == "" ||
		strings.EqualFold(s.cfg.SMIMERevocationMode, "off") {
		return
	}
	status, reason := s.smimeRevocationStatus(ctx, leaf, chain)
	check.Revocation = status
	switch status {
	case "good":
		return
	case "revoked":
		check.Valid = false
		check.Reason = reason
	case "unknown", "unavailable":
		message := reason
		if message == "" {
			message = "certificate revocation status is unavailable"
		}
		if strings.EqualFold(s.cfg.SMIMERevocationMode, "require") {
			check.Valid = false
			check.Reason = message
		} else {
			check.Warnings = append(check.Warnings, message)
		}
	}
}
