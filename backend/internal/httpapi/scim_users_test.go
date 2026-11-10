package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func scimStringPtr(value string) *string { return &value }
func scimBoolPtr(value bool) *bool       { return &value }

func scimUserFixture() scimUserRow {
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	externalID := "ext-1"
	return scimUserRow{
		ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.test",
		Name: "Alice Example", GivenName: "Alice", FamilyName: "Example",
		ExternalID: &externalID, Role: "member", Status: "active",
		CreatedAt: created, UpdatedAt: created.Add(time.Hour),
	}
}

func scimTenantRequest(r *http.Request) *http.Request {
	tenant := scimTenant{TokenID: "token-1", TenantID: "tenant-1", Name: "Entra ID"}
	return r.WithContext(context.WithValue(r.Context(), scimTenantContextKey{}, tenant))
}

func TestSCIMParseFilter(t *testing.T) {
	allowed := map[string]bool{"userName": true, "externalId": true}
	valid := []struct {
		raw   string
		field string
		value string
	}{
		{raw: `userName eq "alice@example.test"`, field: "userName", value: "alice@example.test"},
		{raw: `externalId eq "ext-42"`, field: "externalId", value: "ext-42"},
		{raw: `USERNAME EQ "alice@example.test"`, field: "userName", value: "alice@example.test"},
		{raw: `  userName   eq   "a b"  `, field: "userName", value: "a b"},
		{raw: `userName eq "a\"b"`, field: "userName", value: `a"b`},
		{raw: `externalId eq ""`, field: "externalId", value: ""},
	}
	for _, tc := range valid {
		field, value, err := parseSCIMFilter(tc.raw, allowed)
		if err != nil {
			t.Fatalf("parseSCIMFilter(%q): %v", tc.raw, err)
		}
		if field != tc.field || value != tc.value {
			t.Fatalf("parseSCIMFilter(%q) = %q, %q; want %q, %q", tc.raw, field, value, tc.field, tc.value)
		}
	}
	invalid := []string{
		"",
		"   ",
		`userName co "alice"`,
		`userName sw "alice"`,
		`userName eq alice`,
		`userName eq`,
		`userName`,
		`displayName eq "Alice"`,
		`userName eq "alice" and externalId eq "ext-1"`,
		`userName eq "unterminated`,
		`userName eq 42`,
	}
	for _, raw := range invalid {
		if _, _, err := parseSCIMFilter(raw, allowed); err == nil {
			t.Fatalf("parseSCIMFilter(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestSCIMParsePagination(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		startIndex int
		count      int
		wantErr    bool
	}{
		{name: "defaults", query: "", startIndex: 1, count: 100},
		{name: "explicit", query: "?startIndex=3&count=25", startIndex: 3, count: 25},
		{name: "cap at 500", query: "?count=1000", startIndex: 1, count: 500},
		{name: "zero count", query: "?count=0", startIndex: 1, count: 0},
		{name: "zero start", query: "?startIndex=0", wantErr: true},
		{name: "negative start", query: "?startIndex=-2", wantErr: true},
		{name: "non numeric start", query: "?startIndex=abc", wantErr: true},
		{name: "negative count", query: "?count=-1", wantErr: true},
		{name: "non numeric count", query: "?count=abc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/scim/v2/Users"+tc.query, nil)
			startIndex, count, err := parseSCIMPagination(request)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSCIMPagination(%q) unexpectedly succeeded", tc.query)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSCIMPagination(%q): %v", tc.query, err)
			}
			if startIndex != tc.startIndex || count != tc.count {
				t.Fatalf("parseSCIMPagination(%q) = %d, %d; want %d, %d", tc.query, startIndex, count, tc.startIndex, tc.count)
			}
		})
	}
}

