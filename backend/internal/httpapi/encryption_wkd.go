package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

const maxWKDResponseBytes = 1 << 20

type contactDiscoveryResult struct {
	Email       string `json:"email"`
	KeyID       string `json:"keyId"`
	Fingerprint string `json:"fingerprint"`
	Trust       string `json:"trust"`
	KeyChanged  bool   `json:"keyChanged"`
	Warning     string `json:"warning,omitempty"`
}

func (s *Server) discoverContactCryptoKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	protocol := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	if protocol == "" {
		protocol = "openpgp"
	}
	if protocol == "smime" {
		s.discoverSMIMECertificate(w, r)
		return
	}
	if protocol != "openpgp" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_PROTOCOL", "Choose OpenPGP or S/MIME")
		return
	}
	if _, ok := s.requireMailCrypto(w, r); !ok {
		return
	}
	email := strings.TrimSpace(r.PathValue("email"))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed == nil {
		writeError(w, http.StatusBadRequest, "RECIPIENT", "Choose a valid recipient email")
		return
	}
	email = parsed.Address
	direct, advanced, err := mailcrypto.WKDURLs(email)
	if err != nil {
		writeError(w, http.StatusBadRequest, "RECIPIENT", "Choose a valid recipient email")
		return
	}
	info, sourceURL, err := s.fetchWKDKey(r.Context(), email, []string{direct, advanced})
	if err != nil {
		writeError(w, http.StatusNotFound, "WKD_NOT_FOUND", "No Web Key Directory key is published for this address")
		return
	}
	key, keyChanged, err := s.storeContactCryptoKey(r.Context(), sess, email, info.Armored, "", "wkd")
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", err.Error())
		return
	}
	warning := ""
	if keyChanged {
		warning = "The recipient published a different key. Verify the new fingerprint before encrypting."
	}
	s.audit(r, sess.User, "encryption.contact_key_discovered", email,
		"Discovered a recipient OpenPGP key through Web Key Directory · "+truncate(sourceURL, 160))
	writeJSON(w, http.StatusOK, contactDiscoveryResult{
		Email: email, KeyID: key.KeyID, Fingerprint: key.Fingerprint, Trust: key.Trust,
		KeyChanged: keyChanged, Warning: warning,
	})
}

func (s *Server) fetchWKDKey(ctx context.Context, email string, urls []string) (mailcrypto.OpenPGPKeyInfo, string, error) {
	client := s.wkdHTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("WKD redirects are not followed")
			},
		}
	}
	var lastErr error
	for _, rawURL := range urls {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" {
			lastErr = err
			continue
		}
		if parsed.Scheme != "https" && !s.wkdAllowPrivate {
			lastErr = fmt.Errorf("WKD URL is not HTTPS")
			continue
		}
		if !s.wkdHostAllowed(ctx, parsed.Hostname()) {
			lastErr = fmt.Errorf("WKD host is not a public address")
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("Accept", "application/pgp-keys, application/octet-stream")
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxWKDResponseBytes+1))
		response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if response.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("WKD key not found")
			continue
		}
		if response.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("WKD returned HTTP %d", response.StatusCode)
			continue
		}
		if len(data) > maxWKDResponseBytes {
			lastErr = fmt.Errorf("WKD response is too large")
			continue
		}
		entities, err := mailcrypto.ParseWKDResponse(data)
		if err != nil {
			lastErr = err
			continue
		}
		for _, entity := range entities {
			if !mailcrypto.KeyMatchesEmail(entity, email) {
				continue
			}
			info, err := mailcrypto.OpenPGPInfo(entity, email)
			if err != nil {
				lastErr = err
				continue
			}
			return info, rawURL, nil
		}
		lastErr = fmt.Errorf("WKD response did not contain a key for %s", email)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no WKD URL was available")
	}
	return mailcrypto.OpenPGPKeyInfo{}, "", lastErr
}

// wkdHostAllowed blocks SSRF into loopback, private, link-local and multicast
// addresses. Tests can opt into private addresses through wkdAllowPrivate.
func (s *Server) wkdHostAllowed(ctx context.Context, host string) bool {
	if s.wkdAllowPrivate {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		ip := address.IP
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return false
		}
	}
	return true
}
