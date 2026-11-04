package control

import (
	"fmt"
	"strings"
)

const webNetLogFormat = `log_format a8m_webnet '$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent" $request_time $upstream_response_time';
`

func (s *Server) renderWebNetNginx(settings webNetSettings, routes []webNetRoute, certByRoute map[string]webNetCertificate, fallback webNetCertificate, local []webNetIP, mailHost string) (string, error) {
	allAddresses := []string{}
	for _, ip := range local {
		if ip.Local {
			allAddresses = append(allAddresses, ip.Address)
		}
	}
	if len(allAddresses) == 0 {
		return "", fmt.Errorf("no local IP addresses are available for nginx listeners")
	}
	if fallback.CertPath == "" || fallback.KeyPath == "" {
		return "", fmt.Errorf("a fallback TLS certificate is required for unmatched hosts")
	}
	mailHost = normalizeDomain(mailHost)
	var builder strings.Builder
	builder.WriteString("# Managed by Alt8Mail Web Net. Manual edits are replaced when Web Net is applied.\n")
	builder.WriteString("# webmail: ")
	builder.WriteString(webNetPrimaryHost(routes, "webmail"))
	builder.WriteString(" · admin: ")
	builder.WriteString(webNetPrimaryHost(routes, "admin"))
	builder.WriteString("\n\n")

	// ACME and default handling for any hostname that is not explicitly assigned.
	builder.WriteString("# Catch-all: serve ACME, then the blocked page for unknown hosts.\n")
	builder.WriteString("server {\n")
	for _, line := range nginxListenLines(allAddresses, 80, false, true) {
		builder.WriteString(line)
	}
	builder.WriteString("    server_name _;\n")
	builder.WriteString("    server_tokens off;\n")
	builder.WriteString("    access_log " + nginxQuote(s.cfg.NginxLogDir+"/a8m-blocked.access.log") + " a8m_webnet;\n")
	builder.WriteString(nginxACMELocation(s.cfg.CertbotWebroot))
	builder.WriteString(nginxBlockedLocation(s.cfg.WebNetRoot))
	builder.WriteString("}\n\n")

	// HTTP redirects for configured hostnames.
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		addresses, err := s.webNetRouteAddresses(route, routes, local)
		if err != nil {
			return "", err
		}
		builder.WriteString("server {\n")
		for _, line := range nginxListenLines(addresses, 80, false, false) {
			builder.WriteString(line)
		}
		builder.WriteString("    server_name " + route.Hostname + ";\n")
		builder.WriteString("    server_tokens off;\n")
		builder.WriteString(nginxACMELocation(s.cfg.CertbotWebroot))
		builder.WriteString("    location / { return 301 https://$host$request_uri; }\n")
		builder.WriteString("}\n\n")
	}

	// The MX hostname is never a web application hostname.
	if mailHost != "" {
		builder.WriteString("# Mail/MX hostname browser policy.\n")
		builder.WriteString("server {\n")
		for _, line := range nginxListenLines(allAddresses, 80, false, false) {
			builder.WriteString(line)
		}
		builder.WriteString("    server_name " + mailHost + ";\n")
		builder.WriteString("    server_tokens off;\n")
		builder.WriteString("    access_log " + nginxQuote(s.cfg.NginxLogDir+"/a8m-blocked.access.log") + " a8m_webnet;\n")
		builder.WriteString(nginxACMELocation(s.cfg.CertbotWebroot))
		builder.WriteString(nginxBlockedAction(settings, s.cfg.WebNetRoot))
		builder.WriteString("}\n\n")
	}

	// HTTPS catch-all. The certificate may not match an unmatched host, which
	// is intentional: browsers show a warning instead of a usable application.
	builder.WriteString("# HTTPS catch-all.\n")
	builder.WriteString("server {\n")
	for _, line := range nginxListenLines(allAddresses, 443, true, true) {
		builder.WriteString(line)
	}
	builder.WriteString("    server_name _;\n")
	builder.WriteString("    server_tokens off;\n")
	builder.WriteString(nginxTLSBlock(fallback))
	builder.WriteString("    access_log " + nginxQuote(s.cfg.NginxLogDir+"/a8m-blocked.access.log") + " a8m_webnet;\n")
	builder.WriteString(nginxBlockedLocation(s.cfg.WebNetRoot))
	builder.WriteString("}\n\n")

	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		cert, ok := certByRoute[route.Hostname]
		if !ok || cert.CertPath == "" {
			return "", fmt.Errorf("no certificate is assigned to %s", route.Hostname)
		}
		addresses, err := s.webNetRouteAddresses(route, routes, local)
		if err != nil {
			return "", err
		}
		builder.WriteString("server {\n")
		for _, line := range nginxListenLines(addresses, 443, true, false) {
			builder.WriteString(line)
		}
		builder.WriteString("    server_name " + route.Hostname + ";\n")
		builder.WriteString("    server_tokens off;\n")
		builder.WriteString(nginxTLSBlock(cert))
		builder.WriteString(nginxAccessRules(route.Service, settings))
		if route.Service == "webmail" {
			builder.WriteString("    root " + nginxQuote(s.cfg.WebRoot) + ";\n")
			builder.WriteString("    index index.html;\n")
			builder.WriteString("    client_max_body_size 25m;\n")
			builder.WriteString("    access_log " + nginxQuote(s.cfg.NginxLogDir+"/a8m-webmail.access.log") + " a8m_webnet;\n")
			if settings.WebmailMaintenance {
				builder.WriteString(nginxMaintenance("maintenance-webmail.html", s.cfg.WebNetRoot))
			} else {
				builder.WriteString(webNetWebmailLocations(s.cfg.WebUpstream))
			}
		} else {
			builder.WriteString("    client_max_body_size 25m;\n")
			builder.WriteString("    access_log " + nginxQuote(s.cfg.NginxLogDir+"/a8m-admin.access.log") + " a8m_webnet;\n")
			if settings.AdminMaintenance {
				builder.WriteString(nginxMaintenance("maintenance-admin.html", s.cfg.WebNetRoot))
			} else {
				builder.WriteString(webNetAdminLocations(s.cfg.AdminUpstream))
			}
		}
		builder.WriteString("}\n\n")
	}

	if mailHost != "" {
		builder.WriteString("# Mail/MX hostname HTTPS policy.\n")
		builder.WriteString("server {\n")
		for _, line := range nginxListenLines(allAddresses, 443, true, false) {
			builder.WriteString(line)
		}
		builder.WriteString("    server_name " + mailHost + ";\n")
		builder.WriteString("    server_tokens off;\n")
		builder.WriteString(nginxTLSBlock(fallback))
		builder.WriteString("    access_log " + nginxQuote(s.cfg.NginxLogDir+"/a8m-blocked.access.log") + " a8m_webnet;\n")
		builder.WriteString(nginxBlockedAction(settings, s.cfg.WebNetRoot))
		builder.WriteString("}\n\n")
	}
	return builder.String(), nil
}

