package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newSCIMTestServer builds a server through the production constructor with a
// verified SCIM license, but no database. Tests that reach the token lookup
// would panic, which proves the tested paths never query PostgreSQL.
func newSCIMTestServer(t *testing.T, publicURL string, features ...string) (*Server, *mutableClock) {
	t.Helper()
	server := New(Options{
		Config: config.Config{PublicURL: publicURL},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	clock := newMutableClock(time.Now())
	server.license = &licensing.License{
		Version: licensing.Version, ID: "lic_scim_test", Edition: "enterprise", Seats: 10,
		Features: features, IssuedAt: clock.Now(), ExpiresAt: clock.Now().Add(24 * time.Hour),
		GraceDays: 7,
	}
	server.licenseNow = clock.Now
	return server, clock
}

func TestSCIMBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		public  string
		want    string
		wantErr bool
	}{
		{name: "https origin", public: "https://mail.example.test", want: "https://mail.example.test/scim/v2"},
		{name: "https trailing slash", public: "https://mail.example.test/", want: "https://mail.example.test/scim/v2"},
		{name: "https base path", public: "https://mail.example.test/mail/", want: "https://mail.example.test/mail/scim/v2"},
		{name: "https port", public: "https://mail.example.test:8443", want: "https://mail.example.test:8443/scim/v2"},
		{name: "loopback hostname", public: "http://localhost:8001", want: "http://localhost:8001/scim/v2"},
		{name: "loopback ipv4", public: "http://127.0.0.1:8001", want: "http://127.0.0.1:8001/scim/v2"},
		{name: "loopback ipv6", public: "http://[::1]:8001", want: "http://[::1]:8001/scim/v2"},
		{name: "empty", public: "", wantErr: true},
		{name: "whitespace", public: "   ", wantErr: true},
		{name: "relative", public: "/mail", wantErr: true},
		{name: "cleartext non-loopback", public: "http://mail.example.test", wantErr: true},
		{name: "unsupported scheme", public: "ftp://mail.example.test", wantErr: true},
		{name: "userinfo", public: "https://user:secret@mail.example.test", wantErr: true},
		{name: "query", public: "https://mail.example.test/?tenant=1", wantErr: true},
		{name: "empty query", public: "https://mail.example.test/?", wantErr: true},
		{name: "fragment", public: "https://mail.example.test/#fragment", wantErr: true},
		{name: "empty fragment", public: "https://mail.example.test/#", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{cfg: config.Config{PublicURL: tc.public}}
			got, err := server.scimBaseURL()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("scimBaseURL(%q) = %q, want error", tc.public, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("scimBaseURL(%q): %v", tc.public, err)
			}
			if got != tc.want {
				t.Fatalf("scimBaseURL(%q) = %q, want %q", tc.public, got, tc.want)
			}
		})
	}
}

func TestAdminBootstrapPayloadIncludesSCIMURL(t *testing.T) {
	cases := []struct {
		name     string
		public   string
		want     string
		wantNone bool
	}{
		{name: "https origin", public: "https://mail.example.test", want: "https://mail.example.test/scim/v2"},
		{name: "trailing slash", public: "https://mail.example.test/", want: "https://mail.example.test/scim/v2"},
		{name: "base path", public: "https://mail.example.test/mail/", want: "https://mail.example.test/mail/scim/v2"},
		{name: "loopback", public: "http://127.0.0.1:8001", want: "http://127.0.0.1:8001/scim/v2"},
		{name: "empty", public: "", wantNone: true},
		{name: "cleartext non-loopback", public: "http://mail.example.test", wantNone: true},
		{name: "query", public: "https://mail.example.test/?tenant=1", wantNone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{cfg: config.Config{PublicURL: tc.public}, version: "test"}
			payload := server.adminServerPayload()
			got, exists := payload["scimURL"]
			if tc.wantNone {
				if exists {
					t.Fatalf("adminServerPayload().scimURL = %#v, want omitted", got)
				}
				return
			}
			if !exists || got != tc.want {
				t.Fatalf("adminServerPayload().scimURL = %#v (present %v), want %q", got, exists, tc.want)
			}
		})
	}
}

