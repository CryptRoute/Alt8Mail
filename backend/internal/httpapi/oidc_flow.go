package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	// oidcStateCookie carries the single-use state, nonce and PKCE verifier
	// between the start redirect and the callback.
	oidcStateCookie = "a8m_oidc_state"
	// oidcStateTTL bounds how long a user may spend at the identity provider.
	oidcStateTTL = 10 * time.Minute

	oidcCallbackRateLimit  = 20
	oidcCallbackRateWindow = 10 * time.Minute
)

// Safe, fixed error codes exposed to the browser as /?sso_error=<code>. The
// provider response, code, tokens and claims are never included.
const (
	ssoErrorUnavailable     = "sso_unavailable"
	ssoErrorState           = "sso_state"
	ssoErrorDenied          = "sso_denied"
	ssoErrorExchange        = "sso_exchange"
	ssoErrorToken           = "sso_token"
	ssoErrorNonce           = "sso_nonce"
	ssoErrorEmail           = "sso_email"
	ssoErrorEmailUnverified = "sso_email_unverified"
	ssoErrorDomain          = "sso_domain"
	ssoErrorUnknownUser     = "sso_unknown_user"
	ssoErrorIPBlocked       = "sso_ip_blocked"
	ssoErrorMFARequired     = "sso_mfa_required"
	ssoErrorSession         = "sso_session"
	ssoErrorRateLimited     = "sso_rate_limited"
)

// oidcStatePayload is the signed content of the state cookie. It is bound to a
// single authorization request and expires after oidcStateTTL.
type oidcStatePayload struct {
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	Verifier  string `json:"verifier"`
	Remember  bool   `json:"remember"`
	ExpiresAt int64  `json:"expiresAt"`
}

// oidcIDTokenClaims are the ID-token claims the login flow needs. Anything else
// in the token is ignored and never logged.
type oidcIDTokenClaims struct {
	Email         string          `json:"email"`
	EmailVerified *bool           `json:"email_verified"`
	AMR           []string        `json:"amr"`
	ACR           json.RawMessage `json:"acr"`
}

// oidcAuthDeps groups the account/session operations used by the callback.
// Production uses Server methods; the focused login-flow tests substitute these
// so the protocol can be exercised without PostgreSQL.
type oidcAuthDeps struct {
	userByEmail     func(context.Context, string) (userRecord, error)
	tenantPolicy    func(context.Context, string) adminTenantPolicy
	createSession   func(*http.Request, userRecord, time.Duration) (*sessionRecord, string, error)
	updateLastLogin func(context.Context, string)
	audit           func(*http.Request, userRecord, string, string, string)
}

func (s *Server) oidcAuthDeps() oidcAuthDeps {
	return oidcAuthDeps{
		userByEmail:  s.userByEmail,
		tenantPolicy: s.tenantPolicyFor,
		createSession: func(r *http.Request, user userRecord, ttl time.Duration) (*sessionRecord, string, error) {
			return s.createSession(r, user, ttl)
		},
		updateLastLogin: func(ctx context.Context, userID string) {
			_, _ = s.store.DB.Exec(ctx, `UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1`, userID)
		},
		audit: func(r *http.Request, user userRecord, action, target, detail string) {
			// Focused flow tests call the handler with nil storage; production
			// always has a store.
			if s.store == nil || s.store.DB == nil {
				return
			}
			s.audit(r, user, action, target, detail)
		},
	}
}

