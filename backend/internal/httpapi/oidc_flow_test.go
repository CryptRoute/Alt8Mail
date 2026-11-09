package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// oidcFlowConfig is a complete, local-only OIDC configuration. Callers replace
// OIDCIssuer with the mock provider URL.
func oidcFlowConfig(provider *mockOIDCProvider) config.Config {
	cfg := oidcTestConfig()
	cfg.OIDCIssuer = provider.issuer
	cfg.OIDCRedirectURL = "https://mail.example.test/api/auth/oidc/callback"
	cfg.SessionSecret = "session-secret"
	cfg.CSRFSecret = "csrf-secret"
	cfg.SessionTTL = 12 * time.Hour
	cfg.RememberTTL = 30 * 24 * time.Hour
	return cfg
}

func oidcFlowServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	server := New(Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	// SSO is a paid feature: install a valid sso entitlement so the flow tests
	// exercise the protocol rather than the licensing gate.
	installSSOTestLicense(t, server)
	return server
}

func findOIDCResponseCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

type oidcStartResult struct {
	state        oidcStatePayload
	cookie       *http.Cookie
	challenge    string
	authorizeURL *url.URL
}

// beginOIDCFlow drives GET /api/auth/oidc/start and returns the signed state,
// state cookie and PKCE challenge needed to exercise the callback.
func beginOIDCFlow(t *testing.T, server *Server, query string) oidcStartResult {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start"+query, nil)
	recorder := httptest.NewRecorder()
	server.oidcStart(recorder, request)
	response := recorder.Result()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("start status = %d, want 302; body: %s", response.StatusCode, recorder.Body.String())
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse start redirect: %v", err)
	}
	cookie := findOIDCResponseCookie(response.Cookies(), oidcStateCookie)
	if cookie == nil {
		t.Fatal("start did not set the OIDC state cookie")
	}
	state, err := server.parseOIDCState(cookie.Value)
	if err != nil {
		t.Fatalf("parse state cookie: %v", err)
	}
	return oidcStartResult{
		state:        state,
		cookie:       cookie,
		challenge:    location.Query().Get("code_challenge"),
		authorizeURL: location,
	}
}

func oidcCallbackURL(start oidcStartResult, code, state string) string {
	query := url.Values{}
	if code != "" {
		query.Set("code", code)
	}
	if state != "" {
		query.Set("state", state)
	}
	return "/api/auth/oidc/callback?" + query.Encode()
}

func callOIDCCallback(server *Server, deps oidcAuthDeps, start *oidcStartResult, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if start != nil && start.cookie != nil {
		request.AddCookie(&http.Cookie{Name: start.cookie.Name, Value: start.cookie.Value})
	}
	recorder := httptest.NewRecorder()
	server.handleOIDCCallback(recorder, request, deps)
	return recorder
}

func requireSSOErrorRedirect(t *testing.T, recorder *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302; body: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if location.Path != "/" {
		t.Fatalf("callback redirect path = %q, want /", location.Path)
	}
	query := location.Query()
	if len(query) != 1 || query.Get("sso_error") != wantCode {
		t.Fatalf("callback redirect query = %q, want only sso_error=%s", location.RawQuery, wantCode)
	}
	cleared := findOIDCResponseCookie(recorder.Result().Cookies(), oidcStateCookie)
	if cleared == nil {
		t.Fatal("callback did not clear the OIDC state cookie")
	}
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("state cookie was not cleared: value=%q maxAge=%d", cleared.Value, cleared.MaxAge)
	}
}

// oidcTestBackend substitutes the account/session dependencies of the callback
// so the login-flow tests do not require PostgreSQL.
type oidcTestBackend struct {
	user        userRecord
	userErr     error
	policy      adminTenantPolicy
	sessionErr  error
	lookupEmail []string
	sessionTTLs []time.Duration
	lastLogins  []string
	audits      []oidcTestAudit
}

type oidcTestAudit struct {
	userID string
	action string
	target string
	detail string
}

