package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	scimTestAliceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	scimTestBobID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	scimTestCarolID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func scimGroupFixture() scimGroupRow {
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	externalID := "group-ext-1"
	return scimGroupRow{
		ID: "22222222-2222-4222-8222-222222222222", Name: "Engineering", Email: "scim-group@example.test",
		ExternalID: &externalID, ScimRole: "admin", GroupType: "security", Status: "active",
		CreatedAt: created, UpdatedAt: created.Add(2 * time.Hour),
	}
}

func TestSCIMGroupResourceShape(t *testing.T) {
	group := scimGroupFixture()
	members := []scimGroupMember{
		{Value: "11111111-1111-4111-8111-111111111111", Display: "Alice"},
		{Value: "33333333-3333-4333-8333-333333333333", Display: "Bob"},
	}
	resource := scimGroupResource(group, members, "https://mail.example.test/scim/v2")
	schemas, _ := resource["schemas"].([]string)
	if len(schemas) != 1 || schemas[0] != scimGroupSchema {
		t.Fatalf("schemas = %#v", resource["schemas"])
	}
	if resource["id"] != group.ID || resource["displayName"] != group.Name || resource["externalId"] != "group-ext-1" {
		t.Fatalf("resource identity = %#v", resource)
	}
	if _, ok := resource["scimRole"]; ok {
		t.Fatal("local scim_role must not leak into the SCIM resource")
	}
	rendered, ok := resource["members"].([]map[string]any)
	if !ok || len(rendered) != 2 {
		t.Fatalf("members = %#v", resource["members"])
	}
	if rendered[0]["value"] != members[0].Value || rendered[0]["display"] != "Alice" ||
		rendered[0]["$ref"] != "https://mail.example.test/scim/v2/Users/"+members[0].Value {
		t.Fatalf("member[0] = %#v", rendered[0])
	}
	meta, ok := resource["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v", resource["meta"])
	}
	if meta["resourceType"] != "Group" ||
		meta["created"] != "2026-03-04T05:06:07Z" ||
		meta["lastModified"] != "2026-03-04T07:06:07Z" ||
		meta["location"] != "https://mail.example.test/scim/v2/Groups/"+group.ID ||
		meta["version"] != scimGroupVersion(group) {
		t.Fatalf("meta = %#v", meta)
	}

	group.ExternalID = nil
	resource = scimGroupResource(group, nil, "https://mail.example.test/scim/v2")
	if _, ok := resource["externalId"]; ok {
		t.Fatal("externalId must be omitted when unset")
	}
	if members, ok := resource["members"].([]map[string]any); !ok || len(members) != 0 {
		t.Fatalf("empty members = %#v", resource["members"])
	}
}

func TestSCIMGroupRolePrecedence(t *testing.T) {
	cases := []struct {
		roles []string
		want  string
	}{
		{roles: nil, want: ""},
		{roles: []string{}, want: ""},
		{roles: []string{"readonly"}, want: "readonly"},
		{roles: []string{"member", "readonly"}, want: "member"},
		{roles: []string{"readonly", "admin", "member"}, want: "admin"},
		{roles: []string{"admin", "owner", "garbage"}, want: "admin"},
		{roles: []string{"owner", ""}, want: ""},
	}
	for _, tc := range cases {
		if got := scimHighestMappedRole(tc.roles); got != tc.want {
			t.Fatalf("scimHighestMappedRole(%#v) = %q; want %q", tc.roles, got, tc.want)
		}
	}
}

func TestSCIMGroupMembershipDiff(t *testing.T) {
	added, removed := scimMembershipDiff([]string{"a", "b"}, []string{"b", "c", "c"})
	if strings.Join(added, ",") != "c" || strings.Join(removed, ",") != "a" {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
	added, removed = scimMembershipDiff(nil, nil)
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("empty diff = %v %v", added, removed)
	}
	added, removed = scimMembershipDiff([]string{"a", "a"}, []string{"a"})
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("duplicate diff = %v %v", added, removed)
	}
}

