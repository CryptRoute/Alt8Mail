package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWebNetServer(t *testing.T) *Server {
	t.Helper()
	return &Server{cfg: Config{
		MailHostname:     "mx.example.com",
		WebUpstream:      "127.0.0.1:8080",
		AdminUpstream:    "127.0.0.1:8081",
		WebRoot:          "/var/www/a8m",
		WebNetRoot:       "/var/www/a8m-webnet",
		NginxLogDir:      t.TempDir(),
		CertbotWebroot:   "/var/www/certbot",
		NginxSitePath:    "/etc/nginx/sites-available/a8m",
		NginxEnabledPath: "/etc/nginx/sites-enabled/a8m",
	}}
}

func TestNormalizeCIDRs(t *testing.T) {
	values, err := normalizeCIDRs([]string{"203.0.113.10", "203.0.113.0/24, 198.51.100.0/24", "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("values = %#v", values)
	}
	if _, err := normalizeCIDRs([]string{"example.com"}); err == nil {
		t.Fatal("non-IP value was accepted")
	}
	if _, err := normalizeCIDRs([]string{"203.0.113.0/99"}); err == nil {
		t.Fatal("invalid CIDR was accepted")
	}
}

func TestCertificateCoversHostname(t *testing.T) {
	cert := webNetCertificate{SANs: []string{"webmail.example.com", "*.example.net", "203.0.113.10"}}
	if !certificateCoversHostname(cert, "webmail.example.com") {
		t.Fatal("exact SAN did not match")
	}
	if !certificateCoversHostname(cert, "mail.example.net") {
		t.Fatal("wildcard SAN did not match one label")
	}
	if certificateCoversHostname(cert, "deep.mail.example.net") {
		t.Fatal("wildcard SAN matched more than one label")
	}
	if certificateCoversHostname(cert, "admin.example.com") {
		t.Fatal("unrelated hostname matched")
	}
}

func TestValidWebHostnameLabels(t *testing.T) {
	for _, value := range []string{"mail.example.com", "a-b.example.com", "xn--bcher-kva.example"} {
		if !validWebHostnameLabels(value) {
			t.Fatalf("valid hostname rejected: %s", value)
		}
	}
	for _, value := range []string{"example", "example..com", "-bad.example.com", "bad-.example.com", "a." + strings.Repeat("x", 64) + ".com"} {
		if validWebHostnameLabels(value) {
			t.Fatalf("invalid hostname accepted: %s", value)
		}
	}
}

func TestWebNetMissingAutoServices(t *testing.T) {
	routes := []webNetRoute{
		{Service: "webmail", Hostname: "webmail.example.com", Certificate: "", Primary: true, Enabled: true},
		{Service: "admin", Hostname: "admin.example.com", Certificate: "pinned-cert", Primary: true, Enabled: true},
	}
	checks := []webNetHostCheck{
		{Service: "webmail", Hostname: "webmail.example.com", DNSOK: true, CertOK: false},
		{Service: "admin", Hostname: "admin.example.com", DNSOK: true, CertOK: false},
	}
	services := webNetMissingAutoServices(routes, checks)
	if strings.Join(services, ",") != "webmail" {
		t.Fatalf("services = %#v", services)
	}
	checks[0].DNSOK = false
	if services := webNetMissingAutoServices(routes, checks); len(services) != 0 {
		t.Fatalf("DNS-failed host triggered issuance: %#v", services)
	}
	if domains := webNetAutoDomains(routes, "webmail"); strings.Join(domains, ",") != "webmail.example.com" {
		t.Fatalf("auto domains = %#v", domains)
	}
}

func TestWebNetCertificateName(t *testing.T) {
	if got := webNetCertificateName("webmail.example.com"); got != "webnet-webmail.example.com" {
		t.Fatalf("certificate name = %q", got)
	}
	long := webNetCertificateName(strings.Repeat("a", 250) + ".example.com")
	if len(long) > 180 || !strings.HasPrefix(long, "webnet-") {
		t.Fatalf("long certificate name = %q", long)
	}
}

func TestWebNetRouteAddresses(t *testing.T) {
	server := testWebNetServer(t)
	local := []webNetIP{
		{Address: "203.0.113.10", Local: true},
		{Address: "203.0.113.11", Local: true},
	}
	routes := []webNetRoute{
		{Service: "webmail", Hostname: "mail.example.com", IPMode: "shared"},
		{Service: "admin", Hostname: "admin.example.com", IPMode: "dedicated", IPAddress: "203.0.113.11"},
	}
	webmailAddresses, err := server.webNetRouteAddresses(routes[0], routes, local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(webmailAddresses, ",") != "203.0.113.10" {
		t.Fatalf("webmail addresses = %#v", webmailAddresses)
	}
	adminAddresses, err := server.webNetRouteAddresses(routes[1], routes, local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(adminAddresses, ",") != "203.0.113.11" {
		t.Fatalf("admin addresses = %#v", adminAddresses)
	}
}

func TestWebNetDNSExpected(t *testing.T) {
	server := testWebNetServer(t)
	local := []webNetIP{
		{Address: "203.0.113.10", Local: true, Public: true},
		{Address: "203.0.113.11", Local: true, Public: true},
		{Address: "198.51.100.20", Local: false, Public: true, Label: "Configured public address"},
	}
	routes := []webNetRoute{
		{Service: "webmail", Hostname: "webmail.example.com", IPMode: "shared", Primary: true, Enabled: true},
		{Service: "admin", Hostname: "admin.example.com", IPMode: "dedicated", IPAddress: "203.0.113.11", Primary: true, Enabled: true},
	}
	webmailExpected, err := server.webNetDNSExpected(routes[0], routes, local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(webmailExpected, ",") != "198.51.100.20,203.0.113.10" {
		t.Fatalf("webmail expected = %#v", webmailExpected)
	}
	adminExpected, err := server.webNetDNSExpected(routes[1], routes, local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(adminExpected, ",") != "203.0.113.11" {
		t.Fatalf("admin expected = %#v", adminExpected)
	}
}

func TestRenderWebNetNginx(t *testing.T) {
	server := testWebNetServer(t)
	local := []webNetIP{
		{Address: "203.0.113.10", Interface: "eth0", Local: true, Public: true},
		{Address: "203.0.113.11", Interface: "eth0", Local: true, Public: true},
	}
	cert := webNetCertificate{Name: "mail-cert", CertPath: "/etc/ssl/a8m/mail.crt", KeyPath: "/etc/ssl/a8m/mail.key", DaysLeft: 90, HasKey: true}
	settings := webNetSettings{
		MailHostMode: "drop", MaintenanceTitle: "Maintenance", MaintenanceMessage: "Back soon",
		BlockedTitle: "Not a web host", BlockedMessage: "Use webmail",
		WebmailAllow: []string{"203.0.113.0/24"}, AdminDeny: []string{"192.0.2.0/24"},
	}
	routes := []webNetRoute{
		{Service: "webmail", Hostname: "webmail.example.com", IPMode: "shared", Primary: true, Enabled: true},
		{Service: "admin", Hostname: "admin.example.com", IPMode: "dedicated", IPAddress: "203.0.113.11", Primary: true, Enabled: true},
	}
	config, err := server.renderWebNetNginx(settings, routes, map[string]webNetCertificate{
		"webmail.example.com": cert, "admin.example.com": cert,
	}, cert, local, server.cfg.MailHostname)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"server_name webmail.example.com;",
		"server_name admin.example.com;",
		"server_name mx.example.com;",
		"listen 203.0.113.11:443 ssl http2;",
		"proxy_pass http://127.0.0.1:8081;",
		"location ^~ /admin/ { return 404; }",
		"location ^~ /oauth/ {",
		"return 444;",
		"allow 203.0.113.0/24;",
		"deny 192.0.2.0/24;",
		"Clear-Site-Data",
		"location = /sw.js {",
		"mailhost-sw.js",
		"ssl_certificate \"/etc/ssl/a8m/mail.crt\";",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("rendered config is missing %q\n%s", expected, config)
		}
	}
	if !strings.Contains(config, "proxy_pass http://127.0.0.1:8081;\n        proxy_http_version 1.1;") {
		t.Fatal("admin proxy block is incomplete")
	}
	if got := strings.Count(config, "default_server"); got != 4 {
		t.Fatalf("default_server count = %d, want 4 (HTTP and HTTPS catch-all per address)", got)
	}
}

func TestRenderWebNetMaintenance(t *testing.T) {
	server := testWebNetServer(t)
	local := []webNetIP{{Address: "203.0.113.10", Local: true, Public: true}}
	cert := webNetCertificate{Name: "mail-cert", CertPath: "/etc/ssl/a8m/mail.crt", KeyPath: "/etc/ssl/a8m/mail.key", DaysLeft: 90, HasKey: true}
	settings := webNetSettings{MailHostMode: "default_page", WebmailMaintenance: true, AdminMaintenance: true}
	routes := []webNetRoute{
		{Service: "webmail", Hostname: "webmail.example.com", Primary: true, Enabled: true, IPMode: "shared"},
		{Service: "admin", Hostname: "admin.example.com", Primary: true, Enabled: true, IPMode: "shared"},
	}
	config, err := server.renderWebNetNginx(settings, routes, map[string]webNetCertificate{
		"webmail.example.com": cert, "admin.example.com": cert,
	}, cert, local, server.cfg.MailHostname)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"error_page 503 /maintenance-webmail.html;",
		"error_page 503 /maintenance-admin.html;",
		"location = /maintenance-webmail.html { internal;",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("maintenance config is missing %q\n%s", expected, config)
		}
	}
}

func TestWebNetRetireServiceWorker(t *testing.T) {
	script := webNetRetireServiceWorker("https://webmail.example.com/")
	for _, expected := range []string{"self.registration.unregister()", "caches.delete", "https://webmail.example.com/"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("retire script is missing %q", expected)
		}
	}
}

