package httpapi

import (
	"net/http"
	"strings"
)

// publicBaseURL returns the configured public URL, or the request origin when
// no public URL is configured.
func (s *Server) publicBaseURL(r *http.Request) string {
	if configured := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/"); configured != "" {
		return configured
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
