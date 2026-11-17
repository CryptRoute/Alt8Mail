package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMessageSourceHeaders(t *testing.T) {
	raw := "Return-Path: <sender@example.com>\r\n" +
		"Subject: =?UTF-8?B?SGVsbG8gV29ybGQ=?=\r\n" +
		"Message-ID: <abc123@example.com>\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Body"
	subject, messageID := parseMessageSourceHeaders(raw)
	if subject != "Hello World" {
		t.Fatalf("subject = %q, want Hello World", subject)
	}
	if messageID != "abc123@example.com" {
		t.Fatalf("message id = %q", messageID)
	}
}

func TestSplitRawMessage(t *testing.T) {
	headers, body := splitRawMessage("Subject: Hi\r\nX-Test: yes\r\n\r\nLine one\r\nLine two")
	if headers != "Subject: Hi\nX-Test: yes" {
		t.Fatalf("headers = %q", headers)
	}
	if body != "Line one\nLine two" {
		t.Fatalf("body = %q", body)
	}
}

func TestSanitizeMessageSource(t *testing.T) {
	raw := []byte("hello\x00world\xff")
	value, truncated := sanitizeMessageSource(raw, false)
	if truncated {
		t.Fatal("short source should not be truncated")
	}
	if strings.ContainsRune(value, '\x00') {
		t.Fatal("NUL byte was not removed")
	}
	if !strings.Contains(value, "world") {
		t.Fatalf("sanitized value = %q", value)
	}
	large := make([]byte, maxMailSourceBytes+10)
	for i := range large {
		large[i] = 'a'
	}
	_, truncated = sanitizeMessageSource(large, false)
	if !truncated {
		t.Fatal("large source should be truncated")
	}
}

func TestNormalizePostcatSource(t *testing.T) {
	raw := "postcat: foo\n*** ENVELOPE RECORDS ***\nrecipient\n\n*** MESSAGE CONTENTS ***\nReturn-Path: <a@example.com>\nSubject: Test\n\nBody"
	value := normalizePostcatSource(raw)
	if !strings.HasPrefix(value, "Return-Path:") {
		t.Fatalf("normalized source starts with %q", value)
	}
	if !strings.Contains(value, "Subject: Test") {
		t.Fatalf("normalized source lost headers: %q", value)
	}
}

func TestSafeMaildirCandidate(t *testing.T) {
	root := t.TempDir()
	maildir := filepath.Join(root, "example.com", "alice", "Maildir")
	if err := os.MkdirAll(filepath.Join(maildir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := safeMaildirCandidate(root, "alice@example.com"); !ok {
		t.Fatal("valid Maildir was rejected")
	}
	for _, address := range []string{"../etc@example.com", "alice@../example.com", "alice@example.com/../../x", ""} {
		if _, ok := safeMaildirCandidate(root, address); ok {
			t.Fatalf("unsafe address %q was accepted", address)
		}
	}
}

func TestFindMaildirMessageSource(t *testing.T) {
	root := t.TempDir()
	maildir := filepath.Join(root, "example.com", "bob", "Maildir", "new")
	if err := os.MkdirAll(maildir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(maildir, "message.eml")
	content := "Return-Path: <sender@example.net>\r\nSubject: Delivered\r\nX-Queue: ABC123\r\n\r\nHello"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{VmailRoot: root}}
	source, label, truncated := server.findMaildirMessageSource(context.Background(), mailEventSourceEvent{
		Recipient: "bob@example.com", QueueID: "ABC123", EventAt: now,
	})
	if truncated {
		t.Fatal("short Maildir message should not be truncated")
	}
	if label != "Maildir new" {
		t.Fatalf("label = %q", label)
	}
	if !strings.Contains(source, "Subject: Delivered") {
		t.Fatalf("source = %q", source)
	}
	if fallback, _, _ := server.findMaildirMessageSource(context.Background(), mailEventSourceEvent{
		Recipient: "bob@example.com", QueueID: "OTHER", EventAt: now,
	}); fallback == "" {
		t.Fatal("a unique time-adjacent Maildir message should be used when the queue ID is absent")
	}
	second := filepath.Join(maildir, "message-2.eml")
	if err := os.WriteFile(second, []byte("Subject: Another\r\n\r\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(second, now, now); err != nil {
		t.Fatal(err)
	}
	if ambiguous, _, _ := server.findMaildirMessageSource(context.Background(), mailEventSourceEvent{
		Recipient: "bob@example.com", QueueID: "OTHER", EventAt: now,
	}); ambiguous != "" {
		t.Fatal("ambiguous recent Maildir messages should not be guessed")
	}
}

func TestMaildirSourceDoesNotTrustSpoofedInboundSender(t *testing.T) {
	root := t.TempDir()
	senderDir := filepath.Join(root, "example.com", "ceo", "Maildir", ".Sent", "new")
	if err := os.MkdirAll(senderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(senderDir, "sent-copy.eml")
	if err := os.WriteFile(path, []byte("Subject: Another user's message\r\nX-Queue: LOCAL1\r\n\r\nPrivate"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{VmailRoot: root}}
	if source, _, _ := server.findMaildirMessageSource(context.Background(), mailEventSourceEvent{
		Sender: "ceo@example.com", Recipient: "bob@external.test", Direction: "inbound",
		QueueID: "LOCAL1", EventAt: now,
	}); source != "" {
		t.Fatalf("inbound spoofed sender mailbox was searched: %q", source)
	}
	if source, _, _ := server.findMaildirMessageSource(context.Background(), mailEventSourceEvent{
		Sender: "ceo@example.com", Recipient: "bob@external.test", Direction: "outbound",
		QueueID: "LOCAL1", EventAt: now,
	}); source == "" {
		t.Fatal("outbound sender mailbox was not searched")
	}
}

func TestSetEnvFileValueWebNet(t *testing.T) {
	original := "# app env\nALT8MAIL_ENV=production\nALT8MAIL_PUBLIC_URL=https://old.example.com\n"
	updated := setEnvFileValue(original, "ALT8MAIL_PUBLIC_URL", "https://mail.example.com")
	updated = setEnvFileValue(updated, "ALT8MAIL_ADMIN_URL", "https://admin.example.com/")
	if strings.Contains(updated, "old.example.com") {
		t.Fatalf("old value remains: %q", updated)
	}
	if !strings.Contains(updated, "ALT8MAIL_PUBLIC_URL=https://mail.example.com") {
		t.Fatalf("new value missing: %q", updated)
	}
	if !strings.Contains(updated, "ALT8MAIL_ADMIN_URL=https://admin.example.com/") {
		t.Fatalf("appended value missing: %q", updated)
	}
	if !strings.HasPrefix(updated, "# app env\n") {
		t.Fatalf("comment was not preserved: %q", updated)
	}
}