// oidcStart begins the authorization-code flow. It generates a fresh state,
// nonce and PKCE S256 verifier, stores them in a single-use HttpOnly cookie and
// redirects the browser to the identity provider.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if !s.oidcConfigured() {
		s.clearOIDCStateCookie(w)
		s.oidcErrorRedirect(w, r, ssoErrorUnavailable)
		return
	}
	client, err := s.oidcClient(r.Context())
	if err != nil {
		s.clearOIDCStateCookie(w)
		s.oidcErrorRedirect(w, r, ssoErrorUnavailable)
		return
	}
	verifier := oauth2.GenerateVerifier()
	state := oidcStatePayload{
		State:     randomToken(32),
		Nonce:     randomToken(32),
		Verifier:  verifier,
		Remember:  oidcRememberRequested(r.URL.Query().Get("remember")),
		ExpiresAt: time.Now().Add(oidcStateTTL).Unix(),
	}
	value, err := s.signOIDCState(state)
	if err != nil {
		s.clearOIDCStateCookie(w)
		s.oidcErrorRedirect(w, r, ssoErrorUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(state.ExpiresAt, 0), MaxAge: int(oidcStateTTL.Seconds()),
	})
	authorizeURL := client.config.AuthCodeURL(
		state.State,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", state.Nonce),
	)
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// oidcCallback completes the authorization-code flow. Every terminal path
// clears the single-use state cookie and redirects either to / or to a fixed
// /?sso_error=<code> location.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	s.handleOIDCCallback(w, r, s.oidcAuthDeps())
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request, deps oidcAuthDeps) {
	// The state cookie is single-use: clear it before any validation so every
	// outcome, including malformed callbacks, retires it.
	s.clearOIDCStateCookie(w)

	ip := clientIP(r)
	if !s.oidcConfigured() {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorUnavailable, ip)
		return
	}
	if s.oidcLimit != nil && !s.oidcLimit.Allow(ip) {
		// Already over the limit: record the failure but do not extend it.
		s.auditOIDCFailure(r, deps, userRecord{}, "", ssoErrorRateLimited)
		s.oidcErrorRedirect(w, r, ssoErrorRateLimited)
		return
	}

	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorState, ip)
		return
	}
	state, err := s.parseOIDCState(cookie.Value)
	if err != nil {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorState, ip)
		return
	}
	query := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state.State)) != 1 {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorState, ip)
		return
	}
	if query.Get("error") != "" {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorDenied, ip)
		return
	}
	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorExchange, ip)
		return
	}

	client, err := s.oidcClient(r.Context())
	if err != nil {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorUnavailable, ip)
		return
	}
	token, err := client.config.Exchange(r.Context(), code, oauth2.VerifierOption(state.Verifier))
	if err != nil {
		// Do not log the OAuth2 error: it can contain provider response details.
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorExchange, ip)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorToken, ip)
		return
	}
	idToken, err := client.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorToken, ip)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(state.Nonce)) != 1 {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorNonce, ip)
		return
	}
	var claims oidcIDTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorToken, ip)
		return
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	auditTarget := ""
	if email != "" && strings.Contains(email, "@") {
		auditTarget = email
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		s.failOIDCCallback(w, r, deps, userRecord{}, auditTarget, ssoErrorEmailUnverified, ip)
		return
	}
	if email == "" || !strings.Contains(email, "@") {
		s.failOIDCCallback(w, r, deps, userRecord{}, "", ssoErrorEmail, ip)
		return
	}
	if !s.oidcEmailDomainAllowed(email) {
		s.failOIDCCallback(w, r, deps, userRecord{}, email, ssoErrorDomain, ip)
		return
	}

	// SSO only signs in existing active users. There is no JIT provisioning.
	user, err := deps.userByEmail(r.Context(), email)
	if err != nil || user.ID == "" {
		s.failOIDCCallback(w, r, deps, userRecord{}, email, ssoErrorUnknownUser, ip)
		return
	}
	policy := deps.tenantPolicy(r.Context(), user.TenantID)
	if !adminIPAllowed(policy.Security.AllowedIPRanges, ip) {
		s.failOIDCCallback(w, r, deps, user, user.Email, ssoErrorIPBlocked, ip)
		return
	}
	if oidcPolicyRequiresMFA(policy, user) && !oidcMFAStrength(claims.AMR, claims.ACR) {
		s.failOIDCCallback(w, r, deps, user, user.Email, ssoErrorMFARequired, ip)
		return
	}

	ttl := s.cfg.SessionTTL
	if state.Remember {
		ttl = s.cfg.RememberTTL
	}
	session, rawToken, err := deps.createSession(r, user, ttl)
	if err != nil || session == nil || rawToken == "" {
		s.failOIDCCallback(w, r, deps, user, user.Email, ssoErrorSession, ip)
		return
	}
	if s.oidcLimit != nil {
		s.oidcLimit.Reset(ip)
	}
	s.setSessionCookies(w, rawToken, session.CSRF, ttl)
	if deps.updateLastLogin != nil {
		deps.updateLastLogin(r.Context(), user.ID)
	}
	if deps.audit != nil {
		deps.audit(r, user, "auth.login", user.Email, "OIDC SSO")
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// failOIDCCallback records a failed attempt for rate limiting, writes a
// sanitized audit entry and sends the browser to a fixed, non-leaking error
// location. The audit target is only a normalized email after a verified ID
// token; the detail is the fixed safe error code and nothing else.
func (s *Server) failOIDCCallback(w http.ResponseWriter, r *http.Request, deps oidcAuthDeps, user userRecord, target, code, ip string) {
	if s.oidcLimit != nil {
		s.oidcLimit.Fail(ip)
	}
	s.auditOIDCFailure(r, deps, user, target, code)
	s.oidcErrorRedirect(w, r, code)
}

func (s *Server) auditOIDCFailure(r *http.Request, deps oidcAuthDeps, user userRecord, target, code string) {
	if deps.audit == nil {
		return
	}
	deps.audit(r, user, "auth.sso_failed", target, code)
}

func (s *Server) oidcErrorRedirect(w http.ResponseWriter, r *http.Request, code string) {
	// code is always one of the fixed ssoError* constants above.
	http.Redirect(w, r, "/?sso_error="+url.QueryEscape(code), http.StatusFound)
}

func (s *Server) clearOIDCStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) oidcStateKey() []byte {
	sum := sha256.Sum256([]byte("alt8mail-oidc-state\x00" + s.cfg.SessionSecret + "\x00" + s.cfg.CSRFSecret))
	return sum[:]
}