func TestSCIMUserResourceShape(t *testing.T) {
	row := scimUserFixture()
	resource := scimUserResource(row, "https://mail.example.test/scim/v2")
	schemas, _ := resource["schemas"].([]string)
	if len(schemas) != 1 || schemas[0] != scimUserSchema {
		t.Fatalf("schemas = %#v", resource["schemas"])
	}
	if resource["id"] != row.ID || resource["userName"] != row.Email || resource["displayName"] != row.Name {
		t.Fatalf("identity fields = %#v", resource)
	}
	if resource["externalId"] != "ext-1" {
		t.Fatalf("externalId = %#v", resource["externalId"])
	}
	name, _ := resource["name"].(map[string]any)
	if name["formatted"] != "Alice Example" || name["givenName"] != "Alice" || name["familyName"] != "Example" {
		t.Fatalf("name = %#v", name)
	}
	emails, _ := resource["emails"].([]map[string]any)
	if len(emails) != 1 || emails[0]["value"] != row.Email || emails[0]["type"] != "work" || emails[0]["primary"] != true {
		t.Fatalf("emails = %#v", resource["emails"])
	}
	if resource["active"] != true {
		t.Fatalf("active = %#v", resource["active"])
	}
	roles, _ := resource["roles"].([]map[string]any)
	if len(roles) != 1 || roles[0]["value"] != "member" {
		t.Fatalf("roles = %#v", resource["roles"])
	}
	meta, _ := resource["meta"].(map[string]any)
	if meta["resourceType"] != "User" || meta["location"] != "https://mail.example.test/scim/v2/Users/"+row.ID {
		t.Fatalf("meta = %#v", meta)
	}
	if meta["created"] != "2026-02-03T04:05:06Z" || meta["lastModified"] != "2026-02-03T05:05:06Z" {
		t.Fatalf("meta timestamps = %#v", meta)
	}
	if version, _ := meta["version"].(string); !strings.HasPrefix(version, `W/"`) {
		t.Fatalf("version = %#v", meta["version"])
	}

	// A NULL externalId is omitted, and a suspended user is inactive.
	row.ExternalID = nil
	row.Status = "suspended"
	resource = scimUserResource(row, "https://mail.example.test/scim/v2")
	if _, exists := resource["externalId"]; exists {
		t.Fatalf("NULL externalId should be omitted: %#v", resource)
	}
	if resource["active"] != false {
		t.Fatalf("suspended user active = %#v", resource["active"])
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("marshal resource: %v", err)
	}
	if strings.Contains(string(encoded), "password") {
		t.Fatalf("resource leaks a password field: %s", encoded)
	}
}

func TestSCIMClassifyCreate(t *testing.T) {
	existing := scimUserFixture()
	if got := classifySCIMCreate(nil, nil); got != scimCreateProceed {
		t.Fatalf("nil existing = %v, want proceed", got)
	}
	if got := classifySCIMCreate(&existing, scimStringPtr("ext-1")); got != scimCreateExisting {
		t.Fatalf("same externalId = %v, want existing", got)
	}
	existing.ExternalID = nil
	if got := classifySCIMCreate(&existing, nil); got != scimCreateExisting {
		t.Fatalf("both NULL externalId = %v, want existing", got)
	}
	if got := classifySCIMCreate(&existing, scimStringPtr("ext-2")); got != scimCreateConflict {
		t.Fatalf("added externalId = %v, want conflict", got)
	}
	existing.ExternalID = scimStringPtr("ext-1")
	if got := classifySCIMCreate(&existing, scimStringPtr("ext-2")); got != scimCreateConflict {
		t.Fatalf("different externalId = %v, want conflict", got)
	}
	if got := classifySCIMCreate(&existing, nil); got != scimCreateConflict {
		t.Fatalf("removed externalId = %v, want conflict", got)
	}
	deleted := time.Now()
	existing.DeletedAt = &deleted
	if got := classifySCIMCreate(&existing, scimStringPtr("ext-1")); got != scimCreateConflict {
		t.Fatalf("soft-deleted row = %v, want conflict", got)
	}
}