func TestSCIMLicenseGateReturns404BeforeAuth(t *testing.T) {
	cases := []struct {
		name       string
		publicURL  string
		features   []string
		unlicensed bool
		expired    bool
	}{
		{name: "no license", publicURL: "https://mail.example.test", unlicensed: true},
		{name: "license without scim feature", publicURL: "https://mail.example.test", features: []string{"sso"}},
		{name: "expired license", publicURL: "https://mail.example.test", features: []string{"scim"}, expired: true},
		{name: "cleartext non-loopback", publicURL: "http://mail.example.test", features: []string{"scim"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var server *Server
			if tc.unlicensed {
				server = New(Options{
					Config: config.Config{PublicURL: tc.publicURL},
					Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				})
			} else {
				var clock *mutableClock
				server, clock = newSCIMTestServer(t, tc.publicURL, tc.features...)
				if tc.expired {
					clock.Set(clock.Now().Add(24*time.Hour + 8*24*time.Hour))
				}
			}
			for _, authorization := range []string{"", "Bearer a8m_scim_" + strings.Repeat("a", 43)} {
				request := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
				if authorization != "" {
					request.Header.Set("Authorization", authorization)
				}
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404 (auth %q): %s", recorder.Code, authorization, recorder.Body.String())
				}
				if got := recorder.Header().Get("WWW-Authenticate"); got != "" {
					t.Fatalf("unlicensed SCIM prompted for authentication: %q", got)
				}
				if strings.Contains(strings.ToLower(recorder.Body.String()), "scim") {
					t.Fatalf("unlicensed SCIM response leaks the feature: %s", recorder.Body.String())
				}
			}
		})
	}
}

func TestSCIMAuthRejectsMissingAndMalformedBearerWithoutDatabase(t *testing.T) {
	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	cases := []struct {
		name   string
		header string
	}{
		{name: "missing", header: ""},
		{name: "scheme only", header: "Bearer"},
		{name: "empty token", header: "Bearer "},
		{name: "wrong scheme", header: "Basic dXNlcjpwYXNz"},
		{name: "wrong prefix", header: "Bearer not-a-scim-token"},
		{name: "no scheme", header: "a8m_scim_" + strings.Repeat("a", 43)},
		{name: "extra fields", header: "Bearer a8m_scim_abc a8m_scim_def"},
		{name: "oversized", header: "Bearer a8m_scim_" + strings.Repeat("a", scimTokenMaxLength)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			assertSCIMError(t, recorder, http.StatusUnauthorized, "")
		})
	}
}

func TestSCIMAuthRateLimitsPerIP(t *testing.T) {
	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	server.scimLimit = newLoginLimiter(2, time.Minute)
	for attempt := 1; attempt <= 3; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
		request.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if attempt < 3 {
			assertSCIMError(t, recorder, http.StatusUnauthorized, "")
			continue
		}
		assertSCIMError(t, recorder, http.StatusTooManyRequests, "tooMany")
	}

	// A different client IP still gets a normal authentication failure.
	request := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	request.RemoteAddr = "198.51.100.9:1234"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	assertSCIMError(t, recorder, http.StatusUnauthorized, "")
}

func TestSCIMAuthAllowsLoopbackCleartext(t *testing.T) {
	server, _ := newSCIMTestServer(t, "http://localhost:8001", "scim")
	request := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	// The request is no longer hidden behind a 404; it reaches bearer auth.
	assertSCIMError(t, recorder, http.StatusUnauthorized, "")
}

func TestParseSCIMBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{header: "Bearer a8m_scim_abc", want: "a8m_scim_abc", ok: true},
		{header: "bearer a8m_scim_abc", want: "a8m_scim_abc", ok: true},
		{header: "  Bearer   a8m_scim_abc  ", want: "a8m_scim_abc", ok: true},
		{header: "", ok: false},
		{header: "Bearer", ok: false},
		{header: "Bearer ", ok: false},
		{header: "Bearer abc", ok: false},
		{header: "Bearer a8m_scim_a b", ok: false},
		{header: "Basic a8m_scim_abc", ok: false},
	}
	for _, tc := range cases {
		got, ok := parseSCIMBearer(tc.header)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseSCIMBearer(%q) = %q, %v; want %q, %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSCIMErrorAndJSONShapes(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSCIMError(recorder, http.StatusConflict, "uniqueness", "A user with this userName already exists")
	if got := recorder.Header().Get("Content-Type"); got != "application/scim+json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode SCIM error: %v", err)
	}
	schemas, _ := payload["schemas"].([]any)
	if len(schemas) != 1 || schemas[0] != scimErrorSchema {
		t.Fatalf("schemas = %#v", payload["schemas"])
	}
	if payload["status"] != "409" || payload["scimType"] != "uniqueness" {
		t.Fatalf("unexpected error fields: %#v", payload)
	}
	if payload["detail"] != "A user with this userName already exists" {
		t.Fatalf("detail = %#v", payload["detail"])
	}

	recorder = httptest.NewRecorder()
	writeSCIMError(recorder, http.StatusUnauthorized, "", "A valid SCIM bearer token is required")
	payload = map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode SCIM error: %v", err)
	}
	if _, exists := payload["scimType"]; exists {
		t.Fatalf("empty scimType should be omitted: %#v", payload)
	}

	recorder = httptest.NewRecorder()
	writeSCIMJSON(recorder, http.StatusOK, map[string]any{"ok": true})
	if got := recorder.Header().Get("Content-Type"); got != "application/scim+json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestSCIMTenantContextRoundTrip(t *testing.T) {
	if _, ok := scimTenantFromContext(context.Background()); ok {
		t.Fatal("empty context returned a SCIM tenant")
	}
	want := scimTenant{TokenID: "token-1", TenantID: "tenant-1", Name: "Entra ID"}
	ctx := context.WithValue(context.Background(), scimTenantContextKey{}, want)
	got, ok := scimTenantFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("scimTenantFromContext = %#v, %v; want %#v, true", got, ok, want)
	}
}

