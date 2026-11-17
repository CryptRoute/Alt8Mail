package httpapi

import (
	"strings"
	"testing"
)

func TestSCIMHighestMappedRole(t *testing.T) {
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

func TestAdminGroupRoleAuditAction(t *testing.T) {
	action, detail := adminGroupRoleAuditAction(scimRoleChange{OldRole: "member", NewRole: "admin"})
	if action != "admin.role_mapped" || !strings.Contains(detail, "member") || !strings.Contains(detail, "admin") {
		t.Fatalf("mapped action=%q detail=%q", action, detail)
	}
	action, detail = adminGroupRoleAuditAction(scimRoleChange{OldRole: "readonly", NewRole: ""})
	if action != "admin.role_unmapped" || !strings.Contains(detail, "readonly") || !strings.Contains(detail, "member") {
		t.Fatalf("unmapped action=%q detail=%q", action, detail)
	}
}
