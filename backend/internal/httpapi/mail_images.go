package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxRemoteImageBytes = 8 * 1024 * 1024

var remoteImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true,
	"image/webp": true, "image/avif": true, "image/bmp": true,
	"image/x-icon": true,
}

func publicRemoteIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsPrivate()
}

func validateRemoteImageURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid image URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https image URLs are supported")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("image URLs cannot contain credentials")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != "80" && port != "443" {
		return nil, fmt.Errorf("only image URLs on ports 80 and 443 are supported")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("local image URLs are not allowed")
	}
	if literal := net.ParseIP(host); literal != nil && !publicRemoteIP(literal) {
		return nil, fmt.Errorf("private image URLs are not allowed")
	}
	return parsed, nil
}

// mailRemoteImage proxies remote email images through Alt8Mail instead of
// letting the reader contact tracking hosts directly. It applies a strict
// public-IP dialer (including DNS rebinding protection), a raster-only content
// policy and a bounded response size.
func (s *Server) mailRemoteImage(w http.ResponseWriter, r *http.Request) {
	parsed, err := validateRemoteImageURL(r.URL.Query().Get("url"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMAGE_URL", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 8 * time.Second}
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
			return nil, fmt.Errorf("image host did not resolve to a public address")
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many image redirects")
			}
			if _, err := validateRemoteImageURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMAGE_URL", "The image URL could not be loaded")
		return
	}
	request.Header.Set("User-Agent", "Alt8Mail-Image-Proxy/1.0")
	request.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp,image/avif,image/bmp,image/x-icon")
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "IMAGE_FETCH", "The remote image could not be loaded")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "IMAGE_FETCH", "The remote image host returned an error")
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !remoteImageTypes[contentType] {
		writeError(w, http.StatusUnsupportedMediaType, "IMAGE_TYPE", "Only raster email images can be displayed")
		return
	}
	limited := io.LimitReader(response.Body, maxRemoteImageBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		writeError(w, http.StatusBadGateway, "IMAGE_FETCH", "The remote image could not be read")
		return
	}
	if len(data) > maxRemoteImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "IMAGE_SIZE", "The remote image is too large to display")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