func TestSCIMTokenMaterial(t *testing.T) {
	raw, prefix, hash := newSCIMTokenMaterial()
	if !strings.HasPrefix(raw, scimTokenPrefix) {
		t.Fatalf("raw token %q lacks the SCIM prefix", raw)
	}
	if len(prefix) != scimTokenDisplayLength || !strings.HasPrefix(raw, prefix) {
		t.Fatalf("prefix %q does not match raw token %q", prefix, raw)
	}
	if hash != hashToken(raw) || hash == raw {
		t.Fatalf("hash %q is not the SHA-256 of the raw token", hash)
	}
	other, _, otherHash := newSCIMTokenMaterial()
	if other == raw || otherHash == hash {
		t.Fatal("two generated tokens collided")
	}
}

func TestSCIMBareSubtreeFailsClosed(t *testing.T) {
	unlicensed := New(Options{
		Config: config.Config{PublicURL: "https://mail.example.test"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, path := range []string{"/scim/v2", "/scim/v2/"} {
		recorder := httptest.NewRecorder()
		unlicensed.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unlicensed %s status = %d, want 404 (no redirect)", path, recorder.Code)
		}
	}

	licensed, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	for _, path := range []string{"/scim/v2", "/scim/v2/"} {
		recorder := httptest.NewRecorder()
		licensed.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		assertSCIMError(t, recorder, http.StatusUnauthorized, "")
	}
}

func TestSCIMDiscoveryMethodsAllowGet(t *testing.T) {
	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	handler := server.scimAllowMethod(http.MethodGet, server.scimServiceProviderConfig)

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodPost, "/scim/v2/ServiceProviderConfig", nil))
	assertSCIMError(t, recorder, http.StatusMethodNotAllowed, "")
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
}

func TestSCIMResourceTypeSubpaths(t *testing.T) {
	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	for id, endpoint := range map[string]string{"User": "/Users", "Group": "/Groups"} {
		recorder := httptest.NewRecorder()
		server.scimResourceType(id)(recorder, httptest.NewRequest(http.MethodGet, "/scim/v2/ResourceTypes/"+id, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", id, recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s resource type: %v", id, err)
		}
		if payload["id"] != id || payload["endpoint"] != endpoint {
			t.Fatalf("%s resource type = %#v", id, payload)
		}
		meta, _ := payload["meta"].(map[string]any)
		if meta["location"] != "https://mail.example.test/scim/v2/ResourceTypes/"+id {
			t.Fatalf("%s meta = %#v", id, meta)
		}
	}

	recorder := httptest.NewRecorder()
	server.scimResourceType("Device")(recorder, httptest.NewRequest(http.MethodGet, "/scim/v2/ResourceTypes/Device", nil))
	assertSCIMError(t, recorder, http.StatusNotFound, "")
}

// TestSCIMAdminTokenDatabaseErrorsReturn500 proves a backend failure is not
// misreported as a missing token. The pool is closed before the call, so every
// query fails with a driver error rather than pgx.ErrNoRows.
func TestSCIMAdminTokenDatabaseErrorsReturn500(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/a8m?sslmode=disable")
	if err != nil {
		t.Fatalf("create pgx pool: %v", err)
	}
	pool.Close()
	server := New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	server.store = &store.Store{DB: pool}
	sess := &adminSessionRecord{ActiveTenantID: "tenant-1", User: adminUser{ID: "user-1"}}
	cases := []struct {
		name   string
		method string
		call   http.HandlerFunc
	}{
		{name: "rotate", method: http.MethodPost, call: server.adminRotateSCIMToken},
		{name: "revoke", method: http.MethodDelete, call: server.adminRevokeSCIMToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/scim/tokens/00000000-0000-0000-0000-000000000000", nil)
			request.SetPathValue("id", "00000000-0000-0000-0000-000000000000")
			request = request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, sess))
			recorder := httptest.NewRecorder()
			tc.call(recorder, request)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if payload.Error.Code != "SCIM_TOKEN" || payload.Error.Message == "" {
				t.Fatalf("unexpected error payload: %#v", payload.Error)
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "pool") || strings.Contains(recorder.Body.String(), "127.0.0.1") {
				t.Fatalf("response leaked database internals: %s", recorder.Body.String())
			}
		})
	}
}

