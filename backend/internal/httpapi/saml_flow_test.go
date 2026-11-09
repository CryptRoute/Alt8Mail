package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/crewjam/saml"
)

// ---------------------------------------------------------------------------
// Local mock IdP
//
// Every assertion in these tests is produced by crewjam's saml.IdentityProvider
// from a local, throwaway key pair that is also published in a metadata file
// the server reads. No live identity provider is contacted.
// ---------------------------------------------------------------------------

func newSAMLIDPTestSigner(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate IdP RSA key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "Alt8Mail SAML Test IdP"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create IdP certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse IdP certificate: %v", err)
	}
	return key, certificate
}

type samlTestSPProvider func() *saml.EntityDescriptor

func (provider samlTestSPProvider) GetServiceProvider(*http.Request, string) (*saml.EntityDescriptor, error) {
	if metadata := provider(); metadata != nil {
		return metadata, nil
	}
	return nil, os.ErrNotExist
}

// samlTestBackend substitutes the account/session dependencies of the SAML
// session bridge so the ACS tests do not require PostgreSQL.
type samlTestBackend struct {
	user        userRecord
	userErr     error
	policy      adminTenantPolicy
	sessionErr  error
	lookups     []string
	sessionTTLs []time.Duration
	lastLogins  []string
	audits      []oidcTestAudit
}

func newSAMLTestBackend() *samlTestBackend {
	return &samlTestBackend{
		user: userRecord{
			ID: "user-1", TenantID: "tenant-1", Email: "person@example.test",
			Name: "Person", Role: "member", Status: "active",
		},
		policy: defaultTenantPolicy(),
	}
}

func (backend *samlTestBackend) audit(r *http.Request, user userRecord, action, target, detail string) {
	backend.audits = append(backend.audits, oidcTestAudit{userID: user.ID, action: action, target: target, detail: detail})
}

func (backend *samlTestBackend) deps(server *Server) samlSessionDeps {
	return samlSessionDeps{
		userByEmail: func(ctx context.Context, email string) (userRecord, error) {
			backend.lookups = append(backend.lookups, email)
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
		audit:             backend.audit,
		setSessionCookies: server.setSessionCookies,
	}
}

func (backend *samlTestBackend) reset() {
	backend.lookups = nil
	backend.sessionTTLs = nil
	backend.lastLogins = nil
	backend.audits = nil
}

// samlFlowHarness wires the production start/ACS handlers to a local IdP.
type samlFlowHarness struct {
	server  *Server
	idp     *saml.IdentityProvider
	backend *samlTestBackend
	logs    *strings.Builder
}

func newSAMLFlowHarness(t *testing.T, mutate func(*config.Config)) *samlFlowHarness {
	t.Helper()
	env := newSAMLTestEnv(t)
	idpKey, idpCertificate := newSAMLIDPTestSigner(t)

	idp := &saml.IdentityProvider{
		Key:         idpKey,
		Certificate: idpCertificate,
		MetadataURL: url.URL{Scheme: "https", Host: "idp.example.test", Path: "/metadata"},
		SSOURL:      url.URL{Scheme: "https", Host: "idp.example.test", Path: "/sso"},
	}
	var spMetadata *saml.EntityDescriptor
	idp.ServiceProviderProvider = samlTestSPProvider(func() *saml.EntityDescriptor { return spMetadata })

	rawMetadata, err := xml.MarshalIndent(idp.Metadata(), "", "  ")
	if err != nil {
		t.Fatalf("marshal IdP metadata: %v", err)
	}
	if err := os.WriteFile(env.metadataFile, rawMetadata, 0o600); err != nil {
		t.Fatalf("write IdP metadata: %v", err)
	}

	cfg := env.cfg
	cfg.PublicURL = "http://127.0.0.1:8001"
	cfg.SessionTTL = 12 * time.Hour
	cfg.RememberTTL = 30 * 24 * time.Hour
	if mutate != nil {
		mutate(&cfg)
	}

	var logs strings.Builder
	server := New(Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	installSAMLLicense(t, server)

	middleware, err := server.samlMiddleware(context.Background())
	if err != nil {
		t.Fatalf("build SAML middleware: %v", err)
	}
	spMetadata = middleware.ServiceProvider.Metadata()

	backend := newSAMLTestBackend()
	middleware.Session = &samlSessionProvider{s: server, deps: backend.deps(server)}
	server.samlFailureAudit = backend.audit

	return &samlFlowHarness{server: server, idp: idp, backend: backend, logs: &logs}
}

// samlFlowAttempt is one start request: its IdP redirect, tracking cookie and
// the AuthnRequest ID the eventual response must answer.
type samlFlowAttempt struct {
	harness  *samlFlowHarness
	location *url.URL
	cookie   *http.Cookie
	request  *saml.IdpAuthnRequest
}

func (harness *samlFlowHarness) start(t *testing.T) *samlFlowAttempt {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/saml/start", nil)
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("start status = %d, want 302; body: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse start redirect: %v", err)
	}
	if location.Query().Get("SAMLRequest") == "" {
		t.Fatalf("start redirect %q has no SAMLRequest", location)
	}
	var trackingCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, "saml_") {
			trackingCookie = cookie
			break
		}
	}
	if trackingCookie == nil {
		t.Fatal("start did not set a SAML request-tracking cookie")
	}

	// The IdP receives the signed AuthnRequest exactly as the browser would
	// send it. Validate checks the redirect-binding signature against the SP
	// certificate in the metadata the IdP has for this SP.
	idpRequest := httptest.NewRequest(http.MethodGet, location.String(), nil)
	flowRequest, err := saml.NewIdpAuthnRequest(harness.idp, idpRequest)
	if err != nil {
		t.Fatalf("IdP parse AuthnRequest: %v", err)
	}
	if err := flowRequest.Validate(); err != nil {
		t.Fatalf("IdP reject AuthnRequest: %v", err)
	}
	return &samlFlowAttempt{harness: harness, location: location, cookie: trackingCookie, request: flowRequest}
}