func newOIDCTestBackend() *oidcTestBackend {
	return &oidcTestBackend{
		user:   userRecord{ID: "user-1", TenantID: "tenant-1", Email: "person@example.test", Name: "Person", Role: "member", Status: "active"},
		policy: defaultTenantPolicy(),
	}
}

func (backend *oidcTestBackend) deps() oidcAuthDeps {
	return oidcAuthDeps{
		userByEmail: func(ctx context.Context, email string) (userRecord, error) {
			backend.lookupEmail = append(backend.lookupEmail, email)
			return backend.user, backend.userErr
		},
		tenantPolicy: func(ctx context.Context, tenantID string) adminTenantPolicy {
			return backend.policy
		},
		createSession: func(r *http.Request, user userRecord, ttl time.Duration) (*sessionRecord, string, error) {
			backend.sessionTTLs = append(backend.sessionTTLs, ttl)
			if backend.sessionErr != nil {
				return nil, "", backend.sessionErr
			}
			return &sessionRecord{
				ID: "session-id", UserID: user.ID, TenantID: user.TenantID,
				CSRF: "csrf-token", ExpiresAt: time.Now().Add(ttl), User: user,
			}, "raw-session-token", nil
		},
		updateLastLogin: func(ctx context.Context, userID string) {
			backend.lastLogins = append(backend.lastLogins, userID)
		},
		audit: func(r *http.Request, user userRecord, action, target, detail string) {
			backend.audits = append(backend.audits, oidcTestAudit{userID: user.ID, action: action, target: target, detail: detail})
		},
	}
}

func requireSSOFailureAudit(t *testing.T, backend *oidcTestBackend, wantUserID, wantTarget, wantDetail string) {
	t.Helper()
	if len(backend.audits) != 1 {
		t.Fatalf("audits = %#v, want exactly one failure", backend.audits)
	}
	audit := backend.audits[0]
	if audit.action != "auth.sso_failed" || audit.userID != wantUserID ||
		audit.target != wantTarget || audit.detail != wantDetail {
		t.Fatalf("audit = %#v, want auth.sso_failed user=%q target=%q detail=%q", audit, wantUserID, wantTarget, wantDetail)
	}
}

func oidcFlowClaims(cfg config.Config, provider *mockOIDCProvider, nonce string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":            provider.issuer,
		"aud":            cfg.OIDCClientID,
		"sub":            "subject-1",
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          "person@example.test",
		"email_verified": true,
	}
}

// validateOIDCTokenRequest asserts the authorization-code exchange used the
// expected code and PKCE S256 verifier.
func validateOIDCTokenRequest(t *testing.T, w http.ResponseWriter, r *http.Request, start oidcStartResult) bool {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Errorf("parse token request: %v", err)
		http.Error(w, "bad token request", http.StatusBadRequest)
		return false
	}
	if got := r.Form.Get("grant_type"); got != "authorization_code" {
		t.Errorf("grant_type = %q, want authorization_code", got)
	}
	if got := r.Form.Get("code"); got != "test-code" {
		t.Errorf("code = %q, want test-code", got)
	}
	verifier := r.Form.Get("code_verifier")
	if verifier != start.state.Verifier {
		t.Errorf("code_verifier does not match the state cookie verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != start.challenge {
		t.Errorf("PKCE challenge mismatch: got %q, want %q", got, start.challenge)
	}
	return true
}

func writeOIDCTokenResponse(w http.ResponseWriter, rawIDToken string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     rawIDToken,
	})
}

// serveIDToken configures the mock token endpoint to validate the authorization
// code and PKCE verifier, then issue an ID token with the supplied claims.
func serveIDToken(t *testing.T, provider *mockOIDCProvider, start oidcStartResult, claims jwt.MapClaims) {
	t.Helper()
	provider.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		if !validateOIDCTokenRequest(t, w, r, start) {
			return
		}
		signed, err := provider.signClaims(claims)
		if err != nil {
			t.Errorf("sign ID token: %v", err)
			http.Error(w, "sign", http.StatusInternalServerError)
			return
		}
		writeOIDCTokenResponse(w, signed)
	}
}

