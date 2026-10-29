package httpapi

import (
	"testing"
	"time"
)

func newSyncTestState() map[string]any {
	return map[string]any{
		"messages": []any{
			map[string]any{
				"id": "msg_old", "folder": "inbox", "subject": "Old", "messageId": "<old@example.test>",
				"imapUid": 1, "imapFolder": "inbox", "imapUidValidity": 55, "unread": true,
				"from": map[string]any{"name": "Elena", "email": "elena@example.test"},
				"to":   []any{}, "cc": []any{}, "bcc": []any{}, "date": "2026-10-01T09:00:00Z", "sizeBytes": 100,
			},
		},
		"attachments": map[string]any{},
		"people":      []any{},
	}
}

func syncFetched(uid uint32, subject, messageID string) fetchedMail {
	return fetchedMail{
		UID: uid, UIDValidity: 55, Folder: "inbox", Subject: subject, MessageID: messageID,
		Date: time.Now(), From: []map[string]any{{"name": "Elena", "email": "elena@example.test"}},
		To:       []map[string]any{{"name": "Avery", "email": "avery@example.test"}},
		BodyText: subject, BodyHTML: "<p>" + subject + "</p>", Size: 100,
	}
}

func TestIncrementalMergeUsesLiveUIDs(t *testing.T) {
	state := newSyncTestState()
	user := userRecord{Email: "avery@example.test", Name: "Avery", Initials: "A", Color: "#34557a"}
	// A new message arrives, and both UID 1 and UID 2 are live.
	imported, _, removed := mergeFetchedMailAdvanced(state, user,
		[]fetchedMail{syncFetched(2, "New", "<new@example.test>")}, t.TempDir(),
		map[string]bool{"inbox": true},
		map[string]bool{
			imapMessageKey("inbox", 55, 1): true,
			imapMessageKey("inbox", 55, 2): true,
		}, nil, nil)
	if imported != 1 || removed != 0 {
		t.Fatalf("incremental merge imported=%d removed=%d", imported, removed)
	}
	if len(stateSlice(state, "messages")) != 2 {
		t.Fatalf("expected two messages, got %d", len(stateSlice(state, "messages")))
	}
	// UID 1 is removed by another client; no body is re-downloaded.
	_, _, removed = mergeFetchedMailAdvanced(state, user, nil, t.TempDir(),
		map[string]bool{"inbox": true},
		map[string]bool{imapMessageKey("inbox", 55, 2): true}, nil, nil)
	if removed != 1 || len(stateSlice(state, "messages")) != 1 {
		t.Fatalf("deletion reconciliation removed=%d messages=%d", removed, len(stateSlice(state, "messages")))
	}
}

func TestIncrementalFlagAndUIDValidityReconciliation(t *testing.T) {
	state := newSyncTestState()
	user := userRecord{Email: "avery@example.test", Name: "Avery"}
	updates := []imapFlagUpdate{{
		Folder: "inbox", UIDValidity: 55, UID: 1, Flags: []string{`\Seen`, `\Flagged`}, Size: 140,
	}}
	_, updated, removed := mergeFetchedMailAdvanced(state, user, nil, t.TempDir(),
		map[string]bool{"inbox": true}, map[string]bool{imapMessageKey("inbox", 55, 1): true},
		updates, nil)
	if updated != 1 || removed != 0 {
		t.Fatalf("flag reconciliation updated=%d removed=%d", updated, removed)
	}
	message, _ := stateSlice(state, "messages")[0].(map[string]any)
	if stateBool(message, "unread") || !stateBool(message, "starred") || stateInt(message, "sizeBytes") != 140 {
		t.Fatalf("flags were not applied: %#v", message)
	}
	// A UIDVALIDITY reset drops the old local copies before re-import.
	_, _, removed = mergeFetchedMailAdvanced(state, user,
		[]fetchedMail{{UID: 1, UIDValidity: 77, Folder: "inbox", Subject: "After reset", MessageID: "<reset@example.test>",
			Date: time.Now(), From: []map[string]any{{"name": "Elena", "email": "elena@example.test"}},
			To: []map[string]any{}, BodyText: "body", Size: 10}}, t.TempDir(),
		map[string]bool{"inbox": true}, map[string]bool{imapMessageKey("inbox", 77, 1): true}, nil,
		map[string]bool{"inbox": true})
	if removed != 1 {
		t.Fatalf("UIDVALIDITY reset should remove the stale copy, removed=%d", removed)
	}
	messages := stateSlice(state, "messages")
	if len(messages) != 1 {
		t.Fatalf("expected one post-reset message, got %d", len(messages))
	}
	message, _ = messages[0].(map[string]any)
	if stateString(message, "subject") != "After reset" || stateInt(message, "imapUidValidity") != 77 {
		t.Fatalf("post-reset message is wrong: %#v", message)
	}
}

func TestRepairEncodedSubjects(t *testing.T) {
	state := map[string]any{"messages": []any{
		map[string]any{
			"id": "m1", "subject": "=?Windows-1252?Q?Je_suis_vraiment_d=E9sol=E9,_mon_amour?=",
			"snippet": "=?Windows-1252?Q?Je_suis_vraiment_d=E9sol=E9,_mon_amour?=",
		},
	}}
	repairEncodedSubjects(state)
	message := stateMessages(state)[0].(map[string]any)
	if got := stateString(message, "subject"); got != "Je suis vraiment désolé, mon amour" {
		t.Fatalf("unexpected repaired subject %q", got)
	}
}
