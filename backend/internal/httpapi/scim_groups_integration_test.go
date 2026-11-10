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
//	A8M_SCIM_TEST_DSN='postgres://...' go test ./internal/httpapi -run SCIMGroupsIntegration -count=1 -v
//
// The test owns three tenants (one without a domain), seeds real users and real
// admin sessions, and drives both the mounted SCIM routes and the admin API.
// Nothing is mocked.
func TestSCIMGroupsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the PostgreSQL SCIM Groups integration test")
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
		WHERE table_schema = 'public' AND table_name = 'groups'
		  AND column_name IN ('scim_external_id', 'scim_role')
	`).Scan(&columns); err != nil || columns != 2 {
		t.Fatalf("groups SCIM columns missing; apply migration 0061 first: count=%d err=%v", columns, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'role_source'
	`).Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("users.role_source missing; apply migration 0061 first: count=%d err=%v", columns, err)
	}

	slug := fmt.Sprintf("scim-groups-%d", time.Now().UnixNano())
	createTenant := func(label string, withDomain bool) (tenantID, domain string) {
		if err := pool.QueryRow(ctx, `
			INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text
		`, slug+"-"+label, "SCIM Groups integration "+label).Scan(&tenantID); err != nil {
			t.Fatalf("create tenant %s: %v", label, err)
		}
		if !withDomain {
			return tenantID, ""
		}
		domain = slug + "-" + label + ".example.test"
		if _, err := pool.Exec(ctx, `
			INSERT INTO mail_domains (tenant_id, domain, status, is_primary, verified_at)
			VALUES ($1, $2, 'verified', true, now())
		`, tenantID, domain); err != nil {
			t.Fatalf("create domain %s: %v", domain, err)
		}
		return tenantID, domain
	}
	tenantA, _ := createTenant("a", true)
	tenantB, _ := createTenant("b", true)
	tenantC, _ := createTenant("c", false)
	var primaryDomainA string
	if err := pool.QueryRow(ctx, `
		SELECT domain::text FROM mail_domains WHERE tenant_id = $1 AND is_primary LIMIT 1
	`, tenantA).Scan(&primaryDomainA); err != nil {
		t.Fatalf("load primary domain A: %v", err)
	}
	// A non-primary manual domain must never win over the primary domain.
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_domains (tenant_id, domain, status, is_primary) VALUES ($1, $2, 'manual', false)
	`, tenantA, slug+"-manual.example.test"); err != nil {
		t.Fatalf("create manual domain: %v", err)
	}

	createUser := func(tenantID, label, name, role string) string {
		var id string
		email := fmt.Sprintf("%s-%s@%s", label, slug, "users.example.test")
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, name, role) VALUES ($1, $2, $3, $4) RETURNING id::text
		`, tenantID, email, name, role).Scan(&id); err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		return id
	}
	alice := createUser(tenantA, "alice", "Alice Anderson", "member")
	bob := createUser(tenantA, "bob", "Bob Brown", "member")
	carol := createUser(tenantA, "carol", "Carol Clark", "member")
	dave := createUser(tenantA, "dave", "Dave Davis", "member")
	erin := createUser(tenantA, "erin", "Erin Evans", "member")
	ownerA := createUser(tenantA, "owner", "Olivia Owner", "owner")
	outsider := createUser(tenantB, "outsider", "Oscar Outsider", "member")
	_ = outsider

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
	tokenC := createToken(tenantC, "No Domain C")

	createAdminSession := func(userID, tenantID string) (cookie, csrf string) {
		cookie = randomToken(32)
		csrf = randomToken(24)
		if _, err := pool.Exec(ctx, `
			INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, expires_at)
			VALUES ($1, $2, $3, $3, $4, now() + interval '1 hour')
		`, hashToken(cookie), userID, tenantID, csrf); err != nil {
			t.Fatalf("create admin session: %v", err)
		}
		return cookie, csrf
	}
	adminCookie, adminCSRF := createAdminSession(ownerA, tenantA)

	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM audit_log WHERE tenant_id IN ($1, $2, $3)`, tenantA, tenantB, tenantC)
		_, _ = pool.Exec(cleanup, `DELETE FROM admin_audit_log WHERE tenant_id IN ($1, $2, $3)`, tenantA, tenantB, tenantC)
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id IN ($1, $2, $3)`, tenantA, tenantB, tenantC)
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
	loadGroup := func(t *testing.T, id string) scimGroupRow {
		t.Helper()
		group, err := scanSCIMGroup(pool.QueryRow(ctx, `SELECT `+scimGroupColumns+` FROM groups WHERE id = $1`, id))
		if err != nil {
			t.Fatalf("load group %s: %v", id, err)
		}
		return group
	}
	loadRole := func(t *testing.T, id string) (role, source string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT role, role_source FROM users WHERE id = $1`, id).Scan(&role, &source); err != nil {
			t.Fatalf("load role %s: %v", id, err)
		}
		return role, source
	}
	groupBody := func(displayName, externalID string, members ...string) string {
		body := fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":%q`, displayName)
		if externalID != "" {
			body += fmt.Sprintf(`,"externalId":%q`, externalID)
		}
		if members != nil {
			parts := make([]string, 0, len(members))
			for _, member := range members {
				parts = append(parts, fmt.Sprintf(`{"value":%q}`, member))
			}
			body += `,"members":[` + strings.Join(parts, ",") + `]`
		}
		return body + "}"
	}
	createGroup := func(t *testing.T, rawToken, displayName, externalID string, members ...string) (string, map[string]any) {
		t.Helper()
		recorder := scimRequest(rawToken, http.MethodPost, "/scim/v2/Groups", "", groupBody(displayName, externalID, members...))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create group status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		id, _ := resource["id"].(string)
		if id == "" {
			t.Fatalf("created group has no id: %#v", resource)
		}
		if location := recorder.Header().Get("Location"); location != base+"/Groups/"+id {
			t.Fatalf("Location = %q", location)
		}
		return id, resource
	}
	memberValues := func(t *testing.T, resource map[string]any) []string {
		t.Helper()
		raw, ok := resource["members"].([]any)
		if !ok {
			t.Fatalf("members = %#v", resource["members"])
		}
		values := make([]string, 0, len(raw))
		for _, item := range raw {
			member, _ := item.(map[string]any)
			value, _ := member["value"].(string)
			values = append(values, value)
		}
		return values
	}
	adminSetMapping := func(t *testing.T, groupID, role string) map[string]any {
		t.Helper()
		recorder := adminRequest(adminCookie, adminCSRF, http.MethodPatch, "/admin/api/groups/"+groupID, fmt.Sprintf(`{"scimRole":%q}`, role))
		if recorder.Code != http.StatusOK {
			t.Fatalf("set group mapping %q status = %d: %s", role, recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Group map[string]any `json:"group"`
		}
		decode(t, recorder, &payload)
		return payload.Group
	}
	scimPatch := func(t *testing.T, rawToken, groupID, body string) *httptest.ResponseRecorder {
		t.Helper()
		return scimRequest(rawToken, http.MethodPatch, "/scim/v2/Groups/"+groupID, "", body)
	}

	var engineeringID string

	t.Run("create synthesizes a security group on the primary verified domain", func(t *testing.T) {
		id, resource := createGroup(t, tokenA, "Engineering", "grp-eng", alice)
		engineeringID = id
		if resource["displayName"] != "Engineering" || resource["externalId"] != "grp-eng" {
			t.Fatalf("resource = %#v", resource)
		}
		schemas, _ := resource["schemas"].([]any)
		if len(schemas) != 1 || schemas[0] != scimGroupSchema {
			t.Fatalf("schemas = %#v", resource["schemas"])
		}
		meta, _ := resource["meta"].(map[string]any)
		if meta["resourceType"] != "Group" || meta["location"] != base+"/Groups/"+id || meta["version"] == "" {
			t.Fatalf("meta = %#v", meta)
		}
		members, _ := resource["members"].([]any)
		if len(members) != 1 {
			t.Fatalf("members = %#v", resource["members"])
		}
		member, _ := members[0].(map[string]any)
		if member["value"] != alice || member["display"] != "Alice Anderson" || member["$ref"] != base+"/Users/"+alice {
			t.Fatalf("member = %#v", member)
		}
		if _, leaked := resource["scimRole"]; leaked {
			t.Fatal("local role mapping leaked into SCIM resource")
		}
		group := loadGroup(t, id)
		if group.GroupType != "security" || group.Status != "active" {
			t.Fatalf("group type/status = %q/%q", group.GroupType, group.Status)
		}
		if !strings.HasPrefix(group.Email, "scim-") || !strings.HasSuffix(group.Email, "@"+primaryDomainA) {
			t.Fatalf("synthesized email = %q, want primary domain %q", group.Email, primaryDomainA)
		}
		if group.ExternalID == nil || *group.ExternalID != "grp-eng" {
			t.Fatalf("stored externalId = %#v", group.ExternalID)
		}
		if group.ScimRole != "" {
			t.Fatalf("new SCIM group mapping = %q", group.ScimRole)
		}
	})

	t.Run("create validation, uniqueness and cross-tenant members", func(t *testing.T) {
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", `{}`), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", `{"displayName":"   "}`), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", `{"displayName":"bad\u0000name"}`), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", groupBody("Unknown member", "", "not-a-uuid")), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", groupBody("Cross tenant", "", outsider)), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", groupBody("Duplicate external", "grp-eng")), http.StatusConflict, "uniqueness")
		assertSCIMError(t, scimRequest(tokenC, http.MethodPost, "/scim/v2/Groups", "", groupBody("No domain", "")), http.StatusBadRequest, "invalidValue")
		oversized := `{"displayName":"Big","pad":"` + strings.Repeat("a", int(scimUserBodyLimit)) + `"}`
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "", oversized), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups", "text/plain", groupBody("Plain", "")), http.StatusBadRequest, "invalidValue")
		// A failed create must not leave a row behind.
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM groups WHERE tenant_id = $1`, tenantA).Scan(&count); err != nil || count != 1 {
			t.Fatalf("groups after failed creates = %d err=%v", count, err)
		}
	})

	t.Run("empty externalId is stored as NULL", func(t *testing.T) {
		id, resource := createGroup(t, tokenA, "Research", "")
		if _, ok := resource["externalId"]; ok {
			t.Fatalf("empty externalId rendered: %#v", resource["externalId"])
		}
		if group := loadGroup(t, id); group.ExternalID != nil {
			t.Fatalf("stored externalId = %#v, want NULL", *group.ExternalID)
		}
		// A second empty externalId is not a uniqueness conflict.
		createGroup(t, tokenA, "Sales", "")
	})

	t.Run("list filter and pagination", func(t *testing.T) {
		recorder := scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups", "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var list struct {
			Schemas      []string         `json:"schemas"`
			TotalResults int              `json:"totalResults"`
			ItemsPerPage int              `json:"itemsPerPage"`
			StartIndex   int              `json:"startIndex"`
			Resources    []map[string]any `json:"Resources"`
		}
		decode(t, recorder, &list)
		if list.TotalResults != 3 || list.ItemsPerPage != 3 || list.StartIndex != 1 || len(list.Resources) != 3 {
			t.Fatalf("list = %#v", list)
		}
		names := []string{}
		for _, resource := range list.Resources {
			names = append(names, resource["displayName"].(string))
		}
		if strings.Join(names, ",") != "Engineering,Research,Sales" {
			t.Fatalf("list order = %v", names)
		}

		recorder = scimRequest(tokenA, http.MethodGet, `/scim/v2/Groups?filter=displayName+eq+%22engineering%22`, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("displayName filter status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &list)
		if list.TotalResults != 1 || list.Resources[0]["id"] != engineeringID {
			t.Fatalf("displayName filter = %#v", list)
		}

		recorder = scimRequest(tokenA, http.MethodGet, `/scim/v2/Groups?filter=externalId+eq+%22grp-eng%22`, "", "")
		decode(t, recorder, &list)
		if list.TotalResults != 1 || list.Resources[0]["id"] != engineeringID {
			t.Fatalf("externalId filter = %#v", list)
		}

		recorder = scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups?startIndex=2&count=1", "", "")
		decode(t, recorder, &list)
		if list.TotalResults != 3 || list.ItemsPerPage != 1 || list.StartIndex != 2 ||
			len(list.Resources) != 1 || list.Resources[0]["displayName"] != "Research" {
			t.Fatalf("pagination = %#v", list)
		}

		for _, bad := range []string{
			`/scim/v2/Groups?filter=userName+eq+%22alice%22`,
			`/scim/v2/Groups?filter=displayName+co+%22eng%22`,
			`/scim/v2/Groups?filter=displayName+eq+unquoted`,
		} {
			assertSCIMError(t, scimRequest(tokenA, http.MethodGet, bad, "", ""), http.StatusBadRequest, "invalidFilter")
		}
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups?startIndex=0", "", ""), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups?count=-1", "", ""), http.StatusBadRequest, "invalidValue")

		// Tenant B sees none of tenant A's groups.
		recorder = scimRequest(tokenB, http.MethodGet, "/scim/v2/Groups", "", "")
		decode(t, recorder, &list)
		if list.TotalResults != 0 || len(list.Resources) != 0 {
			t.Fatalf("cross-tenant list = %#v", list)
		}
	})

	t.Run("get is tenant scoped", func(t *testing.T) {
		recorder := scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups/"+engineeringID, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("get status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if resource["id"] != engineeringID {
			t.Fatalf("get resource = %#v", resource)
		}
		assertSCIMError(t, scimRequest(tokenB, http.MethodGet, "/scim/v2/Groups/"+engineeringID, "", ""), http.StatusNotFound, "")
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups/not-a-uuid", "", ""), http.StatusNotFound, "")
		assertSCIMError(t, scimRequest(tokenA, http.MethodGet, "/scim/v2/Groups/11111111-1111-4111-8111-111111111111", "", ""), http.StatusNotFound, "")
	})

	t.Run("PUT replaces attributes and membership", func(t *testing.T) {
		body := fmt.Sprintf(`{"displayName":"Engineering Renamed","externalId":"grp-eng-2","members":[{"value":%q},{"value":%q}]}`, bob, carol)
		recorder := scimRequest(tokenA, http.MethodPut, "/scim/v2/Groups/"+engineeringID, "", body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if resource["displayName"] != "Engineering Renamed" || resource["externalId"] != "grp-eng-2" {
			t.Fatalf("PUT resource = %#v", resource)
		}
		values := memberValues(t, resource)
		if strings.Join(values, ",") != bob+","+carol {
			t.Fatalf("PUT members = %v", values)
		}
		group := loadGroup(t, engineeringID)
		if group.Name != "Engineering Renamed" || group.ExternalID == nil || *group.ExternalID != "grp-eng-2" {
			t.Fatalf("stored group = %#v", group)
		}
		// Omitting members leaves membership unchanged; clearing externalId
		// with an empty string removes it.
		recorder = scimRequest(tokenA, http.MethodPut, "/scim/v2/Groups/"+engineeringID, "", `{"displayName":"Engineering Final","externalId":""}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT clear status = %d: %s", recorder.Code, recorder.Body.String())
		}
		resource = nil
		decode(t, recorder, &resource)
		if _, ok := resource["externalId"]; ok {
			t.Fatalf("cleared externalId rendered: %#v", resource["externalId"])
		}
		if values := memberValues(t, resource); strings.Join(values, ",") != bob+","+carol {
			t.Fatalf("omitted members changed membership: %v", values)
		}
		// Cross-tenant member is rejected without changing the group.
		body = fmt.Sprintf(`{"displayName":"Should Not Apply","members":[{"value":%q}]}`, outsider)
		assertSCIMError(t, scimRequest(tokenA, http.MethodPut, "/scim/v2/Groups/"+engineeringID, "", body), http.StatusBadRequest, "invalidValue")
		if group := loadGroup(t, engineeringID); group.Name != "Engineering Final" {
			t.Fatalf("failed PUT changed group name: %q", group.Name)
		}
		assertSCIMError(t, scimRequest(tokenA, http.MethodPut, "/scim/v2/Groups/11111111-1111-4111-8111-111111111111", "", `{"displayName":"X"}`), http.StatusNotFound, "")
	})

	t.Run("PATCH membership is idempotent", func(t *testing.T) {
		// Add alice.
		recorder := scimPatch(t, tokenA, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, alice))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH add status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var resource map[string]any
		decode(t, recorder, &resource)
		if values := memberValues(t, resource); strings.Join(values, ",") != alice+","+bob+","+carol {
			t.Fatalf("PATCH add members = %v", values)
		}
		// Duplicate add is idempotent.
		recorder = scimPatch(t, tokenA, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":{"value":%q}}]}`, alice))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH duplicate add status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &resource)
		if values := memberValues(t, resource); len(values) != 3 {
			t.Fatalf("duplicate add members = %v", values)
		}
		// Removing a non-member is idempotent.
		recorder = scimPatch(t, tokenA, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"remove","path":"members","value":[{"value":%q}]}]}`, dave))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH remove non-member status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &resource)
		if values := memberValues(t, resource); len(values) != 3 {
			t.Fatalf("remove non-member changed membership: %v", values)
		}
		// Replace and path-less replace.
		recorder = scimPatch(t, tokenA, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"replace","path":"members","value":[{"value":%q}]}]}`, alice))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH replace status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &resource)
		if values := memberValues(t, resource); strings.Join(values, ",") != alice {
			t.Fatalf("PATCH replace members = %v", values)
		}
		recorder = scimPatch(t, tokenA, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"replace","value":{"displayName":"Engineering Pathless","externalId":"grp-pathless","members":[{"value":%q}]}}]}`, bob))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH pathless status = %d: %s", recorder.Code, recorder.Body.String())
		}
		decode(t, recorder, &resource)
		if resource["displayName"] != "Engineering Pathless" || resource["externalId"] != "grp-pathless" {
			t.Fatalf("PATCH pathless resource = %#v", resource)
		}
		if values := memberValues(t, resource); strings.Join(values, ",") != bob {
			t.Fatalf("PATCH pathless members = %v", values)
		}
		// Unknown paths, unknown users and cross-tenant users.
		assertSCIMError(t, scimPatch(t, tokenA, engineeringID, `{"Operations":[{"op":"replace","path":"userName","value":"x"}]}`), http.StatusBadRequest, "invalidPath")
		assertSCIMError(t, scimPatch(t, tokenA, engineeringID, `{"Operations":[{"op":"replace","value":{"password":"x"}}]}`), http.StatusBadRequest, "invalidPath")
		assertSCIMError(t, scimPatch(t, tokenA, engineeringID, `{"Operations":[{"op":"add","path":"members","value":[{"value":"not-a-uuid"}]}]}`), http.StatusBadRequest, "invalidValue")
		assertSCIMError(t, scimPatch(t, tokenA, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, outsider)), http.StatusBadRequest, "invalidValue")
		// externalId remove clears it.
		recorder = scimPatch(t, tokenA, engineeringID, `{"Operations":[{"op":"remove","path":"externalId"}]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH remove externalId status = %d: %s", recorder.Code, recorder.Body.String())
		}
		resource = nil
		decode(t, recorder, &resource)
		if _, ok := resource["externalId"]; ok {
			t.Fatalf("externalId after remove = %#v", resource["externalId"])
		}
		// Method and subpath dispatch.
		assertSCIMError(t, scimRequest(tokenA, http.MethodPost, "/scim/v2/Groups/"+engineeringID, "", "{}"), http.StatusMethodNotAllowed, "")
	})

	var roleAdminGroup, roleMemberGroup, roleReadonlyGroup, unmappedGroup string

	t.Run("group role mapping and precedence", func(t *testing.T) {
		roleAdminGroup, _ = createGroup(t, tokenA, "Role Admins", "grp-role-admin")
		roleMemberGroup, _ = createGroup(t, tokenA, "Role Members", "grp-role-member")
		roleReadonlyGroup, _ = createGroup(t, tokenA, "Role Readonly", "grp-role-readonly")
		unmappedGroup, _ = createGroup(t, tokenA, "Unmapped", "grp-unmapped")

		if group := adminSetMapping(t, roleAdminGroup, "admin"); group["scimRole"] != "admin" {
			t.Fatalf("admin group detail scimRole = %#v", group["scimRole"])
		}
		adminSetMapping(t, roleMemberGroup, "member")
		adminSetMapping(t, roleReadonlyGroup, "readonly")

		// Mapping applies to existing members on the next membership change.
		recorder := scimPatch(t, tokenA, roleAdminGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, alice))
		if recorder.Code != http.StatusOK {
			t.Fatalf("add alice to mapped group: %d %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, alice); role != "admin" || source != "scim" {
			t.Fatalf("alice after admin mapping = %s/%s", role, source)
		}

		// A lower-precedence mapping cannot override admin.
		scimPatch(t, tokenA, roleReadonlyGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, alice))
		if role, source := loadRole(t, alice); role != "admin" || source != "scim" {
			t.Fatalf("alice precedence = %s/%s", role, source)
		}

		// Removing the highest mapping falls back to the next one.
		scimPatch(t, tokenA, roleAdminGroup, fmt.Sprintf(`{"Operations":[{"op":"remove","path":"members","value":[{"value":%q}]}]}`, alice))
		if role, source := loadRole(t, alice); role != "readonly" || source != "scim" {
			t.Fatalf("alice after removing admin group = %s/%s", role, source)
		}

		// Re-adding restores admin.
		scimPatch(t, tokenA, roleAdminGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, alice))
		if role, source := loadRole(t, alice); role != "admin" || source != "scim" {
			t.Fatalf("alice re-added = %s/%s", role, source)
		}

		// Mapping to member sets role_source=scim even when the role already is
		// member.
		scimPatch(t, tokenA, roleMemberGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, bob))
		if role, source := loadRole(t, bob); role != "member" || source != "scim" {
			t.Fatalf("bob member mapping = %s/%s", role, source)
		}

		// Owners are never modified by mapping.
		scimPatch(t, tokenA, roleAdminGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, ownerA))
		if role, source := loadRole(t, ownerA); role != "owner" || source != "manual" {
			t.Fatalf("owner after mapped membership = %s/%s", role, source)
		}

		// owner can never be granted by the local mapping.
		assertRole := func(body string) {
			t.Helper()
			recorder := adminRequest(adminCookie, adminCSRF, http.MethodPatch, "/admin/api/groups/"+roleAdminGroup, body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("mapping %s status = %d: %s", body, recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decode(t, recorder, &payload)
			if payload.Error.Code != "ROLE" {
				t.Fatalf("mapping %s code = %#v", body, payload.Error.Code)
			}
		}
		assertRole(`{"scimRole":"owner"}`)
		assertRole(`{"scimRole":"root"}`)
		if group := loadGroup(t, roleAdminGroup); group.ScimRole != "admin" {
			t.Fatalf("rejected mapping changed stored mapping to %q", group.ScimRole)
		}
	})

	t.Run("manual role change survives a recompute", func(t *testing.T) {
		recorder := adminRequest(adminCookie, adminCSRF, http.MethodPatch, "/admin/api/users/"+dave, `{"role":"admin"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("admin role change status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, dave); role != "admin" || source != "manual" {
			t.Fatalf("dave after manual change = %s/%s", role, source)
		}
		// A membership change in an unmapped group triggers a recompute; the
		// manual decision must survive.
		recorder = scimPatch(t, tokenA, unmappedGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, dave))
		if recorder.Code != http.StatusOK {
			t.Fatalf("add dave to unmapped group: %d %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, dave); role != "admin" || source != "manual" {
			t.Fatalf("dave after recompute = %s/%s", role, source)
		}
	})

	t.Run("admin membership and mapping changes recompute", func(t *testing.T) {
		// Admin-added member of a mapped group receives the mapped role.
		recorder := adminRequest(adminCookie, adminCSRF, http.MethodPost, "/admin/api/groups/"+roleReadonlyGroup+"/members", fmt.Sprintf(`{"userId":%q}`, erin))
		if recorder.Code != http.StatusOK {
			t.Fatalf("admin add member status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, erin); role != "readonly" || source != "scim" {
			t.Fatalf("erin after admin add = %s/%s", role, source)
		}
		// Removing the member reset the mapping-managed role.
		recorder = adminRequest(adminCookie, adminCSRF, http.MethodDelete, "/admin/api/groups/"+roleReadonlyGroup+"/members/"+erin, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("admin remove member status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, erin); role != "member" || source != "manual" {
			t.Fatalf("erin after admin remove = %s/%s", role, source)
		}
		// Clearing a mapping recomputes its members.
		clearGroup, _ := createGroup(t, tokenA, "Clear Mapping", "grp-clear")
		scimPatch(t, tokenA, clearGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, carol))
		adminSetMapping(t, clearGroup, "readonly")
		if role, source := loadRole(t, carol); role != "readonly" || source != "scim" {
			t.Fatalf("carol after mapping = %s/%s", role, source)
		}
		adminSetMapping(t, clearGroup, "")
		if role, source := loadRole(t, carol); role != "member" || source != "manual" {
			t.Fatalf("carol after clearing mapping = %s/%s", role, source)
		}
	})

	t.Run("group delete recomputes roles", func(t *testing.T) {
		// SCIM delete of the admin-mapped group leaves alice on readonly.
		recorder := scimRequest(tokenA, http.MethodDelete, "/scim/v2/Groups/"+roleAdminGroup, "", "")
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("SCIM delete status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, alice); role != "readonly" || source != "scim" {
			t.Fatalf("alice after group delete = %s/%s", role, source)
		}
		if role, source := loadRole(t, ownerA); role != "owner" || source != "manual" {
			t.Fatalf("owner after group delete = %s/%s", role, source)
		}
		// Deleting the last mapped group unmaps the role.
		recorder = scimRequest(tokenA, http.MethodDelete, "/scim/v2/Groups/"+roleReadonlyGroup, "", "")
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("SCIM delete readonly group status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, alice); role != "member" || source != "manual" {
			t.Fatalf("alice after last mapped group delete = %s/%s", role, source)
		}
		// Admin group delete also recomputes former members.
		adminDeleteGroup, _ := createGroup(t, tokenA, "Admin Delete Mapped", "grp-admin-delete")
		scimPatch(t, tokenA, adminDeleteGroup, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, erin))
		adminSetMapping(t, adminDeleteGroup, "admin")
		if role, source := loadRole(t, erin); role != "admin" || source != "scim" {
			t.Fatalf("erin before admin delete = %s/%s", role, source)
		}
		recorder = adminRequest(adminCookie, adminCSRF, http.MethodDelete, "/admin/api/groups/"+adminDeleteGroup, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("admin delete group status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if role, source := loadRole(t, erin); role != "member" || source != "manual" {
			t.Fatalf("erin after admin delete = %s/%s", role, source)
		}
	})

	t.Run("cross-tenant group access is denied", func(t *testing.T) {
		assertSCIMError(t, scimRequest(tokenB, http.MethodGet, "/scim/v2/Groups/"+engineeringID, "", ""), http.StatusNotFound, "")
		assertSCIMError(t, scimRequest(tokenB, http.MethodPut, "/scim/v2/Groups/"+engineeringID, "", `{"displayName":"Hijack"}`), http.StatusNotFound, "")
		assertSCIMError(t, scimRequest(tokenB, http.MethodPatch, "/scim/v2/Groups/"+engineeringID, "", `{"Operations":[{"op":"replace","path":"displayName","value":"Hijack"}]}`), http.StatusNotFound, "")
		assertSCIMError(t, scimRequest(tokenB, http.MethodDelete, "/scim/v2/Groups/"+engineeringID, "", ""), http.StatusNotFound, "")
		if group := loadGroup(t, engineeringID); group.Name != "Engineering Pathless" {
			t.Fatalf("cross-tenant writes changed group: %q", group.Name)
		}
		// Cross-tenant member on an update is rejected too.
		assertSCIMError(t, scimPatch(t, tokenB, engineeringID, fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, outsider)), http.StatusNotFound, "")
	})

	t.Run("audit records fixed group actions without secrets", func(t *testing.T) {
		required := map[string]int{
			"scim.group_created": 9,
			"scim.group_updated": 1,
			"scim.group_deleted": 2,
			"scim.role_mapped":   1,
			"scim.role_unmapped": 1,
		}
		type auditRow struct {
			actor, action, target, detail string
		}
		deadline := time.Now().Add(10 * time.Second)
		var rows []auditRow
		var counts map[string]int
		for {
			rows = nil
			results, err := pool.Query(ctx, `
				SELECT actor, action, target, detail FROM audit_log
				WHERE tenant_id = $1 AND action LIKE 'scim.%'
				ORDER BY id
			`, tenantA)
			if err != nil {
				t.Fatalf("load audit rows: %v", err)
			}
			for results.Next() {
				var row auditRow
				if err := results.Scan(&row.actor, &row.action, &row.target, &row.detail); err != nil {
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
		for _, row := range rows {
			if !strings.HasPrefix(row.actor, "SCIM token ") {
				t.Fatalf("audit actor = %q", row.actor)
			}
			switch row.action {
			case "scim.group_created", "scim.group_updated", "scim.group_deleted":
				if !strings.HasPrefix(row.target, "scim-") || !strings.Contains(row.target, "@") {
					t.Fatalf("group audit target = %q", row.target)
				}
			case "scim.role_mapped", "scim.role_unmapped":
				if !strings.Contains(row.target, "@") {
					t.Fatalf("role audit target = %q", row.target)
				}
			}
			for _, secret := range []string{tokenA, tokenB, tokenC, hashToken(tokenA)} {
				if strings.Contains(row.actor+row.target+row.detail, secret) {
					t.Fatal("audit row contains credential material")
				}
			}
			for _, memberID := range []string{alice, bob, carol, dave, erin, ownerA} {
				if strings.Contains(row.detail, memberID) {
					t.Fatal("audit detail contains a member payload")
				}
			}
		}

		var mappingAudits, adminDeletes int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE action = 'admin.group_role_mapping_updated'),
			       count(*) FILTER (WHERE action = 'admin.group_deleted')
			FROM admin_audit_log WHERE tenant_id = $1
		`, tenantA).Scan(&mappingAudits, &adminDeletes); err != nil {
			t.Fatalf("load admin audit rows: %v", err)
		}
		if mappingAudits < 4 {
			t.Fatalf("admin group_role_mapping_updated rows = %d", mappingAudits)
		}
		if adminDeletes < 1 {
			t.Fatalf("admin.group_deleted rows = %d", adminDeletes)
		}
		var unmappedAdmin int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM admin_audit_log WHERE tenant_id = $1 AND action = 'scim.role_unmapped'
		`, tenantA).Scan(&unmappedAdmin); err != nil || unmappedAdmin < 1 {
			t.Fatalf("admin scim.role_unmapped rows = %d err=%v", unmappedAdmin, err)
		}
	})
}