func TestSCIMTokenPayloadHasNoSecretMaterial(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	lastUsed := now.Add(-time.Minute)
	revoked := now.Add(-time.Second)
	payload := scimTokenPayload(scimTokenRecord{
		ID: "token-1", Name: "Entra ID", Prefix: "a8m_scim_abcdef",
		CreatedAt: now, LastUsedAt: &lastUsed, ExpiresAt: &expires, RevokedAt: &revoked,
	})
	for _, forbidden := range []string{"token", "tokenHash", "token_hash", "hash", "raw"} {
		if _, exists := payload[forbidden]; exists {
			t.Fatalf("token payload exposes %q: %#v", forbidden, payload)
		}
	}
	allowed := map[string]bool{
		"id": true, "name": true, "prefix": true, "createdAt": true,
		"lastUsedAt": true, "expiresAt": true, "revokedAt": true, "status": true,
	}
	for key := range payload {
		if !allowed[key] {
			t.Fatalf("unexpected token payload field %q", key)
		}
	}
	if payload["status"] != "revoked" {
		t.Fatalf("status = %#v", payload["status"])
	}
	payload = scimTokenPayload(scimTokenRecord{ID: "token-2", Name: "Okta", CreatedAt: now, ExpiresAt: &expires})
	if payload["status"] != "active" {
		t.Fatalf("status = %#v", payload["status"])
	}
	past := now.Add(-time.Hour)
	payload = scimTokenPayload(scimTokenRecord{ID: "token-3", Name: "Old", CreatedAt: now, ExpiresAt: &past})
	if payload["status"] != "expired" {
		t.Fatalf("status = %#v", payload["status"])
	}
}

