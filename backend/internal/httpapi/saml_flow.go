package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
)

// Fixed, non-leaking audit codes for the SAML bridge. They are written to the
// audit log only; the browser always receives the same generic
// /?saml_error=1 redirect.
const (
	samlErrorAssertion   = "saml_assertion"
	samlErrorEmail       = "saml_email"
	samlErrorDomain      = "saml_domain"
	samlErrorUnknownUser = "saml_unknown_user"
	samlErrorIPBlocked   = "saml_ip_blocked"
	samlErrorMFARequired = "saml_mfa_required"
	samlErrorSession     = "saml_session"

	// samlReplayCacheSize bounds the in-process memory replay cache. It only
	// protects one process; a multi-server deployment needs a shared store.
	samlReplayCacheSize = 1024
	// samlMaxACSBodySize caps the ACS form body before it is parsed. The ACS
	// accepts only an HTTP-POST form, so 1 MiB is far above any real response.
	samlMaxACSBodySize = 1 << 20
)

// samlAuthError is returned by the session provider after it has already
// audited a failure and applied the rate-limit penalty. Its message is fixed
// and generic: crewjam validation errors can embed the raw assertion.
type samlAuthError struct {
	audited bool
}

func (err *samlAuthError) Error() string {
	return "SAML authentication failed"
}

// samlSessionDeps groups the account and session operations of the ACS
// bridge. Production uses Server methods; the focused flow tests substitute
// these so the protocol can be exercised without PostgreSQL.
type samlSessionDeps struct {
	userByEmail       func(context.Context, string) (userRecord, error)
	tenantPolicy      func(context.Context, string) adminTenantPolicy
	createSession     func(*http.Request, userRecord, time.Duration) (*sessionRecord, string, error)
	updateLastLogin   func(context.Context, string)
	audit             func(*http.Request, userRecord, string, string, string)
	setSessionCookies func(http.ResponseWriter, string, string, time.Duration)
}

func (s *Server) samlSessionDeps() samlSessionDeps {
	return samlSessionDeps{
		userByEmail:  s.userByEmail,
		tenantPolicy: s.tenantPolicyFor,
		createSession: func(r *http.Request, user userRecord, ttl time.Duration) (*sessionRecord, string, error) {
			return s.createSession(r, user, ttl)
		},
		updateLastLogin: func(ctx context.Context, userID string) {
			_, _ = s.store.DB.Exec(ctx, `UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1`, userID)
		},
		audit: func(r *http.Request, user userRecord, action, target, detail string) {
			// Focused flow tests call the bridge with nil storage; production
			// always has a store.
			if s.store == nil || s.store.DB == nil {
				return
			}
			s.audit(r, user, action, target, detail)
		},
		setSessionCookies: s.setSessionCookies,
	}
}

// samlSessionProvider bridges a validated SAML assertion into an Alt8Mail
// session. It replaces the library's cookie session provider; there is no
// SAML-protected route that reads it back, so GetSession always reports no
// session and the SAML session cookie is never issued.
type samlSessionProvider struct {
	s    *Server
	deps samlSessionDeps
}

func (s *Server) newSAMLSessionProvider() *samlSessionProvider {
	return &samlSessionProvider{s: s, deps: s.samlSessionDeps()}
}

var _ samlsp.SessionProvider = (*samlSessionProvider)(nil)

// CreateSession runs the account, domain, tenant IP and MFA policy checks and
// then creates the normal Alt8Mail session. It never writes an error response:
// a failure is audited and returned as *samlAuthError for the middleware's
// sanitized OnError handler.
func (provider *samlSessionProvider) CreateSession(w http.ResponseWriter, r *http.Request, assertion *saml.Assertion) error {
	server := provider.s
	ip := clientIP(r)

	email, err := samlAssertionEmail(assertion, server.cfg.SAMLEmailAttribute)
	if err != nil {
		return provider.fail(r, userRecord{}, "", samlErrorEmail, ip)
	}
	if !emailDomainAllowed(server.cfg.SAMLEmailDomains, email) {
		return provider.fail(r, userRecord{}, email, samlErrorDomain, ip)
	}

	// SSO only signs in existing active users. There is no JIT provisioning and
	// no profile mutation (name, email, aliases, password, role stay untouched).
	user, err := provider.deps.userByEmail(r.Context(), email)
	if err != nil || user.ID == "" {
		return provider.fail(r, userRecord{}, email, samlErrorUnknownUser, ip)
	}
	policy := provider.deps.tenantPolicy(r.Context(), user.TenantID)
	if !adminIPAllowed(policy.Security.AllowedIPRanges, ip) {
		return provider.fail(r, user, user.Email, samlErrorIPBlocked, ip)
	}
	if policyRequiresMFA(policy, user) && !samlMFAStrength(assertion, server.cfg.SAMLMFAContexts) {
		return provider.fail(r, user, user.Email, samlErrorMFARequired, ip)
	}

	// SAML has no "remember me" support in this tranche: the session always
	// uses the configured session TTL.
	ttl := server.cfg.SessionTTL
	session, rawToken, err := provider.deps.createSession(r, user, ttl)
	if err != nil || session == nil || rawToken == "" {
		return provider.fail(r, user, user.Email, samlErrorSession, ip)
	}
	if provider.deps.setSessionCookies != nil {
		provider.deps.setSessionCookies(w, rawToken, session.CSRF, ttl)
	}
	if provider.deps.updateLastLogin != nil {
		provider.deps.updateLastLogin(r.Context(), user.ID)
	}
	if provider.deps.audit != nil {
		provider.deps.audit(r, user, "auth.login", user.Email, "SAML SSO")
	}
	if server.samlLimit != nil {
		server.samlLimit.Reset(ip)
	}
	return nil
}

