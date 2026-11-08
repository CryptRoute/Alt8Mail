package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	imap "github.com/emersion/go-imap"
)

func TestExternalFolderRoles(t *testing.T) {
	cases := []struct {
		name       string
		attributes []string
		want       string
	}{
		{"INBOX", []string{`\Inbox`}, "inbox"},
		{"Sent Items", []string{`\Sent`}, "sent"},
		{"Drafts", []string{`\Drafts`}, "drafts"},
		{"Deleted Items", []string{`\Trash`}, "trash"},
		{"Spam", []string{`\Junk`}, "junk"},
		{"Archive", []string{`\Archive`}, "archive"},
		{"[Gmail]/All Mail", []string{`\All`}, "archive"},
		{"[Gmail]/Starred", []string{`\Flagged`}, ""},
		{"Labels/Project", []string{`\HasChildren`}, "custom"},
		{"Sent", nil, "sent"},
	}
	for _, testCase := range cases {
		got := externalFolderRole(&imap.MailboxInfo{Name: testCase.name, Attributes: testCase.attributes})
		if got != testCase.want {
			t.Errorf("externalFolderRole(%q, %#v) = %q, want %q", testCase.name, testCase.attributes, got, testCase.want)
		}
	}
	if externalAppFolder("junk") != "spam" || externalAppFolder("inbox") != "inbox" {
		t.Fatal("role mapping is incorrect")
	}
	if externalAppFolder("custom") != "" {
		t.Fatal("custom folders must use their stored folder id")
	}
}

func TestCapExternalUIDs(t *testing.T) {
	uids := []uint32{9, 3, 7, 1, 5}
	newest := capExternalUIDs(append([]uint32{}, uids...), 2, true)
	if len(newest) != 2 || newest[0] != 7 || newest[1] != 9 {
		t.Fatalf("newest cap = %#v", newest)
	}
	oldest := capExternalUIDs(append([]uint32{}, uids...), 2, false)
	if len(oldest) != 2 || oldest[0] != 1 || oldest[1] != 3 {
		t.Fatalf("oldest cap = %#v", oldest)
	}
	all := capExternalUIDs(append([]uint32{}, uids...), 0, true)
	if len(all) != 5 || all[0] != 1 || all[4] != 9 {
		t.Fatalf("uncapped = %#v", all)
	}
}

func TestDedupeThreadMessagesPrefersCurrentFolder(t *testing.T) {
	messages := []map[string]any{
		{"id": "inbox-copy", "accountId": "macct_1", "messageId": "<same@example.test>", "folder": "inbox"},
		{"id": "label-copy", "accountId": "macct_1", "messageId": "<same@example.test>", "folder": "mfld_label"},
		{"id": "other-account", "accountId": "macct_2", "messageId": "<same@example.test>", "folder": "inbox"},
		{"id": "no-message-id", "accountId": "macct_1", "folder": "inbox"},
	}
	deduped := dedupeThreadMessages(messages, "mfld_label")
	if len(deduped) != 3 {
		t.Fatalf("deduped messages = %#v", deduped)
	}
	if stateString(deduped[0], "id") != "label-copy" || stateString(deduped[1], "id") != "other-account" {
		t.Fatalf("preferred copy was not kept: %#v", deduped)
	}
	all := dedupeThreadMessages(messages, "")
	if len(all) != 3 || stateString(all[0], "id") != "inbox-copy" {
		t.Fatalf("unpreferred dedupe = %#v", all)
	}
}

func TestExternalFetchedMessage(t *testing.T) {
	server := &Server{}
	account := mailAccountRecord{
		ID: "macct_test", UserID: "user-1", TenantID: "tenant-1",
		Email: "person@example.test", DisplayName: "Person",
	}
	user := userRecord{ID: "user-1", TenantID: "tenant-1", Email: "owner@example.test", Name: "Owner"}
	item := fetchedMail{
		UID: 12, UIDValidity: 7, Folder: "inbox",
		Flags: []string{`\Seen`, `\Flagged`}, Size: 128,
		InternalDate: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC),
		Date:         time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC),
		Subject:      "External hello", MessageID: "<external-1@example.test>",
		From:     []map[string]any{{"name": "Sender", "email": "sender@example.test"}},
		To:       []map[string]any{{"name": "Person", "email": "person@example.test"}},
		BodyText: "hello", BodyHTML: "<p>hello</p>",
	}
	message, attachments := server.externalFetchedMessage(context.Background(), account, user, item, "inbox", "INBOX")
	if len(attachments) != 0 {
		t.Fatalf("unexpected attachments: %#v", attachments)
	}
	if message["accountId"] != account.ID || message["folder"] != "inbox" || message["imapFolder"] != "INBOX" {
		t.Fatalf("unexpected message account/folder: %#v", message)
	}
	if message["unread"] != false || message["starred"] != true || message["flagged"] != true {
		t.Fatalf("flag mapping is incorrect: %#v", message)
	}
	threadID := stateString(message, "threadId")
	if threadID == "" || !strings.HasPrefix(stateString(message, "id"), "msg_imap_") {
		t.Fatalf("message identity is incomplete: %#v", message)
	}
	other, _ := server.externalFetchedMessage(context.Background(), account, user, item, "inbox", "INBOX")
	if stateString(other, "id") != stateString(message, "id") || stateString(other, "threadId") != threadID {
		t.Fatal("external message ids are not stable")
	}
}
