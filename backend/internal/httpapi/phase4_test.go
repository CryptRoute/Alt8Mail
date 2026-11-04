package httpapi

import (
	"strings"
	"testing"
	"time"
)

func phase4TestPolicy() adminTenantPolicy {
	policy := defaultTenantPolicy()
	policy.DLP.Enabled = true
	policy.DLP.DefaultClassification = "Internal"
	policy.DLP.Rules = []adminDLPRule{
		{
			ID: "rule-warn", Name: "Customer identifiers", Terms: []string{"account number"},
			Fields: []string{"subject", "body"}, Classification: "Confidential",
			Action: "warn", Direction: "outbound", Enabled: true,
		},
		{
			ID: "rule-block", Name: "Restricted launch", Terms: []string{"top secret"},
			Fields: []string{"subject", "body", "attachment"}, Classification: "Restricted",
			Action: "block", Direction: "outbound", Enabled: true,
		},
	}
	return policy
}

func TestEvaluateOutboundDLPBlocksAndUpgrades(t *testing.T) {
	policy := phase4TestPolicy()
	classification, warnings, matches, blocked := evaluateOutboundDLP(
		policy, "Top secret launch", "nothing else", nil, "",
	)
	if !blocked {
		t.Fatal("expected the top secret rule to block")
	}
	if classification != "Restricted" {
		t.Fatalf("expected Restricted, got %q", classification)
	}
	if len(warnings) == 0 || len(matches) == 0 {
		t.Fatalf("expected warnings and matches: %#v %#v", warnings, matches)
	}

	classification, warnings, _, blocked = evaluateOutboundDLP(
		policy, "Renewal", "Please confirm the account number", nil, "Public",
	)
	if blocked {
		t.Fatal("warn rule must not block")
	}
	if classification != "Confidential" {
		t.Fatalf("warn rule should raise classification to Confidential, got %q", classification)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Customer identifiers") {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}

	classification, _, _, blocked = evaluateOutboundDLP(
		policy, "Renewal", "Nothing sensitive", nil, "Confidential",
	)
	if blocked || classification != "Confidential" {
		t.Fatalf("selected classification should be preserved: %q blocked=%v", classification, blocked)
	}

	policy.DLP.Enabled = false
	classification, _, _, blocked = evaluateOutboundDLP(policy, "Top secret", "", nil, "")
	if blocked || classification != "Internal" {
		t.Fatalf("disabled DLP must not inspect content: %q blocked=%v", classification, blocked)
	}
}

func TestValidateSIEMEndpoint(t *testing.T) {
	if _, err := validateSIEMEndpoint("https://siem.example.test/a8m"); err != nil {
		t.Fatalf("public endpoint rejected: %v", err)
	}
	for _, value := range []string{
		"http://siem.example.test/a8m",
		"https://localhost/a8m",
		"https://127.0.0.1/a8m",
		"https://10.1.2.3/a8m",
		"https://siem.example.test:8443/a8m",
		"https://user:pass@siem.example.test/a8m",
		"https://siem.example.test/a8m#fragment",
		"",
	} {
		if _, err := validateSIEMEndpoint(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestSIEMSignatureCoversTimestampAndPayload(t *testing.T) {
	signature := signSIEMPayload("test-secret", 1700000000, []byte(`{"action":"send.queued"}`))
	if len(signature) != 64 {
		t.Fatalf("expected a SHA-256 hex signature, got %q", signature)
	}
	if signature == signSIEMPayload("test-secret", 1700000001, []byte(`{"action":"send.queued"}`)) {
		t.Fatal("signature must change with the timestamp")
	}
	if signature == signSIEMPayload("test-secret", 1700000000, []byte(`{"action":"send.sent"}`)) {
		t.Fatal("signature must change with the payload")
	}
	if signature == signSIEMPayload("other-secret", 1700000000, []byte(`{"action":"send.queued"}`)) {
		t.Fatal("signature must change with the secret")
	}
}

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

func TestDiscoveryWhereProducesConsistentPlaceholders(t *testing.T) {
	filter := discoveryFilter{
		Query: "contract renewal", Folder: "sent", UserEmail: "counsel@example.test",
		Classification: "Confidential", From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	where, args := discoveryWhere("11111111-1111-1111-1111-111111111111", filter)
	if strings.Contains(where, "%!") {
		t.Fatalf("malformed placeholder in %q", where)
	}
	if !strings.Contains(where, "m.tenant_id=$1") {
		t.Fatalf("tenant must remain the first argument: %q", where)
	}
	for index := range args {
		placeholder := "$" + itoaForTest(index+1)
		if !strings.Contains(where, placeholder) {
			t.Fatalf("argument %d is not referenced in %q", index+1, where)
		}
	}
	if len(args) != 6 {
		t.Fatalf("expected six filters, got %d", len(args))
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