// fail applies the failure bookkeeping once and returns the authenticated
// error the sanitized OnError handler recognizes as already handled.
func (provider *samlSessionProvider) fail(r *http.Request, user userRecord, target, code, ip string) error {
	if provider.s.samlLimit != nil {
		provider.s.samlLimit.Fail(ip)
	}
	if provider.deps.audit != nil {
		provider.deps.audit(r, user, "auth.sso_failed", target, code)
	}
	return &samlAuthError{audited: true}
}

// GetSession reports that no SAML session exists. No route is protected by
// samlsp.RequireAccount: Alt8Mail sessions are the only session mechanism.
func (provider *samlSessionProvider) GetSession(*http.Request) (samlsp.Session, error) {
	return nil, samlsp.ErrNoSession
}

// DeleteSession clears the Alt8Mail session and CSRF cookies.
func (provider *samlSessionProvider) DeleteSession(w http.ResponseWriter, _ *http.Request) error {
	provider.s.clearSessionCookies(w)
	return nil
}

// samlStart begins the SP-initiated flow. It redirects the browser to the IdP
// with a signed AuthnRequest and a single-use request-tracking cookie.
func (s *Server) samlStart(w http.ResponseWriter, r *http.Request) {
	// With SAML disabled there is no configured failure path: no audit and no
	// limiter touch, mirroring the OIDC start endpoint.
	if !s.samlConfigured() {
		s.samlErrorRedirect(w, r)
		return
	}
	middleware, err := s.samlMiddleware(r.Context())
	if err != nil {
		// The error describes configuration or metadata loading, but log only a
		// fixed warning so nothing provider-specific can leak.
		if s.logger != nil {
			s.logger.Warn("SAML start unavailable", "path", r.URL.Path)
		}
		s.samlErrorRedirect(w, r)
		return
	}

	// The request tracker records the request URI as the post-login
	// destination. Redirecting there after the ACS would restart this endpoint,
	// so present the flow as if it began at the application root. The IdP only
	// sees the SP's configured ACS/metadata URLs, never this path.
	flowRequest := r.Clone(r.Context())
	flowURL := *r.URL
	flowURL.Path = "/"
	flowURL.RawPath = ""
	flowURL.RawQuery = ""
	flowRequest.URL = &flowURL
	middleware.HandleStartAuthFlow(w, flowRequest)
}

// samlACS receives the IdP's HTTP-POST response. The limiter is the only
// pre-assertion gate; validation failures are audited by samlOnError and every
// outcome redirects to the login page.
func (s *Server) samlACS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !s.samlConfigured() {
		s.samlErrorRedirect(w, r)
		return
	}
	ip := clientIP(r)
	if s.samlLimit != nil && !s.samlLimit.Allow(ip) {
		// The limiter is the signal: over-limit requests are not audited and do
		// not extend the limiter.
		s.samlErrorRedirect(w, r)
		return
	}

	// Bound and parse the only ACS representation in scope before the library
	// sees it. Reject any other content type without parsing, then cap the body
	// and parse exactly once; ServeACS reuses the parsed form.
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		s.samlRejectRequest(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, samlMaxACSBodySize)
	if err := r.ParseForm(); err != nil {
		s.samlRejectRequest(w, r)
		return
	}
	// Artifact binding is out of scope and must never reach ServeACS. Its
	// resolution path consumes entries from the shared assertion replay cache
	// before authentication and could lock legitimate logins out.
	if r.PostForm.Has("SAMLart") || r.Form.Has("SAMLart") {
		s.samlRejectRequest(w, r)
		return
	}

	middleware, err := s.samlMiddleware(r.Context())
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("SAML ACS unavailable", "path", r.URL.Path)
		}
		s.samlErrorRedirect(w, r)
		return
	}
	// The SAML library has an upstream nil-pointer advisory
	// (GO-2026-6909) with no fixed release. Contain a malformed assertion
	// panic so it cannot take down the request goroutine or leak a stack.
	defer func() {
		if recovered := recover(); recovered != nil {
			if s.logger != nil {
				s.logger.Warn("SAML assertion processing panic contained", "path", r.URL.Path)
			}
			s.samlRejectRequest(w, r)
		}
	}()
	middleware.ServeACS(w, r)
}