func TestSCIMReplaceChanges(t *testing.T) {
	existing := scimUserFixture()

	changes, handlerErr := scimReplaceChanges(scimUserPayload{UserName: scimStringPtr("other@example.test")}, existing)
	if handlerErr == nil || handlerErr.status != http.StatusConflict || handlerErr.scimType != "mutability" {
		t.Fatalf("changed userName error = %#v", handlerErr)
	}
	if changes.Name != nil || changes.ActiveSet {
		t.Fatalf("failed validation produced changes: %#v", changes)
	}

	if _, handlerErr := scimReplaceChanges(scimUserPayload{Emails: []scimUserEmailPayload{{Value: "other@example.test"}}}, existing); handlerErr == nil || handlerErr.scimType != "mutability" {
		t.Fatalf("changed emails error = %#v", handlerErr)
	}

	changes, handlerErr = scimReplaceChanges(scimUserPayload{
		UserName:   scimStringPtr("ALICE@EXAMPLE.TEST"),
		Emails:     []scimUserEmailPayload{{Value: "Alice@Example.Test", Primary: scimBoolPtr(true)}},
		ExternalID: scimStringPtr(""),
		Active:     scimBoolPtr(false),
		Name:       &scimUserNamePayload{Formatted: scimStringPtr("Alice Updated"), GivenName: scimStringPtr("Alicia"), FamilyName: scimStringPtr("Updated")},
	}, existing)
	if handlerErr != nil {
		t.Fatalf("valid replace: %v", handlerErr)
	}
	if changes.Name == nil || *changes.Name != "Alice Updated" || *changes.GivenName != "Alicia" || *changes.FamilyName != "Updated" {
		t.Fatalf("name changes = %#v", changes)
	}
	if !changes.ExternalIDSet || changes.ExternalID != nil {
		t.Fatalf("empty externalId should clear to NULL: %#v", changes)
	}
	if !changes.ActiveSet || changes.Active {
		t.Fatalf("active change = %#v", changes)
	}

	changes, handlerErr = scimReplaceChanges(scimUserPayload{Name: &scimUserNamePayload{}}, existing)
	if handlerErr != nil {
		t.Fatalf("empty name object: %v", handlerErr)
	}
	if changes.Name == nil || *changes.Name != "alice" || *changes.GivenName != "" || *changes.FamilyName != "" {
		t.Fatalf("name fallback = %#v", changes)
	}

	changes, handlerErr = scimReplaceChanges(scimUserPayload{}, existing)
	if handlerErr != nil || changes.Name != nil || changes.ExternalIDSet || changes.ActiveSet {
		t.Fatalf("empty payload should change nothing: %#v %v", changes, handlerErr)
	}
}

