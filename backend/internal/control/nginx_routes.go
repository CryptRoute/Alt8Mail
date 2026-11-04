package control

import (
	"context"
	"os"
	"strings"
	"time"
)

const publicNginxSnippet = "/etc/nginx/snippets/a8m-public-routes.conf"

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
	if strings.Contains(site, "include "+publicNginxSnippet+";") {
		return
	}
	routes := []string{"/files/s/", "/upload/", "/calendar/rsvp/", "/calendar/poll/", "/calendar/feed/"}
	missing := make([]string, 0, len(routes))
	for _, route := range routes {
		if !strings.Contains(site, "location ^~ "+route) && !strings.Contains(site, "location "+route) {
			missing = append(missing, route)
		}
	}
	if len(missing) == 0 {
		return
	}
	if err := os.MkdirAll("/etc/nginx/snippets", 0o755); err != nil {
		s.logger.Warn("public route snippet directory failed", "err", err)
		return
	}
	var snippet strings.Builder
	snippet.WriteString("# Managed by Alt8Mail. Public download, upload and calendar routes.\n")
	for _, route := range missing {
		snippet.WriteString("location ^~ " + route + " {\n")
		snippet.WriteString("        proxy_pass http://" + s.cfg.WebUpstream + ";\n")
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
	if err := os.WriteFile(publicNginxSnippet, []byte(snippet.String()), 0o644); err != nil {
		s.logger.Warn("public route snippet write failed", "err", err)
		return
	}
	updated, inserted := insertNginxServerInclude(site, publicNginxSnippet)
	if !inserted {
		s.logger.Warn("public route include could not be placed in the nginx site")
		return
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		s.logger.Warn("nginx site update failed", "err", err)
		return
	}
	if result, err := runCommand(ctx, 30*time.Second, "nginx", "-t"); err != nil {
		_ = os.WriteFile(path, content, 0o644)
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