// response produces a signed SAML response for the attempt. maker may be nil
// for the default assertion; a mutating maker lets a test change the assertion
// before it is signed.
func (attempt *samlFlowAttempt) response(t *testing.T, session *saml.Session, maker saml.AssertionMaker) saml.IdpAuthnRequestForm {
	t.Helper()
	request := attempt.request
	if maker == nil {
		maker = saml.DefaultAssertionMaker{}
	}
	if err := maker.MakeAssertion(request, session); err != nil {
		t.Fatalf("IdP make assertion: %v", err)
	}
	if err := request.MakeResponse(); err != nil {
		t.Fatalf("IdP make response: %v", err)
	}
	form, err := request.PostBinding()
	if err != nil {
		t.Fatalf("IdP build post form: %v", err)
	}
	return form
}

func (attempt *samlFlowAttempt) post(t *testing.T, form saml.IdpAuthnRequestForm) *httptest.ResponseRecorder {
	t.Helper()
	body := url.Values{
		"SAMLResponse": {form.SAMLResponse},
		"RelayState":   {form.RelayState},
	}.Encode()
	request := httptest.NewRequest(http.MethodPost, samlACSPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: attempt.cookie.Name, Value: attempt.cookie.Value})
	recorder := httptest.NewRecorder()
	attempt.harness.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

type samlMutatingAssertionMaker struct {
	mutate func(*saml.Assertion)
}

func (maker samlMutatingAssertionMaker) MakeAssertion(request *saml.IdpAuthnRequest, session *saml.Session) error {
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(request, session); err != nil {
		return err
	}
	maker.mutate(request.Assertion)
	return nil
}

func newTestSAMLTextLogger(w *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}

func findSAMLSessionCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func requireSAMLErrorRedirect(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse error redirect: %v", err)
	}
	if location.Path != "/" || location.RawQuery != "saml_error=1" {
		t.Fatalf("error redirect = %q, want /?saml_error=1", recitedLocation(location))
	}
	if session := findSAMLSessionCookie(recorder.Result().Cookies(), sessionCookie); session != nil {
		t.Fatalf("failure response set a session cookie: %#v", session)
	}
	if csrf := findSAMLSessionCookie(recorder.Result().Cookies(), csrfCookie); csrf != nil {
		t.Fatalf("failure response set a CSRF cookie: %#v", csrf)
	}
}

func recitedLocation(location *url.URL) string {
	if location == nil {
		return ""
	}
	return location.String()
}

func requireSAMLFailureAudit(t *testing.T, backend *samlTestBackend, wantUserID, wantTarget, wantDetail string) {
	t.Helper()
	if len(backend.audits) != 1 {
		t.Fatalf("audits = %#v, want exactly one", backend.audits)
	}
	audit := backend.audits[0]
	if audit.action != "auth.sso_failed" || audit.userID != wantUserID || audit.target != wantTarget || audit.detail != wantDetail {
		t.Fatalf("audit = %#v, want auth.sso_failed user=%q target=%q detail=%q", audit, wantUserID, wantTarget, wantDetail)
	}
}

