package httpapi

import (
	"strings"
	"testing"
)

func TestTenantPolicyDefaultsAndValidation(t *testing.T) {
	policy := tenantPolicyFromSettings(nil)
	if policy.Security.MFAEnforcement != "off" || policy.Security.PasswordMinLength != 12 {
		t.Fatalf("unexpected policy defaults: %#v", policy)
	}
	if len(policy.Mail.BlockedExtensions) == 0 {
		t.Fatal("expected blocked attachment extensions by default")
	}
	policy.Security.PasswordMinLength = 7
	policy.Security.MFAEnforcement = "sometimes"
	_, errors := validateTenantPolicy(policy)
	if len(errors) < 2 {
		t.Fatalf("expected validation errors, got %v", errors)
	}
	policy = defaultTenantPolicy()
	policy.Mail.BlockedExtensions = []string{".EXE", "exe", "js"}
	policy.Sharing.AllowedDomains = []string{"partner.test", "partner.test"}
	policy, errors = validateTenantPolicy(policy)
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if len(policy.Mail.BlockedExtensions) != 2 || policy.Mail.BlockedExtensions[0] != "exe" {
		t.Fatalf("extensions not normalized: %#v", policy.Mail.BlockedExtensions)
	}
}

func TestAdminPermissionNormalization(t *testing.T) {
	permissions := normalizeAdminPermissions([]string{"users.read", "users.read", "system.tenants.manage", "not.a.permission"}, false)
	if len(permissions) != 1 || permissions[0] != "users.read" {
		t.Fatalf("unexpected permissions: %#v", permissions)
	}
	permissions = normalizeAdminPermissions([]string{"users.delete", "policies.manage"}, true)
	if len(permissions) != 2 {
		t.Fatalf("unexpected permissions: %#v", permissions)
	}
	if !adminPermissionAllowed("users.read") || adminPermissionAllowed("bogus") {
		t.Fatal("catalog check failed")
	}
}

func TestGeneratedTemporaryPasswordMeetsDefaultPolicy(t *testing.T) {
	password, err := generateTemporaryPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != 20 {
		t.Fatalf("unexpected password length %d", len(password))
	}
	if errors := validatePasswordAgainstPolicy(password, defaultTenantPolicy().Security); len(errors) > 0 {
		t.Fatalf("generated password rejected: %v", errors)
	}
}

func TestAdminInputHelpers(t *testing.T) {
	if !validSlug("northwind") || validSlug("-bad") || validSlug("Bad Slug") {
		t.Fatal("slug validation failed")
	}
	if !validMailDomain("northwind.test") || validMailDomain("nodot") {
		t.Fatal("domain validation failed")
	}
	if admins := adminIPAllowed([]string{"10.0.0.0/8"}, "10.2.3.4"); !admins {
		t.Fatal("CIDR allowlist failed")
	}
	if adminIPAllowed([]string{"10.0.0.0/8"}, "192.0.2.1") {
		t.Fatal("CIDR allowlist accepted an outside address")
	}
	local, domain := splitEmail("avery@northwind.test")
	if local != "avery" || domain != "northwind.test" {
		t.Fatalf("unexpected email split: %s / %s", local, domain)
	}
	if !strings.Contains(strings.Join(sortedPermissionNames(map[string]bool{"*": true}), ","), "users.read") {
		t.Fatal("wildcard permissions did not expand")
	}
}

func adminPermissionAllowed(key string) bool {
	return adminPermissionKeys()[key]
}
