package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

func brandingLicenseServer(t *testing.T, features ...string) *Server {
	t.Helper()
	return &Server{license: &licensing.License{
		Version:   licensing.Version,
		ID:        "lic_branding_test",
		Edition:   "enterprise",
		Seats:     10,
		Features:  features,
		ExpiresAt: time.Now().Add(time.Hour),
	}}
}

func TestBrandingHostnameValidation(t *testing.T) {
	if got := normalizeBrandingHost(" Mail.Example.COM:443. "); got != "mail.example.com" {
		t.Fatalf("normalizeBrandingHost = %q", got)
	}
	valid := []string{"mail.example.com", "mail.example.test", "webmail.sub.example.co.uk"}
	for _, host := range valid {
		if !validBrandingHost(host) {
			t.Errorf("valid host rejected: %q", host)
		}
	}
	invalid := []string{"", "example", "http://mail.example.com", "mail.example.com/path", "*.example.com", "127.0.0.1", "-bad.example.com"}
	for _, host := range invalid {
		if validBrandingHost(host) {
			t.Errorf("invalid host accepted: %q", host)
		}
	}
}

func TestBrandingURLValidation(t *testing.T) {
	valid := []string{"https://cdn.example.com/logo.png", "https://example.com/support?x=1"}
	for _, raw := range valid {
		if !validBrandingURL(raw) {
			t.Errorf("valid URL rejected: %q", raw)
		}
	}
	invalid := []string{"", "http://example.com/logo.png", "https://user:pass@example.com/logo.png", "javascript:alert(1)", "/relative", "https://example.com/" + strings.Repeat("a", 520)}
	for _, raw := range invalid {
		if validBrandingURL(raw) {
			t.Errorf("invalid URL accepted: %q", raw)
		}
	}
}

func TestNormalizeBrandingPolicy(t *testing.T) {
	policy, errors := normalizeBrandingPolicy(adminBrandingPolicy{
		LogoURL:      " https://cdn.example.com/logo.png ",
		SupportURL:   "https://example.com/support",
		LegalURL:     "https://example.com/legal",
		WebmailHosts: []string{"Mail.Example.com", "mail.example.com.", "second.example.com", "bad host"},
	})
	if len(errors) != 1 {
		t.Fatalf("errors = %v; want one invalid-host error", errors)
	}
	if policy.LogoURL != "https://cdn.example.com/logo.png" || len(policy.WebmailHosts) != 2 ||
		policy.WebmailHosts[0] != "mail.example.com" || policy.WebmailHosts[1] != "second.example.com" {
		t.Fatalf("normalized policy = %#v", policy)
	}
	many := adminBrandingPolicy{WebmailHosts: []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com", "f.example.com"}}
	if _, errors := normalizeBrandingPolicy(many); len(errors) != 1 {
		t.Fatalf("six hosts errors = %v; want limit error", errors)
	}
}

func TestBrandingExtendedRequested(t *testing.T) {
	if brandingExtendedRequested(map[string]any{"productName": "Northwind"}) {
		t.Fatal("ordinary branding was treated as extended")
	}
	cases := []map[string]any{
		{"logoUrl": "https://cdn.example.com/logo.png"},
		{"supportUrl": "https://example.com/support"},
		{"legalUrl": "https://example.com/legal"},
		{"hideAttribution": true},
		{"webmailHosts": []any{"mail.example.com"}},
	}
	for _, raw := range cases {
		if !brandingExtendedRequested(raw) {
			t.Errorf("extended branding not detected: %#v", raw)
		}
	}
}

func TestBrandingEntitlementStripping(t *testing.T) {
	policy := adminTenantPolicy{}
	policy.Branding = adminBrandingPolicy{
		ProductName: "Northwind", PrimaryColor: "#123456", SupportEmail: "help@example.com",
		LogoURL: "https://cdn.example.com/logo.png", SupportURL: "https://example.com/support",
		LegalURL: "https://example.com/legal", HideAttribution: true,
		WebmailHosts: []string{"mail.example.com"},
	}
	unentitled := brandingLicenseServer(t)
	stripped := unentitled.applyBrandingEntitlement(policy)
	if stripped.Branding.LogoURL != "" || stripped.Branding.SupportURL != "" || stripped.Branding.LegalURL != "" ||
		stripped.Branding.HideAttribution || len(stripped.Branding.WebmailHosts) != 0 {
		t.Fatalf("unentitled branding not stripped: %#v", stripped.Branding)
	}
	if stripped.Branding.ProductName != "Northwind" || stripped.Branding.PrimaryColor != "#123456" {
		t.Fatalf("existing free branding was stripped: %#v", stripped.Branding)
	}
	payload := unentitled.publicBrandingPayload(policy)
	if payload["logoUrl"] != "" || payload["supportUrl"] != "" || payload["legalUrl"] != "" || payload["hideAttribution"] != false {
		t.Fatalf("unentitled payload leaked extended fields: %#v", payload)
	}
	entitled := brandingLicenseServer(t, "white_label")
	payload = entitled.publicBrandingPayload(policy)
	if payload["logoUrl"] != policy.Branding.LogoURL || payload["hideAttribution"] != true {
		t.Fatalf("entitled payload missing fields: %#v", payload)
	}
	if _, exists := payload["webmailHosts"]; exists {
		t.Fatal("public payload must not expose webmail hostnames")
	}
}