func requireSAMLSuccess(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusFound {
		t.Fatalf("ACS status = %d, want 302; body: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "/" {
		t.Fatalf("ACS redirect = %q, want /", got)
	}
	session := findSAMLSessionCookie(recorder.Result().Cookies(), sessionCookie)
	if session == nil || session.Value != "raw-session-token" {
		t.Fatalf("session cookie = %#v, want raw-session-token", session)
	}
	csrf := findSAMLSessionCookie(recorder.Result().Cookies(), csrfCookie)
	if csrf == nil || csrf.Value != "csrf-token" {
		t.Fatalf("CSRF cookie = %#v, want csrf-token", csrf)
	}
}

func requireSAMLLoginAudit(t *testing.T, backend *samlTestBackend, wantTarget string) {
	t.Helper()
	if len(backend.audits) != 1 {
		t.Fatalf("audits = %#v, want exactly one login audit", backend.audits)
	}
	audit := backend.audits[0]
	if audit.action != "auth.login" || audit.userID != "user-1" || audit.target != wantTarget || audit.detail != "SAML SSO" {
		t.Fatalf("audit = %#v, want auth.login target=%q detail=SAML SSO", audit, wantTarget)
	}
}

func samlFlowSession() *saml.Session {
	return &saml.Session{
		NameID:       "Person@Example.Test",
		NameIDFormat: string(saml.EmailAddressNameIDFormat),
		UserEmail:    "person@example.test",
		CreateTime:   time.Now().Add(-time.Minute),
	}
}

func testSAMLSessionEmail(nameID string) *saml.Session {
	return &saml.Session{NameID: nameID, CreateTime: time.Now().Add(-time.Minute)}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestSAMLBuildSAMLSPConfiguration(t *testing.T) {
	env := newSAMLTestEnv(t)
	server := newSAMLTestServer(t, env.cfg)
	middleware, err := server.samlMiddleware(context.Background())
	if err != nil {
		t.Fatalf("samlMiddleware() error = %v", err)
	}
	if middleware.Session == nil {
		t.Fatal("middleware.Session is nil")
	}
	if _, ok := middleware.Session.(*samlSessionProvider); !ok {
		t.Fatalf("middleware.Session = %T, want *samlSessionProvider", middleware.Session)
	}
	if middleware.OnError == nil {
		t.Fatal("middleware.OnError is nil")
	}
	if middleware.ServiceProvider.ReplayCache == nil {
		t.Fatal("middleware ReplayCache is nil, want an in-process replay cache")
	}
	if middleware.ServiceProvider.DefaultRedirectURI != "/" {
		t.Fatalf("DefaultRedirectURI = %q, want /", middleware.ServiceProvider.DefaultRedirectURI)
	}
	if middleware.ServiceProvider.AllowIDPInitiated {
		t.Fatal("AllowIDPInitiated = true, want false")
	}
}

func TestSAMLStartRedirectAndTrackingCookie(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)

	if got := attempt.location.String(); !strings.HasPrefix(got, "https://idp.example.test/sso") {
		t.Fatalf("start redirect = %q, want the IdP SSO endpoint", got)
	}
	query := attempt.location.Query()
	if query.Get("RelayState") == "" {
		t.Fatal("start redirect has no RelayState")
	}
	if query.Get("SigAlg") == "" || query.Get("Signature") == "" {
		t.Fatal("start redirect is not signed")
	}
	if !strings.HasPrefix(attempt.cookie.Name, "saml_") {
		t.Fatalf("tracking cookie name = %q, want saml_ prefix", attempt.cookie.Name)
	}
	if !attempt.cookie.HttpOnly {
		t.Error("tracking cookie is not HttpOnly")
	}
	if attempt.cookie.Path != samlACSPath {
		t.Errorf("tracking cookie path = %q, want %q", attempt.cookie.Path, samlACSPath)
	}
	if attempt.cookie.MaxAge != int(samlTrackedRequestMaxAge.Seconds()) {
		t.Errorf("tracking cookie MaxAge = %d, want %d", attempt.cookie.MaxAge, int(samlTrackedRequestMaxAge.Seconds()))
	}
	if attempt.request.Request.ID == "" {
		t.Fatal("AuthnRequest has no ID")
	}
}

func TestSAMLStartUnavailableIsSanitized(t *testing.T) {
	env := newSAMLTestEnv(t)
	const marker = "SAML_METADATA_SECRET_MARKER"
	if err := os.WriteFile(env.metadataFile, []byte("<not-metadata>"+marker), 0o600); err != nil {
		t.Fatalf("write broken metadata: %v", err)
	}
	var logs strings.Builder
	server := New(Options{Config: env.cfg, Logger: newTestSAMLTextLogger(&logs)})
	installSAMLLicense(t, server)
	audits := 0
	server.samlFailureAudit = func(*http.Request, userRecord, string, string, string) { audits++ }

	request := httptest.NewRequest(http.MethodGet, "/api/auth/saml/start", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	requireSAMLErrorRedirect(t, recorder)
	if audits != 0 {
		t.Fatalf("unavailable start wrote %d audits, want none before the limiter admits", audits)
	}
	if strings.Contains(recorder.Body.String(), marker) || strings.Contains(logs.String(), marker) {
		t.Fatalf("metadata contents leaked: body=%q log=%q", recorder.Body.String(), logs.String())
	}
	if !strings.Contains(logs.String(), "SAML start unavailable") {
		t.Fatalf("start did not log the fixed warning: %q", logs.String())
	}
}

func TestSAMLACSDisabledAndNonPost(t *testing.T) {
	env := newSAMLTestEnv(t)
	server := New(Options{Config: env.cfg, Logger: newTestSAMLTextLogger(&strings.Builder{})})
	audits := 0
	server.samlFailureAudit = func(*http.Request, userRecord, string, string, string) { audits++ }

	request := httptest.NewRequest(http.MethodGet, "/api/auth/saml/start", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	requireSAMLErrorRedirect(t, recorder)

	request = httptest.NewRequest(http.MethodPost, samlACSPath, strings.NewReader("SAMLResponse=x"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	requireSAMLErrorRedirect(t, recorder)

	request = httptest.NewRequest(http.MethodGet, samlACSPath, nil)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET ACS status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if audits != 0 {
		t.Fatalf("disabled SAML wrote %d audits, want none", audits)
	}
	server.samlLimit.mu.Lock()
	entries := len(server.samlLimit.attempts)
	server.samlLimit.mu.Unlock()
	if entries != 0 {
		t.Fatalf("disabled SAML touched the limiter: %d entries", entries)
	}
}

// ---------------------------------------------------------------------------
// Session bridge
// ---------------------------------------------------------------------------

func TestSAMLACSSuccess(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)

	// A previous failed attempt must not penalize a successful sign-in.
	harness.server.samlLimit.Fail("192.0.2.1")

	form := attempt.response(t, samlFlowSession(), nil)
	recorder := attempt.post(t, form)
	requireSAMLSuccess(t, recorder)

	if got := harness.backend.lookups; len(got) != 1 || got[0] != "person@example.test" {
		t.Fatalf("user lookup = %v, want normalized email", got)
	}
	if got := harness.backend.sessionTTLs; len(got) != 1 || got[0] != 12*time.Hour {
		t.Fatalf("session TTL = %v, want the configured 12h session TTL", got)
	}
	if got := harness.backend.lastLogins; len(got) != 1 || got[0] != "user-1" {
		t.Fatalf("last login updates = %v, want user-1", got)
	}
	requireSAMLLoginAudit(t, harness.backend, "person@example.test")
	harness.server.samlLimit.mu.Lock()
	entries := len(harness.server.samlLimit.attempts)
	harness.server.samlLimit.mu.Unlock()
	if entries != 0 {
		t.Fatalf("successful ACS left %d limiter entries, want a reset", entries)
	}
}

func TestSAMLACSRejectsInvalidAssertions(t *testing.T) {
	past := time.Now().Add(-10 * time.Minute)
	cases := []struct {
		name   string
		maker  saml.AssertionMaker
		tamper func(*testing.T, *saml.IdpAuthnRequestForm)
	}{
		{
			name: "invalid signature",
			tamper: func(t *testing.T, form *saml.IdpAuthnRequestForm) {
				t.Helper()
				tamperSAMLResponseInResponseTo(t, form, "different-request-id")
			},
		},
		{
			name: "wrong audience",
			maker: samlMutatingAssertionMaker{mutate: func(assertion *saml.Assertion) {
				assertion.Conditions.AudienceRestrictions[0].Audience.Value = "https://wrong.example.test"
			}},
		},
		{
			name: "wrong recipient",
			maker: samlMutatingAssertionMaker{mutate: func(assertion *saml.Assertion) {
				assertion.Subject.SubjectConfirmations[0].SubjectConfirmationData.Recipient = "https://wrong.example.test/api/auth/saml/acs"
			}},
		},
		{
			name: "expired assertion",
			maker: samlMutatingAssertionMaker{mutate: func(assertion *saml.Assertion) {
				assertion.Conditions.NotBefore = past.Add(-time.Minute)
				assertion.Conditions.NotOnOrAfter = past
				assertion.Subject.SubjectConfirmations[0].SubjectConfirmationData.NotOnOrAfter = past
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newSAMLFlowHarness(t, nil)
			attempt := harness.start(t)
			form := attempt.response(t, samlFlowSession(), tc.maker)
			if tc.tamper != nil {
				tc.tamper(t, &form)
			}
			recorder := attempt.post(t, form)
			requireSAMLErrorRedirect(t, recorder)
			if len(harness.backend.sessionTTLs) != 0 || len(harness.backend.lastLogins) != 0 {
				t.Fatal("invalid assertion reached session creation")
			}
			requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorAssertion)
		})
	}
}

// TestSAMLACSRejectsRogueSigningKey keeps the response's InResponseTo,
// audience, recipient, conditions and timing valid and only changes the
// signing key. It fails if signature verification against the trusted IdP
// metadata certificate is ever weakened.
func TestSAMLACSRejectsRogueSigningKey(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)

	rogueKey, rogueCertificate := newSAMLIDPTestSigner(t)
	rogueIDP := *harness.idp
	rogueIDP.Key = rogueKey
	rogueIDP.Certificate = rogueCertificate

	// Reuse the AuthnRequest the real IdP already validated so the response
	// still answers the correct request ID for this SP and ACS.
	rogueRequest := *attempt.request
	rogueRequest.IDP = &rogueIDP
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(&rogueRequest, samlFlowSession()); err != nil {
		t.Fatalf("rogue IdP make assertion: %v", err)
	}
	if err := rogueRequest.MakeResponse(); err != nil {
		t.Fatalf("rogue IdP make response: %v", err)
	}
	form, err := rogueRequest.PostBinding()
	if err != nil {
		t.Fatalf("rogue IdP build post form: %v", err)
	}

	recorder := attempt.post(t, form)
	requireSAMLErrorRedirect(t, recorder)
	if len(harness.backend.lookups) != 0 || len(harness.backend.sessionTTLs) != 0 || len(harness.backend.lastLogins) != 0 {
		t.Fatal("rogue-signed assertion reached account lookup or session creation")
	}
	requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorAssertion)
}

// TestSAMLACSInvalidSignaturePenalty pins the OnError limiter penalty for
// pre-CreateSession validation failures: exactly one failure is recorded, and
// the already-sanitized OnError path does not penalize the client twice.
func TestSAMLACSInvalidSignaturePenalty(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)
	form := attempt.response(t, samlFlowSession(), nil)
	tamperSAMLResponseInResponseTo(t, &form, "different-request-id")

	requireSAMLErrorRedirect(t, attempt.post(t, form))

	harness.server.samlLimit.mu.Lock()
	count := harness.server.samlLimit.attempts["192.0.2.1"].Count
	harness.server.samlLimit.mu.Unlock()
	if count != 1 {
		t.Fatalf("limiter count = %d, want exactly 1 (no double penalty)", count)
	}
	requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorAssertion)
}

