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

func TestPublicNginxRouteStateMergesWithoutDuplicates(t *testing.T) {
	site := `server {
    listen 443 ssl http2;
    location ^~ /files/s/ { proxy_pass http://127.0.0.1:8080; }
    location ^~ /upload/ { proxy_pass http://127.0.0.1:8080; }
}
`
	snippet := `location ^~ /calendar/rsvp/ { proxy_pass http://127.0.0.1:8080; }`
	routes, missing := publicNginxRouteState(site, snippet)
	wantRoutes := []string{"/calendar/rsvp/", "/calendar/poll/", "/calendar/feed/", "/oauth/"}
	wantMissing := []string{"/calendar/poll/", "/calendar/feed/", "/oauth/"}
	if strings.Join(routes, ",") != strings.Join(wantRoutes, ",") {
		t.Fatalf("snippet routes = %v, want %v", routes, wantRoutes)
	}
	if strings.Join(missing, ",") != strings.Join(wantMissing, ",") {
		t.Fatalf("missing routes = %v, want %v", missing, wantMissing)
	}
	for _, route := range routes {
		if route == "/files/s/" || route == "/upload/" {
			t.Fatalf("route already declared in the site was duplicated: %s", route)
		}
	}
}

func TestPublicNginxRouteStateComplete(t *testing.T) {
	site := "location ^~ /files/s/ { }\nlocation ^~ /upload/ { }\nlocation ^~ /oauth/ { }\n"
	snippet := "location ^~ /calendar/rsvp/ { }\nlocation ^~ /calendar/poll/ { }\nlocation ^~ /calendar/feed/ { }\n"
	if _, missing := publicNginxRouteState(site, snippet); len(missing) != 0 {
		t.Fatalf("unexpected missing routes: %v", missing)
	}
}

func TestPublicNginxSnippetText(t *testing.T) {
	text := publicNginxSnippetText([]string{"/upload/", "/oauth/"}, "127.0.0.1:8080")
	if strings.Count(text, "location ^~ /oauth/ {") != 1 {
		t.Fatalf("OAuth callback route missing from snippet:\n%s", text)
	}
	if !strings.Contains(text, "client_max_body_size 12m;") {
		t.Fatalf("upload body limit missing from snippet:\n%s", text)
	}
	if !strings.Contains(text, "proxy_pass http://127.0.0.1:8080;") {
		t.Fatalf("upstream missing from snippet:\n%s", text)
	}
}

func TestInsertNginxServerIncludeWithoutTLS(t *testing.T) {
	config := "server {\n    listen 80;\n    location / { return 204; }\n}\n"
	if _, inserted := insertNginxServerInclude(config, "/tmp/routes.conf"); inserted {
		t.Fatal("an HTTP-only site must not receive the TLS include")
	}
}
