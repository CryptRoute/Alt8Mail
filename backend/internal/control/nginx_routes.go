package control

import (
	"context"
	"os"
	"strings"
	"time"
)

const publicNginxSnippet = "/etc/nginx/snippets/a8m-public-routes.conf"

var publicNginxRoutes = []string{"/files/s/", "/upload/", "/calendar/rsvp/", "/calendar/poll/", "/calendar/feed/", "/oauth/", "/office-editor"}

func publicNginxRouteState(site, snippet string) (snippetRoutes, missing []string) {
	routePresent := func(text, route string) bool {
		return strings.Contains(text, "location ^~ "+route) || strings.Contains(text, "location "+route)
	}
	snippetRoutes = make([]string, 0, len(publicNginxRoutes))
	missing = make([]string, 0, len(publicNginxRoutes))
	for _, route := range publicNginxRoutes {
		if routePresent(site, route) {
			continue
		}
		snippetRoutes = append(snippetRoutes, route)
		if !routePresent(snippet, route) {
			missing = append(missing, route)
		}
	}
	return snippetRoutes, missing
}

func publicNginxSnippetText(routes []string, upstream string) string {
	var snippet strings.Builder
	snippet.WriteString("# Managed by Alt8Mail. Public download, upload, calendar, OAuth and Office editor routes.\n")
	for _, route := range routes {
		if route == "/office-editor" {
			snippet.WriteString(publicNginxOfficeEditorLocation(upstream))
			continue
		}
		snippet.WriteString("location ^~ " + route + " {\n")
		snippet.WriteString("        proxy_pass http://" + upstream + ";\n")
		snippet.WriteString("        proxy_http_version 1.1;\n")
		snippet.WriteString("        proxy_set_header Host $host;\n")
		snippet.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
		snippet.WriteString("        proxy_set_header X-Forwarded-For $remote_addr;\n")
		snippet.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
		if route == "/upload/" {
			snippet.WriteString("        client_max_body_size 12m;\n")
		}
		snippet.WriteString("    }\n")
	}
	return snippet.String()
}

func publicNginxOfficeEditorLocation(upstream string) string {
	return `location ^~ /office-editor {
        proxy_pass http://` + upstream + `;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
        proxy_hide_header Content-Security-Policy;
        proxy_hide_header X-Content-Type-Options;
        proxy_hide_header X-Frame-Options;
        proxy_hide_header Referrer-Policy;
        proxy_hide_header Permissions-Policy;
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
        add_header X-Content-Type-Options nosniff always;
        add_header X-Frame-Options SAMEORIGIN always;
        add_header Referrer-Policy no-referrer always;
        add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;
        add_header Content-Security-Policy "default-src 'self'; img-src 'self' data: blob: https:; style-src 'self' 'unsafe-inline' https:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob: https:; connect-src 'self' https: wss:; font-src 'self' data: https:; worker-src 'self' blob:; frame-src 'self' blob: https:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'" always;
    }
`
}

// ensurePublicNginxRoutes repairs an older nginx site that predates the public
// file-share, upload and calendar pages. Application updates intentionally do
// not overwrite administrator nginx configuration, so the first boot of a new
// release adds a small include instead. Failures are logged and never prevent
// System Control from starting.
func (s *Server) ensurePublicNginxRoutes(ctx context.Context) {
	if os.Geteuid() != 0 {
		return
	}
	// Web Net may be regenerating the same file during startup.
	s.webNetMu.Lock()
	defer s.webNetMu.Unlock()

	path := s.cfg.NginxSitePath
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	site := string(content)
	snippetContent, _ := os.ReadFile(publicNginxSnippet)
	snippetRoutes, missing := publicNginxRouteState(site, string(snippetContent))
	if len(missing) == 0 {
		return
	}
	if err := os.MkdirAll("/etc/nginx/snippets", 0o755); err != nil {
		s.logger.Warn("public route snippet directory failed", "err", err)
		return
	}
	restoreSnippet := func() {
		if len(snippetContent) > 0 {
			_ = os.WriteFile(publicNginxSnippet, snippetContent, 0o644)
		} else {
			_ = os.Remove(publicNginxSnippet)
		}
	}
	// Preserve routes already supplied by the snippet and add newly required
	// ones. Routes defined directly in the site are skipped so nginx never
	// sees duplicate location blocks.
	if err := os.WriteFile(publicNginxSnippet, []byte(publicNginxSnippetText(snippetRoutes, s.cfg.WebUpstream)), 0o644); err != nil {
		s.logger.Warn("public route snippet write failed", "err", err)
		return
	}
	if !strings.Contains(site, "include "+publicNginxSnippet+";") {
		updated, inserted := insertNginxServerInclude(site, publicNginxSnippet)
		if !inserted {
			restoreSnippet()
			s.logger.Warn("public route include could not be placed in the nginx site")
			return
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			restoreSnippet()
			s.logger.Warn("nginx site update failed", "err", err)
			return
		}
	}
	if result, err := runCommand(ctx, 30*time.Second, "nginx", "-t"); err != nil {
		_ = os.WriteFile(path, content, 0o644)
		restoreSnippet()
		s.logger.Warn("nginx rejected the public route include; previous site restored",
			"err", err, "output", strings.TrimSpace(result.Stderr+" "+result.Stdout))
		return
	}
	if _, err := runCommand(ctx, 20*time.Second, "systemctl", "reload", "nginx"); err != nil {
		s.logger.Warn("nginx reload after public route include failed", "err", err)
		return
	}
	s.logger.Info("installed missing public nginx routes", "routes", missing)
}

// insertNginxServerInclude places the include before the closing brace of the
// first TLS server block. It returns inserted=false when no such block exists.
func insertNginxServerInclude(config, include string) (string, bool) {
	lines := strings.SplitAfter(config, "\n")
	var out strings.Builder
	inServer, tls, inserted := false, false, false
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inServer && strings.HasPrefix(trimmed, "server") && strings.Contains(trimmed, "{") {
			inServer = true
			tls = false
			depth = 0
		}
		if inServer {
			if strings.Contains(trimmed, "listen") && strings.Contains(trimmed, "443") {
				tls = true
			}
			opens := strings.Count(line, "{")
			closes := strings.Count(line, "}")
			if closes > 0 && depth+opens-closes == 0 {
				if tls && !inserted {
					out.WriteString("    include " + include + ";\n")
					inserted = true
				}
				inServer = false
			}
			depth += opens - closes
		}
		out.WriteString(line)
	}
	return out.String(), inserted
}
