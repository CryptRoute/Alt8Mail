package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestMailFolderCondition(t *testing.T) {
	where, args := mailFolderCondition("user-1", "inbox", "all")
	if !strings.Contains(where, "folder = $2") || len(args) != 2 || args[1] != "inbox" {
		t.Fatalf("unexpected inbox condition: %q %#v", where, args)
	}
	where, args = mailFolderCondition("user-1", "starred", "unread")
	if !strings.Contains(where, "starred OR flagged") || !strings.Contains(where, "NOT IN") ||
		!strings.Contains(where, "unread") || len(args) != 1 {
		t.Fatalf("unexpected starred condition: %q %#v", where, args)
	}
	where, args = mailFolderCondition("user-1", "inbox", "attachments")
	if !strings.Contains(where, "mail_message_attachments") || len(args) != 2 {
		t.Fatalf("unexpected attachment condition: %q %#v", where, args)
	}
}

func TestMailMessageMatchesScope(t *testing.T) {
	inboxUnread := map[string]any{"folder": "inbox", "unread": true, "starred": false, "flagged": false}
	if !mailMessageMatchesScope(inboxUnread, "inbox", "all") {
		t.Fatal("inbox message should match inbox")
	}
	if mailMessageMatchesScope(inboxUnread, "archive", "all") {
		t.Fatal("inbox message should not match archive")
	}
	if !mailMessageMatchesScope(inboxUnread, "inbox", "unread") {
		t.Fatal("unread message should match unread filter")
	}
	spamStarred := map[string]any{"folder": "spam", "starred": true}
	if mailMessageMatchesScope(spamStarred, "starred", "all") {
		t.Fatal("starred spam should not appear in Starred")
	}
	withAttachment := map[string]any{"folder": "inbox", "attachments": []any{map[string]any{"id": "att_x"}}}
	if !mailMessageMatchesScope(withAttachment, "inbox", "attachments") {
		t.Fatal("attachment message should match attachment filter")
	}
}

func TestMailScopeHidesIgnoredAndSnoozed(t *testing.T) {
	ignored := map[string]any{"folder": "inbox", "ignored": true}
	if mailMessageMatchesScope(ignored, "inbox", "all") {
		t.Fatal("ignored message must not appear in Inbox")
	}
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	snoozed := map[string]any{"folder": "inbox", "snoozedUntil": future}
	if mailMessageMatchesScope(snoozed, "inbox", "all") {
		t.Fatal("future-snoozed message must not appear in Inbox")
	}
	if !mailMessageMatchesScope(snoozed, "snoozed", "all") {
		t.Fatal("future-snoozed message should appear in Snoozed")
	}
	where, _ := mailFolderCondition("user-1", "snoozed", "all")
	if !strings.Contains(where, "snoozed_until > now()") || !strings.Contains(where, "NOT ignored") {
		t.Fatalf("unexpected snoozed condition: %q", where)
	}
}
