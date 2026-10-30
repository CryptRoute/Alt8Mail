package httpapi

import (
	"strings"
	"testing"
)

func TestSanitizeComposeHTML(t *testing.T) {
	raw := `<p style="text-align:center;color:#123456" onclick="alert(1)">Hello <strong>team</strong></p>` +
		`<script>alert(1)</script><a href="javascript:alert(1)">bad</a>` +
		`<a href="https://example.com">good</a><iframe src="https://evil.test"></iframe>`
	clean := sanitizeComposeHTML(raw)
	if strings.Contains(strings.ToLower(clean), "script") ||
		strings.Contains(strings.ToLower(clean), "onclick") ||
		strings.Contains(strings.ToLower(clean), "javascript:") ||
		strings.Contains(strings.ToLower(clean), "iframe") {
		t.Fatalf("sanitizer retained active content: %s", clean)
	}
	if !strings.Contains(clean, "text-align") || !strings.Contains(clean, "https://example.com") {
		t.Fatalf("sanitizer removed safe formatting: %s", clean)
	}
}

func TestSanitizeComposeHTMLFonts(t *testing.T) {
	clean := sanitizeComposeHTML(`<p style="font-family:&quot;Segoe UI&quot;, Arial, sans-serif;font-size:15px">Hello</p>`)
	if !strings.Contains(clean, "font-family") || !strings.Contains(clean, "font-size") {
		t.Fatalf("safe font formatting was removed: %s", clean)
	}
	unsafe := sanitizeComposeHTML(`<span style="font-family: Arial; background-image: url(https://evil.test/x); font-size: expression(alert(1))">x</span>`)
	if strings.Contains(strings.ToLower(unsafe), "url(") || strings.Contains(strings.ToLower(unsafe), "expression") {
		t.Fatalf("unsafe font CSS survived sanitization: %s", unsafe)
	}
}

func TestSanitizeComposeHTMLImages(t *testing.T) {
	clean := sanitizeComposeHTML(`<p>Hello</p><img src="https://images.example.test/logo.png" alt="Logo" onerror="alert(1)"><img src="javascript:alert(1)"><img src="/api/mail/image?url=https%3A%2F%2Fimages.example.test%2Fquoted.png" data-email-src="https://images.example.test/quoted.png">`)
	if !strings.Contains(clean, "https://images.example.test/logo.png") {
		t.Fatalf("safe quoted image was removed: %s", clean)
	}
	if !strings.Contains(clean, "/api/mail/image") || !strings.Contains(clean, "data-email-src") {
		t.Fatalf("draft image proxy was removed: %s", clean)
	}
	quoted := sanitizeComposeHTML(`<div class="compose-quote" style="border-top:1px solid #ccc"><p class="compose-quote-lead">On Monday, A wrote:</p><blockquote class="email-quote"><p>Hi</p></blockquote></div><div class="evil-class">x</div>`)
	if !strings.Contains(quoted, `class="compose-quote"`) || !strings.Contains(quoted, `class="email-quote"`) {
		t.Fatalf("quoted-message structure was removed from a draft: %s", quoted)
	}
	if strings.Contains(quoted, "evil-class") {
		t.Fatalf("unsafe class attribute survived: %s", quoted)
	}
	if strings.Contains(strings.ToLower(clean), "onerror") || strings.Contains(strings.ToLower(clean), "javascript:") {
		t.Fatalf("unsafe image attribute survived: %s", clean)
	}
}

func TestSanitizeComposeHTMLLayout(t *testing.T) {
	clean := sanitizeComposeHTML(`<div style="padding:18px;margin:0 0 12px;background:#f4f7fb;border:1px solid #d7e0ea;max-width:100%"><table style="width:100%;border-collapse:collapse"><tr><td style="padding:8px">Cell</td></tr></table></div>`)
	for _, expected := range []string{"padding: 18px", "background: #f4f7fb", "border: 1px solid #d7e0ea", "max-width: 100%", "width: 100%"} {
		if !strings.Contains(clean, expected) {
			t.Fatalf("quoted layout style %q was removed: %s", expected, clean)
		}
	}
	unsafe := sanitizeComposeHTML(`<div style="background:url(https://evil.test/x);position:fixed">x</div>`)
	if strings.Contains(strings.ToLower(unsafe), "url(") || strings.Contains(strings.ToLower(unsafe), "position") {
		t.Fatalf("unsafe layout style survived: %s", unsafe)
	}
}

func TestNormalizeComposeBody(t *testing.T) {
	htmlBody, textBody := normalizeComposeBody("", "Hello\n\nTeam")
	if !strings.Contains(htmlBody, "<p>Hello</p>") || !strings.Contains(textBody, "Hello") {
		t.Fatalf("plain text was not converted: %q / %q", htmlBody, textBody)
	}
	htmlBody, textBody = normalizeComposeBody("<p>Hello <strong>Team</strong></p>", "")
	if strings.TrimSpace(textBody) != "Hello Team" {
		t.Fatalf("HTML text extraction = %q", textBody)
	}
}

func TestRenderForwarding(t *testing.T) {
	lines := renderForwarding(map[string]any{
		"enabled": true, "target": "archive@example.com", "keepCopy": false,
		"permanent": false, "start": "2026-10-01", "end": "2026-12-31",
	})
	script := strings.Join(lines, "\n")
	for _, expected := range []string{"redirect \"archive@example.com\";", "stop;", "currentdate"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("forwarding script missing %q:\n%s", expected, script)
		}
	}
}