// serveRawIDToken issues an already-built token so negative tests can exercise
// signature, issuer, audience, expiry and algorithm rejection.
func serveRawIDToken(t *testing.T, provider *mockOIDCProvider, start oidcStartResult, rawIDToken string) {
	t.Helper()
	provider.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		if !validateOIDCTokenRequest(t, w, r, start) {
			return
		}
		writeOIDCTokenResponse(w, rawIDToken)
	}
}

func TestOIDCStartRedirectAndStateCookie(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)

	request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start?remember=0", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	response := recorder.Result()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302; body: %s", response.StatusCode, recorder.Body.String())
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorization redirect: %v", err)
	}
	if !strings.HasPrefix(location.String(), provider.issuer+"authorize") {
		t.Fatalf("authorization endpoint = %q, want prefix %q", location.String(), provider.issuer+"authorize")
	}
	query := location.Query()
	if got := query.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want code", got)
	}
	if got := query.Get("client_id"); got != cfg.OIDCClientID {
		t.Errorf("client_id = %q, want %q", got, cfg.OIDCClientID)
	}
	if got := query.Get("redirect_uri"); got != cfg.OIDCRedirectURL {
		t.Errorf("redirect_uri = %q, want %q", got, cfg.OIDCRedirectURL)
	}
	scope := strings.Fields(query.Get("scope"))
	if strings.Join(scope, " ") != "openid email profile" {
		t.Errorf("scope = %q, want openid email profile", query.Get("scope"))
	}
	if query.Get("state") == "" || query.Get("nonce") == "" {
		t.Fatal("authorization request is missing state or nonce")
	}
	if query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE parameters missing or not S256: %q", location.RawQuery)
	}

	cookie := findOIDCResponseCookie(response.Cookies(), oidcStateCookie)
	if cookie == nil {
		t.Fatal("start did not set the OIDC state cookie")
	}
	if !cookie.HttpOnly {
		t.Error("state cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Secure {
		t.Error("state cookie is Secure while cookies are configured insecure")
	}
	if cookie.Path != "/" {
		t.Errorf("state cookie path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge != int(oidcStateTTL.Seconds()) {
		t.Errorf("state cookie MaxAge = %d, want %d", cookie.MaxAge, int(oidcStateTTL.Seconds()))
	}
	state, err := server.parseOIDCState(cookie.Value)
	if err != nil {
		t.Fatalf("parse state cookie: %v", err)
	}
	if state.State != query.Get("state") {
		t.Errorf("cookie state = %q, query state = %q", state.State, query.Get("state"))
	}
	if state.Nonce != query.Get("nonce") {
		t.Errorf("cookie nonce = %q, query nonce = %q", state.Nonce, query.Get("nonce"))
	}
	if state.Verifier == "" {
		t.Error("state cookie is missing the PKCE verifier")
	}
	if state.Remember {
		t.Error("remember=0 was not preserved in the state cookie")
	}
	expires := time.Unix(state.ExpiresAt, 0)
	if expires.Before(time.Now().Add(9*time.Minute)) || expires.After(time.Now().Add(11*time.Minute)) {
		t.Errorf("state expiry = %v, want about 10 minutes from now", expires)
	}

	t.Run("remember defaults to true", func(t *testing.T) {
		start := beginOIDCFlow(t, server, "")
		if !start.state.Remember {
			t.Error("remember should default to true")
		}
	})
	t.Run("remember=1", func(t *testing.T) {
		start := beginOIDCFlow(t, server, "?remember=1")
		if !start.state.Remember {
			t.Error("remember=1 should set remember")
		}
	})
	t.Run("remember=false", func(t *testing.T) {
		start := beginOIDCFlow(t, server, "?remember=false")
		if start.state.Remember {
			t.Error("remember=false should disable remember")
		}
	})
	t.Run("secure cookies", func(t *testing.T) {
		secureCfg := oidcFlowConfig(provider)
		secureCfg.CookieSecure = true
		secureServer := oidcFlowServer(t, secureCfg)
		request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start", nil)
		recorder := httptest.NewRecorder()
		secureServer.oidcStart(recorder, request)
		cookie := findOIDCResponseCookie(recorder.Result().Cookies(), oidcStateCookie)
		if cookie == nil || !cookie.Secure {
			t.Fatal("state cookie is not Secure when ALT8MAIL_COOKIE_SECURE is enabled")
		}
	})
}

func TestOIDCDisabledStartAndCallback(t *testing.T) {
	server := oidcFlowServer(t, config.Config{})
	request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	requireSSOErrorRedirect(t, recorder, ssoErrorUnavailable)

	// Exercise the public callback route. A disabled deployment must not audit
	// or touch the callback limiter.
	request = httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=test-code&state=state", nil)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	requireSSOErrorRedirect(t, recorder, ssoErrorUnavailable)
	server.oidcLimit.mu.Lock()
	entries := len(server.oidcLimit.attempts)
	server.oidcLimit.mu.Unlock()
	if entries != 0 {
		t.Fatalf("disabled callback touched the limiter: %d entries", entries)
	}

	// With a limit of one, two disabled callbacks must both stay unavailable:
	// any Fail call would rate-limit the second. The fake audit sink must stay
	// empty, proving no audit was even attempted.
	server.oidcLimit = newLoginLimiter(1, time.Minute)
	backend := newOIDCTestBackend()
	for attempt := 0; attempt < 2; attempt++ {
		recorder = callOIDCCallback(server, backend.deps(), nil, "/api/auth/oidc/callback?code=test-code&state=state")
		requireSSOErrorRedirect(t, recorder, ssoErrorUnavailable)
	}
	if len(backend.audits) != 0 {
		t.Fatalf("disabled callback produced audits: %#v", backend.audits)
	}
	server.oidcLimit.mu.Lock()
	entries = len(server.oidcLimit.attempts)
	server.oidcLimit.mu.Unlock()
	if entries != 0 {
		t.Fatalf("disabled callback touched the limiter: %d entries", entries)
	}
}

func TestOIDCCallbackRouteRegistered(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	server := oidcFlowServer(t, oidcFlowConfig(provider))
	request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=test-code&state=state", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	requireSSOErrorRedirect(t, recorder, ssoErrorState)
}

func TestOIDCCallbackSuccess(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantTTL time.Duration
	}{
		{name: "remember", query: "", wantTTL: 30 * 24 * time.Hour},
		{name: "remember=1", query: "?remember=1", wantTTL: 30 * 24 * time.Hour},
		{name: "remember=0", query: "?remember=0", wantTTL: 12 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newMockOIDCProvider(t, false)
			cfg := oidcFlowConfig(provider)
			server := oidcFlowServer(t, cfg)
			start := beginOIDCFlow(t, server, tc.query)

			claims := oidcFlowClaims(cfg, provider, start.state.Nonce)
			claims["email"] = "Person@Example.Test"
			serveIDToken(t, provider, start, claims)

			backend := newOIDCTestBackend()
			recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
			if recorder.Code != http.StatusFound {
				t.Fatalf("callback status = %d, want 302; body: %s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("Location"); got != "/" {
				t.Fatalf("callback redirect = %q, want /", got)
			}
			session := findOIDCResponseCookie(recorder.Result().Cookies(), sessionCookie)
			if session == nil || session.Value != "raw-session-token" {
				t.Fatalf("session cookie = %#v, want raw-session-token", session)
			}
			csrf := findOIDCResponseCookie(recorder.Result().Cookies(), csrfCookie)
			if csrf == nil || csrf.Value != "csrf-token" {
				t.Fatalf("CSRF cookie = %#v, want csrf-token", csrf)
			}
			cleared := findOIDCResponseCookie(recorder.Result().Cookies(), oidcStateCookie)
			if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
				t.Fatal("callback did not clear the state cookie")
			}
			if got := backend.lookupEmail; len(got) != 1 || got[0] != "person@example.test" {
				t.Fatalf("user lookup = %v, want normalized email", got)
			}
			if got := backend.sessionTTLs; len(got) != 1 || got[0] != tc.wantTTL {
				t.Fatalf("session TTL = %v, want %v", got, tc.wantTTL)
			}
			if got := backend.lastLogins; len(got) != 1 || got[0] != "user-1" {
				t.Fatalf("last login updates = %v, want user-1", got)
			}
			if len(backend.audits) != 1 || backend.audits[0].userID != "user-1" || backend.audits[0].action != "auth.login" ||
				backend.audits[0].target != "person@example.test" || backend.audits[0].detail != "OIDC SSO" {
				t.Fatalf("audit calls = %#v, want auth.login/OIDC SSO", backend.audits)
			}
		})
	}
}

