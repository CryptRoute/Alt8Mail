package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run with:
//
//	A8M_SCIM_TEST_DSN='postgres://...' go test ./internal/httpapi -run SCIMUsersIntegration -count=1
//
// The test owns two tenants, seeds a real mailbox and real sessions, and drives
// the mounted HTTP routes. Nothing is mocked.
func TestSCIMUsersIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the PostgreSQL SCIM Users integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users'
		  AND column_name IN ('given_name', 'family_name', 'scim_external_id', 'role_source')
	`).Scan(&columns); err != nil || columns != 4 {
		t.Fatalf("users SCIM columns missing; apply migration 0061 first: count=%d err=%v", columns, err)
	}

	slug := fmt.Sprintf("scim-users-%d", time.Now().UnixNano())
	domainA := slug + "-a.example.test"
	domainB := slug + "-b.example.test"
	createTenant := func(label, domain string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text
		`, slug+"-"+label, "SCIM Users integration "+label).Scan(&id); err != nil {
			t.Fatalf("create tenant %s: %v", label, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO mail_domains (tenant_id, domain, status, verified_at)
			VALUES ($1, $2, 'verified', now())
		`, id, domain); err != nil {
			t.Fatalf("create domain %s: %v", domain, err)
		}
		return id
	}
	tenantA := createTenant("a", domainA)
	tenantB := createTenant("b", domainB)
	createToken := func(tenantID, name string) string {
		raw := scimTokenPrefix + randomToken(32)
		if _, err := pool.Exec(ctx, `
			INSERT INTO scim_tokens (tenant_id, name, token_hash, token_prefix)
			VALUES ($1, $2, $3, $4)
		`, tenantID, name, hashToken(raw), raw[:scimTokenDisplayLength]); err != nil {
			t.Fatalf("create SCIM token %s: %v", name, err)
		}
		return raw
	}
	tokenA := createToken(tenantA, "Entra A")
	tokenB := createToken(tenantB, "Okta B")

	// Tenant B owns a live owner account to prove SCIM cannot lock it out.
	var ownerB string
	ownerBEmail := "owner@" + domainB
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role) VALUES ($1, $2, 'Owner B', 'owner') RETURNING id::text
	`, tenantB, ownerBEmail).Scan(&ownerB); err != nil {
		t.Fatalf("create owner B: %v", err)
	}
	adminCookie := randomToken(32)
	adminCSRF := randomToken(24)
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, expires_at)
		VALUES ($1, $2, $3, $3, $4, now() + interval '1 hour')
	`, hashToken(adminCookie), ownerB, tenantB, adminCSRF); err != nil {
		t.Fatalf("create tenant B admin session: %v", err)
	}

	// A non-owner platform administrator must be untouchable through SCIM.
	var systemAdminB string
	systemAdminEmail := "sysadmin@" + domainB
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role, is_system_admin, scim_external_id)
		VALUES ($1, $2, 'System Admin B', 'member', true, 'sys-ext') RETURNING id::text
	`, tenantB, systemAdminEmail).Scan(&systemAdminB); err != nil {
		t.Fatalf("create system admin B: %v", err)
	}
	systemAdminLocal, _ := splitEmail(systemAdminEmail)
	if _, err := pool.Exec(ctx, `
		INSERT INTO mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active)
		VALUES ($1, $2, $3, $4, $5, '/tmp/scim-sysadmin/Maildir', true)
	`, tenantB, systemAdminB, systemAdminEmail, domainB, systemAdminLocal); err != nil {
		t.Fatalf("create system admin mailbox: %v", err)
	}
	var systemAdminGroup string
	if err := pool.QueryRow(ctx, `
		INSERT INTO groups (tenant_id, name, email, group_type, status, scim_role)
		VALUES ($1, 'System Admins', $2, 'security', 'active', '') RETURNING id::text
	`, tenantB, "sysadmins@"+domainB).Scan(&systemAdminGroup); err != nil {
		t.Fatalf("create system admin group: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
	`, systemAdminGroup, systemAdminB); err != nil {
		t.Fatalf("add system admin to group: %v", err)
	}

	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM audit_log WHERE tenant_id IN ($1, $2)`, tenantA, tenantB)
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	}()

	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	server.store = &store.Store{DB: pool}
	server.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	base := "https://mail.example.test/scim/v2"

	scimRequest := func(rawToken, method, path, contentType, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			if contentType == "" {
				contentType = "application/scim+json"
			}
			request.Header.Set("Content-Type", contentType)
		}
		if rawToken != "" {
			request.Header.Set("Authorization", "Bearer "+rawToken)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
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
	decode := func(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
		}
	}
	createBody := func(email, externalID, formatted, given, family string, active *bool, extra string) string {
		body := fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":%q,`, email)
		if externalID != "" {
			body += fmt.Sprintf(`"externalId":%q,`, externalID)
		}
		body += fmt.Sprintf(`"name":{"formatted":%q,"givenName":%q,"familyName":%q},"displayName":%q,`, formatted, given, family, formatted)
		body += fmt.Sprintf(`"emails":[{"value":%q,"type":"work","primary":true}]`, email)
		if active != nil {
			body += fmt.Sprintf(`,"active":%t`, *active)
		}
		if extra != "" {
			body += "," + extra
		}
		return body + "}"
	}
	createUser := func(t *testing.T, rawToken, body string) (string, map[string]any) {
		t.Helper()
		recorder := scimRequest(rawToken, http.MethodPost, "/scim/v2/Users", "", body)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create user status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		id, _ := resource["id"].(string)
		if id == "" {
			t.Fatalf("created resource has no id: %#v", resource)
		}
		return id, resource
	}
	loadUser := func(t *testing.T, id string) scimUserRow {
		t.Helper()
		user, err := scanSCIMUser(pool.QueryRow(ctx, `SELECT `+scimUserColumns+` FROM users WHERE id = $1`, id))
		if err != nil {
			t.Fatalf("load user %s: %v", id, err)
		}
		return user
	}
	seedSession := func(t *testing.T, tenantID, userID string) string {
		t.Helper()
		id := randomToken(16)
		if _, err := pool.Exec(ctx, `
			INSERT INTO sessions (id, user_id, tenant_id, csrf_token, user_agent, expires_at)
			VALUES ($1, $2, $3, $4, 'scim users integration', now() + interval '1 hour')
		`, id, userID, tenantID, randomToken(16)); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		return id
	}
	seedMail := func(t *testing.T, tenantID, userID string) {
		t.Helper()
		messageID := "msg-" + randomToken(8)
		attachmentID := "att-" + randomToken(8)
		if _, err := pool.Exec(ctx, `
			INSERT INTO mail_messages (id, tenant_id, user_id, folder, thread_id, subject)
			VALUES ($1, $2, $3, 'INBOX', $1, 'Preserved message')
		`, messageID, tenantID, userID); err != nil {
			t.Fatalf("seed mail message: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO attachments (id, tenant_id, user_id, name) VALUES ($1, $2, $3, 'preserved.txt')
		`, attachmentID, tenantID, userID); err != nil {
			t.Fatalf("seed attachment: %v", err)
		}
		eventID := "evt-" + randomToken(8)
		if _, err := pool.Exec(ctx, `
			INSERT INTO calendar_events (id, tenant_id, user_id, uid, title, starts_at, ends_at)
			VALUES ($1, $2, $3, $1, 'Preserved event', now(), now() + interval '1 hour')
		`, eventID, tenantID, userID); err != nil {
			t.Fatalf("seed calendar event: %v", err)
		}
	}

	var aliceID, bobID string

	t.Run("create provisions a user, mailbox and primary alias with unusable credentials", func(t *testing.T) {
		email := "alice@" + domainA
		body := createBody(email, "ext-1", "Alice Example", "Alice", "Example", scimBoolPtr(true),
			`"roles":[{"value":"admin"}],"password":"Sup3rSecret!","activation":{"type":"activation"}`)
		recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "", body)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/scim+json; charset=utf-8" {
			t.Fatalf("content type = %q", got)
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		aliceID, _ = resource["id"].(string)
		if aliceID == "" {
			t.Fatalf("created resource has no id: %#v", resource)
		}
		if got := recorder.Header().Get("Location"); got != base+"/Users/"+aliceID {
			t.Fatalf("Location = %q", got)
		}
		if resource["userName"] != email || resource["externalId"] != "ext-1" || resource["displayName"] != "Alice Example" {
			t.Fatalf("resource identity = %#v", resource)
		}
		if resource["active"] != true {
			t.Fatalf("resource active = %#v", resource["active"])
		}
		roles, _ := resource["roles"].([]any)
		if len(roles) != 1 {
			t.Fatalf("roles = %#v", resource["roles"])
		}
		role, _ := roles[0].(map[string]any)
		if role["value"] != "member" {
			t.Fatalf("SCIM create granted role %#v, want member", role["value"])
		}
		if _, exists := resource["password"]; exists {
			t.Fatalf("resource exposes password: %#v", resource)
		}
		meta, _ := resource["meta"].(map[string]any)
		if meta["resourceType"] != "User" || meta["location"] != base+"/Users/"+aliceID {
			t.Fatalf("meta = %#v", meta)
		}
		if version, _ := meta["version"].(string); !strings.HasPrefix(version, `W/"`) {
			t.Fatalf("meta version = %#v", meta["version"])
		}

		user := loadUser(t, aliceID)
		if user.Email != email || user.Name != "Alice Example" || user.GivenName != "Alice" || user.FamilyName != "Example" {
			t.Fatalf("stored user = %#v", user)
		}
		var roleName, roleSource, passwordHash, mailPasswordHash string
		var forceChange, onboarding bool
		var externalID *string
		if err := pool.QueryRow(ctx, `
			SELECT role, role_source, COALESCE(password_hash, ''), COALESCE(mail_password_hash, ''),
			       force_password_change, onboarding_required, scim_external_id
			FROM users WHERE id = $1
		`, aliceID).Scan(&roleName, &roleSource, &passwordHash, &mailPasswordHash, &forceChange, &onboarding, &externalID); err != nil {
			t.Fatalf("load stored credentials: %v", err)
		}
		if roleName != "member" || roleSource != "manual" {
			t.Fatalf("role = %q source = %q", roleName, roleSource)
		}
		if !forceChange || !onboarding {
			t.Fatalf("forceChange=%v onboarding=%v", forceChange, onboarding)
		}
		if !strings.HasPrefix(passwordHash, "$argon2id$") {
			t.Fatalf("web password hash = %q", passwordHash)
		}
		if auth.VerifyPassword(passwordHash, "Sup3rSecret!") {
			t.Fatal("SCIM create accepted the client-supplied password")
		}
		if !strings.HasPrefix(mailPasswordHash, "{SHA512-CRYPT}$6$") {
			t.Fatalf("mail password hash = %q", mailPasswordHash)
		}
		if externalID == nil || *externalID != "ext-1" {
			t.Fatalf("stored externalId = %#v", externalID)
		}

		var mailboxEmail, maildir string
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT email::text, maildir_path, active FROM mailboxes WHERE user_id = $1`, aliceID).Scan(&mailboxEmail, &maildir, &mailboxActive); err != nil {
			t.Fatalf("load mailbox: %v", err)
		}
		if mailboxEmail != email || !mailboxActive || !strings.Contains(maildir, domainA) {
			t.Fatalf("mailbox = %q %q %v", mailboxEmail, maildir, mailboxActive)
		}
		var aliasKind string
		if err := pool.QueryRow(ctx, `SELECT kind FROM user_aliases WHERE user_id = $1 AND email = $2`, aliceID, email).Scan(&aliasKind); err != nil {
			t.Fatalf("load primary alias: %v", err)
		}
		if aliasKind != "primary" {
			t.Fatalf("alias kind = %q", aliasKind)
		}
		var calendars int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendars WHERE user_id = $1`, aliceID).Scan(&calendars); err != nil || calendars == 0 {
			t.Fatalf("calendar not provisioned: count=%d err=%v", calendars, err)
		}
		// app_states must exist for an active user so bootstrap/onboarding work.
		var appStates int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_states WHERE user_id = $1`, aliceID).Scan(&appStates); err != nil || appStates != 1 {
			t.Fatalf("app_states for active SCIM user = %d err=%v", appStates, err)
		}
	})

	t.Run("idempotent replay returns the existing resource", func(t *testing.T) {
		email := "alice@" + domainA
		body := createBody(email, "ext-1", "Alice Example", "Alice", "Example", scimBoolPtr(true), "")
		recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "", body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("replay status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if resource["id"] != aliceID {
			t.Fatalf("replay id = %#v, want %s", resource["id"], aliceID)
		}
		if got := recorder.Header().Get("Location"); got != base+"/Users/"+aliceID {
			t.Fatalf("replay Location = %q", got)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE tenant_id = $1 AND email = $2`, tenantA, email).Scan(&count); err != nil || count != 1 {
			t.Fatalf("replay created duplicate rows: count=%d err=%v", count, err)
		}
	})

	emailA := "alice@" + domainA
	t.Run("duplicate userName or externalId conflicts", func(t *testing.T) {
		recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "", createBody(emailA, "ext-1-different", "Alice Again", "Alice", "Again", nil, ""))
		assertSCIMError(t, recorder, http.StatusConflict, "uniqueness")
		recorder = scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "", createBody("other@"+domainA, "ext-1", "Other Ext", "Other", "Ext", nil, ""))
		assertSCIMError(t, recorder, http.StatusConflict, "uniqueness")
	})

	t.Run("empty externalId stores NULL and is omitted from the resource", func(t *testing.T) {
		email := "bob@" + domainA
		body := createBody(email, "", "Bob Example", "Bob", "Example", nil, `"externalId":""`)
		bobID, _ = createUser(t, tokenA, body)
		var externalID *string
		if err := pool.QueryRow(ctx, `SELECT scim_external_id FROM users WHERE id = $1`, bobID).Scan(&externalID); err != nil {
			t.Fatalf("load externalId: %v", err)
		}
		if externalID != nil {
			t.Fatalf("empty externalId stored %q, want NULL", *externalID)
		}
		recorder := scimRequest(tokenA, http.MethodGet, "/scim/v2/Users/"+bobID, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("get bob status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if _, exists := resource["externalId"]; exists {
			t.Fatalf("NULL externalId rendered: %#v", resource)
		}
	})

	t.Run("create with active=false provisions a suspended user", func(t *testing.T) {
		email := "carol@" + domainA
		carolID, resource := createUser(t, tokenA, createBody(email, "ext-carol", "Carol Example", "Carol", "Example", scimBoolPtr(false), ""))
		if resource["active"] != false {
			t.Fatalf("resource active = %#v", resource["active"])
		}
		user := loadUser(t, carolID)
		if user.Status != "suspended" {
			t.Fatalf("status = %q", user.Status)
		}
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, carolID).Scan(&mailboxActive); err != nil || mailboxActive {
			t.Fatalf("mailbox active = %v err=%v", mailboxActive, err)
		}
		var appStates int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_states WHERE user_id = $1`, carolID).Scan(&appStates); err != nil || appStates != 0 {
			t.Fatalf("suspended SCIM user should have no app_states: count=%d err=%v", appStates, err)
		}

		// Reactivating initializes app_states so the first sign-in can bootstrap.
		recorder := scimRequest(tokenA, http.MethodPatch, "/scim/v2/Users/"+carolID, "", `{"Operations":[{"op":"replace","path":"active","value":true}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("reactivate suspended user status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_states WHERE user_id = $1`, carolID).Scan(&appStates); err != nil || appStates != 1 {
			t.Fatalf("app_states after reactivation = %d err=%v", appStates, err)
		}
	})

	t.Run("list, filter, get and pagination bounds", func(t *testing.T) {
		recorder := scimRequest(tokenA, http.MethodGet, "/scim/v2/Users", "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var listed struct {
			Schemas      []string         `json:"schemas"`
			TotalResults int              `json:"totalResults"`
			ItemsPerPage int              `json:"itemsPerPage"`
			StartIndex   int              `json:"startIndex"`
			Resources    []map[string]any `json:"Resources"`
		}
		decode(t, recorder, &listed)
		if len(listed.Schemas) != 1 || listed.Schemas[0] != scimListResponseSchema {
			t.Fatalf("schemas = %#v", listed.Schemas)
		}
		if listed.TotalResults != 3 || listed.ItemsPerPage != 3 || listed.StartIndex != 1 {
			t.Fatalf("pagination = %#v", listed)
		}
		byEmail := map[string]map[string]any{}
		for _, resource := range listed.Resources {
			email, _ := resource["userName"].(string)
			byEmail[email] = resource
		}
		for _, email := range []string{"alice@" + domainA, "bob@" + domainA, "carol@" + domainA} {
			if byEmail[email] == nil {
				t.Fatalf("list missing %s: %#v", email, byEmail)
			}
		}

		filtered := func(query string) *httptest.ResponseRecorder {
			return scimRequest(tokenA, http.MethodGet, "/scim/v2/Users?filter="+query, "", "")
		}
		recorder = filtered(`userName%20eq%20%22alice%40` + strings.ReplaceAll(domainA, ".", "%2E") + `%22`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("userName filter status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var filterPayload struct {
			TotalResults int              `json:"totalResults"`
			Resources    []map[string]any `json:"Resources"`
		}
		decode(t, recorder, &filterPayload)
		if filterPayload.TotalResults != 1 || len(filterPayload.Resources) != 1 || filterPayload.Resources[0]["id"] != aliceID {
			t.Fatalf("userName filter = %#v", filterPayload)
		}
		recorder = filtered("externalId%20eq%20%22ext-carol%22")
		decode(t, recorder, &filterPayload)
		if filterPayload.TotalResults != 1 || filterPayload.Resources[0]["userName"] != "carol@"+domainA {
			t.Fatalf("externalId filter = %#v", filterPayload)
		}
		recorder = filtered("USERNAME%20EQ%20%22bob%40" + strings.ReplaceAll(domainA, ".", "%2E") + "%22")
		decode(t, recorder, &filterPayload)
		if filterPayload.TotalResults != 1 || filterPayload.Resources[0]["id"] != bobID {
			t.Fatalf("case-insensitive filter = %#v", filterPayload)
		}
		for _, invalid := range []string{
			"displayName%20eq%20%22Alice%22",
			"userName%20co%20%22alice%22",
			"userName%20eq%20alice",
		} {
			assertSCIMError(t, filtered(invalid), http.StatusBadRequest, "invalidFilter")
		}

		// Ordered by email: alice, bob, carol.
		recorder = scimRequest(tokenA, http.MethodGet, "/scim/v2/Users?startIndex=2&count=1", "", "")
		decode(t, recorder, &filterPayload)
		if filterPayload.TotalResults != 3 || len(filterPayload.Resources) != 1 || filterPayload.Resources[0]["id"] != bobID {
			t.Fatalf("pagination page = %#v", filterPayload)
		}
		recorder = scimRequest(tokenA, http.MethodGet, "/scim/v2/Users?count=1000", "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("capped count status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &filterPayload)
		if filterPayload.TotalResults != 3 || len(filterPayload.Resources) != 3 {
			t.Fatalf("capped count = %#v", filterPayload)
		}
		recorder = scimRequest(tokenA, http.MethodGet, "/scim/v2/Users?startIndex=10", "", "")
		decode(t, recorder, &filterPayload)
		if filterPayload.TotalResults != 3 || len(filterPayload.Resources) != 0 {
			t.Fatalf("out-of-range page = %#v", filterPayload)
		}
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Users?startIndex=0", "", ""), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Users?count=-1", "", ""), http.StatusBadRequest, "invalidValue")

		recorder = scimRequest(tokenA, http.MethodGet, "/scim/v2/Users/"+aliceID, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("get status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var alice map[string]any
		decode(t, recorder, &alice)
		if alice["userName"] != emailA {
			t.Fatalf("get resource = %#v", alice)
		}
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Users/00000000-0000-4000-8000-000000000000", "", ""), http.StatusNotFound, "")
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Users/not-a-uuid", "", ""), http.StatusNotFound, "")
	})

	t.Run("concurrent creates with the same externalId yield one success and one conflict", func(t *testing.T) {
		externalID := "ext-race"
		emails := []string{"race1@" + domainA, "race2@" + domainA}
		start := make(chan struct{})
		results := make([]*httptest.ResponseRecorder, len(emails))
		var wait sync.WaitGroup
		for index := range emails {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				<-start
				results[index] = scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "",
					createBody(emails[index], externalID, "Race User", "Race", "User", nil, ""))
			}(index)
		}
		close(start)
		wait.Wait()

		codes := []int{results[0].Code, results[1].Code}
		sort.Ints(codes)
		if codes[0] != http.StatusCreated || codes[1] != http.StatusConflict {
			t.Fatalf("concurrent create statuses = %v: %s | %s", codes, results[0].Body.String(), results[1].Body.String())
		}
		for _, recorder := range results {
			if recorder.Code == http.StatusConflict {
				assertSCIMError(t, recorder, http.StatusConflict, "uniqueness")
			}
		}
		var winnerID, winnerEmail string
		if err := pool.QueryRow(ctx, `
			SELECT id::text, email::text FROM users WHERE tenant_id = $1 AND scim_external_id = $2
		`, tenantA, externalID).Scan(&winnerID, &winnerEmail); err != nil {
			t.Fatalf("load race winner: %v", err)
		}
		if winnerID == "" || (winnerEmail != emails[0] && winnerEmail != emails[1]) {
			t.Fatalf("race winner = %q %q", winnerID, winnerEmail)
		}
		var users, mailboxes int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM users WHERE tenant_id = $1 AND (email = $2 OR email = $3)
		`, tenantA, emails[0], emails[1]).Scan(&users); err != nil || users != 1 {
			t.Fatalf("users after concurrent create = %d err=%v", users, err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM mailboxes mb JOIN users u ON u.id = mb.user_id
			WHERE u.tenant_id = $1 AND (u.email = $2 OR u.email = $3)
		`, tenantA, emails[0], emails[1]).Scan(&mailboxes); err != nil || mailboxes != 1 {
			t.Fatalf("mailboxes after concurrent create = %d err=%v", mailboxes, err)
		}
		for _, recorder := range results {
			if recorder.Code != http.StatusCreated {
				continue
			}
			var created map[string]any
			decode(t, recorder, &created)
			if created["id"] != winnerID {
				t.Fatalf("created response id = %#v, want winner %q", created["id"], winnerID)
			}
		}
	})

	t.Run("cross-tenant reads and writes are invisible", func(t *testing.T) {
		for _, tc := range []struct {
			method string
			path   string
			body   string
		}{
			{method: http.MethodGet, path: "/scim/v2/Users/" + aliceID},
			{method: http.MethodPut, path: "/scim/v2/Users/" + aliceID, body: `{"userName":"` + emailA + `","name":{"formatted":"Nope"}}`},
			{method: http.MethodPatch, path: "/scim/v2/Users/" + aliceID, body: `{"Operations":[{"op":"replace","path":"active","value":false}]}`},
			{method: http.MethodDelete, path: "/scim/v2/Users/" + aliceID},
		} {
			recorder := scimRequest(tokenB, tc.method, tc.path, "", tc.body)
			assertSCIMError(t, recorder, http.StatusNotFound, "")
		}
		recorder := scimRequest(tokenB, http.MethodGet, "/scim/v2/Users?filter=userName%20eq%20%22alice%40"+strings.ReplaceAll(domainA, ".", "%2E")+"%22", "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("cross-tenant filter status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			TotalResults int `json:"totalResults"`
		}
		decode(t, recorder, &payload)
		if payload.TotalResults != 0 {
			t.Fatalf("cross-tenant filter leaked %d users", payload.TotalResults)
		}
		recorder = scimRequest(tokenB, http.MethodGet, "/scim/v2/Users?filter=externalId%20eq%20%22ext-1%22", "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("cross-tenant externalId filter status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &payload)
		if payload.TotalResults != 0 {
			t.Fatalf("cross-tenant externalId filter leaked %d users", payload.TotalResults)
		}
		if recorder := scimRequest(tokenB, http.MethodGet, "/scim/v2/Users", "", ""); strings.Contains(recorder.Body.String(), emailA) {
			t.Fatalf("cross-tenant list leaked tenant A users: %s", recorder.Body.String())
		}
	})

	t.Run("unverified and foreign domains are rejected without creating a domain", func(t *testing.T) {
		var before int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_domains WHERE tenant_id = $1`, tenantA).Scan(&before); err != nil {
			t.Fatalf("count domains: %v", err)
		}
		for _, email := range []string{"x@" + domainB, "x@not-" + slug + ".example.test"} {
			recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "", createBody(email, "", "Rejected", "Re", "Jected", nil, ""))
			assertSCIMError(t, recorder, http.StatusBadRequest, "invalidValue")
			var users int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE tenant_id = $1 AND email = $2`, tenantA, email).Scan(&users); err != nil || users != 0 {
				t.Fatalf("rejected domain created a user: count=%d err=%v", users, err)
			}
		}
		var after int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_domains WHERE tenant_id = $1`, tenantA).Scan(&after); err != nil || after != before {
			t.Fatalf("domain count changed: before=%d after=%d err=%v", before, after, err)
		}
	})

	t.Run("PUT updates mutable fields and rejects immutable ones", func(t *testing.T) {
		seedSession(t, tenantA, aliceID)
		seedMail(t, tenantA, aliceID)

		put := func(body string) *httptest.ResponseRecorder {
			return scimRequest(tokenA, http.MethodPut, "/scim/v2/Users/"+aliceID, "", body)
		}
		recorder := put(fmt.Sprintf(`{"userName":%q,"emails":[{"value":%q}],"externalId":"ext-1b","name":{"formatted":"Alice Updated","givenName":"Alicia","familyName":"Updated"},"active":true}`, strings.ToUpper(emailA), emailA))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if resource["externalId"] != "ext-1b" || resource["displayName"] != "Alice Updated" {
			t.Fatalf("PUT resource = %#v", resource)
		}
		user := loadUser(t, aliceID)
		if user.Name != "Alice Updated" || user.GivenName != "Alicia" || user.FamilyName != "Updated" {
			t.Fatalf("PUT stored name = %#v", user)
		}
		if user.ExternalID == nil || *user.ExternalID != "ext-1b" {
			t.Fatalf("PUT stored externalId = %#v", user.ExternalID)
		}

		assertSCIMError(t, put(fmt.Sprintf(`{"userName":%q}`, "changed@"+domainA)), http.StatusConflict, "mutability")
		assertSCIMError(t, put(fmt.Sprintf(`{"userName":%q,"emails":[{"value":%q}]}`, emailA, "changed@"+domainA)), http.StatusConflict, "mutability")

		// A conflicting externalId on another tenant user is a uniqueness error.
		if _, err := pool.Exec(ctx, `UPDATE users SET scim_external_id = 'ext-1b-conflict' WHERE id = $1`, bobID); err != nil {
			t.Fatalf("seed conflicting externalId: %v", err)
		}
		assertSCIMError(t, put(fmt.Sprintf(`{"userName":%q,"externalId":"ext-1b-conflict"}`, emailA)), http.StatusConflict, "uniqueness")

		// Explicit JSON null clears externalId; an omitted attribute keeps it.
		recorder = put(fmt.Sprintf(`{"userName":%q,"externalId":null}`, emailA))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT null externalId status = %d: %s", recorder.Code, recorder.Body.String())
		}
		nullResource := map[string]any{}
		decode(t, recorder, &nullResource)
		if _, exists := nullResource["externalId"]; exists {
			t.Fatalf("PUT null externalId was not cleared: %#v", nullResource["externalId"])
		}
		if user = loadUser(t, aliceID); user.ExternalID != nil {
			t.Fatalf("PUT null externalId stored %#v", user.ExternalID)
		}

		// Seed every live-authentication artefact so deprovision can prove it
		// clears them while retaining the passkey credential.
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth_challenges (id, user_id, tenant_id, remember, expires_at)
			VALUES ($1, $2, $3, true, now() + interval '10 minutes')
		`, randomToken(16), aliceID, tenantA); err != nil {
			t.Fatalf("seed auth challenge: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, expires_at)
			VALUES ($1, $2, $3, $3, $4, now() + interval '1 hour')
		`, hashToken(randomToken(16)), aliceID, tenantA, randomToken(16)); err != nil {
			t.Fatalf("seed admin session: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO trusted_devices (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '30 days')
		`, aliceID, hashToken(randomToken(16))); err != nil {
			t.Fatalf("seed trusted device: %v", err)
		}
		credentialID := []byte("cred-" + randomToken(8))
		if _, err := pool.Exec(ctx, `
			INSERT INTO webauthn_credentials (user_id, credential_id, public_key) VALUES ($1, $2, $3)
		`, aliceID, credentialID, []byte("public-key")); err != nil {
			t.Fatalf("seed passkey: %v", err)
		}

		recorder = put(fmt.Sprintf(`{"userName":%q,"active":false}`, emailA))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT deactivate status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &resource)
		if resource["active"] != false {
			t.Fatalf("PUT deactivate active = %#v", resource["active"])
		}
		user = loadUser(t, aliceID)
		if user.Status != "suspended" {
			t.Fatalf("PUT deactivate status = %q", user.Status)
		}
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, aliceID).Scan(&mailboxActive); err != nil || mailboxActive {
			t.Fatalf("PUT deactivate mailbox active = %v err=%v", mailboxActive, err)
		}
		var authChallenges, adminSessions, trustedDevices, passkeys int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_challenges WHERE user_id = $1`, aliceID).Scan(&authChallenges); err != nil || authChallenges != 0 {
			t.Fatalf("pending auth challenges after deactivate = %d err=%v", authChallenges, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_sessions WHERE user_id = $1`, aliceID).Scan(&adminSessions); err != nil || adminSessions != 0 {
			t.Fatalf("admin sessions after deactivate = %d err=%v", adminSessions, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM trusted_devices WHERE user_id = $1`, aliceID).Scan(&trustedDevices); err != nil || trustedDevices != 0 {
			t.Fatalf("trusted devices after deactivate = %d err=%v", trustedDevices, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, aliceID).Scan(&passkeys); err != nil || passkeys != 1 {
			t.Fatalf("passkeys after deactivate = %d err=%v", passkeys, err)
		}
		var sessions, messages, attachments, events int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, aliceID).Scan(&sessions); err != nil || sessions != 0 {
			t.Fatalf("sessions after deactivate = %d err=%v", sessions, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_messages WHERE user_id = $1`, aliceID).Scan(&messages); err != nil || messages != 1 {
			t.Fatalf("mail messages after deactivate = %d err=%v", messages, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE user_id = $1`, aliceID).Scan(&attachments); err != nil || attachments != 1 {
			t.Fatalf("attachments after deactivate = %d err=%v", attachments, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendar_events WHERE user_id = $1`, aliceID).Scan(&events); err != nil || events != 1 {
			t.Fatalf("calendar events after deactivate = %d err=%v", events, err)
		}

		recorder = put(fmt.Sprintf(`{"userName":%q,"active":true}`, emailA))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT reactivate status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &resource)
		if resource["active"] != true {
			t.Fatalf("PUT reactivate active = %#v", resource["active"])
		}
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, aliceID).Scan(&mailboxActive); err != nil || !mailboxActive {
			t.Fatalf("PUT reactivate mailbox active = %v err=%v", mailboxActive, err)
		}
	})

	t.Run("PATCH add replace remove and invalid paths", func(t *testing.T) {
		patch := func(body string) *httptest.ResponseRecorder {
			return scimRequest(tokenA, http.MethodPatch, "/scim/v2/Users/"+aliceID, "", body)
		}
		recorder := patch(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"name.givenName","value":"PatchGiven"}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH givenName status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if user := loadUser(t, aliceID); user.GivenName != "PatchGiven" {
			t.Fatalf("PATCH givenName = %q", user.GivenName)
		}
		recorder = patch(`{"Operations":[{"op":"add","path":"externalId","value":"ext-patched"}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH add externalId status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if user := loadUser(t, aliceID); user.ExternalID == nil || *user.ExternalID != "ext-patched" {
			t.Fatalf("PATCH externalId = %#v", user.ExternalID)
		}
		recorder = patch(`{"Operations":[{"op":"remove","path":"externalId"}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH remove externalId status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if user := loadUser(t, aliceID); user.ExternalID != nil {
			t.Fatalf("PATCH externalId not cleared: %#v", user.ExternalID)
		}
		recorder = patch(`{"Operations":[{"op":"replace","value":{"active":false,"name":{"formatted":"Patched Name","givenName":"Patched","familyName":"Name"}}}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH pathless status = %d: %s", recorder.Code, recorder.Body.String())
		}
		user := loadUser(t, aliceID)
		if user.Status != "suspended" || user.Name != "Patched Name" || user.GivenName != "Patched" {
			t.Fatalf("PATCH pathless result = %#v", user)
		}
		recorder = patch(`{"Operations":[{"op":"replace","path":"active","value":true}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH reactivate status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if user := loadUser(t, aliceID); user.Status != "active" {
			t.Fatalf("PATCH reactivate status = %q", user.Status)
		}

		assertSCIMError(t, patch(`{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`), http.StatusBadRequest, "invalidPath")
		assertSCIMError(t, patch(`{"Operations":[{"op":"replace","path":"password","value":"Sup3rSecret!"}]}`), http.StatusBadRequest, "invalidPath")
		assertSCIMError(t, patch(fmt.Sprintf(`{"Operations":[{"op":"replace","path":"userName","value":%q}]}`, "changed@"+domainA)), http.StatusConflict, "mutability")
		assertSCIMError(t, patch(`{"Operations":[]}`), http.StatusBadRequest, "invalidValue")
	})

	t.Run("DELETE deprovisions and preserves all mail data", func(t *testing.T) {
		seedSession(t, tenantA, bobID)
		seedSession(t, tenantA, bobID)
		seedMail(t, tenantA, bobID)
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth_challenges (id, user_id, tenant_id, remember, expires_at)
			VALUES ($1, $2, $3, true, now() + interval '10 minutes')
		`, randomToken(16), bobID, tenantA); err != nil {
			t.Fatalf("seed auth challenge: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, expires_at)
			VALUES ($1, $2, $3, $3, $4, now() + interval '1 hour')
		`, hashToken(randomToken(16)), bobID, tenantA, randomToken(16)); err != nil {
			t.Fatalf("seed admin session: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO trusted_devices (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '30 days')
		`, bobID, hashToken(randomToken(16))); err != nil {
			t.Fatalf("seed trusted device: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO webauthn_credentials (user_id, credential_id, public_key) VALUES ($1, $2, $3)
		`, bobID, []byte("cred-"+randomToken(8)), []byte("public-key")); err != nil {
			t.Fatalf("seed passkey: %v", err)
		}
		recorder := scimRequest(tokenA, http.MethodDelete, "/scim/v2/Users/"+bobID, "", "")
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("DELETE status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("DELETE body = %q", recorder.Body.String())
		}
		user := loadUser(t, bobID)
		if user.Status != "suspended" {
			t.Fatalf("DELETE status = %q", user.Status)
		}
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, bobID).Scan(&mailboxActive); err != nil || mailboxActive {
			t.Fatalf("DELETE mailbox active = %v err=%v", mailboxActive, err)
		}
		var sessions, messages, attachments, events, calendars int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, bobID).Scan(&sessions); err != nil || sessions != 0 {
			t.Fatalf("DELETE sessions = %d err=%v", sessions, err)
		}
		var authChallenges, adminSessions, trustedDevices, passkeys int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_challenges WHERE user_id = $1`, bobID).Scan(&authChallenges); err != nil || authChallenges != 0 {
			t.Fatalf("DELETE auth challenges = %d err=%v", authChallenges, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_sessions WHERE user_id = $1`, bobID).Scan(&adminSessions); err != nil || adminSessions != 0 {
			t.Fatalf("DELETE admin sessions = %d err=%v", adminSessions, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM trusted_devices WHERE user_id = $1`, bobID).Scan(&trustedDevices); err != nil || trustedDevices != 0 {
			t.Fatalf("DELETE trusted devices = %d err=%v", trustedDevices, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, bobID).Scan(&passkeys); err != nil || passkeys != 1 {
			t.Fatalf("DELETE passkeys = %d err=%v", passkeys, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_messages WHERE user_id = $1`, bobID).Scan(&messages); err != nil || messages != 1 {
			t.Fatalf("DELETE mail messages = %d err=%v", messages, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE user_id = $1`, bobID).Scan(&attachments); err != nil || attachments != 1 {
			t.Fatalf("DELETE attachments = %d err=%v", attachments, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendar_events WHERE user_id = $1`, bobID).Scan(&events); err != nil || events != 1 {
			t.Fatalf("DELETE calendar events = %d err=%v", events, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendars WHERE user_id = $1`, bobID).Scan(&calendars); err != nil || calendars == 0 {
			t.Fatalf("DELETE calendars = %d err=%v", calendars, err)
		}

		// A second DELETE is idempotent and still never removes the row.
		if recorder := scimRequest(tokenA, http.MethodDelete, "/scim/v2/Users/"+bobID, "", ""); recorder.Code != http.StatusNoContent {
			t.Fatalf("second DELETE status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if user := loadUser(t, bobID); user.DeletedAt != nil {
			t.Fatal("DELETE soft-deleted the user row")
		}
		recorder = scimRequest(tokenA, http.MethodGet, "/scim/v2/Users/"+bobID, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET after DELETE status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if resource["active"] != false {
			t.Fatalf("GET after DELETE active = %#v", resource["active"])
		}
	})

	t.Run("owner accounts cannot be deactivated through SCIM", func(t *testing.T) {
		recorder := scimRequest(tokenB, http.MethodPut, "/scim/v2/Users/"+ownerB, "", fmt.Sprintf(`{"userName":%q,"active":false}`, ownerBEmail))
		assertSCIMError(t, recorder, http.StatusConflict, "mutability")
		assertSCIMError(t, scimRequest(tokenB, http.MethodDelete, "/scim/v2/Users/"+ownerB, "", ""), http.StatusConflict, "mutability")
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, ownerB).Scan(&status); err != nil || status != "active" {
			t.Fatalf("owner status = %q err=%v", status, err)
		}
	})

	t.Run("system administrator accounts are read-only through SCIM", func(t *testing.T) {
		sessionID := seedSession(t, tenantB, systemAdminB)
		putBody := fmt.Sprintf(`{"userName":%q,"active":false}`, systemAdminEmail)
		assertSCIMError(t, scimRequest(tokenB, http.MethodPut, "/scim/v2/Users/"+systemAdminB, "", putBody), http.StatusForbidden, "mutability")
		assertSCIMError(t, scimRequest(tokenB, http.MethodPatch, "/scim/v2/Users/"+systemAdminB, "", `{"Operations":[{"op":"replace","path":"name.givenName","value":"Hacked"}]}`), http.StatusForbidden, "mutability")
		assertSCIMError(t, scimRequest(tokenB, http.MethodPatch, "/scim/v2/Users/"+systemAdminB, "", `{"Operations":[{"op":"replace","path":"active","value":true}]}`), http.StatusForbidden, "mutability")
		assertSCIMError(t, scimRequest(tokenB, http.MethodDelete, "/scim/v2/Users/"+systemAdminB, "", ""), http.StatusForbidden, "mutability")

		var status, name, role, roleSource string
		var externalID *string
		var isSystemAdmin bool
		if err := pool.QueryRow(ctx, `
			SELECT status, name, role, role_source, scim_external_id, is_system_admin
			FROM users WHERE id = $1
		`, systemAdminB).Scan(&status, &name, &role, &roleSource, &externalID, &isSystemAdmin); err != nil {
			t.Fatalf("load system admin: %v", err)
		}
		if status != "active" || name != "System Admin B" || role != "member" || roleSource != "manual" || !isSystemAdmin {
			t.Fatalf("system admin mutated: status=%q name=%q role=%q source=%q admin=%v", status, name, role, roleSource, isSystemAdmin)
		}
		if externalID == nil || *externalID != "sys-ext" {
			t.Fatalf("system admin externalId changed: %#v", externalID)
		}
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, systemAdminB).Scan(&mailboxActive); err != nil || !mailboxActive {
			t.Fatalf("system admin mailbox changed: active=%v err=%v", mailboxActive, err)
		}
		var sessions int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2`, systemAdminB, sessionID).Scan(&sessions); err != nil || sessions != 1 {
			t.Fatalf("system admin session revoked: count=%d err=%v", sessions, err)
		}

		// A local group→role mapping is authoritative for mapped users, but it
		// must still skip platform administrators.
		recorder := adminRequest(adminCookie, adminCSRF, http.MethodPatch, "/admin/api/groups/"+systemAdminGroup, `{"scimRole":"admin"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("set system admin group mapping status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if err := pool.QueryRow(ctx, `SELECT role, role_source FROM users WHERE id = $1`, systemAdminB).Scan(&role, &roleSource); err != nil {
			t.Fatalf("reload system admin role: %v", err)
		}
		if role != "member" || roleSource != "manual" {
			t.Fatalf("group mapping changed system admin role: %q/%q", role, roleSource)
		}
	})

	t.Run("body cap and JSON-only enforcement", func(t *testing.T) {
		oversized := `{"userName":"big@` + domainA + `","pad":"` + strings.Repeat("a", int(scimUserBodyLimit)) + `"}`
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "", oversized), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", "text/plain", `{"userName":"x@`+domainA+`"}`), http.StatusBadRequest, "invalidValue")
	})

	t.Run("audit records fixed actions without secrets", func(t *testing.T) {
		// Audit writes are asynchronous; wait for the full expected set rather
		// than for the first row of each action.
		required := map[string]int{
			"scim.user_created":     3,
			"scim.user_updated":     1,
			"scim.user_deactivated": 2,
			"scim.user_reactivated": 2,
			"scim.user_deleted":     2,
		}
		type auditRow struct {
			action, actor, target, detail string
		}
		deadline := time.Now().Add(10 * time.Second)
		var rows []auditRow
		var counts map[string]int
		for {
			rows = nil
			results, err := pool.Query(ctx, `
				SELECT action, actor, target, detail FROM audit_log
				WHERE tenant_id IN ($1, $2) AND action LIKE 'scim.%'
				ORDER BY id
			`, tenantA, tenantB)
			if err != nil {
				t.Fatalf("load audit rows: %v", err)
			}
			for results.Next() {
				var row auditRow
				if err := results.Scan(&row.action, &row.actor, &row.target, &row.detail); err != nil {
					results.Close()
					t.Fatalf("scan audit row: %v", err)
				}
				rows = append(rows, row)
			}
			err = results.Err()
			results.Close()
			if err != nil {
				t.Fatalf("iterate audit rows: %v", err)
			}
			counts = map[string]int{}
			for _, row := range rows {
				counts[row.action]++
			}
			complete := true
			for action, minimum := range required {
				if counts[action] < minimum {
					complete = false
					break
				}
			}
			if complete {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("audit actions incomplete; counts %#v", counts)
			}
			time.Sleep(50 * time.Millisecond)
		}
		deactivated, deleted := 0, 0
		for _, row := range rows {
			if !strings.HasPrefix(row.actor, "SCIM token ") {
				t.Fatalf("audit actor = %q", row.actor)
			}
			if !strings.Contains(row.target, "@") {
				t.Fatalf("audit target is not an email: %q", row.target)
			}
			for _, secret := range []string{tokenA, tokenB, hashToken(tokenA), hashToken(tokenB), "Sup3rSecret!"} {
				if strings.Contains(row.actor+row.target+row.detail, secret) {
					t.Fatal("audit row contains credential material")
				}
			}
			switch row.action {
			case "scim.user_deactivated":
				deactivated++
				if row.detail != "sessions revoked; mailbox preserved" {
					t.Fatalf("deactivated detail = %q", row.detail)
				}
			case "scim.user_deleted":
				deleted++
				if row.detail != "deactivated; sessions revoked; mailbox preserved" {
					t.Fatalf("deleted detail = %q", row.detail)
				}
			}
		}
		if deactivated < 2 || deleted < 1 {
			t.Fatalf("deactivated=%d deleted=%d", deactivated, deleted)
		}
	})
}

// TestLicenseSeatEnforcementIntegration drives the real PostgreSQL-backed
// admin and SCIM provisioning paths against a licensed seat limit. Run with:
//
//	A8M_SCIM_TEST_DSN='postgres://...' go test ./internal/httpapi -run LicenseSeatEnforcement -count=1
func TestLicenseSeatEnforcementIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the PostgreSQL license seat enforcement integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users'
		  AND column_name IN ('given_name', 'family_name', 'scim_external_id', 'role_source')
	`).Scan(&columns); err != nil || columns != 4 {
		t.Fatalf("users SCIM columns missing; apply migration 0061 first: count=%d err=%v", columns, err)
	}

	slug := fmt.Sprintf("seat-enforce-%d", time.Now().UnixNano())
	domainA := slug + "-a.example.test"
	domainB := slug + "-b.example.test"
	createTenant := func(label, domain string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text
		`, slug+"-"+label, "Seat enforcement integration "+label).Scan(&id); err != nil {
			t.Fatalf("create tenant %s: %v", label, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO mail_domains (tenant_id, domain, status, verified_at)
			VALUES ($1, $2, 'verified', now())
		`, id, domain); err != nil {
			t.Fatalf("create domain %s: %v", domain, err)
		}
		return id
	}
	tenantA := createTenant("a", domainA)
	tenantB := createTenant("b", domainB)
	createToken := func(tenantID, name string) string {
		raw := scimTokenPrefix + randomToken(32)
		if _, err := pool.Exec(ctx, `
			INSERT INTO scim_tokens (tenant_id, name, token_hash, token_prefix)
			VALUES ($1, $2, $3, $4)
		`, tenantID, name, hashToken(raw), raw[:scimTokenDisplayLength]); err != nil {
			t.Fatalf("create SCIM token %s: %v", name, err)
		}
		return raw
	}
	tokenA := createToken(tenantA, "Seat Entra A")
	tokenB := createToken(tenantB, "Seat Okta B")

	// The operator is a platform administrator, so it never consumes a seat and
	// still has users.create/users.update/users.delete permissions.
	var operatorID string
	operatorEmail := "seat-operator@" + domainA
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role, status, is_system_admin)
		VALUES ($1, $2, 'Seat Operator', 'owner', 'active', true) RETURNING id::text
	`, tenantA, operatorEmail).Scan(&operatorID); err != nil {
		t.Fatalf("create operator: %v", err)
	}
	adminCookie := randomToken(32)
	adminCSRF := randomToken(24)
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, expires_at)
		VALUES ($1, $2, $3, $3, $4, now() + interval '1 hour')
	`, hashToken(adminCookie), operatorID, tenantA, adminCSRF); err != nil {
		t.Fatalf("create admin session: %v", err)
	}

	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM audit_log WHERE tenant_id IN ($1, $2)`, tenantA, tenantB)
		_, _ = pool.Exec(cleanup, `DELETE FROM admin_audit_log WHERE tenant_id IN ($1, $2)`, tenantA, tenantB)
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	}()

	server, clock := newSCIMTestServer(t, "https://mail.example.test", "scim")
	server.store = &store.Store{DB: pool}
	server.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	license := server.license
	start := clock.Now()
	license.Seats = 2
	license.Tenant = ""
	license.ExpiresAt = start.Add(24 * time.Hour)
	license.GraceDays = 7

	adminRequest := func(method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: adminCookie})
		if method != http.MethodGet {
			request.Header.Set("X-CSRF-Token", adminCSRF)
		}
		recorder := httptest.NewRecorder()
		server.AdminHandler().ServeHTTP(recorder, request)
		return recorder
	}
	scimRequest := func(rawToken, method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/scim+json")
		}
		if rawToken != "" {
			request.Header.Set("Authorization", "Bearer "+rawToken)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	decode := func(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
		}
	}
	adminCreate := func(t *testing.T, email, status string) *httptest.ResponseRecorder {
		t.Helper()
		return adminRequest(http.MethodPost, "/admin/api/users",
			fmt.Sprintf(`{"name":%q,"email":%q,"status":%q}`, "Seat "+email, email, status))
	}
	adminCreateID := func(t *testing.T, email, status string) string {
		t.Helper()
		recorder := adminCreate(t, email, status)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create %s (%s) status = %d: %s", email, status, recorder.Code, recorder.Body.String())
		}
		var payload struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		}
		decode(t, recorder, &payload)
		if payload.User.ID == "" {
			t.Fatalf("created user %s has no id: %s", email, recorder.Body.String())
		}
		return payload.User.ID
	}
	assertSeatLimit := func(t *testing.T, recorder *httptest.ResponseRecorder) {
		t.Helper()
		if recorder.Code != http.StatusConflict {
			t.Fatalf("seat-limit status = %d, want 409: %s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		decode(t, recorder, &payload)
		if payload.Error.Code != "SEAT_LIMIT" || payload.Error.Message != seatLimitMessage {
			t.Fatalf("seat-limit error = %#v", payload.Error)
		}
	}
	scimBody := func(email string, active bool) string {
		return fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":%q,"name":{"formatted":"Seat User","givenName":"Seat","familyName":"User"},"emails":[{"value":%q,"type":"work","primary":true}],"active":%t}`, email, email, active)
	}
	assertSCIMSeatLimit := func(t *testing.T, recorder *httptest.ResponseRecorder) {
		t.Helper()
		if recorder.Code != http.StatusConflict {
			t.Fatalf("SCIM seat-limit status = %d, want 409: %s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			ScimType string `json:"scimType"`
			Detail   string `json:"detail"`
		}
		decode(t, recorder, &payload)
		if payload.ScimType != "invalidValue" || payload.Detail != scimSeatLimitDetail {
			t.Fatalf("SCIM seat-limit error = %#v", payload)
		}
	}
	activeCount := func(t *testing.T, tenantID string) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM users
			WHERE tenant_id = $1 AND status = 'active' AND deleted_at IS NULL AND is_system_admin = false
		`, tenantID).Scan(&count); err != nil {
			t.Fatalf("count active seats: %v", err)
		}
		return count
	}
	userExists := func(t *testing.T, tenantID, email string) bool {
		t.Helper()
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE tenant_id = $1 AND email = $2)`, tenantID, email).Scan(&exists); err != nil {
			t.Fatalf("check user %s: %v", email, err)
		}
		return exists
	}
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			UPDATE users SET status = 'suspended', deleted_at = NULL
			WHERE tenant_id IN ($1, $2) AND is_system_admin = false
		`, tenantA, tenantB); err != nil {
			t.Fatalf("reset seat users: %v", err)
		}
		clock.Set(start)
		server.license = license
		license.Seats = 2
		license.Tenant = ""
		license.ExpiresAt = start.Add(24 * time.Hour)
		license.GraceDays = 7
		server.licenseConfigured = false
	}

	t.Run("admin create blocks the third active user without side effects", func(t *testing.T) {
		reset(t)
		first := "seat-one@" + domainA
		second := "seat-two@" + domainA
		third := "seat-three@" + domainA
		adminCreateID(t, first, "active")
		adminCreateID(t, second, "active")
		recorder := adminCreate(t, third, "active")
		assertSeatLimit(t, recorder)
		if got := activeCount(t, tenantA); got != 2 {
			t.Fatalf("active seats = %d, want 2", got)
		}
		if userExists(t, tenantA, third) {
			t.Fatal("rejected create left a user row")
		}
		var mailboxes int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM mailboxes mb JOIN user_aliases ua ON ua.user_id = mb.user_id
			WHERE mb.tenant_id = $1 AND ua.email = $2
		`, tenantA, third).Scan(&mailboxes); err != nil || mailboxes != 0 {
			t.Fatalf("rejected create left mailboxes: count=%d err=%v", mailboxes, err)
		}

		var target, detail string
		var metadata []byte
		if err := pool.QueryRow(ctx, `
			SELECT target, detail, metadata FROM admin_audit_log
			WHERE tenant_id = $1 AND action = 'license.seat_limit_blocked'
			ORDER BY id DESC LIMIT 1
		`, tenantA).Scan(&target, &detail, &metadata); err != nil {
			t.Fatalf("load admin seat-limit audit: %v", err)
		}
		if target != third {
			t.Fatalf("audit target = %q, want %q", target, third)
		}
		var metadataFields map[string]any
		if err := json.Unmarshal(metadata, &metadataFields); err != nil {
			t.Fatalf("decode audit metadata %s: %v", metadata, err)
		}
		if metadataFields["tenantId"] != tenantA || metadataFields["limit"] != float64(2) || metadataFields["used"] != float64(2) {
			t.Fatalf("audit metadata = %#v, want tenant=%s limit=2 used=2", metadataFields, tenantA)
		}
		encoded := string(metadata)
		for _, secret := range []string{"A8M1.", tokenA, tokenB, adminCookie, adminCSRF} {
			if strings.Contains(target+detail+encoded, secret) {
				t.Fatal("seat-limit audit contains credential or license material")
			}
		}

		// Invited users do not consume a seat, and neither single-user update nor
		// bulk activation may activate one at the limit.
		invited := "seat-invited@" + domainA
		invitedID := adminCreateID(t, invited, "invited")
		if got := activeCount(t, tenantA); got != 2 {
			t.Fatalf("invited user consumed a seat: active=%d", got)
		}
		patch := adminRequest(http.MethodPatch, "/admin/api/users/"+invitedID, `{"status":"active"}`)
		assertSeatLimit(t, patch)
		var status string
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, invitedID).Scan(&status); err != nil || status != "invited" {
			t.Fatalf("invited user status after blocked update = %q err=%v", status, err)
		}
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, invitedID).Scan(&mailboxActive); err != nil || mailboxActive {
			t.Fatalf("invited mailbox after blocked update = %v err=%v", mailboxActive, err)
		}
		bulk := adminRequest(http.MethodPost, "/admin/api/users/bulk",
			fmt.Sprintf(`{"action":"activate","ids":[%q]}`, invitedID))
		assertSeatLimit(t, bulk)
		if err := pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, invitedID).Scan(&status); err != nil || status != "invited" {
			t.Fatalf("invited user status after blocked bulk = %q err=%v", status, err)
		}
	})

	t.Run("delete frees a seat and restore is enforced", func(t *testing.T) {
		reset(t)
		first := "seat-restore-one@" + domainA
		second := "seat-restore-two@" + domainA
		firstID := adminCreateID(t, first, "active")
		adminCreateID(t, second, "active")

		if recorder := adminRequest(http.MethodDelete, "/admin/api/users/"+firstID, ""); recorder.Code != http.StatusOK {
			t.Fatalf("delete status = %d: %s", recorder.Code, recorder.Body.String())
		}
		replacement := "seat-restore-replacement@" + domainA
		adminCreateID(t, replacement, "active")
		assertSeatLimit(t, adminRequest(http.MethodPost, "/admin/api/users/"+firstID+"/restore", ""))

		var deletedAt *time.Time
		var status string
		if err := pool.QueryRow(ctx, `SELECT deleted_at, status FROM users WHERE id = $1`, firstID).Scan(&deletedAt, &status); err != nil {
			t.Fatalf("reload closed user: %v", err)
		}
		if deletedAt == nil || status != "suspended" {
			t.Fatalf("blocked restore changed the user: deletedAt=%v status=%q", deletedAt, status)
		}

		var replacementID string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE tenant_id = $1 AND email = $2`, tenantA, replacement).Scan(&replacementID); err != nil {
			t.Fatalf("load replacement: %v", err)
		}
		if recorder := adminRequest(http.MethodDelete, "/admin/api/users/"+replacementID, ""); recorder.Code != http.StatusOK {
			t.Fatalf("second delete status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if recorder := adminRequest(http.MethodPost, "/admin/api/users/"+firstID+"/restore", ""); recorder.Code != http.StatusOK {
			t.Fatalf("restore after freeing a seat status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT deleted_at, status FROM users WHERE id = $1`, firstID).Scan(&deletedAt, &status); err != nil || deletedAt != nil || status != "active" {
			t.Fatalf("restored user = deletedAt %v status %q err %v", deletedAt, status, err)
		}
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, firstID).Scan(&mailboxActive); err != nil || !mailboxActive {
			t.Fatalf("restored mailbox active = %v err=%v", mailboxActive, err)
		}
		if got := activeCount(t, tenantA); got != 2 {
			t.Fatalf("active seats after restore = %d, want 2", got)
		}
	})

	t.Run("system administrators are excluded", func(t *testing.T) {
		reset(t)
		adminCreateID(t, "seat-sys-one@"+domainA, "active")
		adminCreateID(t, "seat-sys-two@"+domainA, "active")
		var activeSystemAdmin string
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, name, role, status, is_system_admin)
			VALUES ($1, $2, 'Seat Admin', 'member', 'active', true) RETURNING id::text
		`, tenantA, "seat-system-admin@"+domainA).Scan(&activeSystemAdmin); err != nil {
			t.Fatalf("create active system admin: %v", err)
		}
		if got := activeCount(t, tenantA); got != 2 {
			t.Fatalf("system admin consumed a seat: active=%d", got)
		}
		assertSeatLimit(t, adminCreate(t, "seat-sys-three@"+domainA, "active"))

		// A suspended platform administrator can be activated at the limit
		// because it never occupies a seat.
		var invitedSystemAdmin string
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, name, role, status, is_system_admin)
			VALUES ($1, $2, 'Seat Admin Two', 'member', 'invited', true) RETURNING id::text
		`, tenantA, "seat-system-admin-two@"+domainA).Scan(&invitedSystemAdmin); err != nil {
			t.Fatalf("create invited system admin: %v", err)
		}
		recorder := adminRequest(http.MethodPatch, "/admin/api/users/"+invitedSystemAdmin, `{"status":"active"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("system admin activation status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, invitedSystemAdmin).Scan(&status); err != nil || status != "active" {
			t.Fatalf("system admin status = %q err=%v", status, err)
		}
	})

	t.Run("tenant claim scopes enforcement and snapshot usage", func(t *testing.T) {
		reset(t)
		license.Tenant = tenantA
		adminCreateID(t, "seat-tenant-one@"+domainA, "active")
		adminCreateID(t, "seat-tenant-two@"+domainA, "active")
		assertSeatLimit(t, adminCreate(t, "seat-tenant-three@"+domainA, "active"))

		// Another tenant is not limited by this tenant's license claim.
		other := "seat-other-tenant@" + domainB
		recorder := scimRequest(tokenB, http.MethodPost, "/scim/v2/Users", scimBody(other, true))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("other-tenant SCIM create status = %d: %s", recorder.Code, recorder.Body.String())
		}
		snapshot := server.licenseSnapshot(ctx)
		if snapshot["usedSeats"] != 2 || snapshot["seatLimit"] != 2 || snapshot["seats"] != 2 {
			t.Fatalf("tenant-scoped snapshot seats = %#v", snapshot)
		}
		if got := activeCount(t, tenantB); got != 1 {
			t.Fatalf("other tenant active seats = %d, want 1", got)
		}

		// A global license counts every tenant, so the other tenant is limited
		// as soon as the global total reaches the cap.
		license.Tenant = ""
		snapshot = server.licenseSnapshot(ctx)
		if snapshot["usedSeats"] != 3 {
			t.Fatalf("global snapshot usedSeats = %#v, want 3", snapshot["usedSeats"])
		}
		assertSCIMSeatLimit(t, scimRequest(tokenB, http.MethodPost, "/scim/v2/Users", scimBody("seat-other-two@"+domainB, true)))
	})

	t.Run("SCIM create and reactivation are blocked at the limit", func(t *testing.T) {
		reset(t)
		first := "seat-scim-one@" + domainA
		second := "seat-scim-two@" + domainA
		if recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", scimBody(first, true)); recorder.Code != http.StatusCreated {
			t.Fatalf("SCIM create one status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", scimBody(second, true)); recorder.Code != http.StatusCreated {
			t.Fatalf("SCIM create two status = %d: %s", recorder.Code, recorder.Body.String())
		}
		third := "seat-scim-three@" + domainA
		assertSCIMSeatLimit(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", scimBody(third, true)))
		if userExists(t, tenantA, third) {
			t.Fatal("rejected SCIM create left a user row")
		}
		var mailboxes int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM mailboxes mb JOIN user_aliases ua ON ua.user_id = mb.user_id
			WHERE mb.tenant_id = $1 AND ua.email = $2
		`, tenantA, third).Scan(&mailboxes); err != nil || mailboxes != 0 {
			t.Fatalf("rejected SCIM create left mailboxes: count=%d err=%v", mailboxes, err)
		}

		suspended := "seat-scim-suspended@" + domainA
		recorder := scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", scimBody(suspended, false))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("suspended SCIM create status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource struct {
			ID string `json:"id"`
		}
		decode(t, recorder, &resource)
		if resource.ID == "" {
			t.Fatal("suspended SCIM create returned no id")
		}
		assertSCIMSeatLimit(t, scimRequest(tokenA, http.MethodPatch, "/scim/v2/Users/"+resource.ID,
			`{"Operations":[{"op":"replace","path":"active","value":true}]}`))
		assertSCIMSeatLimit(t, scimRequest(tokenA, http.MethodPut, "/scim/v2/Users/"+resource.ID,
			fmt.Sprintf(`{"userName":%q,"emails":[{"value":%q}],"active":true}`, suspended, suspended)))
		var status string
		var mailboxActive bool
		if err := pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, resource.ID).Scan(&status); err != nil || status != "suspended" {
			t.Fatalf("SCIM reactivation changed status = %q err=%v", status, err)
		}
		if err := pool.QueryRow(ctx, `SELECT active FROM mailboxes WHERE user_id = $1`, resource.ID).Scan(&mailboxActive); err != nil || mailboxActive {
			t.Fatalf("SCIM reactivation changed mailbox = %v err=%v", mailboxActive, err)
		}

		// SCIM audit writes are asynchronous; wait for all three rejections.
		deadline := time.Now().Add(10 * time.Second)
		for {
			var count int
			if err := pool.QueryRow(ctx, `
				SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND action = 'license.seat_limit_blocked'
			`, tenantA).Scan(&count); err != nil {
				t.Fatalf("count SCIM seat-limit audit: %v", err)
			}
			if count >= 3 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("SCIM seat-limit audit rows = %d, want 3", count)
			}
			time.Sleep(25 * time.Millisecond)
		}
		rows, err := pool.Query(ctx, `
			SELECT actor, target, detail FROM audit_log
			WHERE tenant_id = $1 AND action = 'license.seat_limit_blocked' ORDER BY id
		`, tenantA)
		if err != nil {
			t.Fatalf("load SCIM seat-limit audit: %v", err)
		}
		defer rows.Close()
		seen := 0
		for rows.Next() {
			var actor, target, detail string
			if err := rows.Scan(&actor, &target, &detail); err != nil {
				t.Fatalf("scan SCIM seat-limit audit: %v", err)
			}
			seen++
			if !strings.HasPrefix(actor, "SCIM token ") {
				t.Fatalf("SCIM audit actor = %q", actor)
			}
			for _, fragment := range []string{"tenant " + tenantA, "limit 2", "used 2"} {
				if !strings.Contains(detail, fragment) {
					t.Fatalf("SCIM audit detail %q missing %q", detail, fragment)
				}
			}
			for _, secret := range []string{"A8M1.", tokenA, tokenB, hashToken(tokenA)} {
				if strings.Contains(actor+target+detail, secret) {
					t.Fatal("SCIM seat-limit audit contains credential or license material")
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate SCIM seat-limit audit: %v", err)
		}
		if seen < 3 {
			t.Fatalf("SCIM seat-limit audit rows = %d, want at least 3", seen)
		}
	})

	t.Run("concurrent SCIM creates never exceed the limit", func(t *testing.T) {
		reset(t)
		const attempts = 6
		startGate := make(chan struct{})
		results := make([]*httptest.ResponseRecorder, attempts)
		var wait sync.WaitGroup
		for index := 0; index < attempts; index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				<-startGate
				email := fmt.Sprintf("seat-race-%d@%s", index, domainA)
				results[index] = scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", scimBody(email, true))
			}(index)
		}
		close(startGate)
		wait.Wait()

		created, blocked := 0, 0
		for _, recorder := range results {
			switch recorder.Code {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				blocked++
				assertSCIMSeatLimit(t, recorder)
			default:
				t.Fatalf("concurrent create status = %d: %s", recorder.Code, recorder.Body.String())
			}
		}
		if created != 2 || blocked != attempts-2 {
			t.Fatalf("concurrent creates: created=%d blocked=%d, want 2 and %d", created, blocked, attempts-2)
		}
		if got := activeCount(t, tenantA); got != 2 {
			t.Fatalf("active seats after concurrent creates = %d, want 2", got)
		}
		var raceUsers int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM users WHERE tenant_id = $1 AND email LIKE $2
		`, tenantA, "seat-race-%@"+domainA).Scan(&raceUsers); err != nil || raceUsers != 2 {
			t.Fatalf("race users = %d err=%v, want 2", raceUsers, err)
		}
	})

	t.Run("grace enforces while expired, unlicensed and invalid deployments allow creation", func(t *testing.T) {
		reset(t)
		adminCreateID(t, "seat-lifecycle-one@"+domainA, "active")
		adminCreateID(t, "seat-lifecycle-two@"+domainA, "active")

		clock.Set(start.Add(25 * time.Hour)) // past expiry, inside grace
		assertSeatLimit(t, adminCreate(t, "seat-grace@"+domainA, "active"))
		assertSCIMSeatLimit(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Users", scimBody("seat-grace-scim@"+domainA, true)))

		clock.Set(start.Add(24*time.Hour + 8*24*time.Hour)) // past expiry and grace
		adminCreateID(t, "seat-expired@"+domainA, "active")
		if server.seatEnforced() {
			t.Fatal("expired license still enforced seats")
		}

		server.license = nil // unlicensed
		adminCreateID(t, "seat-unlicensed@"+domainA, "active")
		server.licenseConfigured = true // configured but invalid
		adminCreateID(t, "seat-invalid@"+domainA, "active")

		reset(t)
	})

	t.Run("bulk import stops at the seat limit and audits", func(t *testing.T) {
		reset(t)
		adminCreateID(t, "seat-import-one@"+domainA, "active")
		adminCreateID(t, "seat-import-two@"+domainA, "active")
		imported := "seat-import-blocked@" + domainA
		job := userImportJobRecord{ID: "uimp_seat_test", TenantID: tenantA, CreatedBy: "Seat Operator", CreatedByID: operatorID}
		web, mail, userID, err := server.provisionImportedUser(ctx, job, "uimpi_seat_test", imported, "Import User", "member", 16*1024*1024*1024)
		if !errors.Is(err, errSeatLimit) {
			t.Fatalf("provisionImportedUser error = %v, want errSeatLimit", err)
		}
		if web != "" || mail != "" || userID != "" {
			t.Fatalf("blocked import returned credentials/user: %q %q %q", web, mail, userID)
		}
		if userExists(t, tenantA, imported) {
			t.Fatal("blocked import left a user row")
		}
		var target string
		var metadata []byte
		if err := pool.QueryRow(ctx, `
			SELECT target, metadata FROM admin_audit_log
			WHERE tenant_id = $1 AND action = 'license.seat_limit_blocked'
			ORDER BY id DESC LIMIT 1
		`, tenantA).Scan(&target, &metadata); err != nil {
			t.Fatalf("load import seat-limit audit: %v", err)
		}
		if target != imported {
			t.Fatalf("import audit target = %q, want %q", target, imported)
		}
		var metadataFields map[string]any
		if err := json.Unmarshal(metadata, &metadataFields); err != nil {
			t.Fatalf("decode import audit metadata %s: %v", metadata, err)
		}
		if metadataFields["tenantId"] != tenantA || metadataFields["limit"] != float64(2) || metadataFields["used"] != float64(2) {
			t.Fatalf("import audit metadata = %#v, want tenant=%s limit=2 used=2", metadataFields, tenantA)
		}

		// Freeing a seat lets the next imported row provision normally.
		var firstID string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE tenant_id = $1 AND email = $2`, tenantA, "seat-import-one@"+domainA).Scan(&firstID); err != nil {
			t.Fatalf("load import user: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE id = $1`, firstID); err != nil {
			t.Fatalf("free import seat: %v", err)
		}
		if _, _, userID, err = server.provisionImportedUser(ctx, job, "uimpi_seat_test_2", "seat-import-allowed@"+domainA, "Import User Two", "member", 16*1024*1024*1024); err != nil || userID == "" {
			t.Fatalf("import after freeing a seat = %q, %v", userID, err)
		}
	})
}
