package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	maxSenderAvatarBytes    = 2 * 1024 * 1024
	senderAvatarCacheTTL    = 24 * time.Hour
	senderAvatarNegativeTTL = 2 * time.Hour
	senderAvatarCacheLimit  = 1024
)

type senderAvatarCacheEntry struct {
	Data        []byte
	ContentType string
	Expires     time.Time
	Found       bool
}

var senderAvatarTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/pjpeg": true, "image/gif": true,
	"image/webp": true, "image/avif": true, "image/bmp": true, "image/x-icon": true,
	"image/vnd.microsoft.icon": true, "image/svg+xml": true,
}

func validAvatarDomain(value string) bool {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") || net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

// parseBIMILogoURL extracts the logo URL from a default._bimi TXT record.
// Only HTTPS URLs are accepted; the authority (a=) is intentionally not
// required because Alt8Mail displays the supplied logo without making a VMC
// claim to the reader.
func parseBIMILogoURL(records []string) string {
	for _, record := range records {
		tags := map[string]string{}
		for _, part := range strings.Split(record, ";") {
			key, value, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			tags[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
		if !strings.EqualFold(tags["v"], "BIMI1") {
			continue
		}
		logo := strings.TrimSpace(tags["l"])
		if logo == "" || !strings.HasPrefix(strings.ToLower(logo), "https://") {
			continue
		}
		if _, err := validateRemoteImageURL(logo); err == nil {
			return logo
		}
	}
	return ""
}

func dmarcPolicyEnforced(records []string) bool {
	for _, record := range records {
		for _, part := range strings.Split(record, ";") {
			key, value, ok := strings.Cut(part, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "p") {
				continue
			}
			policy := strings.ToLower(strings.TrimSpace(value))
			if policy == "quarantine" || policy == "reject" {
				return true
			}
		}
	}
	return false
}

func (s *Server) cachedSenderAvatar(domain string) (senderAvatarCacheEntry, bool) {
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	entry, ok := s.avatarCache[domain]
	if !ok || time.Now().After(entry.Expires) {
		if ok {
			delete(s.avatarCache, domain)
		}
		return senderAvatarCacheEntry{}, false
	}
	return entry, true
}

func (s *Server) storeSenderAvatar(domain string, entry senderAvatarCacheEntry) {
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	now := time.Now()
	if len(s.avatarCache) >= senderAvatarCacheLimit {
		for key, cached := range s.avatarCache {
			if now.After(cached.Expires) {
				delete(s.avatarCache, key)
			}
		}
		if len(s.avatarCache) >= senderAvatarCacheLimit {
			s.avatarCache = map[string]senderAvatarCacheEntry{}
		}
	}
	s.avatarCache[domain] = entry
}

func (s *Server) senderAvatar(w http.ResponseWriter, r *http.Request) {
	email, domain := splitEmail(r.URL.Query().Get("address"))
	if email == "" || !validAvatarDomain(domain) {
		writeError(w, http.StatusBadRequest, "AVATAR_ADDRESS", "Provide a valid sender address")
		return
	}
	domain = strings.ToLower(domain)
	if entry, ok := s.cachedSenderAvatar(domain); ok {
		s.writeSenderAvatar(w, r, entry)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	data, contentType := s.resolveSenderAvatar(ctx, domain)
	entry := senderAvatarCacheEntry{
		Data:        data,
		ContentType: contentType,
		Found:       len(data) > 0 && contentType != "",
	}
	if entry.Found {
		entry.Expires = time.Now().Add(senderAvatarCacheTTL)
	} else {
		entry.Expires = time.Now().Add(senderAvatarNegativeTTL)
	}
	s.storeSenderAvatar(domain, entry)
	s.writeSenderAvatar(w, r, entry)
}

func (s *Server) writeSenderAvatar(w http.ResponseWriter, r *http.Request, entry senderAvatarCacheEntry) {
	if !entry.Found {
		w.Header().Set("Cache-Control", "private, max-age=300")
		writeError(w, http.StatusNotFound, "AVATAR_NOT_FOUND", "No sender image is published for this domain")
		return
	}
	w.Header().Set("Content-Type", entry.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(entry.Data)))
	if entry.ContentType == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(entry.Data)
	}
}

func (s *Server) resolveSenderAvatar(ctx context.Context, domain string) ([]byte, string) {
	if strings.EqualFold(domain, strings.TrimSuffix(strings.ToLower(s.cfg.MailDomain), ".")) ||
		strings.EqualFold(domain, strings.TrimSuffix(strings.ToLower(s.cfg.MailHostname), ".")) {
		return nil, ""
	}

	records, err := net.DefaultResolver.LookupTXT(ctx, "_dmarc."+domain)
	if err == nil && dmarcPolicyEnforced(records) {
		if bimi, err := net.DefaultResolver.LookupTXT(ctx, "default._bimi."+domain); err == nil {
			if logo := parseBIMILogoURL(bimi); logo != "" {
				if data, contentType := fetchSenderAvatar(ctx, logo); len(data) > 0 {
					return data, contentType
				}
			}
		}
	}

	// A favicon is a useful fallback for organizations without BIMI. It still
	// goes through the same public-IP-only fetcher and is cached by domain.
	if data, contentType := fetchSenderAvatar(ctx, "https://"+domain+"/favicon.ico"); len(data) > 0 {
		return data, contentType
	}
	return nil, ""
}

func fetchSenderAvatar(ctx context.Context, rawURL string) ([]byte, string) {
	parsed, err := validateRemoteImageURL(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, ""
	}
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 4 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			ips, lookupErr := resolver.LookupIPAddr(ctx, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			for _, candidate := range ips {
				if !publicRemoteIP(candidate.IP) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			}
			return nil, fmt.Errorf("sender image host did not resolve to a public address")
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   4 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   6 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return fmt.Errorf("too many sender image redirects")
			}
			if _, err := validateRemoteImageURL(req.URL.String()); err != nil {
				return err
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("sender images must use HTTPS")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, ""
	}
	request.Header.Set("User-Agent", "Alt8Mail-Sender-Avatar/1.0")
	request.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp,image/avif,image/svg+xml,image/x-icon")
	response, err := client.Do(request)
	if err != nil {
		return nil, ""
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, ""
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !senderAvatarTypes[contentType] {
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSenderAvatarBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxSenderAvatarBytes {
		return nil, ""
	}
	return data, contentType
}

// mailSenderAvatar is the authenticated handler used by the webmail avatar
// markup. Keeping the resolver in its own file isolates DNS and outbound HTTP
// from the rest of the mailbox API.
func (s *Server) mailSenderAvatar(w http.ResponseWriter, r *http.Request) {
	s.senderAvatar(w, r)
}
