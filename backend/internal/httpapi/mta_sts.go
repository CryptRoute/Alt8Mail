package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxMTASTSPolicyBytes = 64 * 1024
	defaultMTASTSMaxAge  = 604800
)

type mtaSTSPolicy struct {
	Domain   string
	Mode     string
	MaxAge   int
	MailHost string
}

func normalizeMTASTSMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "testing":
		return "testing"
	case "enforce":
		return "enforce"
	default:
		return "none"
	}
}

func normalizeMTASTSMaxAge(value int) int {
	if value < 86400 {
		return defaultMTASTSMaxAge
	}
	if value > 31557600 {
		return 31557600
	}
	return value
}

func (p mtaSTSPolicy) Body() []byte {
	mode := normalizeMTASTSMode(p.Mode)
	if mode == "none" {
		return nil
	}
	maxAge := normalizeMTASTSMaxAge(p.MaxAge)
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.MailHost)), ".")
	return []byte("version: STSv1\r\nmode: " + mode + "\r\nmx: " + host + "\r\nmax_age: " + strconv.Itoa(maxAge) + "\r\n")
}

// ID is the value advertised in the _mta-sts TXT record. It changes whenever
// any policy field changes, which is exactly what RFC 8461 requires.
func (p mtaSTSPolicy) ID() string {
	return hashShort(string(p.Body()) + "|" + strings.ToLower(strings.TrimSpace(p.Domain)))
}

func (p mtaSTSPolicy) URL() string {
	return "https://mta-sts." + strings.ToLower(strings.TrimSpace(p.Domain)) + "/.well-known/mta-sts.txt"
}

// serveMTASTSPolicy implements the HTTPS half of MTA-STS. Nginx must route
// mta-sts.<customer-domain> (including /.well-known/mta-sts.txt) to the API.
func (s *Server) serveMTASTSPolicy(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	domain := strings.TrimPrefix(strings.TrimSuffix(host, "."), "mta-sts.")
	if domain == host || !validMailDomain(domain) {
		http.NotFound(w, r)
		return
	}
	var mode string
	var maxAge int
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT mta_sts_mode, mta_sts_max_age
		FROM mail_domains
		WHERE domain = $1 AND status IN ('verified', 'manual') AND mta_sts_mode <> 'none'
		LIMIT 1
	`, domain).Scan(&mode, &maxAge)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	policy := mtaSTSPolicy{Domain: domain, Mode: mode, MaxAge: maxAge, MailHost: s.cfg.MailHostname}
	body := policy.Body()
	if len(body) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type mtaSTSFetcher func(context.Context, string) (string, error)

func (s *Server) fetchMTASTSPolicy(ctx context.Context, url string) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(timeoutCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "Meridian-Mail-MTASTS-Verifier/1.0")
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			// Never use an environment proxy here: the request is deliberately
			// constrained to a public IP by DialContext.
			Proxy:               nil,
			DialContext:         safePublicDialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        1,
			IdleConnTimeout:     10 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// A policy fetch must not follow a redirect to an internal address.
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("policy host returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMTASTSPolicyBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxMTASTSPolicyBytes {
		return "", fmt.Errorf("MTA-STS policy exceeds %d bytes", maxMTASTSPolicyBytes)
	}
	return string(data), nil
}

// safePublicDialContext prevents a customer-controlled policy hostname from
// making the verifier connect to loopback, link-local, private or carrier-grade
// NAT addresses.
func safePublicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			lastErr = fmt.Errorf("policy host resolved to a non-public address")
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("policy host has no public address")
	}
	return nil, lastErr
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 carrier-grade NAT.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 0.0.0.0/8 and 192.0.0.0/24 protocol assignments.
		if v4[0] == 0 || (v4[0] == 192 && v4[1] == 0 && v4[2] == 0) {
			return false
		}
	}
	return true
}

func parseMTASTSPolicy(body string) (map[string]string, []string) {
	fields := map[string]string{}
	mx := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "mx" {
			mx = append(mx, strings.TrimSuffix(strings.ToLower(value), "."))
			continue
		}
		if key != "" && fields[key] == "" {
			fields[key] = value
		}
	}
	return fields, mx
}

func verifyMTASTSPolicyRecord(body, domain, mode string, maxAge int, mailHost string) error {
	if normalizeMTASTSMode(mode) == "none" {
		return nil
	}
	fields, mx := parseMTASTSPolicy(body)
	if !strings.EqualFold(fields["version"], "STSv1") {
		return fmt.Errorf("policy must declare version: STSv1")
	}
	if !strings.EqualFold(fields["mode"], normalizeMTASTSMode(mode)) {
		return fmt.Errorf("policy mode %q does not match the configured mode %q", fields["mode"], normalizeMTASTSMode(mode))
	}
	parsedMaxAge, err := strconv.Atoi(fields["max_age"])
	if err != nil || parsedMaxAge != normalizeMTASTSMaxAge(maxAge) {
		return fmt.Errorf("policy max_age does not match %d", normalizeMTASTSMaxAge(maxAge))
	}
	expectedHost := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mailHost)), ".")
	if expectedHost == "" {
		return fmt.Errorf("mail hostname is not configured")
	}
	for _, record := range mx {
		if record == expectedHost || record == "*."+expectedHost {
			return nil
		}
	}
	return fmt.Errorf("policy mx does not include %s", expectedHost)
}