func TestOIDCCallbackStateAndProviderErrors(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)

	valid := beginOIDCFlow(t, server, "")
	tampered := valid
	tampered.cookie = &http.Cookie{Name: oidcStateCookie, Value: valid.cookie.Value + "x"}

	expiredPayload := oidcStatePayload{
		State: "expired-state", Nonce: "expired-nonce", Verifier: "expired-verifier",
		Remember: true, ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}
	expiredValue, err := server.signOIDCState(expiredPayload)
	if err != nil {
		t.Fatalf("sign expired state: %v", err)
	}
	expired := oidcStartResult{state: expiredPayload, cookie: &http.Cookie{Name: oidcStateCookie, Value: expiredValue}}

	cases := []struct {
		name   string
		start  *oidcStartResult
		target string
		want   string
	}{
		{name: "missing cookie", start: nil, target: oidcCallbackURL(valid, "test-code", valid.state.State), want: ssoErrorState},
		{name: "tampered cookie", start: &tampered, target: oidcCallbackURL(valid, "test-code", valid.state.State), want: ssoErrorState},
		{name: "wrong state", start: &valid, target: oidcCallbackURL(valid, "test-code", "different-state"), want: ssoErrorState},
		{name: "expired state", start: &expired, target: oidcCallbackURL(expired, "test-code", expired.state.State), want: ssoErrorState},
		{name: "provider error", start: &valid, target: "/api/auth/oidc/callback?error=access_denied&state=" + url.QueryEscape(valid.state.State), want: ssoErrorDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newOIDCTestBackend()
			recorder := callOIDCCallback(server, backend.deps(), tc.start, tc.target)
			requireSSOErrorRedirect(t, recorder, tc.want)
			requireSSOFailureAudit(t, backend, "", "", tc.want)
		})
	}
}