func TestSAMLACSReplayRejected(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)
	form := attempt.response(t, samlFlowSession(), nil)

	requireSAMLSuccess(t, attempt.post(t, form))
	harness.backend.reset()
	requireSAMLErrorRedirect(t, attempt.post(t, form))

	if len(harness.backend.sessionTTLs) != 0 {
		t.Fatal("replayed assertion created a second session")
	}
	requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorAssertion)
}

func TestSAMLACSReplayWithoutTrackingCookie(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)
	form := attempt.response(t, samlFlowSession(), nil)
	requireSAMLSuccess(t, attempt.post(t, form))

	harness.backend.reset()
	body := url.Values{"SAMLResponse": {form.SAMLResponse}, "RelayState": {form.RelayState}}.Encode()
	request := httptest.NewRequest(http.MethodPost, samlACSPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	requireSAMLErrorRedirect(t, recorder)
	requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorAssertion)
}

func TestSAMLACSEmailExtraction(t *testing.T) {
	t.Run("NameID must be an email", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, nil)
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, testSAMLSessionEmail("opaque-subject"), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})
	t.Run("NameID with two at signs", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, nil)
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, testSAMLSessionEmail("person@example.test@evil.test"), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})
	t.Run("NameID with whitespace", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, nil)
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, testSAMLSessionEmail("person @example.test"), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})

	attributeSession := func(attributes ...saml.Attribute) *saml.Session {
		return &saml.Session{
			NameID:           "opaque-subject",
			CustomAttributes: attributes,
			CreateTime:       time.Now().Add(-time.Minute),
		}
	}

	t.Run("configured attribute absent", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLEmailAttribute = "mail" })
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, attributeSession(), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})
	t.Run("exact attribute Name, not FriendlyName", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLEmailAttribute = "mail" })
		attempt := harness.start(t)
		session := attributeSession(saml.Attribute{
			FriendlyName: "mail",
			Name:         "urn:oid:0.9.2342.19200300.100.1.3",
			Values:       []saml.AttributeValue{{Value: "person@example.test"}},
		})
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, session, nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})
	t.Run("conflicting attribute values", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLEmailAttribute = "mail" })
		attempt := harness.start(t)
		session := attributeSession(saml.Attribute{
			Name: "mail",
			Values: []saml.AttributeValue{
				{Value: "person@example.test"},
				{Value: "other@example.test"},
			},
		})
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, session, nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})
	t.Run("attribute value is not an email", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLEmailAttribute = "mail" })
		attempt := harness.start(t)
		session := attributeSession(saml.Attribute{
			Name:   "mail",
			Values: []saml.AttributeValue{{Value: "not-an-email"}},
		})
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, session, nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorEmail)
	})
	t.Run("exactly one non-empty attribute value", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLEmailAttribute = "mail" })
		attempt := harness.start(t)
		session := attributeSession(saml.Attribute{
			Name: "mail",
			Values: []saml.AttributeValue{
				{Value: "  "},
				{Value: " Person@Example.Test "},
			},
		})
		requireSAMLSuccess(t, attempt.post(t, attempt.response(t, session, nil)))
		if got := harness.backend.lookups; len(got) != 1 || got[0] != "person@example.test" {
			t.Fatalf("user lookup = %v, want the normalized attribute email", got)
		}
		requireSAMLLoginAudit(t, harness.backend, "person@example.test")
	})
}