func TestNormalizeAdminSCIMRole(t *testing.T) {
	for _, valid := range []string{"", "admin", "member", "readonly", " admin "} {
		if _, ok := normalizeAdminSCIMRole(valid); !ok {
			t.Fatalf("normalizeAdminSCIMRole(%q) rejected", valid)
		}
	}
	for _, invalid := range []string{"owner", "ADMIN", "Admin", "root", "scim"} {
		if _, ok := normalizeAdminSCIMRole(invalid); ok {
			t.Fatalf("normalizeAdminSCIMRole(%q) accepted", invalid)
		}
	}
}

func TestSCIMGroupRoleAuditAction(t *testing.T) {
	action, detail := scimGroupRoleAuditAction(scimRoleChange{OldRole: "member", NewRole: "admin"})
	if action != "scim.role_mapped" || !strings.Contains(detail, "member") || !strings.Contains(detail, "admin") {
		t.Fatalf("mapped action=%q detail=%q", action, detail)
	}
	action, detail = scimGroupRoleAuditAction(scimRoleChange{OldRole: "readonly", NewRole: ""})
	if action != "scim.role_unmapped" || !strings.Contains(detail, "readonly") || !strings.Contains(detail, "member") {
		t.Fatalf("unmapped action=%q detail=%q", action, detail)
	}
	action, detail = adminGroupRoleAuditAction(scimRoleChange{OldRole: "member", NewRole: "admin"})
	if action != "admin.role_mapped" || !strings.Contains(detail, "admin") {
		t.Fatalf("admin mapped action=%q detail=%q", action, detail)
	}
	action, _ = adminGroupRoleAuditAction(scimRoleChange{OldRole: "readonly", NewRole: ""})
	if action != "admin.role_unmapped" {
		t.Fatalf("admin unmapped action=%q", action)
	}
}

func TestSCIMCanonicalMemberIDs(t *testing.T) {
	upper := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	lower := strings.ToLower(upper)
	ids, handlerErr := scimCanonicalMemberIDs([]string{upper, lower, " " + lower + " "})
	if handlerErr != nil || len(ids) != 1 || ids[0] != lower {
		t.Fatalf("canonical ids = %#v err=%v", ids, handlerErr)
	}
	if _, handlerErr := scimCanonicalMemberIDs([]string{"not-a-uuid"}); handlerErr == nil ||
		handlerErr.status != http.StatusBadRequest || handlerErr.scimType != "invalidValue" {
		t.Fatalf("malformed id err = %#v", handlerErr)
	}
}

func TestSCIMGroupRoutesAuthAndLicense(t *testing.T) {
	unlicensed, _ := newSCIMTestServer(t, "https://mail.example.test")
	for _, path := range []string{"/scim/v2/Groups", "/scim/v2/Groups/22222222-2222-4222-8222-222222222222"} {
		recorder := httptest.NewRecorder()
		unlicensed.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unlicensed %s status = %d", path, recorder.Code)
		}
	}

	licensed, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := httptest.NewRecorder()
		licensed.Handler().ServeHTTP(recorder, httptest.NewRequest(method, "/scim/v2/Groups", nil))
		assertSCIMError(t, recorder, http.StatusUnauthorized, "")
	}
	request := httptest.NewRequest(http.MethodGet, "/scim/v2/Groups", nil)
	request.Header.Set("Authorization", "Bearer not-a-scim-token")
	recorder := httptest.NewRecorder()
	licensed.Handler().ServeHTTP(recorder, request)
	assertSCIMError(t, recorder, http.StatusUnauthorized, "")
}