func TestOIDCCallbackRejectsInvalidIDTokenClaims(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)

	cases := []struct {
		name       string
		mutate     func(jwt.MapClaims) jwt.MapClaims
		want       string
		wantTarget string
	}{
		{
			name: "nonce mismatch",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				claims["nonce"] = "different-nonce"
				return claims
			},
			want: ssoErrorNonce,
		},
		{
			name: "nonce missing",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				delete(claims, "nonce")
				return claims
			},
			want: ssoErrorNonce,
		},
		{
			name: "email unverified",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				claims["email_verified"] = false
				return claims
			},
			want:       ssoErrorEmailUnverified,
			wantTarget: "person@example.test",
		},
		{
			name: "email missing",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				delete(claims, "email")
				return claims
			},
			want: ssoErrorEmail,
		},
		{
			name: "wrong domain",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				claims["email"] = "person@other.test"
				return claims
			},
			want:       ssoErrorDomain,
			wantTarget: "person@other.test",
		},
		{
			name: "domain suffix is not allowed",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				claims["email"] = "person@notexample.test"
				return claims
			},
			want:       ssoErrorDomain,
			wantTarget: "person@notexample.test",
		},
		{
			name: "subdomain is not allowed",
			mutate: func(claims jwt.MapClaims) jwt.MapClaims {
				claims["email"] = "person@sub.example.test"
				return claims
			},
			want:       ssoErrorDomain,
			wantTarget: "person@sub.example.test",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := beginOIDCFlow(t, server, "")
			claims := tc.mutate(oidcFlowClaims(cfg, provider, start.state.Nonce))
			serveIDToken(t, provider, start, claims)
			backend := newOIDCTestBackend()
			recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
			requireSSOErrorRedirect(t, recorder, tc.want)
			if len(backend.lookupEmail) != 0 {
				t.Fatalf("user lookup happened before claim validation: %v", backend.lookupEmail)
			}
			if len(backend.sessionTTLs) != 0 {
				t.Fatal("session was created despite invalid claims")
			}
			requireSSOFailureAudit(t, backend, "", tc.wantTarget, tc.want)
			if strings.Contains(backend.audits[0].detail, "@") || strings.Contains(backend.audits[0].detail, "different-nonce") {
				t.Fatalf("audit detail leaked claim material: %q", backend.audits[0].detail)
			}
		})
	}
}