func TestSAMLACSDomainAllowlist(t *testing.T) {
	t.Run("wrong domain", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) {
			cfg.SAMLEmailDomains = []string{"example.test"}
		})
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, testSAMLSessionEmail("person@other.test"), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "person@other.test", samlErrorDomain)
		if len(harness.backend.lookups) != 0 {
			t.Fatalf("domain mismatch reached account lookup: %v", harness.backend.lookups)
		}
	})
	t.Run("subdomain does not match", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) {
			cfg.SAMLEmailDomains = []string{"example.test"}
		})
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, testSAMLSessionEmail("person@sub.example.test"), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "person@sub.example.test", samlErrorDomain)
	})
	t.Run("empty allowlist is unrestricted", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLEmailDomains = nil })
		attempt := harness.start(t)
		requireSAMLSuccess(t, attempt.post(t, attempt.response(t, testSAMLSessionEmail("person@other.test"), nil)))
	})
}

func TestSAMLACSUnknownUserIsNotCreated(t *testing.T) {
	t.Run("lookup failure", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, nil)
		harness.backend.userErr = errors.New("no rows")
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, samlFlowSession(), nil)))
		if len(harness.backend.lookups) != 1 {
			t.Fatalf("user lookups = %v, want exactly one", harness.backend.lookups)
		}
		if len(harness.backend.sessionTTLs) != 0 || len(harness.backend.lastLogins) != 0 {
			t.Fatal("unknown user reached session creation or last-login update")
		}
		requireSAMLFailureAudit(t, harness.backend, "", "person@example.test", samlErrorUnknownUser)
	})
	t.Run("inactive user", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, nil)
		harness.backend.user = userRecord{}
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, samlFlowSession(), nil)))
		requireSAMLFailureAudit(t, harness.backend, "", "person@example.test", samlErrorUnknownUser)
	})
}

