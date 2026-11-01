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

func TestMailScopeProductivityFilters(t *testing.T) {
	me := "avery.chen@a8m.test"
	toMe := map[string]any{"folder": "inbox", "to": []any{map[string]any{"email": me}}}
	if !mailMessageMatchesScope(toMe, "inbox", "toMe", me) {
		t.Fatal("message addressed to the account should match toMe")
	}
	if mailMessageMatchesScope(toMe, "inbox", "toMe", "other@a8m.test") {
		t.Fatal("message addressed elsewhere should not match toMe")
	}
	mention := map[string]any{"folder": "inbox", "bodyText": "Please review this, @avery.chen"}
	if !mailMessageMatchesScope(mention, "inbox", "mentions", me) {
		t.Fatal("mention should match mentions filter")
	}
	flagged := map[string]any{"folder": "inbox", "flagged": true}
	if !mailMessageMatchesScope(flagged, "inbox", "flagged", me) {
		t.Fatal("flagged message should match flagged filter")
	}
	invite := map[string]any{"folder": "inbox", "attachments": []any{map[string]any{"mime": "text/calendar; method=REQUEST"}}}
	if !mailMessageMatchesScope(invite, "inbox", "calendar", me) {
		t.Fatal("calendar attachment should match calendar filter")
	}
	where, _ := mailFolderCondition("user-1", "inbox", "toMe")
	if !strings.Contains(where, "to_text ILIKE") || !strings.Contains(where, "cc_text ILIKE") {
		t.Fatalf("toMe condition missing recipient clauses: %q", where)
	}
}
