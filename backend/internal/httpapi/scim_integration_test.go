package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run with:
//
//	A8M_SCIM_TEST_DSN='postgres://...' go test ./internal/httpapi -run SCIMIntegration -count=1
//
// The test owns two tenants and deletes them afterwards. It exercises the real
// PostgreSQL token lookup and admin token API; nothing is mocked.
func TestSCIMIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the PostgreSQL SCIM integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	// Prove migration 0061 is applied before testing its behaviour.
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users'
		  AND column_name IN ('given_name', 'family_name', 'scim_external_id', 'role_source')
	`).Scan(&columns); err != nil || columns != 4 {
		t.Fatalf("users SCIM columns missing; apply migration 0061 first: count=%d err=%v", columns, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'groups'
		  AND column_name IN ('scim_external_id', 'scim_role')
	`).Scan(&columns); err != nil || columns != 2 {
		t.Fatalf("groups SCIM columns missing; apply migration 0061 first: count=%d err=%v", columns, err)
	}
	var migrations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version = '0061_scim'`).Scan(&migrations); err != nil || migrations != 1 {
		t.Fatalf("schema_migrations row for 0061_scim missing: count=%d err=%v", migrations, err)
	}
	var tokenTable string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.scim_tokens')::text`).Scan(&tokenTable); err != nil || tokenTable == "" {
		t.Fatalf("scim_tokens table missing; apply migration 0061 first: %v", err)
	}

	slug := fmt.Sprintf("scim-test-%d", time.Now().UnixNano())
	createTenant := func(label string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text
		`, slug+"-"+label, "SCIM integration "+label).Scan(&id); err != nil {
			t.Fatalf("create tenant %s: %v", label, err)
		}
		return id
	}
	tenantA := createTenant("a")
	tenantB := createTenant("b")
	createOwner := func(tenantID, label string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, name, role)
			VALUES ($1, $2, $3, 'owner') RETURNING id::text
		`, tenantID, fmt.Sprintf("scim-owner-%s@example.test", label), "SCIM owner "+label).Scan(&id); err != nil {
			t.Fatalf("create owner %s: %v", label, err)
		}
		return id
	}
	userA := createOwner(tenantA, slug+"-a")
	userB := createOwner(tenantB, slug+"-b")
	createAdminSession := func(userID, tenantID string) (cookie, csrf string) {
		cookie = randomToken(32)
		csrf = randomToken(24)
		if _, err := pool.Exec(ctx, `
			INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, ip, user_agent, expires_at)
			VALUES ($1, $2, $3, $3, $4, NULL, 'scim integration test', now() + interval '1 hour')
		`, hashToken(cookie), userID, tenantID, csrf); err != nil {
			t.Fatalf("create admin session: %v", err)
		}
		return cookie, csrf
	}
	adminA, csrfA := createAdminSession(userA, tenantA)
	adminB, csrfB := createAdminSession(userB, tenantB)

	// A tenant admin whose role can read users but cannot create SCIM tokens.
	var limitedUser, limitedRole string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role)
		VALUES ($1, $2, 'SCIM limited admin', 'member') RETURNING id::text
	`, tenantA, fmt.Sprintf("scim-limited-%s@example.test", slug)).Scan(&limitedUser); err != nil {
		t.Fatalf("create limited admin: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO admin_roles (tenant_id, name, permissions)
		VALUES ($1, 'SCIM read only', ARRAY['users.read']::text[]) RETURNING id::text
	`, tenantA).Scan(&limitedRole); err != nil {
		t.Fatalf("create limited admin role: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_role_assignments (user_id, role_id, tenant_id) VALUES ($1, $2, $3)
	`, limitedUser, limitedRole, tenantA); err != nil {
		t.Fatalf("assign limited admin role: %v", err)
	}
	limitedCookie, limitedCSRF := createAdminSession(limitedUser, tenantA)

	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM admin_audit_log WHERE actor_id IN ($1, $2, $3)`, userA, userB, limitedUser)
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	}()

	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	server.store = &store.Store{DB: pool}
	server.logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	adminRequest := func(cookie, csrf, method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: cookie})
		if method != http.MethodGet {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		recorder := httptest.NewRecorder()
		server.AdminHandler().ServeHTTP(recorder, request)
		return recorder
	}
	methodRequest := func(method, rawToken, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		if rawToken != "" {
			request.Header.Set("Authorization", "Bearer "+rawToken)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	scimRequest := func(rawToken, path string) *httptest.ResponseRecorder {
		return methodRequest(http.MethodGet, rawToken, path)
	}
	decode := func(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
		}
	}
	createToken := func(t *testing.T, cookie, csrf, name, expiresAt string) map[string]any {
		t.Helper()
		body := fmt.Sprintf(`{"name":%q`, name)
		if expiresAt != "" {
			body += fmt.Sprintf(`,"expiresAt":%q`, expiresAt)
		}
		body += "}"
		recorder := adminRequest(cookie, csrf, http.MethodPost, "/admin/api/scim/tokens", body)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create SCIM token status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		decode(t, recorder, &payload)
		return payload
	}

	t.Run("create stores only hash and prefix", func(t *testing.T) {
		created := createToken(t, adminA, csrfA, "Entra ID", time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339))
		raw, _ := created["token"].(string)
		id, _ := created["id"].(string)
		prefix, _ := created["prefix"].(string)
		if !strings.HasPrefix(raw, scimTokenPrefix) || id == "" {
			t.Fatalf("unexpected create payload: %#v", created)
		}
		if !strings.HasPrefix(raw, prefix) || created["status"] != "active" || created["expiresAt"] == nil {
			t.Fatalf("unexpected create payload: %#v", created)
		}
		var storedHash, storedPrefix, storedName, storedTenant string
		if err := pool.QueryRow(ctx, `
			SELECT token_hash, token_prefix, name, tenant_id::text FROM scim_tokens WHERE id = $1
		`, id).Scan(&storedHash, &storedPrefix, &storedName, &storedTenant); err != nil {
			t.Fatalf("load stored token: %v", err)
		}
		if storedHash != hashToken(raw) || storedHash == raw {
			t.Fatalf("stored token hash does not match the raw token")
		}
		if storedPrefix != prefix || storedName != "Entra ID" || storedTenant != tenantA {
			t.Fatalf("stored token = %q %q %q", storedPrefix, storedName, storedTenant)
		}
	})

	var activeTokenID, activeRaw string
	t.Run("list never exposes raw token or hash", func(t *testing.T) {
		created := createToken(t, adminA, csrfA, "List check", "")
		activeTokenID, _ = created["id"].(string)
		activeRaw, _ = created["token"].(string)
		recorder := adminRequest(adminA, csrfA, http.MethodGet, "/admin/api/scim/tokens", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Tokens []map[string]any `json:"tokens"`
		}
		decode(t, recorder, &payload)
		found := false
		for _, token := range payload.Tokens {
			if token["id"] == activeTokenID {
				found = true
			}
			for _, forbidden := range []string{"token", "tokenHash", "token_hash", "hash", "raw"} {
				if _, exists := token[forbidden]; exists {
					t.Fatalf("list token exposes %q: %#v", forbidden, token)
				}
			}
		}
		if !found {
			t.Fatalf("created token missing from list: %#v", payload.Tokens)
		}
		body := recorder.Body.String()
		if strings.Contains(body, activeRaw) || strings.Contains(body, hashToken(activeRaw)) {
			t.Fatal("list response contains raw token or hash material")
		}
	})

	t.Run("valid token authenticates and records last use", func(t *testing.T) {
		recorder := scimRequest(activeRaw, "/scim/v2/ServiceProviderConfig")
		if recorder.Code != http.StatusOK {
			t.Fatalf("valid token status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/scim+json; charset=utf-8" {
			t.Fatalf("content type = %q", got)
		}
		var payload map[string]any
		decode(t, recorder, &payload)
		schemas, _ := payload["schemas"].([]any)
		if len(schemas) != 1 || schemas[0] != scimServiceProviderConfigSchema {
			t.Fatalf("schemas = %#v", payload["schemas"])
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			var lastUsed *time.Time
			if err := pool.QueryRow(ctx, `SELECT last_used_at FROM scim_tokens WHERE id = $1`, activeTokenID).Scan(&lastUsed); err != nil {
				t.Fatalf("load last_used_at: %v", err)
			}
			if lastUsed != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("last_used_at was not recorded asynchronously")
			}
			time.Sleep(25 * time.Millisecond)
		}
	})

	t.Run("discovery routes, bare subtree and methods", func(t *testing.T) {
		// The bare base path reaches scimAuth instead of a ServeMux redirect.
		assertSCIMError(t, scimRequest(activeRaw, "/scim/v2"), http.StatusNotFound, "")

		for _, id := range []string{"User", "Group"} {
			recorder := scimRequest(activeRaw, "/scim/v2/ResourceTypes/"+id)
			if recorder.Code != http.StatusOK {
				t.Fatalf("ResourceTypes/%s status = %d: %s", id, recorder.Code, recorder.Body.String())
			}
			var payload map[string]any
			decode(t, recorder, &payload)
			if payload["id"] != id {
				t.Fatalf("ResourceTypes/%s payload = %#v", id, payload)
			}
		}

		recorder := methodRequest(http.MethodPost, activeRaw, "/scim/v2/ServiceProviderConfig")
		assertSCIMError(t, recorder, http.StatusMethodNotAllowed, "")
		if got := recorder.Header().Get("Allow"); got != http.MethodGet {
			t.Fatalf("Allow = %q, want GET", got)
		}
	})

	t.Run("wrong token is rejected", func(t *testing.T) {
		wrong := scimTokenPrefix + randomToken(32)
		assertSCIMError(t, scimRequest(wrong, "/scim/v2/ServiceProviderConfig"), http.StatusUnauthorized, "")
	})

	var rotatedRaw, rotatedID string
	t.Run("rotation revokes the old token", func(t *testing.T) {
		recorder := adminRequest(adminA, csrfA, http.MethodPost, "/admin/api/scim/tokens/"+activeTokenID+"/rotate", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("rotate status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		decode(t, recorder, &payload)
		rotatedRaw, _ = payload["token"].(string)
		rotatedID, _ = payload["id"].(string)
		if !strings.HasPrefix(rotatedRaw, scimTokenPrefix) || rotatedID == "" || rotatedID == activeTokenID {
			t.Fatalf("unexpected rotate payload: %#v", payload)
		}
		assertSCIMError(t, scimRequest(activeRaw, "/scim/v2/ServiceProviderConfig"), http.StatusUnauthorized, "")
		if recorder := scimRequest(rotatedRaw, "/scim/v2/ServiceProviderConfig"); recorder.Code != http.StatusOK {
			t.Fatalf("rotated token status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var oldRevoked *time.Time
		var newHash string
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM scim_tokens WHERE id = $1`, activeTokenID).Scan(&oldRevoked); err != nil {
			t.Fatalf("load old token: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT token_hash FROM scim_tokens WHERE id = $1`, rotatedID).Scan(&newHash); err != nil {
			t.Fatalf("load rotated token: %v", err)
		}
		if oldRevoked == nil {
			t.Fatal("old token was not revoked")
		}
		if newHash != hashToken(rotatedRaw) {
			t.Fatal("rotated token hash does not match its raw token")
		}
	})

	t.Run("revocation is immediate and idempotent statuses are explicit", func(t *testing.T) {
		recorder := adminRequest(adminA, csrfA, http.MethodDelete, "/admin/api/scim/tokens/"+rotatedID, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("revoke status = %d: %s", recorder.Code, recorder.Body.String())
		}
		assertSCIMError(t, scimRequest(rotatedRaw, "/scim/v2/ServiceProviderConfig"), http.StatusUnauthorized, "")
		if recorder := adminRequest(adminA, csrfA, http.MethodPost, "/admin/api/scim/tokens/"+rotatedID+"/rotate", ""); recorder.Code != http.StatusConflict {
			t.Fatalf("rotate revoked status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if recorder := adminRequest(adminA, csrfA, http.MethodDelete, "/admin/api/scim/tokens/"+rotatedID, ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("second revoke status = %d: %s", recorder.Code, recorder.Body.String())
		}
	})

	var tenantBRaw string
	t.Run("cross-tenant tokens and admin actions are isolated", func(t *testing.T) {
		createdB := createToken(t, adminB, csrfB, "Okta B", "")
		tenantBRaw, _ = createdB["token"].(string)
		tenantBTokenID, _ := createdB["id"].(string)
		if tenantBRaw == "" || tenantBTokenID == "" {
			t.Fatalf("unexpected tenant B token: %#v", createdB)
		}

		// The token lookup binds the request to the token's own tenant.
		echo := server.scimAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant, ok := scimTenantFromContext(r.Context())
			if !ok {
				http.Error(w, "missing SCIM tenant context", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, tenant)
		}))
		request := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
		request.Header.Set("Authorization", "Bearer "+tenantBRaw)
		recorder := httptest.NewRecorder()
		echo.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("tenant B auth status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var tenant scimTenant
		decode(t, recorder, &tenant)
		if tenant.TenantID != tenantB || tenant.Name != "Okta B" || tenant.TokenID != tenantBTokenID {
			t.Fatalf("token resolved to the wrong tenant: %#v", tenant)
		}

		// Tenant B's list never shows tenant A's token.
		recorder = adminRequest(adminB, csrfB, http.MethodGet, "/admin/api/scim/tokens", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("tenant B list status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), activeTokenID) || strings.Contains(recorder.Body.String(), rotatedID) {
			t.Fatalf("tenant B list leaked tenant A tokens: %s", recorder.Body.String())
		}

		// Tenant B cannot rotate or revoke tenant A's token.
		createdA := createToken(t, adminA, csrfA, "Tenant A isolated", "")
		isolatedAID, _ := createdA["id"].(string)
		if recorder := adminRequest(adminB, csrfB, http.MethodPost, "/admin/api/scim/tokens/"+isolatedAID+"/rotate", ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant rotate status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if recorder := adminRequest(adminB, csrfB, http.MethodDelete, "/admin/api/scim/tokens/"+isolatedAID, ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant revoke status = %d: %s", recorder.Code, recorder.Body.String())
		}
		// The failed cross-tenant attempts left tenant A's token usable.
		if recorder := scimRequest(createdA["token"].(string), "/scim/v2/ServiceProviderConfig"); recorder.Code != http.StatusOK {
			t.Fatalf("tenant A token status after cross-tenant attempts = %d: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("expired tokens are rejected", func(t *testing.T) {
		expiredRaw := scimTokenPrefix + randomToken(32)
		prefix := expiredRaw[:scimTokenDisplayLength]
		if _, err := pool.Exec(ctx, `
			INSERT INTO scim_tokens (tenant_id, name, token_hash, token_prefix, expires_at)
			VALUES ($1, 'Expired', $2, $3, now() - interval '1 hour')
		`, tenantA, hashToken(expiredRaw), prefix); err != nil {
			t.Fatalf("seed expired token: %v", err)
		}
		assertSCIMError(t, scimRequest(expiredRaw, "/scim/v2/ServiceProviderConfig"), http.StatusUnauthorized, "")
	})

	t.Run("admin permission is required", func(t *testing.T) {
		for _, tc := range []struct {
			method string
			path   string
			body   string
		}{
			{method: http.MethodGet, path: "/admin/api/scim/tokens"},
			{method: http.MethodPost, path: "/admin/api/scim/tokens", body: `{"name":"Denied"}`},
		} {
			recorder := adminRequest(limitedCookie, limitedCSRF, tc.method, tc.path, tc.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("%s %s status = %d, want 403: %s", tc.method, tc.path, recorder.Code, recorder.Body.String())
			}
		}
	})

	t.Run("successful auth resets the rate limiter", func(t *testing.T) {
		created := createToken(t, adminA, csrfA, "Limiter reset", "")
		raw, _ := created["token"].(string)
		server.scimLimit = newLoginLimiter(2, time.Minute)
		defer func() {
			server.scimLimit = newLoginLimiter(oidcCallbackRateLimit, oidcCallbackRateWindow)
		}()
		badAttempt := func() *httptest.ResponseRecorder {
			return scimRequest(scimTokenPrefix+randomToken(32), "/scim/v2/ServiceProviderConfig")
		}
		assertSCIMError(t, badAttempt(), http.StatusUnauthorized, "")
		if recorder := scimRequest(raw, "/scim/v2/ServiceProviderConfig"); recorder.Code != http.StatusOK {
			t.Fatalf("valid token after a failure status = %d: %s", recorder.Code, recorder.Body.String())
		}
		// The successful request reset the counter, so a full window of
		// failures is available again before the limiter returns 429.
		assertSCIMError(t, badAttempt(), http.StatusUnauthorized, "")
		assertSCIMError(t, badAttempt(), http.StatusUnauthorized, "")
		assertSCIMError(t, badAttempt(), http.StatusTooManyRequests, "tooMany")
	})

	t.Run("a8m_app can use scim_tokens", func(t *testing.T) {
		var roleExists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'a8m_app')`).Scan(&roleExists); err != nil {
			t.Fatalf("check a8m_app role: %v", err)
		}
		if !roleExists {
			t.Skip("a8m_app role is not present in this database")
		}
		var schemaUsage bool
		if err := pool.QueryRow(ctx, `SELECT has_schema_privilege('a8m_app', 'public', 'USAGE')`).Scan(&schemaUsage); err != nil || !schemaUsage {
			t.Fatalf("a8m_app lacks public schema USAGE: %v", err)
		}
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var allowed bool
			if err := pool.QueryRow(ctx, `SELECT has_table_privilege('a8m_app', 'public.scim_tokens', $1)`, privilege).Scan(&allowed); err != nil || !allowed {
				t.Fatalf("a8m_app lacks %s on scim_tokens: %v", privilege, err)
			}
		}
	})

	t.Run("audit records token ids but never token material", func(t *testing.T) {
		rows, err := pool.Query(ctx, `
			SELECT action, target, detail, metadata::text
			FROM admin_audit_log
			WHERE actor_id IN ($1, $2) AND action LIKE 'admin.scim_token_%'
			ORDER BY id
		`, userA, userB)
		if err != nil {
			t.Fatalf("load audit rows: %v", err)
		}
		defer rows.Close()
		actions := map[string]int{}
		auditText := strings.Builder{}
		for rows.Next() {
			var action, target, detail, metadata string
			if err := rows.Scan(&action, &target, &detail, &metadata); err != nil {
				t.Fatalf("scan audit row: %v", err)
			}
			actions[action]++
			auditText.WriteString(target + "\n" + detail + "\n" + metadata + "\n")
			var parsed map[string]any
			if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
				t.Fatalf("decode audit metadata: %v", err)
			}
			if id, _ := parsed["tokenId"].(string); id == "" {
				t.Fatalf("audit %s lacks a token id: %s", action, metadata)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate audit rows: %v", err)
		}
		for _, action := range []string{"admin.scim_token_created", "admin.scim_token_rotated", "admin.scim_token_revoked"} {
			if actions[action] == 0 {
				t.Fatalf("missing audit action %s: %#v", action, actions)
			}
		}
		text := auditText.String()
		for _, secret := range []string{activeRaw, rotatedRaw, tenantBRaw} {
			if strings.Contains(text, secret) || strings.Contains(text, hashToken(secret)) {
				t.Fatalf("audit rows contain token material")
			}
		}
	})
}