func TestSCIMPatchChanges(t *testing.T) {
	existing := scimUserFixture()
	value := func(raw string) json.RawMessage { return json.RawMessage(raw) }
	patch := func(operations ...scimPatchOperation) scimPatchRequest {
		return scimPatchRequest{Operations: operations}
	}
	requireError := func(t *testing.T, handlerErr *scimHandlerError, status int, scimType string) {
		t.Helper()
		if handlerErr == nil {
			t.Fatalf("expected %d/%s error, got nil", status, scimType)
		}
		if handlerErr.status != status || handlerErr.scimType != scimType {
			t.Fatalf("error = %d/%s (%s), want %d/%s", handlerErr.status, handlerErr.scimType, handlerErr.detail, status, scimType)
		}
	}

	t.Run("replace sub-attribute", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "name.givenName", Value: value(`"Alicia"`)}), existing)
		if handlerErr != nil {
			t.Fatalf("patch: %v", handlerErr)
		}
		if changes.GivenName == nil || *changes.GivenName != "Alicia" || changes.Name != nil {
			t.Fatalf("changes = %#v", changes)
		}
	})

	t.Run("add external id", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "add", Path: "externalId", Value: value(`"ext-2"`)}), existing)
		if handlerErr != nil {
			t.Fatalf("patch: %v", handlerErr)
		}
		if !changes.ExternalIDSet || changes.ExternalID == nil || *changes.ExternalID != "ext-2" {
			t.Fatalf("changes = %#v", changes)
		}
	})

	t.Run("replace external id with empty clears", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "externalId", Value: value(`""`)}), existing)
		if handlerErr != nil {
			t.Fatalf("patch: %v", handlerErr)
		}
		if !changes.ExternalIDSet || changes.ExternalID != nil {
			t.Fatalf("changes = %#v", changes)
		}
	})

	t.Run("remove external id clears", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "remove", Path: "externalId"}), existing)
		if handlerErr != nil {
			t.Fatalf("patch: %v", handlerErr)
		}
		if !changes.ExternalIDSet || changes.ExternalID != nil {
			t.Fatalf("changes = %#v", changes)
		}
	})

	t.Run("replace and remove active", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "active", Value: value(`false`)}), existing)
		if handlerErr != nil || !changes.ActiveSet || changes.Active {
			t.Fatalf("replace active = %#v %v", changes, handlerErr)
		}
		changes, handlerErr = scimPatchChanges(patch(scimPatchOperation{Op: "remove", Path: "active"}), existing)
		if handlerErr != nil || !changes.ActiveSet || changes.Active {
			t.Fatalf("remove active = %#v %v", changes, handlerErr)
		}
	})

	t.Run("path-less replace object", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Value: value(`{"active":false,"externalId":"ext-3","name":{"givenName":"Patched"}}`)}), existing)
		if handlerErr != nil {
			t.Fatalf("patch: %v", handlerErr)
		}
		if !changes.ActiveSet || changes.Active {
			t.Fatalf("active = %#v", changes)
		}
		if changes.ExternalID == nil || *changes.ExternalID != "ext-3" {
			t.Fatalf("externalId = %#v", changes)
		}
		if changes.GivenName == nil || *changes.GivenName != "Patched" {
			t.Fatalf("givenName = %#v", changes)
		}
	})

	t.Run("path-less replace ignores envelope", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Value: value(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"x","meta":{}}`)}), existing)
		if handlerErr != nil {
			t.Fatalf("patch: %v", handlerErr)
		}
		if changes.Name != nil || changes.ExternalIDSet || changes.ActiveSet {
			t.Fatalf("envelope fields changed state: %#v", changes)
		}
	})

	t.Run("path-less replace rejects password", func(t *testing.T) {
		_, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Value: value(`{"password":"Sup3rSecret!"}`)}), existing)
		requireError(t, handlerErr, http.StatusBadRequest, "invalidPath")
	})

	t.Run("password path rejected", func(t *testing.T) {
		_, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "password", Value: value(`"Sup3rSecret!"`)}), existing)
		requireError(t, handlerErr, http.StatusBadRequest, "invalidPath")
	})

	t.Run("roles path rejected", func(t *testing.T) {
		_, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "roles", Value: value(`[{"value":"admin"}]`)}), existing)
		requireError(t, handlerErr, http.StatusBadRequest, "invalidPath")
	})

	t.Run("userName is immutable", func(t *testing.T) {
		_, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "userName", Value: value(`"other@example.test"`)}), existing)
		requireError(t, handlerErr, http.StatusConflict, "mutability")
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "userName", Value: value(`"ALICE@EXAMPLE.TEST"`)}), existing)
		if handlerErr != nil || changes.Name != nil {
			t.Fatalf("same userName = %#v %v", changes, handlerErr)
		}
		_, handlerErr = scimPatchChanges(patch(scimPatchOperation{Op: "remove", Path: "userName"}), existing)
		requireError(t, handlerErr, http.StatusConflict, "mutability")
	})

	t.Run("emails are immutable", func(t *testing.T) {
		_, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "emails", Value: value(`[{"value":"other@example.test"}]`)}), existing)
		requireError(t, handlerErr, http.StatusConflict, "mutability")
		if _, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "emails", Value: value(`[{"value":"alice@example.test"}]`)}), existing); handlerErr != nil {
			t.Fatalf("same email: %v", handlerErr)
		}
		_, handlerErr = scimPatchChanges(patch(scimPatchOperation{Op: "remove", Path: "emails"}), existing)
		requireError(t, handlerErr, http.StatusConflict, "mutability")
	})

	t.Run("name object and removal", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "name", Value: value(`{"formatted":"Patched Name","givenName":"Patched","familyName":"Name"}`)}), existing)
		if handlerErr != nil || *changes.Name != "Patched Name" || *changes.GivenName != "Patched" || *changes.FamilyName != "Name" {
			t.Fatalf("name object = %#v %v", changes, handlerErr)
		}
		changes, handlerErr = scimPatchChanges(patch(scimPatchOperation{Op: "remove", Path: "name"}), existing)
		if handlerErr != nil || *changes.Name != "alice" || *changes.GivenName != "" || *changes.FamilyName != "" {
			t.Fatalf("name removal = %#v %v", changes, handlerErr)
		}
	})

	t.Run("formatted fallback", func(t *testing.T) {
		changes, handlerErr := scimPatchChanges(patch(scimPatchOperation{Op: "replace", Path: "name.formatted", Value: value(`""`)}), existing)
		if handlerErr != nil || *changes.Name != "alice" {
			t.Fatalf("formatted fallback = %#v %v", changes, handlerErr)
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		cases := []struct {
			name     string
			payload  scimPatchRequest
			status   int
			scimType string
		}{
			{name: "no operations", payload: scimPatchRequest{}, status: 400, scimType: "invalidValue"},
			{name: "unknown op", payload: patch(scimPatchOperation{Op: "move", Path: "active", Value: value(`true`)}), status: 400, scimType: "invalidValue"},
			{name: "add without path", payload: patch(scimPatchOperation{Op: "add", Value: value(`{"active":true}`)}), status: 400, scimType: "invalidPath"},
			{name: "path-less replace without value", payload: patch(scimPatchOperation{Op: "replace"}), status: 400, scimType: "invalidValue"},
			{name: "path-less replace scalar", payload: patch(scimPatchOperation{Op: "replace", Value: value(`true`)}), status: 400, scimType: "invalidValue"},
			{name: "unknown path", payload: patch(scimPatchOperation{Op: "replace", Path: "displayName", Value: value(`"x"`)}), status: 400, scimType: "invalidPath"},
			{name: "deep name path", payload: patch(scimPatchOperation{Op: "replace", Path: "name.foo.bar", Value: value(`"x"`)}), status: 400, scimType: "invalidPath"},
			{name: "active wrong type", payload: patch(scimPatchOperation{Op: "replace", Path: "active", Value: value(`"yes"`)}), status: 400, scimType: "invalidValue"},
			{name: "externalId wrong type", payload: patch(scimPatchOperation{Op: "replace", Path: "externalId", Value: value(`42`)}), status: 400, scimType: "invalidValue"},
			{name: "name wrong type", payload: patch(scimPatchOperation{Op: "replace", Path: "name", Value: value(`"Alice"`)}), status: 400, scimType: "invalidValue"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, handlerErr := scimPatchChanges(tc.payload, existing)
				requireError(t, handlerErr, tc.status, tc.scimType)
			})
		}
	})
}

func TestSCIMCreateValidationWithoutDatabase(t *testing.T) {
	server := New(Options{
		Config: config.Config{PublicURL: "https://mail.example.test"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	cases := []struct {
		name        string
		body        string
		contentType string
	}{
		{name: "missing content type", body: `{"userName":"alice@example.test"}`},
		{name: "wrong content type", body: `{"userName":"alice@example.test"}`, contentType: "text/plain"},
		{name: "invalid json", body: `{`, contentType: "application/json"},
		{name: "empty body", body: "", contentType: "application/json"},
		{name: "missing userName", body: `{}`, contentType: "application/scim+json"},
		{name: "blank userName", body: `{"userName":"   "}`, contentType: "application/json"},
		{name: "not an email", body: `{"userName":"alice"}`, contentType: "application/json"},
		{name: "spaces in email", body: `{"userName":"a b@example.test"}`, contentType: "application/json"},
		{name: "oversized body", body: `{"userName":"alice@example.test","pad":"` + strings.Repeat("a", int(scimUserBodyLimit)) + `"}`, contentType: "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/scim/v2/Users", strings.NewReader(tc.body))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			request = scimTenantRequest(request)
			recorder := httptest.NewRecorder()
			// A nil store proves every rejection happens before PostgreSQL.
			server.scimCreateUser(recorder, request)
			assertSCIMError(t, recorder, http.StatusBadRequest, "invalidValue")
		})
	}
}

func TestSCIMUserRoutesAreRegisteredBehindAuth(t *testing.T) {
	unlicensed := New(Options{
		Config: config.Config{PublicURL: "https://mail.example.test"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/scim/v2/Users"},
		{method: http.MethodPost, path: "/scim/v2/Users"},
		{method: http.MethodGet, path: "/scim/v2/Users/11111111-1111-4111-8111-111111111111"},
		{method: http.MethodPatch, path: "/scim/v2/Users/11111111-1111-4111-8111-111111111111"},
	} {
		recorder := httptest.NewRecorder()
		unlicensed.Handler().ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unlicensed %s %s status = %d, want 404", tc.method, tc.path, recorder.Code)
		}
	}

	licensed, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	for _, tc := range []struct {
		method        string
		path          string
		authorization string
	}{
		{method: http.MethodGet, path: "/scim/v2/Users"},
		{method: http.MethodPost, path: "/scim/v2/Users"},
		{method: http.MethodGet, path: "/scim/v2/Users/11111111-1111-4111-8111-111111111111"},
		{method: http.MethodPut, path: "/scim/v2/Users/11111111-1111-4111-8111-111111111111"},
		{method: http.MethodPatch, path: "/scim/v2/Users/11111111-1111-4111-8111-111111111111"},
		{method: http.MethodDelete, path: "/scim/v2/Users/11111111-1111-4111-8111-111111111111"},
		{method: http.MethodGet, path: "/scim/v2/Users", authorization: "Bearer not-a-scim-token"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.authorization != "" {
			request.Header.Set("Authorization", tc.authorization)
		}
		recorder := httptest.NewRecorder()
		licensed.Handler().ServeHTTP(recorder, request)
		assertSCIMError(t, recorder, http.StatusUnauthorized, "")
	}
}

func TestSCIMUsersMethodDispatch(t *testing.T) {
	server := New(Options{
		Config: config.Config{PublicURL: "https://mail.example.test"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	recorder := httptest.NewRecorder()
	server.scimUsersCollection(recorder, scimTenantRequest(httptest.NewRequest(http.MethodDelete, "/scim/v2/Users", nil)))
	assertSCIMError(t, recorder, http.StatusMethodNotAllowed, "")
	if got := recorder.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("collection Allow = %q", got)
	}

	recorder = httptest.NewRecorder()
	server.scimUsersItem(recorder, scimTenantRequest(httptest.NewRequest(http.MethodPost, "/scim/v2/Users/id", nil)))
	assertSCIMError(t, recorder, http.StatusMethodNotAllowed, "")
	if got := recorder.Header().Get("Allow"); got != "GET, PUT, PATCH, DELETE" {
		t.Fatalf("item Allow = %q", got)
	}
}

func TestSCIMUserPayloadNeverRendersCredentials(t *testing.T) {
	row := scimUserFixture()
	resource := scimUserResource(row, "https://mail.example.test/scim/v2")
	encoded, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("marshal resource: %v", err)
	}
	text := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"password", "passwordhash", "token", "activation"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("resource contains %q: %s", forbidden, encoded)
		}
	}
}