func TestWebNetPageEscaping(t *testing.T) {
	page := webNetPage("<script>alert(1)</script>", "Use <b>webmail</b>", "Restricted")
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatal("page title was not escaped")
	}
	if !strings.Contains(page, "Use &lt;b&gt;webmail&lt;/b&gt;") {
		t.Fatal("page message was not escaped")
	}
}

func TestWebNetMetrics(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	path := filepath.Join(dir, "a8m-webmail.access.log")
	lines := []string{
		`203.0.113.5 - - [` + now.Format("02/Jan/2006:15:04:05 -0700") + `] "GET /api/messages?folder=inbox HTTP/1.1" 200 1234 "-" "Mozilla/5.0 Test" 0.045 0.030`,
		`203.0.113.6 - - [` + now.Format("02/Jan/2006:15:04:05 -0700") + `] "GET /api/messages?folder=inbox HTTP/1.1" 200 100 "-" "Mozilla/5.0 Test" 0.055 0.040`,
		`203.0.113.7 - - [` + now.Format("02/Jan/2006:15:04:05 -0700") + `] "POST /api/send HTTP/1.1" 500 50 "-" "Mozilla/5.0 Test" 0.120 0.100`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := testWebNetServer(t)
	metrics := server.webNetServiceMetrics(path, now)
	if metrics["available"] != true {
		t.Fatalf("metrics unavailable: %#v", metrics)
	}
	if metrics["requests"].(int64) != 3 {
		t.Fatalf("requests = %#v", metrics["requests"])
	}
	if metrics["serverErrors"].(int64) != 1 {
		t.Fatalf("server errors = %#v", metrics["serverErrors"])
	}
	if metrics["uniqueClients"].(int) != 3 {
		t.Fatalf("unique clients = %#v", metrics["uniqueClients"])
	}
	paths, ok := metrics["topPaths"].([]map[string]any)
	if !ok || len(paths) == 0 || paths[0]["path"] != "/api/messages" {
		t.Fatalf("top paths = %#v", metrics["topPaths"])
	}
}

func TestDumpWebNetConfigForNginx(t *testing.T) {
	dump := os.Getenv("WEBNET_DUMP")
	if dump == "" {
		t.Skip("set WEBNET_DUMP to write a renderable nginx configuration")
	}
	certPath := os.Getenv("WEBNET_CERT")
	keyPath := os.Getenv("WEBNET_KEY")
	if certPath == "" || keyPath == "" {
		t.Fatal("WEBNET_CERT and WEBNET_KEY are required")
	}
	server := testWebNetServer(t)
	if root := os.Getenv("WEBNET_ROOT"); root != "" {
		server.cfg.WebRoot = root + "/www"
		server.cfg.WebNetRoot = root + "/webnet"
		server.cfg.CertbotWebroot = root + "/certbot"
		server.cfg.NginxLogDir = root + "/logs"
	}
	cert := webNetCertificate{Name: "webnet-test", CertPath: certPath, KeyPath: keyPath, DaysLeft: 90, HasKey: true}
	settings := webNetSettings{
		MailHostMode: "drop", WebmailMaintenance: true, MaintenanceTitle: "Maintenance",
		MaintenanceMessage: "Back soon", BlockedTitle: "Not a web host", BlockedMessage: "Use webmail",
		WebmailAllow: []string{"203.0.113.0/24"}, AdminDeny: []string{"192.0.2.0/24"},
	}
	routes := []webNetRoute{
		{Service: "webmail", Hostname: "webmail.example.test", IPMode: "shared", Primary: true, Enabled: true},
		{Service: "admin", Hostname: "admin.example.test", IPMode: "shared", Primary: true, Enabled: true},
	}
	local := []webNetIP{{Address: "127.0.0.1", Local: true}}
	config, err := server.renderWebNetNginx(settings, routes, map[string]webNetCertificate{
		"webmail.example.test": cert, "admin.example.test": cert,
	}, cert, local, server.cfg.MailHostname)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dump, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRedactWebNetPath(t *testing.T) {
	if got := redactWebNetPath("/files/s/abcdefghijklmnopqrstuv"); got != "/files/s/:id" {
		t.Fatalf("redacted path = %q", got)
	}
	if got := redactWebNetPath("/api/messages/msg_imap_12ab34cd56ef"); got != "/api/messages/:id" {
		t.Fatalf("message path = %q", got)
	}
	if got := redactWebNetPath("/api/messages"); got != "/api/messages" {
		t.Fatalf("normal path = %q", got)
	}
}

func TestReadWebNetLogMissing(t *testing.T) {
	if _, err := readWebNetLog(filepath.Join(t.TempDir(), "missing.log"), 1024); err == nil {
		t.Fatal("missing log did not return an error")
	}
}