// samlRejectRequest records one fixed assertion failure, applies one rate-limit
// penalty and sends the fixed redirect. It is shared by the ACS input gates
// and the middleware's sanitized OnError handler so neither path can audit or
// penalize twice.
func (s *Server) samlRejectRequest(w http.ResponseWriter, r *http.Request) {
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	if s.logger != nil {
		s.logger.Warn("SAML request failed", "path", path)
	}
	s.auditSAMLFailure(r, userRecord{}, "", samlErrorAssertion)
	if s.samlLimit != nil {
		s.samlLimit.Fail(clientIP(r))
	}
	s.samlErrorRedirect(w, r)
}

// samlReplayCacheEntriesOrDefault returns the configured replay cache bound,
// or the production default when no test override is set.
func (s *Server) samlReplayCacheEntriesOrDefault() int {
	if s.samlReplayCacheEntries > 0 {
		return s.samlReplayCacheEntries
	}
	return samlReplayCacheSize
}

// samlErrorRedirect sends the browser to a fixed, non-leaking location. It is
// the only failure representation the browser ever sees.
func (s *Server) samlErrorRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/?saml_error=1", http.StatusFound)
}

// auditSAMLFailure records an assertion-validation failure. samlFailureAudit is
// a test seam; production uses the normal audit log.
func (s *Server) auditSAMLFailure(r *http.Request, user userRecord, target, code string) {
	if s.samlFailureAudit != nil {
		s.samlFailureAudit(r, user, "auth.sso_failed", target, code)
		return
	}
	if s.store == nil || s.store.DB == nil {
		return
	}
	s.audit(r, user, "auth.sso_failed", target, code)
}

// samlAssertionEmail extracts and normalizes the user's email from the signed
// assertion. A configured attribute is authoritative; otherwise the NameID
// value must itself be an email. The signed assertion is the trust boundary.
func samlAssertionEmail(assertion *saml.Assertion, attribute string) (string, error) {
	attribute = strings.TrimSpace(attribute)
	if attribute != "" {
		if assertion == nil {
			return "", errors.New("SAML assertion is missing")
		}
		values := []string{}
		for _, statement := range assertion.AttributeStatements {
			for _, item := range statement.Attributes {
				// Match Name exactly: FriendlyName is display metadata and may
				// collide across attributes.
				if item.Name != attribute {
					continue
				}
				for _, value := range item.Values {
					if trimmed := strings.TrimSpace(value.Value); trimmed != "" {
						values = append(values, trimmed)
					}
				}
			}
		}
		if len(values) != 1 {
			return "", errors.New("SAML email attribute must have exactly one non-empty value")
		}
		email := strings.ToLower(values[0])
		if !samlEmailValid(email) {
			return "", errors.New("SAML email attribute is not a valid email address")
		}
		return email, nil
	}

	if assertion == nil || assertion.Subject == nil || assertion.Subject.NameID == nil {
		return "", errors.New("SAML assertion has no NameID")
	}
	email := strings.ToLower(strings.TrimSpace(assertion.Subject.NameID.Value))
	if !samlEmailValid(email) {
		return "", errors.New("SAML NameID is not a valid email address")
	}
	return email, nil
}

// samlEmailValid requires exactly one '@' with non-empty local and domain parts
// and no whitespace. It does not require a particular NameID Format.
func samlEmailValid(email string) bool {
	if strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	at := strings.Index(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	return !strings.Contains(email[at+1:], "@")
}

// samlMFAContextClassRefs is the built-in allowlist of exact
// AuthnContextClassRef values that prove a second factor. Substring matching is
// deliberately not used: an IdP-controlled value must match exactly.
var samlMFAContextClassRefs = map[string]bool{
	"urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorContract":                       true,
	"urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorUnregistered":                   true,
	"urn:oasis:names:tc:SAML:2.0:ac:classes:Smartcard":                                     true,
	"urn:oasis:names:tc:SAML:2.0:ac:classes:SmartcardPKI":                                  true,
	"urn:oasis:names:tc:SAML:2.0:ac:classes:SoftwarePKI":                                   true,
	"urn:oasis:names:tc:SAML:2.0:ac:classes:TimeSyncToken":                                 true,
	"urn:oasis:names:tc:SAML:2.0:ac:classes:Token":                                         true,
	"http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa":            true,
	"http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/multipleauthn":  true,
	"http://schemas.microsoft.com/claims/multipleauthn":                                    true,
	"https://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa":           true,
	"https://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/multipleauthn": true,
}

// samlACRPasswordProtectedTransport is the SAML class a password-only IdP
// asserts. It never satisfies a tenant MFA policy.
const samlACRPasswordProtectedTransport = "urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport"

// samlMFAStrength reports whether any AuthnStatement carries an exact
// AuthnContextClassRef from the built-in allowlist or the configured additions.
func samlMFAStrength(assertion *saml.Assertion, extra []string) bool {
	if assertion == nil {
		return false
	}
	for _, statement := range assertion.AuthnStatements {
		classRef := statement.AuthnContext.AuthnContextClassRef
		if classRef == nil {
			continue
		}
		value := classRef.Value
		if value == "" {
			continue
		}
		if samlMFAContextClassRefs[value] {
			return true
		}
		for _, configured := range extra {
			if value == strings.TrimSpace(configured) {
				return true
			}
		}
	}
	return false
}