func TestSAMLACSTenantPolicies(t *testing.T) {
	t.Run("IP policy blocks sign-in", func(t *testing.T) {
		harness := newSAMLFlowHarness(t, nil)
		harness.backend.policy.Security.AllowedIPRanges = []string{"10.0.0.0/8"}
		attempt := harness.start(t)
		requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, samlFlowSession(), nil)))
		requireSAMLFailureAudit(t, harness.backend, "user-1", "person@example.test", samlErrorIPBlocked)
		if len(harness.backend.sessionTTLs) != 0 {
			t.Fatal("IP-blocked user reached session creation")
		}
	})

	mfaCases := []struct {
		name   string
		acr    string
		extra  []string
		wantOK bool
	}{
		{name: "password transport rejected", acr: samlACRPasswordProtectedTransport},
		{name: "built-in mobile two-factor accepted", acr: "urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorContract", wantOK: true},
		{name: "built-in smartcard accepted", acr: "urn:oasis:names:tc:SAML:2.0:ac:classes:Smartcard", wantOK: true},
		{name: "built-in Microsoft MFA accepted", acr: "http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa", wantOK: true},
		{name: "substring lookalike rejected", acr: "urn:example:acme:MobileTwoFactorContract"},
		{name: "suffix lookalike rejected", acr: "urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorContract:extra"},
		{name: "configured class accepted", acr: "urn:example:acme:mfa", extra: []string{"urn:example:acme:mfa"}, wantOK: true},
		{name: "configured class is exact", acr: "urn:example:acme:mfa-extra", extra: []string{"urn:example:acme:mfa"}},
	}
	for _, tc := range mfaCases {
		t.Run("MFA "+tc.name, func(t *testing.T) {
			harness := newSAMLFlowHarness(t, func(cfg *config.Config) { cfg.SAMLMFAContexts = tc.extra })
			harness.backend.policy.Security.MFAEnforcement = "all"
			attempt := harness.start(t)
			session := samlFlowSession()
			session.AuthnContextClassRef = tc.acr
			recorder := attempt.post(t, attempt.response(t, session, nil))
			if tc.wantOK {
				requireSAMLSuccess(t, recorder)
				return
			}
			requireSAMLErrorRedirect(t, recorder)
			requireSAMLFailureAudit(t, harness.backend, "user-1", "person@example.test", samlErrorMFARequired)
			if len(harness.backend.sessionTTLs) != 0 {
				t.Fatal("MFA-policy rejection created a session")
			}
		})
	}

	t.Run("admins MFA policy only affects admin roles", func(t *testing.T) {
		for _, tc := range []struct {
			role   string
			wantOK bool
		}{
			{role: "member", wantOK: true},
			{role: "admin", wantOK: false},
			{role: "owner", wantOK: false},
		} {
			t.Run(tc.role, func(t *testing.T) {
				harness := newSAMLFlowHarness(t, nil)
				harness.backend.user.Role = tc.role
				harness.backend.policy.Security.MFAEnforcement = "admins"
				attempt := harness.start(t)
				session := samlFlowSession()
				session.AuthnContextClassRef = samlACRPasswordProtectedTransport
				recorder := attempt.post(t, attempt.response(t, session, nil))
				if tc.wantOK {
					requireSAMLSuccess(t, recorder)
					return
				}
				requireSAMLErrorRedirect(t, recorder)
				requireSAMLFailureAudit(t, harness.backend, "user-1", "person@example.test", samlErrorMFARequired)
			})
		}
	})
}

