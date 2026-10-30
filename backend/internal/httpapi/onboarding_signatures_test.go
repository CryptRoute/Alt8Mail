package httpapi

import (
	"strings"
	"testing"
)

func TestOnboardingOnlyAllowsTheGuidedSetupRoutes(t *testing.T) {
	for _, path := range []string{"/api/bootstrap", "/api/session", "/api/password/change", "/api/onboarding", "/api/onboarding/complete"} {
		if !onboardingAllowedPath(path) {
			t.Fatalf("%s should be reachable during onboarding", path)
		}
	}
	for _, path := range []string{"/api/send", "/api/signatures", "/api/profile", "/api/preferences"} {
		if onboardingAllowedPath(path) {
			t.Fatalf("%s must stay blocked until onboarding completes", path)
		}
	}
}

func TestNormalizeSignatureBodiesDerivesBothFormats(t *testing.T) {
	text, html, err := normalizeSignatureBodies("", `<p onclick="steal()">Avery Chen</p><script>alert(1)</script>`)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Avery Chen" {
		t.Fatalf("unexpected plain text %q", text)
	}
	if strings.Contains(html, "script") || strings.Contains(html, "onclick") {
		t.Fatalf("signature HTML was not sanitized: %s", html)
	}
	text, html = normalizeIdentitySignature("Avery Chen\nAlt8Mail", "")
	if !strings.Contains(html, "<p>") || !strings.Contains(html, "Avery Chen") {
		t.Fatalf("plain signature did not get an HTML fallback: %s", html)
	}
	if !strings.Contains(text, "Alt8Mail") {
		t.Fatalf("plain signature changed unexpectedly: %q", text)
	}
}

func TestNormalizeSignatureBodiesRejectsEmptySignature(t *testing.T) {
	if _, _, err := normalizeSignatureBodies("  ", "<p> </p>"); err == nil {
		t.Fatal("empty signature should be rejected")
	}
}

func TestSignatureDefaultsSelectOnePerContext(t *testing.T) {
	defaults := signatureDefaults([]map[string]any{
		{"id": "sig_new", "defaultNew": true, "defaultReply": true, "defaultForward": false},
		{"id": "sig_forward", "defaultNew": false, "defaultReply": false, "defaultForward": true},
	})
	if defaults["new"] != "sig_new" || defaults["reply"] != "sig_new" || defaults["forward"] != "sig_forward" {
		t.Fatalf("unexpected signature defaults: %#v", defaults)
	}
}