func TestOIDCCallbackEmptyDomainAllowlistIsUnrestricted(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	cfg.OIDCEmailDomains = nil
	server := oidcFlowServer(t, cfg)
	start := beginOIDCFlow(t, server, "")
	claims := oidcFlowClaims(cfg, provider, start.state.Nonce)
	claims["email"] = "person@other.test"
	serveIDToken(t, provider, start, claims)

	backend := newOIDCTestBackend()
	backend.userErr = errors.New("no rows")
	recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
	requireSSOErrorRedirect(t, recorder, ssoErrorUnknownUser)
	if len(backend.lookupEmail) != 1 || backend.lookupEmail[0] != "person@other.test" {
		t.Fatalf("empty domain allowlist should not reject other.test: %v", backend.lookupEmail)
	}
}

func TestOIDCCallbackUnknownUserIsNotCreated(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)
	start := beginOIDCFlow(t, server, "")
	serveIDToken(t, provider, start, oidcFlowClaims(cfg, provider, start.state.Nonce))

	backend := newOIDCTestBackend()
	backend.userErr = errors.New("no rows")
	recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
	requireSSOErrorRedirect(t, recorder, ssoErrorUnknownUser)
	if len(backend.lookupEmail) != 1 {
		t.Fatalf("user lookups = %v, want exactly one", backend.lookupEmail)
	}
	if len(backend.sessionTTLs) != 0 {
		t.Fatal("callback created a session for an unknown user")
	}
	if len(backend.lastLogins) != 0 {
		t.Fatal("callback updated last_login_at for an unknown user")
	}
	requireSSOFailureAudit(t, backend, "", "person@example.test", ssoErrorUnknownUser)
	if session := findOIDCResponseCookie(recorder.Result().Cookies(), sessionCookie); session != nil {
		t.Fatal("callback set a session cookie for an unknown user")
	}
}

func TestOIDCCallbackTenantPolicies(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)

	t.Run("IP policy blocks callback", func(t *testing.T) {
		start := beginOIDCFlow(t, server, "")
		serveIDToken(t, provider, start, oidcFlowClaims(cfg, provider, start.state.Nonce))
		backend := newOIDCTestBackend()
		backend.policy.Security.AllowedIPRanges = []string{"10.0.0.0/8"}
		recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
		requireSSOErrorRedirect(t, recorder, ssoErrorIPBlocked)
		requireSSOFailureAudit(t, backend, "user-1", "person@example.test", ssoErrorIPBlocked)
		if len(backend.sessionTTLs) != 0 {
			t.Fatal("session created for an IP-blocked user")
		}
	})

	t.Run("MFA policy rejects non-MFA token", func(t *testing.T) {
		start := beginOIDCFlow(t, server, "")
		claims := oidcFlowClaims(cfg, provider, start.state.Nonce)
		claims["amr"] = []string{"pwd"}
		serveIDToken(t, provider, start, claims)
		backend := newOIDCTestBackend()
		backend.policy.Security.MFAEnforcement = "all"
		recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
		requireSSOErrorRedirect(t, recorder, ssoErrorMFARequired)
		requireSSOFailureAudit(t, backend, "user-1", "person@example.test", ssoErrorMFARequired)
		if len(backend.sessionTTLs) != 0 {
			t.Fatal("session created despite MFA policy rejection")
		}
	})

	t.Run("MFA policy accepts amr and acr evidence", func(t *testing.T) {
		variants := []struct {
			name string
			amr  []string
			acr  string
		}{
			{name: "amr mfa", amr: []string{"mfa"}},
			{name: "amr otp", amr: []string{"otp"}},
			{name: "amr hwk", amr: []string{"hwk"}},
			{name: "acr mfa", acr: "urn:example:acr:mfa"},
		}
		for _, variant := range variants {
			t.Run(variant.name, func(t *testing.T) {
				start := beginOIDCFlow(t, server, "")
				claims := oidcFlowClaims(cfg, provider, start.state.Nonce)
				if len(variant.amr) > 0 {
					claims["amr"] = variant.amr
				}
				if variant.acr != "" {
					claims["acr"] = variant.acr
				}
				serveIDToken(t, provider, start, claims)
				backend := newOIDCTestBackend()
				backend.policy.Security.MFAEnforcement = "all"
				recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
				if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/" {
					t.Fatalf("callback status=%d location=%q, want 302 /", recorder.Code, recorder.Header().Get("Location"))
				}
				if len(backend.sessionTTLs) != 1 {
					t.Fatal("MFA evidence did not create a session")
				}
			})
		}
	})

	t.Run("admins MFA policy only affects admin roles", func(t *testing.T) {
		for _, role := range []string{"member", "admin"} {
			t.Run(role, func(t *testing.T) {
				start := beginOIDCFlow(t, server, "")
				serveIDToken(t, provider, start, oidcFlowClaims(cfg, provider, start.state.Nonce))
				backend := newOIDCTestBackend()
				backend.user.Role = role
				backend.policy.Security.MFAEnforcement = "admins"
				recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
				if role == "admin" {
					requireSSOErrorRedirect(t, recorder, ssoErrorMFARequired)
				} else if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/" {
					t.Fatalf("member under admins policy status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
				}
			})
		}
	})
}