func TestSAMLACSSessionCreationFailure(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	harness.backend.sessionErr = errors.New("insert failed")
	attempt := harness.start(t)
	requireSAMLErrorRedirect(t, attempt.post(t, attempt.response(t, samlFlowSession(), nil)))

	if len(harness.backend.sessionTTLs) != 1 {
		t.Fatalf("session attempts = %d, want 1", len(harness.backend.sessionTTLs))
	}
	if len(harness.backend.lastLogins) != 0 {
		t.Fatal("last_login_at was updated despite a session failure")
	}
	requireSAMLFailureAudit(t, harness.backend, "user-1", "person@example.test", samlErrorSession)
	harness.server.samlLimit.mu.Lock()
	count := harness.server.samlLimit.attempts["192.0.2.1"].Count
	harness.server.samlLimit.mu.Unlock()
	if count != 1 {
		t.Fatalf("limiter count = %d, want exactly one failure (no double count)", count)
	}
}

func TestSAMLACSSanitizedErrors(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	attempt := harness.start(t)
	const marker = "SAML_SECRET_MARKER"
	form := attempt.response(t, samlFlowSession(), nil)
	// Break the signed Response while planting the marker in its visible XML.
	tamperSAMLResponseInResponseTo(t, &form, marker)

	recorder := attempt.post(t, form)
	requireSAMLErrorRedirect(t, recorder)
	if strings.Contains(recorder.Body.String(), marker) || strings.Contains(recorder.Header().Get("Location"), marker) {
		t.Fatalf("assertion contents leaked into the response: body=%q location=%q", recorder.Body.String(), recorder.Header().Get("Location"))
	}
	if strings.Contains(harness.logs.String(), marker) {
		t.Fatalf("assertion contents leaked into the log: %q", harness.logs.String())
	}
	if !strings.Contains(harness.logs.String(), "SAML request failed") {
		t.Fatalf("fixed failure log line missing: %q", harness.logs.String())
	}
	requireSAMLFailureAudit(t, harness.backend, "", "", samlErrorAssertion)
	if strings.Contains(harness.backend.audits[0].detail, marker) {
		t.Fatalf("audit detail leaked assertion contents: %q", harness.backend.audits[0].detail)
	}
}

func TestSAMLACSRateLimit(t *testing.T) {
	harness := newSAMLFlowHarness(t, nil)
	harness.server.samlLimit = newLoginLimiter(2, time.Minute)

	fail := func() *httptest.ResponseRecorder {
		attempt := harness.start(t)
		return attempt.post(t, attempt.response(t, testSAMLSessionEmail("opaque-subject"), nil))
	}
	requireSAMLErrorRedirect(t, fail())
	requireSAMLErrorRedirect(t, fail())
	if len(harness.backend.audits) != 2 {
		t.Fatalf("in-limit audits = %#v, want two", harness.backend.audits)
	}
	// Over-limit requests are rejected by the limiter itself: no audit, no
	// rate-limit extension, and the fixed redirect.
	requireSAMLErrorRedirect(t, fail())
	requireSAMLErrorRedirect(t, fail())
	if len(harness.backend.audits) != 2 {
		t.Fatalf("over-limit requests produced audits: %#v", harness.backend.audits)
	}
	harness.server.samlLimit.mu.Lock()
	count := harness.server.samlLimit.attempts["192.0.2.1"].Count
	harness.server.samlLimit.mu.Unlock()
	if count != 2 {
		t.Fatalf("limiter count = %d, want 2 (over-limit requests must not extend it)", count)
	}
}

// ---------------------------------------------------------------------------
// Unit coverage for the email and MFA policy helpers
// ---------------------------------------------------------------------------