func (s *Server) signOIDCState(state oidcStatePayload) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.oidcStateKey())
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) parseOIDCState(value string) (oidcStatePayload, error) {
	var state oidcStatePayload
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return state, errors.New("malformed OIDC state cookie")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return state, errors.New("malformed OIDC state cookie")
	}
	mac := hmac.New(sha256.New, s.oidcStateKey())
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(mac.Sum(nil), provided) {
		return state, errors.New("invalid OIDC state signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return state, errors.New("malformed OIDC state cookie")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, errors.New("malformed OIDC state cookie")
	}
	if state.State == "" || state.Nonce == "" || state.Verifier == "" || state.ExpiresAt == 0 {
		return state, errors.New("incomplete OIDC state cookie")
	}
	if !time.Now().Before(time.Unix(state.ExpiresAt, 0)) {
		return state, errors.New("expired OIDC state cookie")
	}
	return state, nil
}

func oidcRememberRequested(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (s *Server) oidcEmailDomainAllowed(email string) bool {
	if len(s.cfg.OIDCEmailDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, allowed := range s.cfg.OIDCEmailDomains {
		allowed = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(allowed, "@")))
		if allowed != "" && domain == allowed {
			return true
		}
	}
	return false
}

func oidcPolicyRequiresMFA(policy adminTenantPolicy, user userRecord) bool {
	switch policy.Security.MFAEnforcement {
	case "all":
		return true
	case "admins":
		return user.Role == "owner" || user.Role == "admin"
	default:
		return false
	}
}

// oidcMFAStrength reports whether the ID token carries evidence of a second
// factor. Accepted amr values are mfa, otp and hwk; any acr containing "mfa" is
// also accepted.
func oidcMFAStrength(amr []string, acr json.RawMessage) bool {
	for _, method := range amr {
		switch strings.ToLower(strings.TrimSpace(method)) {
		case "mfa", "otp", "hwk":
			return true
		}
	}
	if len(acr) == 0 {
		return false
	}
	var single string
	if err := json.Unmarshal(acr, &single); err == nil {
		return strings.Contains(strings.ToLower(single), "mfa")
	}
	var list []string
	if err := json.Unmarshal(acr, &list); err == nil {
		for _, value := range list {
			if strings.Contains(strings.ToLower(value), "mfa") {
				return true
			}
		}
	}
	return false
}
