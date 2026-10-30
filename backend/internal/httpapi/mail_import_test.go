package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMailImportRequestAcceptsNestedAndLegacyOptions(t *testing.T) {
	payloads := []string{
		`{"protocol":"imap","host":"mail.example.com","port":993,"security":"ssl","items":[{"email":"one@example.com","password":"secret"}],"options":{"includeJunk":true,"includeTrash":true,"includeDrafts":false,"maxMessageMB":25}}`,
		`{"protocol":"imap","host":"mail.example.com","port":993,"security":"ssl","items":[{"email":"one@example.com","password":"secret"}],"includeJunk":true,"includeTrash":true,"includeDrafts":false,"maxMessageMB":25,"options":{"includeJunk":true,"includeTrash":true,"includeDrafts":false,"maxMessageMB":25}}`,
	}
	for _, payload := range payloads {
		var body mailImportRequestBody
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatalf("request body was rejected: %v", err)
		}
		options := body.normalizedOptions()
		if options["includeJunk"] != true || options["includeTrash"] != true || options["includeDrafts"] != false || options["maxMessageMB"] != 25 {
			t.Fatalf("unexpected normalized options: %#v", options)
		}
	}
}

func TestMailImportOptionsNormalization(t *testing.T) {
	body := mailImportRequestBody{
		Options: &mailImportRequestOptions{
			IncludeJunk:   boolPtr(true),
			IncludeDrafts: boolPtr(false),
			MaxMessageMB:  intPtr(125),
		},
	}
	options := body.normalizedOptions()
	if options["includeJunk"] != true || options["includeTrash"] != false || options["includeDrafts"] != false {
		t.Fatalf("unexpected flag normalization: %#v", options)
	}
	if options["maxMessageMB"] != mailImportHardMaxMB {
		t.Fatalf("max message size was not clamped: %#v", options)
	}
	if got := mailImportMaxBytes(options); got != int64(mailImportHardMaxMB)<<20 {
		t.Fatalf("max bytes = %d", got)
	}

	legacy := mailImportRequestBody{IncludeJunk: boolPtr(true), IncludeDrafts: boolPtr(false), MaxMessageMB: intPtr(25)}
	legacyOptions := legacy.normalizedOptions()
	if legacyOptions["includeJunk"] != true || legacyOptions["includeDrafts"] != false {
		t.Fatalf("legacy flags were not accepted: %#v", legacyOptions)
	}
	if got := mailImportMaxBytes(legacyOptions); got != 25<<20 {
		t.Fatalf("legacy max bytes = %d", got)
	}
}

func TestNormalizeMailImportItems(t *testing.T) {
	items, err := normalizeMailImportItems([]mailImportItemInput{
		{Email: "One@Example.com", Password: "secret", NotifyEmail: "Notify@Example.net"},
		{Email: "two@example.com", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Email != "one@example.com" || items[0].NotifyEmail != "notify@example.net" {
		t.Fatalf("addresses were not normalized: %#v", items)
	}
	if _, err := normalizeMailImportItems([]mailImportItemInput{
		{Email: "one@example.com", Password: "a"}, {Email: "ONE@example.com", Password: "b"},
	}); err == nil {
		t.Fatal("duplicate mailboxes must be rejected")
	}
	if _, err := normalizeMailImportItems([]mailImportItemInput{{Email: "not-an-email", Password: "a"}}); err == nil {
		t.Fatal("invalid email must be rejected")
	}
	if _, err := normalizeMailImportItems([]mailImportItemInput{{Email: "one@example.com", Password: ""}}); err == nil {
		t.Fatal("missing password must be rejected")
	}
}

func TestValidMailImportHost(t *testing.T) {
	for _, host := range []string{"mail.example.com", "130.94.64.32", "localhost", "2001:db8::10"} {
		if !validMailImportHost(host) {
			t.Fatalf("expected %q to be valid", host)
		}
	}
	for _, host := range []string{"", "bad host", "https://mail.example.com", "-bad.example.com", "bad..example.com"} {
		if validMailImportHost(host) {
			t.Fatalf("expected %q to be rejected", host)
		}
	}
}

func boolPtr(value bool) *bool { return &value }
func intPtr(value int) *int    { return &value }

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
