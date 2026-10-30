package httpapi

import "testing"

func TestParseDelegatedMessageID(t *testing.T) {
	owner, message, ok := parseDelegatedMessageID("dlg:owner-uuid:msg_123")
	if !ok || owner != "owner-uuid" || message != "msg_123" {
		t.Fatalf("unexpected parse: %q %q %v", owner, message, ok)
	}
	if _, _, ok := parseDelegatedMessageID("msg_123"); ok {
		t.Fatal("a local message ID must not parse as delegated")
	}
	if _, _, ok := parseDelegatedMessageID("dlg:owner"); ok {
		t.Fatal("an incomplete delegated ID must be rejected")
	}
}

func TestImportFolderTarget(t *testing.T) {
	options := map[string]any{"includeJunk": false, "includeTrash": false, "includeDrafts": true}
	cases := map[string]string{
		"INBOX": "INBOX", "Sent Items": "Sent", "Drafts": "Drafts",
		"Archive": "Archive", "Projects": "Projects",
	}
	for source, want := range cases {
		got, include := importFolderTarget(source, options)
		if !include || got != want {
			t.Fatalf("%s: got %q include=%v", source, got, include)
		}
	}
	if _, include := importFolderTarget("Junk", options); include {
		t.Fatal("Junk must be excluded by default")
	}
	options["includeJunk"] = true
	if got, include := importFolderTarget("Junk E-mail", options); !include || got != "Junk" {
		t.Fatalf("junk mapping: %q %v", got, include)
	}
}

func TestRawMessageHeaderHelpers(t *testing.T) {
	raw := []byte("From: Avery <avery@example.test>\r\nReceived: from mx (mx.example.test [203.0.113.9]) by mail.example.test\r\nSubject: Hello\r\nMessage-ID: <m1@example.test>\r\n\r\nBody")
	headers, body := splitRawMessage(raw)
	if body != "Body" {
		t.Fatalf("unexpected body: %q", body)
	}
	if countHeaderLines(headers) != 4 {
		t.Fatalf("unexpected header count: %d", countHeaderLines(headers))
	}
	if ip := headerSenderIP(headers); ip != "203.0.113.9" {
		t.Fatalf("unexpected sender IP: %q", ip)
	}
	if id := rawMessageID(raw); id != "<m1@example.test>" {
		t.Fatalf("unexpected message id: %q", id)
	}
}

func TestNormalizeImportQuota(t *testing.T) {
	if quota := normalizeImportQuota(userImportRow{}); quota != 16*1024*1024*1024 {
		t.Fatalf("default quota = %d", quota)
	}
	if quota := normalizeImportQuota(userImportRow{QuotaGB: 32}); quota != 32*1024*1024*1024 {
		t.Fatalf("GB quota = %d", quota)
	}
	if quota := normalizeImportQuota(userImportRow{QuotaBytes: 1}); quota != minImportQuotaByte {
		t.Fatalf("minimum quota = %d", quota)
	}
}
