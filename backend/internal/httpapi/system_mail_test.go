package httpapi

import (
	"strings"
	"testing"
)

func TestAccountNoticeEscapesAndKeepsBranding(t *testing.T) {
	text, htmlBody := renderAccountNotice(accountNotice{
		To: "user@example.test", ToName: `<script>alert(1)</script>`, Mailbox: "user@example.test",
		Subject: "Welcome", Heading: "Your mailbox is ready", Intro: "Sign in and finish setup.",
		Password: "Temp!Pass1", FooterNote: "One-time credential.",
	}, "Acme", "Acme Mail", "#123456", "https://mail.example.test/", "support@example.test")
	if strings.Contains(htmlBody, "<script>") {
		t.Fatal("notification HTML did not escape the recipient name")
	}
	if !strings.Contains(htmlBody, "https://mail.example.test/") || !strings.Contains(htmlBody, "Acme Mail") {
		t.Fatal("notification HTML is missing the login URL or product name")
	}
	if !strings.Contains(text, "Email address: user@example.test") || !strings.Contains(text, "Temp!Pass1") {
		t.Fatal("plain-text alternative is missing the mailbox or password")
	}
}