func TestSCIMGroupMethodDispatch(t *testing.T) {
	server, _ := newSCIMTestServer(t, "https://mail.example.test", "scim")
	request := scimTenantRequest(httptest.NewRequest(http.MethodDelete, "/scim/v2/Groups", nil))
	recorder := httptest.NewRecorder()
	server.scimGroupsCollection(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("collection dispatch status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
	request = scimTenantRequest(httptest.NewRequest(http.MethodPost, "/scim/v2/Groups/x", nil))
	recorder = httptest.NewRecorder()
	server.scimGroupsItem(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, PUT, PATCH, DELETE" {
		t.Fatalf("item dispatch status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func TestSCIMGroupMemberValues(t *testing.T) {
	value, handlerErr := decodeSCIMGroupMemberValues(json.RawMessage(fmt.Sprintf(`{"value":%q}`, scimTestAliceID)))
	if handlerErr != nil || len(value) != 1 || value[0] != scimTestAliceID {
		t.Fatalf("object member = %v err=%v", value, handlerErr)
	}
	// Uppercase spellings are canonicalized and duplicates collapse.
	value, handlerErr = decodeSCIMGroupMemberValues(json.RawMessage(fmt.Sprintf(
		`[{"value":%q},{"value": " %s "}]`, strings.ToUpper(scimTestAliceID), scimTestAliceID)))
	if handlerErr != nil || len(value) != 1 || value[0] != scimTestAliceID {
		t.Fatalf("array members = %v err=%v", value, handlerErr)
	}
	if value, handlerErr := decodeSCIMGroupMemberValues(json.RawMessage(`[]`)); handlerErr != nil || len(value) != 0 {
		t.Fatalf("empty array should be a valid no-op: %v err=%v", value, handlerErr)
	}
	for _, raw := range []string{`null`, ``, `{"display":"Alice"}`, `{"value":""}`, `"user-1"`, `{"value":"not-a-uuid"}`, `[{"value":"a"},5]`} {
		if _, handlerErr := decodeSCIMGroupMemberValues(json.RawMessage(raw)); handlerErr == nil ||
			handlerErr.status != http.StatusBadRequest || handlerErr.scimType != "invalidValue" {
			t.Fatalf("decodeSCIMGroupMemberValues(%q) = %v", raw, handlerErr)
		}
	}
}

func TestSCIMGroupPatchChanges(t *testing.T) {
	current := []string{scimTestAliceID}
	parse := func(t *testing.T, body string) (scimGroupChangeSet, *scimHandlerError) {
		t.Helper()
		var payload scimPatchRequest
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("decode patch %q: %v", body, err)
		}
		return scimGroupPatchChanges(payload, current)
	}
	addBody := func(ids ...string) string {
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"value":%q}`, id))
		}
		return `{"Operations":[{"op":"add","path":"members","value":[` + strings.Join(parts, ",") + `]}]}`
	}

	t.Run("add single and array members", func(t *testing.T) {
		changes, err := parse(t, `{"Operations":[{"op":"add","path":"members","value":{"value":"`+scimTestBobID+`"}}]}`)
		if err != nil {
			t.Fatalf("add single: %v", err)
		}
		if !changes.MembersSet || strings.Join(changes.MemberIDs, ",") != scimTestAliceID+","+scimTestBobID {
			t.Fatalf("add single changes = %#v", changes)
		}
		if strings.Join(changes.LiveMemberIDs, ",") != scimTestBobID {
			t.Fatalf("add single live ids = %#v", changes.LiveMemberIDs)
		}
		changes, err = parse(t, addBody(scimTestBobID, scimTestBobID))
		if err != nil {
			t.Fatalf("add array: %v", err)
		}
		if strings.Join(changes.MemberIDs, ",") != scimTestAliceID+","+scimTestBobID {
			t.Fatalf("duplicate add changes = %#v", changes)
		}
		// An uppercase id is the same set member.
		changes, err = parse(t, addBody(strings.ToUpper(scimTestBobID)))
		if err != nil {
			t.Fatalf("uppercase add: %v", err)
		}
		if strings.Join(changes.MemberIDs, ",") != scimTestAliceID+","+scimTestBobID {
			t.Fatalf("uppercase add changes = %#v", changes)
		}
	})

	t.Run("remove non-member is idempotent but referenced", func(t *testing.T) {
		changes, err := parse(t, `{"Operations":[{"op":"remove","path":"members","value":[{"value":"`+scimTestCarolID+`"}]}]}`)
		if err != nil {
			t.Fatalf("remove: %v", err)
		}
		if strings.Join(changes.MemberIDs, ",") != scimTestAliceID {
			t.Fatalf("remove changes = %#v", changes)
		}
		if strings.Join(changes.ReferencedIDs, ",") != scimTestCarolID {
			t.Fatalf("referenced = %#v", changes.ReferencedIDs)
		}
	})

	t.Run("uppercase remove matches the canonical member", func(t *testing.T) {
		changes, err := parse(t, `{"Operations":[{"op":"remove","path":"members","value":[{"value":"`+strings.ToUpper(scimTestAliceID)+`"}]}]}`)
		if err != nil {
			t.Fatalf("uppercase remove: %v", err)
		}
		if len(changes.MemberIDs) != 0 {
			t.Fatalf("uppercase remove did not remove the canonical member: %#v", changes.MemberIDs)
		}
		if strings.Join(changes.ReferencedIDs, ",") != scimTestAliceID {
			t.Fatalf("uppercase remove referenced = %#v", changes.ReferencedIDs)
		}
	})

	t.Run("replace members", func(t *testing.T) {
		changes, err := parse(t, `{"Operations":[{"op":"replace","path":"members","value":[{"value":"`+scimTestBobID+`"}]}]}`)
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if strings.Join(changes.MemberIDs, ",") != scimTestBobID || strings.Join(changes.LiveMemberIDs, ",") != scimTestBobID {
			t.Fatalf("replace changes = %#v", changes)
		}
	})

	t.Run("displayName and externalId", func(t *testing.T) {
		changes, err := parse(t, `{"Operations":[{"op":"replace","path":"displayName","value":"New Name"},{"op":"replace","path":"externalId","value":"ext-2"}]}`)
		if err != nil {
			t.Fatalf("attributes: %v", err)
		}
		if !changes.DisplayNameSet || changes.DisplayName != "New Name" || !changes.ExternalIDSet || changes.ExternalID == nil || *changes.ExternalID != "ext-2" {
			t.Fatalf("attributes changes = %#v", changes)
		}
		changes, err = parse(t, `{"Operations":[{"op":"remove","path":"externalId"}]}`)
		if err != nil {
			t.Fatalf("remove externalId: %v", err)
		}
		if !changes.ExternalIDSet || changes.ExternalID != nil {
			t.Fatalf("remove externalId changes = %#v", changes)
		}
	})

	t.Run("path-less replace object", func(t *testing.T) {
		changes, err := parse(t, `{"Operations":[{"op":"replace","value":{"displayName":"Pathless","externalId":null,"members":[{"value":"`+scimTestBobID+`"}],"schemas":["ignored"]}}]}`)
		if err != nil {
			t.Fatalf("pathless: %v", err)
		}
		if !changes.DisplayNameSet || changes.DisplayName != "Pathless" || !changes.ExternalIDSet || changes.ExternalID != nil ||
			!changes.MembersSet || strings.Join(changes.MemberIDs, ",") != scimTestBobID {
			t.Fatalf("pathless changes = %#v", changes)
		}
	})

	invalid := []struct {
		name     string
		body     string
		scimType string
	}{
		{name: "no operations", body: `{"Operations":[]}`, scimType: "invalidValue"},
		{name: "bad op", body: `{"Operations":[{"op":"merge","path":"displayName","value":"x"}]}`, scimType: "invalidValue"},
		{name: "unknown path", body: `{"Operations":[{"op":"replace","path":"userName","value":"x"}]}`, scimType: "invalidPath"},
		{name: "remove displayName", body: `{"Operations":[{"op":"remove","path":"displayName"}]}`, scimType: "invalidValue"},
		{name: "empty displayName", body: `{"Operations":[{"op":"replace","path":"displayName","value":"  "}]}`, scimType: "invalidValue"},
		{name: "missing member value", body: `{"Operations":[{"op":"add","path":"members","value":{"display":"x"}}]}`, scimType: "invalidValue"},
		{name: "add without path", body: `{"Operations":[{"op":"add","value":{"displayName":"x"}}]}`, scimType: "invalidPath"},
		{name: "pathless unknown key", body: `{"Operations":[{"op":"replace","value":{"password":"x"}}]}`, scimType: "invalidPath"},
		{name: "externalId wrong type", body: `{"Operations":[{"op":"replace","path":"externalId","value":42}]}`, scimType: "invalidValue"},
		{name: "control character", body: `{"Operations":[{"op":"replace","path":"displayName","value":"bad\u0000name"}]}`, scimType: "invalidValue"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.body)
			if err == nil || err.status != http.StatusBadRequest || err.scimType != tc.scimType {
				t.Fatalf("%s: err = %#v", tc.name, err)
			}
		})
	}
}

func TestSCIMGroupReplaceChanges(t *testing.T) {
	displayName := "Research"
	changes, handlerErr := scimGroupReplaceChanges(scimGroupPayload{DisplayName: &displayName})
	if handlerErr != nil || !changes.DisplayNameSet || changes.MembersSet || changes.ExternalIDSet {
		t.Fatalf("displayName only = %#v err=%v", changes, handlerErr)
	}

	empty := []scimGroupMemberPayload{}
	changes, handlerErr = scimGroupReplaceChanges(scimGroupPayload{Members: empty})
	if handlerErr != nil || !changes.MembersSet || len(changes.MemberIDs) != 0 {
		t.Fatalf("empty members = %#v err=%v", changes, handlerErr)
	}

	changes, handlerErr = scimGroupReplaceChanges(scimGroupPayload{
		ExternalID: scimNullableString{Set: true, Value: nil},
	})
	if handlerErr != nil || !changes.ExternalIDSet || changes.ExternalID != nil {
		t.Fatalf("null externalId = %#v err=%v", changes, handlerErr)
	}

	emptyID := ""
	changes, handlerErr = scimGroupReplaceChanges(scimGroupPayload{
		ExternalID: scimNullableString{Set: true, Value: &emptyID},
	})
	if handlerErr != nil || !changes.ExternalIDSet || changes.ExternalID != nil {
		t.Fatalf("empty externalId = %#v err=%v", changes, handlerErr)
	}

	badName := "   "
	if _, handlerErr := scimGroupReplaceChanges(scimGroupPayload{DisplayName: &badName}); handlerErr == nil ||
		handlerErr.scimType != "invalidValue" {
		t.Fatalf("blank displayName err = %v", handlerErr)
	}

	changes, handlerErr = scimGroupReplaceChanges(scimGroupPayload{
		Members: []scimGroupMemberPayload{{Value: " " + strings.ToUpper(scimTestAliceID) + " "}, {Value: scimTestAliceID}, {Value: scimTestBobID}},
	})
	if handlerErr != nil || strings.Join(changes.MemberIDs, ",") != scimTestAliceID+","+scimTestBobID {
		t.Fatalf("member dedupe = %#v err=%v", changes, handlerErr)
	}
}

func TestSCIMGroupInternalEmail(t *testing.T) {
	first := scimGroupInternalEmail("Example.TEST")
	second := scimGroupInternalEmail("example.test")
	if !strings.HasPrefix(first, "scim-") || !strings.HasSuffix(first, "@example.test") {
		t.Fatalf("email = %q", first)
	}
	if first == second {
		t.Fatal("internal emails must be unique")
	}
}
