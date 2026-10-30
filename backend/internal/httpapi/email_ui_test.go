package httpapi

import (
	"net"
	"strings"
	"testing"
)

func TestFolderIMAPMappingSupportsCustomFolders(t *testing.T) {
	state := map[string]any{"folders": []any{
		map[string]any{"id": "inbox", "label": "Inbox", "system": true},
		map[string]any{"id": "folder_projects", "label": "Projects", "imapFolder": "Projects", "system": false},
	}}
	if got := folderIMAPName(state, "inbox"); got != "INBOX" {
		t.Fatalf("inbox mapped to %q", got)
	}
	if got := folderIMAPName(state, "folder_projects"); got != "Projects" {
		t.Fatalf("custom folder mapped to %q", got)
	}
	custom := customFolderIMAPMap(state)
	if custom["projects"] != "folder_projects" {
		t.Fatalf("custom reverse map missing: %#v", custom)
	}
	if !reservedFolderLabel("Junk") || !reservedFolderLabel("Sent Items") {
		t.Fatal("reserved mailbox names must be rejected")
	}
	if reservedFolderLabel("Projects") {
		t.Fatal("ordinary folder name should be allowed")
	}
}

func TestRemoteImageURLValidation(t *testing.T) {
	for _, value := range []string{
		"https://example.com/a.png",
		"http://images.example.net/logo.jpg?size=2",
	} {
		if _, err := validateRemoteImageURL(value); err != nil {
			t.Fatalf("%s should be valid: %v", value, err)
		}
	}
	for _, value := range []string{
		"file:///etc/passwd",
		"http://localhost/a.png",
		"http://127.0.0.1/a.png",
		"https://example.com:8443/a.png",
		"javascript:alert(1)",
	} {
		if _, err := validateRemoteImageURL(value); err == nil {
			t.Fatalf("%s should be rejected", value)
		}
	}
	if publicRemoteIP(net.ParseIP("127.0.0.1")) || publicRemoteIP(net.ParseIP("10.0.0.2")) || publicRemoteIP(net.ParseIP("169.254.1.1")) {
		t.Fatal("private and link-local addresses must not be considered public")
	}
	if !publicRemoteIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address should be accepted")
	}
}

func TestSieveFolderNamesUseCustomDestination(t *testing.T) {
	script := renderSieveWithFolders(map[string]any{
		"rules": []any{map[string]any{
			"name": "Projects", "enabled": true,
			"conditions": []any{map[string]any{"field": "from", "operator": "contains", "value": "@example.com"}},
			"action":     map[string]any{"type": "move", "value": "folder_projects"},
		}},
		"revision": 1,
	}, "avery@a8m.test", map[string]string{"folder_projects": "Projects"})
	if !strings.Contains(script, `fileinto "Projects";`) {
		t.Fatalf("custom folder fileinto missing:\n%s", script)
	}
}

func TestReportReasonNormalization(t *testing.T) {
	cases := map[string]string{"phishing": "phishing", "MALWARE": "malware", "other": "other", "anything": "spam", "": "spam"}
	for input, expected := range cases {
		if got := normalizeReportReason(input); got != expected {
			t.Fatalf("normalizeReportReason(%q) = %q, want %q", input, got, expected)
		}
	}
}