func TestSAMLAssertionEmail(t *testing.T) {
	attributeAssertion := func(attributes ...saml.Attribute) *saml.Assertion {
		return &saml.Assertion{
			Subject:             &saml.Subject{NameID: &saml.NameID{Value: "opaque-subject"}},
			AttributeStatements: []saml.AttributeStatement{{Attributes: attributes}},
		}
	}
	cases := []struct {
		name      string
		assertion *saml.Assertion
		attribute string
		want      string
		wantError bool
	}{
		{
			name:      "NameID email normalized",
			assertion: &saml.Assertion{Subject: &saml.Subject{NameID: &saml.NameID{Value: " Person@Example.Test "}}},
			want:      "person@example.test",
		},
		{
			name:      "NameID with two at signs rejected",
			assertion: &saml.Assertion{Subject: &saml.Subject{NameID: &saml.NameID{Value: "a@b@c"}}},
			wantError: true,
		},
		{
			name:      "NameID whitespace rejected",
			assertion: &saml.Assertion{Subject: &saml.Subject{NameID: &saml.NameID{Value: "a b@c.test"}}},
			wantError: true,
		},
		{
			name:      "missing Subject rejected",
			assertion: &saml.Assertion{},
			wantError: true,
		},
		{
			name:      "missing NameID rejected",
			assertion: &saml.Assertion{Subject: &saml.Subject{}},
			wantError: true,
		},
		{
			name: "exact attribute Name",
			assertion: attributeAssertion(saml.Attribute{
				FriendlyName: "ignored",
				Name:         "mail",
				Values:       []saml.AttributeValue{{Value: "person@example.test"}},
			}),
			attribute: "mail",
			want:      "person@example.test",
		},
		{
			name: "FriendlyName only is rejected",
			assertion: attributeAssertion(saml.Attribute{
				FriendlyName: "mail",
				Name:         "urn:oid:0.9.2342.19200300.100.1.3",
				Values:       []saml.AttributeValue{{Value: "person@example.test"}},
			}),
			attribute: "mail",
			wantError: true,
		},
		{
			name: "multiple values rejected",
			assertion: attributeAssertion(saml.Attribute{
				Name:   "mail",
				Values: []saml.AttributeValue{{Value: "a@example.test"}, {Value: "b@example.test"}},
			}),
			attribute: "mail",
			wantError: true,
		},
		{
			name: "one non-empty value accepted",
			assertion: attributeAssertion(saml.Attribute{
				Name:   "mail",
				Values: []saml.AttributeValue{{Value: " "}, {Value: " Person@Example.Test "}},
			}),
			attribute: "mail",
			want:      "person@example.test",
		},
		{
			name: "attribute value must be an email",
			assertion: attributeAssertion(saml.Attribute{
				Name:   "mail",
				Values: []saml.AttributeValue{{Value: "not-an-email"}},
			}),
			attribute: "mail",
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := samlAssertionEmail(tc.assertion, tc.attribute)
			if tc.wantError {
				if err == nil {
					t.Fatalf("samlAssertionEmail() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("samlAssertionEmail() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("samlAssertionEmail() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSAMLMFAClassRefAllowlist(t *testing.T) {
	builtin := []string{
		"urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorContract",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorUnregistered",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:Smartcard",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:SmartcardPKI",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:SoftwarePKI",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:TimeSyncToken",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:Token",
		"http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa",
		"http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/multipleauthn",
		"http://schemas.microsoft.com/claims/multipleauthn",
		"https://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa",
		"https://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/multipleauthn",
	}
	for _, classRef := range builtin {
		if !samlMFAStrength(samlAssertionWithACR(classRef), nil) {
			t.Fatalf("built-in class ref %q was not accepted", classRef)
		}
	}
	rejected := []string{
		"",
		samlACRPasswordProtectedTransport,
		"urn:oasis:names:tc:SAML:2.0:ac:classes:Password",
		"urn:example:acme:MobileTwoFactorContract",
		"urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorContract:extra",
		"http://schemas.microsoft.com/claims/multipleauthn-extra",
		"URN:OASIS:NAMES:TC:SAML:2.0:AC:CLASSES:SMARTCARD",
	}
	for _, classRef := range rejected {
		if samlMFAStrength(samlAssertionWithACR(classRef), nil) {
			t.Fatalf("class ref %q was accepted but must not count as MFA", classRef)
		}
	}
	if !samlMFAStrength(samlAssertionWithACR("urn:example:acme:mfa"), []string{"urn:example:acme:mfa"}) {
		t.Fatal("configured MFA class ref was not accepted")
	}
	if samlMFAStrength(samlAssertionWithACR("urn:example:acme:mfa-extra"), []string{"urn:example:acme:mfa"}) {
		t.Fatal("configured MFA class ref matched as a substring")
	}
	if samlMFAStrength(nil, nil) {
		t.Fatal("nil assertion counted as MFA")
	}
	if samlMFAStrength(&saml.Assertion{AuthnStatements: []saml.AuthnStatement{{}}}, nil) {
		t.Fatal("AuthnStatement without a class ref counted as MFA")
	}
}

func samlAssertionWithACR(classRef string) *saml.Assertion {
	return &saml.Assertion{
		AuthnStatements: []saml.AuthnStatement{{
			AuthnContext: saml.AuthnContext{
				AuthnContextClassRef: &saml.AuthnContextClassRef{Value: classRef},
			},
		}},
	}
}

// tamperSAMLResponseInResponseTo rewrites the signed Response's InResponseTo
// attribute. The XML stays well-formed but the enveloped signature no longer
// verifies. The assertion itself is encrypted to the SP, so only visible
// Response attributes can be used to plant a marker.
func tamperSAMLResponseInResponseTo(t *testing.T, form *saml.IdpAuthnRequestForm, value string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(form.SAMLResponse)
	if err != nil {
		t.Fatalf("decode SAMLResponse: %v", err)
	}
	tampered := samlInResponseToPattern.ReplaceAll(raw, []byte(`InResponseTo="`+value+`"`))
	if bytes.Equal(tampered, raw) {
		t.Fatalf("SAMLResponse has no InResponseTo attribute to tamper with: %s", raw)
	}
	form.SAMLResponse = base64.StdEncoding.EncodeToString(tampered)
}

var samlInResponseToPattern = regexp.MustCompile(`InResponseTo="[^"]*"`)