func (s *Server) webNetRouteAddresses(route webNetRoute, routes []webNetRoute, local []webNetIP) ([]string, error) {
	if route.IPMode == "dedicated" {
		if route.IPAddress == "" {
			return nil, fmt.Errorf("%s has no dedicated address", route.Hostname)
		}
		return []string{route.IPAddress}, nil
	}
	reserved := map[string]bool{}
	for _, other := range routes {
		if other.Service != route.Service && other.IPMode == "dedicated" && other.IPAddress != "" {
			reserved[other.IPAddress] = true
		}
	}
	addresses := []string{}
	for _, ip := range local {
		if ip.Local && !reserved[ip.Address] {
			addresses = append(addresses, ip.Address)
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no shared address remains for %s; move the other service to a different IP", route.Hostname)
	}
	return addresses, nil
}

func nginxListenLines(addresses []string, port int, tls bool, defaultServer bool) []string {
	lines := []string{}
	for _, address := range addresses {
		host := address
		if strings.Contains(address, ":") {
			host = "[" + address + "]"
		}
		line := "    listen " + host + ":" + fmt.Sprintf("%d", port)
		if tls {
			line += " ssl http2"
		}
		if defaultServer {
			line += " default_server"
		}
		line += ";\n"
		lines = append(lines, line)
	}
	return lines
}

func nginxTLSBlock(cert webNetCertificate) string {
	return "    ssl_certificate " + nginxQuote(cert.CertPath) + ";\n" +
		"    ssl_certificate_key " + nginxQuote(cert.KeyPath) + ";\n" +
		"    ssl_protocols TLSv1.2 TLSv1.3;\n" +
		"    ssl_session_cache shared:A8MTLS:10m;\n" +
		"    ssl_session_timeout 1h;\n" +
		"    ssl_prefer_server_ciphers off;\n"
}

func nginxACMELocation(webroot string) string {
	return "    location ^~ /.well-known/acme-challenge/ { root " + nginxQuote(webroot) + "; }\n"
}

func nginxBlockedLocation(webroot string) string {
	// Clear any service worker and cache left behind by the previous webmail
	// origin. Without this, an installed PWA could keep an offline app shell on
	// the mail hostname after it has been blocked at the edge.
	return "    add_header Clear-Site-Data '\"cache\", \"storage\"' always;\n" +
		nginxRetireServiceWorkerLocation(webroot) +
		"    error_page 403 /blocked.html;\n" +
		"    location = /blocked.html { internal; root " + nginxQuote(webroot) + "; }\n" +
		"    location / { return 403; }\n"
}

func nginxRetireServiceWorkerLocation(webroot string) string {
	return "    location = /sw.js {\n" +
		"        default_type application/javascript;\n" +
		"        add_header Cache-Control \"no-cache\" always;\n" +
		"        alias " + nginxQuote(strings.TrimRight(webroot, "/")+"/mailhost-sw.js") + ";\n" +
		"    }\n"
}

func nginxBlockedAction(settings webNetSettings, webroot string) string {
	if settings.MailHostMode == "drop" {
		// A 444 has no response body, but the service-worker update check still
		// needs a network response to replace an old cached webmail shell.
		return nginxRetireServiceWorkerLocation(webroot) + "    location / { return 444; }\n"
	}
	return nginxBlockedLocation(webroot)
}

func nginxMaintenance(page, webroot string) string {
	return "    error_page 503 /" + page + ";\n" +
		"    location = /" + page + " { internal; root " + nginxQuote(webroot) + "; }\n" +
		"    location / { return 503; }\n"
}

func nginxAccessRules(service string, settings webNetSettings) string {
	allow, deny := settings.WebmailAllow, settings.WebmailDeny
	if service == "admin" {
		allow, deny = settings.AdminAllow, settings.AdminDeny
	}
	var builder strings.Builder
	for _, cidr := range deny {
		builder.WriteString("    deny " + cidr + ";\n")
	}
	if len(allow) > 0 {
		for _, cidr := range allow {
			builder.WriteString("    allow " + cidr + ";\n")
		}
		builder.WriteString("    deny all;\n")
	}
	return builder.String()
}

func webNetWebmailLocations(upstream string) string {
	var builder strings.Builder
	builder.WriteString(`    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options SAMEORIGIN always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;
    add_header Content-Security-Policy "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; font-src 'self'; worker-src 'self'; manifest-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'" always;
`)
	builder.WriteString(nginxProxyLocation("^~ /dav/", upstream, "", "4m"))
	builder.WriteString(nginxProxyLocation("^~ /.well-known/caldav", upstream, "", ""))
	builder.WriteString(nginxProxyLocation("^~ /.well-known/carddav", upstream, "", ""))
	builder.WriteString(nginxProxyLocation("= /.well-known/mta-sts.txt", upstream, "", ""))
	builder.WriteString(nginxProxyLocation("^~ /files/s/", upstream, "", ""))
	builder.WriteString(nginxProxyLocation("^~ /upload/", upstream, "", ""))
	builder.WriteString(nginxProxyLocation("^~ /api/", upstream, "", ""))
	builder.WriteString(nginxProxyLocation("^~ /system/api/", upstream, "/api/", ""))
	builder.WriteString("    location = /admin { return 404; }\n")
	builder.WriteString("    location ^~ /admin/ { return 404; }\n")
	builder.WriteString("    location = /sw.js { add_header Cache-Control \"no-cache\"; try_files $uri =404; }\n")
	builder.WriteString("    location = /manifest.webmanifest { add_header Cache-Control \"no-cache\"; try_files $uri =404; }\n")
	builder.WriteString("    location ~* \\.(css|js|mjs|json|png|svg|woff2)$ { expires 1h; add_header Cache-Control \"public, max-age=3600\"; try_files $uri =404; }\n")
	builder.WriteString("    location / { expires -1; try_files $uri $uri/ /index.html; }\n")
	builder.WriteString("    location ~ /\\.(?!well-known) { deny all; }\n")
	return builder.String()
}

func nginxProxyLocation(match, upstream, proxyPath, clientMaxBody string) string {
	var builder strings.Builder
	builder.WriteString("    location " + match + " {\n")
	builder.WriteString("        proxy_pass http://" + upstream + proxyPath + ";\n")
	builder.WriteString("        proxy_http_version 1.1;\n")
	builder.WriteString("        proxy_set_header Host $host;\n")
	builder.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
	builder.WriteString("        proxy_set_header X-Forwarded-For $remote_addr;\n")
	builder.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	builder.WriteString("        proxy_read_timeout 120s;\n")
	builder.WriteString("        proxy_send_timeout 120s;\n")
	if clientMaxBody != "" {
		builder.WriteString("        client_max_body_size " + clientMaxBody + ";\n")
	}
	builder.WriteString("    }\n")
	return builder.String()
}

func webNetAdminLocations(upstream string) string {
	return `    location = /admin { return 308 https://$host/; }
    location / {
        proxy_pass http://` + upstream + `;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
        proxy_hide_header X-Content-Type-Options;
        proxy_hide_header X-Frame-Options;
        proxy_hide_header Referrer-Policy;
        proxy_hide_header Permissions-Policy;
        proxy_hide_header Content-Security-Policy;
        proxy_hide_header Cross-Origin-Resource-Policy;
        proxy_hide_header X-Robots-Tag;
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
        add_header X-Content-Type-Options nosniff always;
        add_header X-Frame-Options DENY always;
        add_header Referrer-Policy strict-origin-when-cross-origin always;
        add_header Cross-Origin-Resource-Policy same-origin always;
        add_header X-Robots-Tag "noindex, nofollow" always;
        add_header Permissions-Policy "camera=(), microphone=(), geolocation=(), interest-cohort=()" always;
        add_header Content-Security-Policy "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; font-src 'self'; worker-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'" always;
    }
`
}

func nginxQuote(value string) string {
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