func TestSCIMServiceProviderConfigAndResourceTypes(t *testing.T) {
	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")

	recorder := httptest.NewRecorder()
	server.scimServiceProviderConfig(recorder, httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ServiceProviderConfig status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var configPayload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &configPayload); err != nil {
		t.Fatalf("decode ServiceProviderConfig: %v", err)
	}
	schemas, _ := configPayload["schemas"].([]any)
	if len(schemas) != 1 || schemas[0] != scimServiceProviderConfigSchema {
		t.Fatalf("schemas = %#v", configPayload["schemas"])
	}
	if patch, _ := configPayload["patch"].(map[string]any); patch["supported"] != true {
		t.Fatalf("patch = %#v", configPayload["patch"])
	}
	if bulk, _ := configPayload["bulk"].(map[string]any); bulk["supported"] != false {
		t.Fatalf("bulk = %#v", configPayload["bulk"])
	}
	if filter, _ := configPayload["filter"].(map[string]any); filter["supported"] != true || filter["maxResults"] != float64(500) {
		t.Fatalf("filter = %#v", configPayload["filter"])
	}
	authSchemes, _ := configPayload["authenticationSchemes"].([]any)
	if len(authSchemes) != 1 {
		t.Fatalf("authenticationSchemes = %#v", configPayload["authenticationSchemes"])
	}
	scheme, _ := authSchemes[0].(map[string]any)
	if scheme["type"] != "oauthbearertoken" || scheme["primary"] != true {
		t.Fatalf("authentication scheme = %#v", scheme)
	}
	meta, _ := configPayload["meta"].(map[string]any)
	if meta["location"] != "https://mail.example.test/scim/v2/ServiceProviderConfig" {
		t.Fatalf("meta = %#v", meta)
	}

	recorder = httptest.NewRecorder()
	server.scimResourceTypes(recorder, httptest.NewRequest(http.MethodGet, "/scim/v2/ResourceTypes", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ResourceTypes status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var listPayload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode ResourceTypes: %v", err)
	}
	listSchemas, _ := listPayload["schemas"].([]any)
	if len(listSchemas) != 1 || listSchemas[0] != scimListResponseSchema {
		t.Fatalf("schemas = %#v", listPayload["schemas"])
	}
	if listPayload["totalResults"] != float64(2) || listPayload["itemsPerPage"] != float64(2) || listPayload["startIndex"] != float64(1) {
		t.Fatalf("pagination = %#v", listPayload)
	}
	resources, _ := listPayload["Resources"].([]any)
	if len(resources) != 2 {
		t.Fatalf("Resources = %#v", listPayload["Resources"])
	}
	byID := map[string]map[string]any{}
	for _, entry := range resources {
		resource, _ := entry.(map[string]any)
		byID[resource["id"].(string)] = resource
	}
	if byID["User"]["schema"] != scimUserSchema || byID["User"]["endpoint"] != "/Users" {
		t.Fatalf("User resource type = %#v", byID["User"])
	}
	if byID["Group"]["schema"] != scimGroupSchema || byID["Group"]["endpoint"] != "/Groups" {
		t.Fatalf("Group resource type = %#v", byID["Group"])
	}

	// A misconfigured public URL hides the discovery documents even when the
	// handler is called directly.
	badServer := &Server{cfg: config.Config{PublicURL: "http://mail.example.test"}}
	recorder = httptest.NewRecorder()
	badServer.scimServiceProviderConfig(recorder, httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("misconfigured ServiceProviderConfig status = %d", recorder.Code)
	}
}

func TestSCIMAdminTokenCreateValidation(t *testing.T) {
	server := New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	sess := &adminSessionRecord{ActiveTenantID: "tenant-1", User: adminUser{ID: "user-1"}}
	cases := []struct {
		name        string
		body        string
		contentType string
		wantCode    string
	}{
		{name: "bad content type", body: `{"name":"Entra"}`, contentType: "text/plain", wantCode: "BAD_JSON"},
		{name: "bad json", body: `{`, contentType: "application/json", wantCode: "BAD_JSON"},
		{name: "unknown field", body: `{"name":"Entra","hash":"x"}`, contentType: "application/json", wantCode: "BAD_JSON"},
		{name: "missing name", body: `{}`, contentType: "application/json", wantCode: "NAME"},
		{name: "blank name", body: `{"name":"   "}`, contentType: "application/json", wantCode: "NAME"},
		{name: "long name", body: `{"name":"` + strings.Repeat("n", scimTokenNameMaxLength+1) + `"}`, contentType: "application/json", wantCode: "NAME"},
		{name: "oversized body", body: `{"name":"` + strings.Repeat("a", scimTokenBodyLimit) + `"}`, contentType: "application/json", wantCode: "BAD_JSON"},
		{name: "invalid expiresAt", body: `{"name":"Entra","expiresAt":"tomorrow"}`, contentType: "application/json", wantCode: "EXPIRES_AT"},
		{name: "past expiresAt", body: `{"name":"Entra","expiresAt":"2020-01-01T00:00:00Z"}`, contentType: "application/json", wantCode: "EXPIRES_AT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/scim/tokens", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			request = request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, sess))
			recorder := httptest.NewRecorder()
			// The nil store proves validation rejects the request before any
			// token material is generated or written.
			server.adminCreateSCIMToken(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if payload.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", payload.Error.Code, tc.wantCode)
			}
		})
	}
}

// assertSCIMError validates the RFC 7644 error envelope and status.
func assertSCIMError(t *testing.T, recorder *httptest.ResponseRecorder, status int, scimType string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/scim+json; charset=utf-8" {
		t.Fatalf("content type = %q, want application/scim+json", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode SCIM error: %v; body=%s", err, recorder.Body.String())
	}
	schemas, _ := payload["schemas"].([]any)
	if len(schemas) != 1 || schemas[0] != scimErrorSchema {
		t.Fatalf("schemas = %#v", payload["schemas"])
	}
	if payload["status"] != strconv.Itoa(status) {
		t.Fatalf("status field = %#v, want %q", payload["status"], strconv.Itoa(status))
	}
	if detail, _ := payload["detail"].(string); detail == "" {
		t.Fatalf("detail is empty: %#v", payload)
	}
	if status == http.StatusUnauthorized {
		if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
		}
	}
	if status == http.StatusTooManyRequests {
		seconds, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
		if err != nil || seconds <= 0 {
			t.Fatalf("Retry-After = %q, want a positive number of seconds", recorder.Header().Get("Retry-After"))
		}
	}
	if scimType == "" {
		if _, exists := payload["scimType"]; exists {
			t.Fatalf("unexpected scimType: %#v", payload)
		}
	} else if payload["scimType"] != scimType {
		t.Fatalf("scimType = %#v, want %q", payload["scimType"], scimType)
	}
}
