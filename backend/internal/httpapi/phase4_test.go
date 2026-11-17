package httpapi

import (
	"strings"
	"testing"
)

func TestBrandingFooter(t *testing.T) {
	policy := defaultTenantPolicy()
	text, htmlBody := applyBrandingFooter(policy, "Hello", "<p>Hello</p>")
	if text != "Hello" || htmlBody != "<p>Hello</p>" {
		t.Fatal("default branding should not alter outbound mail")
	}
	policy.Branding.ProductName = "Northwind <Mail>"
	policy.Branding.SupportEmail = "help@northwind.test"
	text, htmlBody = applyBrandingFooter(policy, "Hello", "<p>Hello</p>")
	if !strings.Contains(text, "Northwind <Mail>") || !strings.Contains(text, "help@northwind.test") {
		t.Fatalf("text footer missing: %q", text)
	}
	if !strings.Contains(htmlBody, "data-a8m-brand-footer") || !strings.Contains(htmlBody, "&lt;Mail&gt;") {
		t.Fatalf("HTML footer missing or unescaped: %q", htmlBody)
	}
	if strings.Contains(htmlBody, "<Mail>") {
		t.Fatal("product name must be escaped in the HTML footer")
	}
	textAgain, htmlAgain := applyBrandingFooter(policy, text, htmlBody)
	if textAgain != text || htmlAgain != htmlBody {
		t.Fatal("the footer must not be added twice")
	}
}

func itoaForTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