func TestOIDCCallbackSanitizedProviderErrors(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)
	start := beginOIDCFlow(t, server, "")
	provider.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"provider-secret-detail"}`))
	}

	backend := newOIDCTestBackend()
	recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
	requireSSOErrorRedirect(t, recorder, ssoErrorExchange)
	body := recorder.Body.String()
	if strings.Contains(body, "provider-secret-detail") || strings.Contains(body, "invalid_grant") {
		t.Fatalf("provider error details leaked into the redirect body: %s", body)
	}
	requireSSOFailureAudit(t, backend, "", "", ssoErrorExchange)
	if strings.Contains(backend.audits[0].detail, "provider-secret-detail") || strings.Contains(backend.audits[0].detail, "invalid_grant") {
		t.Fatalf("provider details leaked into the audit detail: %q", backend.audits[0].detail)
	}
}

func TestOIDCCallbackRejectsInvalidIDTokens(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate second RSA key: %v", err)
	}
	signedWith := func(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey) string {
		t.Helper()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = provider.kid
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("sign ID token: %v", err)
		}
		return raw
	}
	cases := []struct {
		name  string
		build func(t *testing.T, nonce string) string
	}{
		{
			name: "signed by a second RSA key",
			build: func(t *testing.T, nonce string) string {
				return signedWith(t, oidcFlowClaims(cfg, provider, nonce), otherKey)
			},
		},
		{
			name: "wrong issuer",
			build: func(t *testing.T, nonce string) string {
				claims := oidcFlowClaims(cfg, provider, nonce)
				claims["iss"] = "https://wrong.example.test/"
				return signedWith(t, claims, provider.key)
			},
		},
		{
			name: "wrong audience",
			build: func(t *testing.T, nonce string) string {
				claims := oidcFlowClaims(cfg, provider, nonce)
				claims["aud"] = "other-client"
				return signedWith(t, claims, provider.key)
			},
		},
		{
			name: "expired exp",
			build: func(t *testing.T, nonce string) string {
				claims := oidcFlowClaims(cfg, provider, nonce)
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
				return signedWith(t, claims, provider.key)
			},
		},
		{
			name: "unsigned alg none",
			build: func(t *testing.T, nonce string) string {
				token := jwt.NewWithClaims(jwt.SigningMethodNone, oidcFlowClaims(cfg, provider, nonce))
				raw, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
				if err != nil {
					t.Fatalf("build alg=none token: %v", err)
				}
				return raw
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := beginOIDCFlow(t, server, "")
			raw := tc.build(t, start.state.Nonce)
			serveRawIDToken(t, provider, start, raw)
			backend := newOIDCTestBackend()
			recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
			requireSSOErrorRedirect(t, recorder, ssoErrorToken)
			if len(backend.lookupEmail) != 0 || len(backend.sessionTTLs) != 0 {
				t.Fatal("invalid ID token reached account lookup or session creation")
			}
			requireSSOFailureAudit(t, backend, "", "", ssoErrorToken)
			if detail := backend.audits[0].detail; strings.Contains(detail, raw) || strings.Contains(detail, "person@") {
				t.Fatalf("audit detail leaked token or claim material: %q", detail)
			}
		})
	}
}

func TestOIDCCallbackSessionCreationFailure(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)
	start := beginOIDCFlow(t, server, "")
	serveIDToken(t, provider, start, oidcFlowClaims(cfg, provider, start.state.Nonce))

	backend := newOIDCTestBackend()
	backend.sessionErr = errors.New("insert failed")
	recorder := callOIDCCallback(server, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
	requireSSOErrorRedirect(t, recorder, ssoErrorSession)
	if len(backend.sessionTTLs) != 1 {
		t.Fatalf("session attempts = %d, want 1", len(backend.sessionTTLs))
	}
	if len(backend.lastLogins) != 0 {
		t.Fatal("last_login_at was updated despite a session failure")
	}
	if session := findOIDCResponseCookie(recorder.Result().Cookies(), sessionCookie); session != nil {
		t.Fatal("session cookie was set despite a session failure")
	}
	requireSSOFailureAudit(t, backend, "user-1", "person@example.test", ssoErrorSession)
}

func TestOIDCCallbackStateCookieKeyMismatch(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	startCfg := oidcFlowConfig(provider)
	startServer := oidcFlowServer(t, startCfg)
	start := beginOIDCFlow(t, startServer, "")

	callbackCfg := oidcFlowConfig(provider)
	callbackCfg.SessionSecret = "different-session-secret"
	callbackCfg.CSRFSecret = "different-csrf-secret"
	callbackServer := oidcFlowServer(t, callbackCfg)

	backend := newOIDCTestBackend()
	recorder := callOIDCCallback(callbackServer, backend.deps(), &start, oidcCallbackURL(start, "test-code", start.state.State))
	requireSSOErrorRedirect(t, recorder, ssoErrorState)
	requireSSOFailureAudit(t, backend, "", "", ssoErrorState)
}

func TestOIDCCallbackRateLimit(t *testing.T) {
	provider := newMockOIDCProvider(t, false)
	cfg := oidcFlowConfig(provider)
	server := oidcFlowServer(t, cfg)
	server.oidcLimit = newLoginLimiter(2, time.Minute)
	backend := newOIDCTestBackend()
	request := func() *httptest.ResponseRecorder {
		return callOIDCCallback(server, backend.deps(), nil, "/api/auth/oidc/callback?code=test-code&state=state")
	}
	requireSSOErrorRedirect(t, request(), ssoErrorState)
	requireSSOErrorRedirect(t, request(), ssoErrorState)
	// The limiter itself is the signal: over-limit requests are rejected
	// without an audit entry and without extending the limit.
	requireSSOErrorRedirect(t, request(), ssoErrorRateLimited)
	requireSSOErrorRedirect(t, request(), ssoErrorRateLimited)
	if len(backend.audits) != 2 {
		t.Fatalf("rate-limit audits = %#v, want only the two in-limit failures", backend.audits)
	}
	for index, audit := range backend.audits {
		if audit.action != "auth.sso_failed" || audit.userID != "" || audit.target != "" || audit.detail != ssoErrorState {
			t.Fatalf("in-limit audit %d = %#v", index, audit)
		}
	}
	server.oidcLimit.mu.Lock()
	count := server.oidcLimit.attempts["192.0.2.1"].Count
	server.oidcLimit.mu.Unlock()
	if count != 2 {
		t.Fatalf("limiter count = %d, want 2 (over-limit requests must not extend it)", count)
	}
}
