package httpapi

import "testing"

func TestParseBIMILogoURL(t *testing.T) {
	records := []string{
		"v=BIMI1; a=https://example.test/vmc.pem",
		"v=BIMI1; l=http://insecure.test/logo.svg",
		"v=BIMI1; l=https://cdn.example.test/brand.svg",
	}
	if got := parseBIMILogoURL(records); got != "https://cdn.example.test/brand.svg" {
		t.Fatalf("parseBIMILogoURL = %q", got)
	}
	if got := parseBIMILogoURL([]string{"v=BIMI1; l=javascript:alert(1)"}); got != "" {
		t.Fatalf("unsafe BIMI URL accepted: %q", got)
	}
}

func TestDMARCPolicyEnforced(t *testing.T) {
	for _, record := range []string{"v=DMARC1; p=reject;", "v=DMARC1; p=quarantine; rua=mailto:x@example.test"} {
		if !dmarcPolicyEnforced([]string{record}) {
			t.Fatalf("expected enforced policy for %q", record)
		}
	}
	for _, record := range []string{"v=DMARC1; p=none;", "v=DMARC1; sp=reject;"} {
		if dmarcPolicyEnforced([]string{record}) {
			t.Fatalf("unexpected enforced policy for %q", record)
		}
	}
}

func TestValidAvatarDomain(t *testing.T) {
	for _, domain := range []string{"example.test", "mail.example.test", "xn--bcher-kva.example"} {
		if !validAvatarDomain(domain) {
			t.Fatalf("valid domain rejected: %q", domain)
		}
	}
	for _, domain := range []string{"", "localhost", "127.0.0.1", "http://example.test", "-bad.test", "bad_.test", "example.test/path"} {
		if validAvatarDomain(domain) {
			t.Fatalf("invalid domain accepted: %q", domain)
		}
	}
}

func TestAISupportsVision(t *testing.T) {
	if aiSupportsVision(aiSettings{Provider: "openai", Model: "deepseek-chat"}) {
		t.Fatal("text-only DeepSeek model reported vision support")
	}
	if !aiSupportsVision(aiSettings{Provider: "openai", Model: "gpt-4o-mini"}) {
		t.Fatal("vision-capable OpenAI model was not detected")
	}
	if !aiSupportsVision(aiSettings{Provider: "anthropic", Model: "claude-sonnet"}) {
		t.Fatal("Anthropic model should support image content")
	}
}
