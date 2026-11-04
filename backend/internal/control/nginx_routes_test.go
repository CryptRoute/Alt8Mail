package control

import (
	"strings"
	"testing"
)

func TestInsertNginxServerInclude(t *testing.T) {
	config := `server {
    listen 80;
    server_name example.test;
    location / { return 301 https://$host$request_uri; }
}
server {
    listen 443 ssl http2;
    server_name example.test;
    location / { try_files $uri $uri/ /index.html; }
}
`
	updated, inserted := insertNginxServerInclude(config, "/etc/nginx/snippets/a8m-public-routes.conf")
	if !inserted {
		t.Fatal("include was not inserted")
	}
	if !strings.Contains(updated, "include /etc/nginx/snippets/a8m-public-routes.conf;") {
		t.Fatal("include line is missing")
	}
	if strings.Count(updated, "include /etc/nginx/snippets/a8m-public-routes.conf;") != 1 {
		t.Fatal("include should appear exactly once")
	}
	httpBlock := strings.Index(updated, "listen 80;")
	tlsBlock := strings.Index(updated, "listen 443")
	includeAt := strings.Index(updated, "include /etc/nginx/snippets/a8m-public-routes.conf;")
	if includeAt < tlsBlock || includeAt < httpBlock {
		t.Fatal("include was not placed in the TLS server block")
	}
}

func TestInsertNginxServerIncludeWithoutTLS(t *testing.T) {
	config := "server {\n    listen 80;\n    location / { return 204; }\n}\n"
	if _, inserted := insertNginxServerInclude(config, "/tmp/routes.conf"); inserted {
		t.Fatal("an HTTP-only site must not receive the TLS include")
	}
}
