package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

// oidcTestConfig returns a complete OIDC configuration used by the focused
// Task 1 tests. It never points at a live identity provider: provider
// discovery is only exercised by the Task 2 start/callback tests.
func oidcTestConfig() config.Config {
	return config.Config{
		OIDCIssuer:       "https://idp.example.test/tenant",
		OIDCClientID:     "alt8mail-client",
		OIDCClientSecret: "client-secret",
		OIDCRedirectURL:  "https://mail.example.test/api/auth/oidc/callback",
		OIDCDisplayName:  "Example SSO",
		OIDCEmailDomains: []string{"example.test"},
	}
}

// stabilizeConfigEnv pins the unrelated required settings so config.Load()
// assertions only observe the OIDC variables under test.
func stabilizeConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ALT8MAIL_ENV", "development")
	t.Setenv("ALT8MAIL_COOKIE_SECURE", "false")
	t.Setenv("ALT8MAIL_DB_MAX_CONNS", "20")
	t.Setenv("ALT8MAIL_DEMO_MODE", "false")
	t.Setenv("ALT8MAIL_MEMORY_LIMIT_MB", "512")
	t.Setenv("ALT8MAIL_SESSION_SECRET", "test-session-secret")
	t.Setenv("ALT8MAIL_CSRF_SECRET", "test-csrf-secret")
}

func TestOIDCConfigParsing(t *testing.T) {
	stabilizeConfigEnv(t)
	t.Setenv("ALT8MAIL_OIDC_ISSUER", "https://idp.example.test/tenant")
	t.Setenv("ALT8MAIL_OIDC_CLIENT_ID", "alt8mail-client")
	t.Setenv("ALT8MAIL_OIDC_CLIENT_SECRET", "client-secret")
	t.Setenv("ALT8MAIL_OIDC_REDIRECT_URL", "https://mail.example.test/api/auth/oidc/callback")
	t.Setenv("ALT8MAIL_OIDC_DISPLAY_NAME", "Example SSO")
	t.Setenv("ALT8MAIL_OIDC_EMAIL_DOMAINS", " Example.com, @Example.org ,, example.com ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if cfg.OIDCIssuer != "https://idp.example.test/tenant" {
		t.Errorf("OIDCIssuer = %q", cfg.OIDCIssuer)
	}
	if cfg.OIDCClientID != "alt8mail-client" {
		t.Errorf("OIDCClientID = %q", cfg.OIDCClientID)
	}
	if cfg.OIDCClientSecret != "client-secret" {
		t.Errorf("OIDCClientSecret = %q", cfg.OIDCClientSecret)
	}
	if cfg.OIDCRedirectURL != "https://mail.example.test/api/auth/oidc/callback" {
		t.Errorf("OIDCRedirectURL = %q", cfg.OIDCRedirectURL)
	}
	if cfg.OIDCDisplayName != "Example SSO" {
		t.Errorf("OIDCDisplayName = %q", cfg.OIDCDisplayName)
	}
	wantDomains := []string{"example.com", "example.org"}
	if len(cfg.OIDCEmailDomains) != len(wantDomains) {
		t.Fatalf("OIDCEmailDomains = %v, want %v", cfg.OIDCEmailDomains, wantDomains)
	}
	for i, want := range wantDomains {
		if cfg.OIDCEmailDomains[i] != want {
			t.Fatalf("OIDCEmailDomains = %v, want %v", cfg.OIDCEmailDomains, wantDomains)
		}
	}
}

func TestOIDCConfigured(t *testing.T) {
	base := oidcTestConfig()
	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   bool
	}{
		{name: "complete", mutate: func(*config.Config) {}, want: true},
		{name: "issuer missing", mutate: func(c *config.Config) { c.OIDCIssuer = "" }, want: false},
		{name: "issuer whitespace", mutate: func(c *config.Config) { c.OIDCIssuer = "   " }, want: false},
		{name: "client id missing", mutate: func(c *config.Config) { c.OIDCClientID = "" }, want: false},
		{name: "client secret missing", mutate: func(c *config.Config) { c.OIDCClientSecret = "" }, want: false},
		{name: "redirect url missing", mutate: func(c *config.Config) { c.OIDCRedirectURL = "" }, want: false},
		{name: "display name optional", mutate: func(c *config.Config) { c.OIDCDisplayName = "" }, want: true},
		{name: "email domains optional", mutate: func(c *config.Config) { c.OIDCEmailDomains = nil }, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			server := &Server{cfg: cfg}
			if got := server.oidcConfigured(); got != tc.want {
				t.Fatalf("oidcConfigured() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOIDCConfigEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		cfg         config.Config
		wantEnabled bool
		wantName    string
	}{
		{
			name:        "disabled without configuration",
			cfg:         config.Config{},
			wantEnabled: false,
			wantName:    "",
		},
		{
			name:        "enabled with configuration",
			cfg:         oidcTestConfig(),
			wantEnabled: true,
			wantName:    "Example SSO",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := New(Options{
				Config: tc.cfg,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/config", nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			var payload struct {
				Enabled     bool   `json:"enabled"`
				DisplayName string `json:"displayName"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", payload.Enabled, tc.wantEnabled)
			}
			if payload.DisplayName != tc.wantName {
				t.Fatalf("displayName = %q, want %q", payload.DisplayName, tc.wantName)
			}
		})
	}
}
