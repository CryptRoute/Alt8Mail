package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/golang-jwt/jwt/v5"
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

// mockOIDCProvider is a local, test-only OpenID Provider. It serves a
// discovery document and a JWKS so provider discovery and ID-token
// verification never require a live IdP.
type mockOIDCProvider struct {
	server        *httptest.Server
	issuer        string
	key           *rsa.PrivateKey
	kid           string
	jwks          []byte
	discoveryHits int32
	jwksHits      int32
	started       chan struct{}
	release       chan struct{}
}

// newMockOIDCProvider starts a mock issuer whose issuer identifier ends with a
// trailing slash, matching providers that publish URLs in that form. When
// blockFirstDiscovery is true the first discovery response is held until the
// release channel is closed.
func newMockOIDCProvider(t *testing.T, blockFirstDiscovery bool) *mockOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	provider := &mockOIDCProvider{key: key, kid: "test-key", jwks: jwksDocument(&key.PublicKey, "test-key")}
	if blockFirstDiscovery {
		provider.started = make(chan struct{})
		provider.release = make(chan struct{})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&provider.discoveryHits, 1) == 1 && provider.started != nil {
			close(provider.started)
			<-provider.release
		}
		issuer := "http://" + r.Host + "/"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "authorize",
			"token_endpoint":                        issuer + "token",
			"jwks_uri":                              issuer + "jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&provider.jwksHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(provider.jwks)
	})
	provider.server = httptest.NewServer(mux)
	provider.issuer = provider.server.URL + "/"
	t.Cleanup(provider.server.Close)
	return provider
}

func (m *mockOIDCProvider) signIDToken(t *testing.T, audience, issuer, nonce string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":            issuer,
		"aud":            audience,
		"sub":            "user-1",
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          "person@example.test",
		"email_verified": true,
	})
	token.Header["kid"] = m.kid
	signed, err := token.SignedString(m.key)
	if err != nil {
		t.Fatalf("sign ID token: %v", err)
	}
	return signed
}

func jwksDocument(key *rsa.PublicKey, kid string) []byte {
	document := map[string]any{"keys": []map[string]any{{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestOIDCDiscoveryAndCaching(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcTestConfig()
	// Deliberately retain the trailing slash: go-oidc requires the configured
	// issuer to match the discovery document byte for byte.
	cfg.OIDCIssuer = provider.issuer
	server := New(Options{
		Config: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	_ = server.Handler()
	if hits := atomic.LoadInt32(&provider.discoveryHits); hits != 0 {
		t.Fatalf("server startup performed %d OIDC discovery request(s), want 0", hits)
	}

	client, err := server.oidcClient(context.Background())
	if err != nil {
		t.Fatalf("oidcClient() with a trailing-slash issuer: %v", err)
	}
	if client == nil || client.provider == nil || client.verifier == nil || client.config == nil {
		t.Fatalf("oidcClient() returned an incomplete client: %#v", client)
	}
	if client.config.ClientID != cfg.OIDCClientID {
		t.Errorf("OAuth2 ClientID = %q, want %q", client.config.ClientID, cfg.OIDCClientID)
	}
	if client.config.ClientSecret != cfg.OIDCClientSecret {
		t.Errorf("OAuth2 ClientSecret = %q, want %q", client.config.ClientSecret, cfg.OIDCClientSecret)
	}
	if client.config.RedirectURL != cfg.OIDCRedirectURL {
		t.Errorf("OAuth2 RedirectURL = %q, want %q", client.config.RedirectURL, cfg.OIDCRedirectURL)
	}
	if got, want := strings.Join(client.config.Scopes, " "), "openid email profile"; got != want {
		t.Errorf("OAuth2 scopes = %q, want %q", got, want)
	}
	if client.config.Endpoint.AuthURL != provider.issuer+"authorize" {
		t.Errorf("authorization endpoint = %q", client.config.Endpoint.AuthURL)
	}
	if client.config.Endpoint.TokenURL != provider.issuer+"token" {
		t.Errorf("token endpoint = %q", client.config.Endpoint.TokenURL)
	}

	// The verifier must accept the exact issuer including its trailing slash,
	// which also proves the JWKS endpoint is wired through the provider.
	signed := provider.signIDToken(t, cfg.OIDCClientID, provider.issuer, "test-nonce")
	idToken, err := client.verifier.Verify(context.Background(), signed)
	if err != nil {
		t.Fatalf("verify an ID token from the trailing-slash issuer: %v", err)
	}
	if idToken.Issuer != provider.issuer {
		t.Errorf("verified issuer = %q, want %q", idToken.Issuer, provider.issuer)
	}
	if idToken.Subject != "user-1" {
		t.Errorf("verified subject = %q, want user-1", idToken.Subject)
	}
	if hits := atomic.LoadInt32(&provider.jwksHits); hits == 0 {
		t.Fatal("ID token verification did not fetch the mock JWKS")
	}
	if hits := atomic.LoadInt32(&provider.discoveryHits); hits != 1 {
		t.Fatalf("discovery requests = %d, want 1", hits)
	}

	cached, err := server.oidcClient(context.Background())
	if err != nil {
		t.Fatalf("second oidcClient() call: %v", err)
	}
	if cached != client {
		t.Fatal("second oidcClient() call did not reuse the cached client")
	}
	if hits := atomic.LoadInt32(&provider.discoveryHits); hits != 1 {
		t.Fatalf("cached client triggered %d discovery requests, want 1", hits)
	}
}

func TestOIDCDiscoveryDetachedFromCallerCancellation(t *testing.T) {
	provider := newMockOIDCProvider(t, true)
	cfg := oidcTestConfig()
	cfg.OIDCIssuer = provider.issuer
	server := New(Options{
		Config: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		client *oidcClient
		err    error
	}
	results := make(chan outcome, 1)
	go func() {
		client, err := server.oidcClient(ctx)
		results <- outcome{client: client, err: err}
	}()

	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery request never reached the mock provider")
	}
	// Cancel the browser-style request while discovery is still in flight.
	// Discovery must not inherit this cancellation.
	cancel()
	close(provider.release)

	select {
	case got := <-results:
		if got.err != nil {
			t.Fatalf("cancelling the caller context aborted discovery: %v", got.err)
		}
		if got.client == nil {
			t.Fatal("cancelled caller context returned a nil client")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not finish after the caller context was cancelled")
	}

	// A cancelled first attempt must not poison the cached result or the
	// failure backoff for the next sign-in.
	client, err := server.oidcClient(context.Background())
	if err != nil {
		t.Fatalf("follow-up discovery after caller cancellation: %v", err)
	}
	if client == nil {
		t.Fatal("follow-up discovery returned a nil client")
	}
	if hits := atomic.LoadInt32(&provider.discoveryHits); hits != 1 {
		t.Fatalf("discovery requests = %d, want 1", hits)
	}
}
