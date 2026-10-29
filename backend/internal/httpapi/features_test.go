package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

func TestParseRecipientsDeduplicatesAndRejectsInvalid(t *testing.T) {
	state := map[string]any{"people": []any{
		map[string]any{"name": "Elena Vasquez", "email": "elena.vasquez@a8m.test"},
	}}
	people, invalid := parseRecipients("Elena Vasquez <elena.vasquez@a8m.test>, not-an-email, elena.vasquez@a8m.test", state)
	if len(invalid) != 1 || invalid[0] != "not-an-email" {
		t.Fatalf("expected one invalid recipient, got %#v", invalid)
	}
	if len(people) != 1 {
		t.Fatalf("expected one deduplicated recipient, got %#v", people)
	}
	person := people[0].(map[string]any)
	if person["email"] != "elena.vasquez@a8m.test" {
		t.Fatalf("unexpected email: %#v", person)
	}
}

func TestSieveNormalizeRenderAndValidation(t *testing.T) {
	input := map[string]any{
		"rules": []any{
			map[string]any{
				"id": "rule_invoice", "name": "Invoices", "enabled": true, "match": "all",
				"conditions": []any{map[string]any{"field": "subject", "operator": "contains", "value": "invoice"}},
				"action":     map[string]any{"type": "move", "value": "archive"},
			},
		},
		"vacation": map[string]any{
			"enabled": true, "start": "2026-11-01", "end": "2026-11-10", "days": 7,
			"internal": map[string]any{"subject": "Away", "message": "Back soon"},
			"external": map[string]any{"enabled": true, "subject": "Away", "message": "Back soon"},
		},
	}
	rules, vacation, errors := normalizeSieve(input, []string{"inbox", "archive", "spam", "trash"})
	if len(errors) != 0 {
		t.Fatalf("unexpected Sieve errors: %v", errors)
	}
	script := renderSieve(map[string]any{"rules": rules, "vacation": vacation, "revision": 2}, "avery.chen@a8m.test")
	for _, expected := range []string{`require ["date", "fileinto", "relational", "vacation"];`, `fileinto "Archive";`, `vacation :days 7`, `address :domain :is "from" "a8m.test"`} {
		if !strings.Contains(script, expected) {
			t.Fatalf("script missing %q:\n%s", expected, script)
		}
	}

	bad, _, badErrors := normalizeSieve(map[string]any{"rules": []any{
		map[string]any{"name": "Bad", "conditions": []any{map[string]any{"field": "subject", "operator": "contains", "value": ""}}, "action": map[string]any{"type": "move", "value": "outside"}},
	}}, []string{"inbox"})
	if len(bad) != 1 || len(badErrors) < 2 {
		t.Fatalf("expected validation errors, got rules=%#v errors=%v", bad, badErrors)
	}
}

func TestBuildMIMEMessage(t *testing.T) {
	item := map[string]any{
		"from": map[string]any{"name": "Avery Chen", "email": "avery.chen@a8m.test"},
		"to":   []any{map[string]any{"name": "Nora Patel", "email": "nora.patel@a8m.test"}},
		"cc":   []any{}, "bcc": []any{},
		"subject": "Quarterly plan — review", "bodyText": "Hello Nora,\nThe plan is attached.",
	}
	raw, err := buildMIMEMessage(item, "<test@a8m.test>", []outboundAttachment{{Name: "plan.pdf", MIMEType: "application/pdf", Data: []byte("%PDF-1.4")}})
	if err != nil {
		t.Fatal(err)
	}
	message := string(raw)
	for _, expected := range []string{"From: ", "<avery.chen@a8m.test>", "To: ", "<nora.patel@a8m.test>", "Subject: =?utf-8?", "Content-Type: multipart/mixed", `filename=plan.pdf`, "Message-ID: <test@a8m.test>"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message missing %q:\n%s", expected, message)
		}
	}
	if strings.Contains(message, "Bcc:") {
		t.Fatal("Bcc header must not be written")
	}
}

func TestApplyAuthenticationResultsFolded(t *testing.T) {
	fetched := &fetchedMail{Auth: map[string]any{}}
	applyAuthenticationResults(fetched, `mail.a8m.test;
	dkim=pass header.d=hotmail.com header.s=selector1 header.b=abc;
	spf=pass smtp.mailfrom=cryptroute@hotmail.com;
	dmarc=pass (policy=none) header.from=hotmail.com`)
	if fetched.Auth["dkim"] != "pass" || fetched.Auth["spf"] != "pass" || fetched.Auth["dmarc"] != "pass" {
		t.Fatalf("folded authentication results not parsed: %#v", fetched.Auth)
	}
	if fetched.Auth["signer"] != "hotmail.com" {
		t.Fatalf("DKIM signer not parsed: %#v", fetched.Auth)
	}
}

func TestParseIMAPMessageBodiesAndAttachments(t *testing.T) {
	raw := strings.Join([]string{
		"From: Elena Vasquez <elena.vasquez@a8m.test>",
		"To: Avery Chen <avery.chen@a8m.test>",
		"Subject: =?UTF-8?Q?Quarterly_plan_=E2=80=94_review?=",
		"Message-ID: <mail-1@a8m.test>",
		"Date: Wed, 28 Oct 2026 09:00:00 +0000",
		"Authentication-Results: mx.a8m.test; spf=pass smtp.mailfrom=a8m.test; dkim=pass header.d=a8m.test; dmarc=pass",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="mix"`,
		"",
		"--mix",
		`Content-Type: multipart/alternative; boundary="alt"`,
		"",
		"--alt",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"The plan is ready.",
		"--alt",
		"Content-Type: text/html; charset=UTF-8",
		"",
		"<p>The plan is ready.</p>",
		"--alt--",
		"--mix",
		"Content-Type: application/pdf; name=plan.pdf",
		"Content-Disposition: attachment; filename=plan.pdf",
		"Content-Transfer-Encoding: base64",
		"",
		"JVBERi0xLjQ=",
		"--mix--",
		"",
	}, "\r\n")
	message := &imap.Message{
		Uid: 7, Flags: []string{`\Seen`, `\Flagged`}, Size: uint32(len(raw)),
		InternalDate: time.Date(2026, 10, 28, 8, 55, 0, 0, time.UTC),
	}
	parsed := parseIMAPMessage(message, []byte(raw), "inbox")
	if parsed.Subject != "Quarterly plan — review" {
		t.Fatalf("unexpected subject %q", parsed.Subject)
	}
	if !strings.Contains(parsed.BodyText, "The plan is ready.") || !strings.Contains(parsed.BodyHTML, "<p>") {
		t.Fatalf("unexpected bodies: %q / %q", parsed.BodyText, parsed.BodyHTML)
	}
	if len(parsed.Attachments) != 1 || parsed.Attachments[0].Name != "plan.pdf" {
		t.Fatalf("unexpected attachments: %#v", parsed.Attachments)
	}
	if string(parsed.Attachments[0].Data) != "%PDF-1.4" {
		t.Fatalf("attachment decode failed: %q", parsed.Attachments[0].Data)
	}
	if parsed.Auth["spf"] != "pass" || parsed.Auth["dkim"] != "pass" || parsed.Auth["dmarc"] != "pass" {
		t.Fatalf("authentication results not parsed: %#v", parsed.Auth)
	}
}

func TestMergeFetchedMailKeepsSelfSentAndInboxCopies(t *testing.T) {
	state := map[string]any{"messages": []any{
		map[string]any{
			"id": "local_sent", "folder": "sent", "subject": "Self message", "messageId": "<self@a8m.test>",
			"threadId": "t_local", "imapUid": 8, "imapFolder": "sent", "imapUidValidity": 99,
			"from": map[string]any{"name": "Avery", "email": "avery.chen@a8m.test"},
			"to":   []any{}, "cc": []any{}, "bcc": []any{}, "date": "2026-10-28T10:40:34Z", "sizeBytes": 100,
		},
	}, "attachments": map[string]any{}, "people": []any{}}
	user := userRecord{Email: "avery.chen@a8m.test", Name: "Avery Chen", Initials: "AC", Color: "#34557a"}
	inboxCopy := fetchedMail{
		UID: 11, UIDValidity: 99, Folder: "inbox", Subject: "Self message", MessageID: "<self@a8m.test>",
		Date: time.Now(), From: []map[string]any{{"name": "Avery", "email": "avery.chen@a8m.test"}},
		To:       []map[string]any{{"name": "Avery", "email": "avery.chen@a8m.test"}},
		BodyText: "Self message", BodyHTML: "<p>Self message</p>", Size: 120,
	}
	if imported, _, removed := mergeFetchedMail(state, user, []fetchedMail{inboxCopy}, t.TempDir(), nil); imported != 1 || removed != 0 {
		t.Fatalf("quick INBOX scan should add a separate copy, got imported=%d removed=%d", imported, removed)
	}
	messages := stateSlice(state, "messages")
	if len(messages) != 2 {
		t.Fatalf("expected both Sent and INBOX copies, got %d", len(messages))
	}
	sentCopy, _ := messages[0].(map[string]any)
	inboxImported, _ := messages[1].(map[string]any)
	if stateString(sentCopy, "folder") != "sent" || stateString(inboxImported, "folder") != "inbox" {
		t.Fatalf("self-send copies collapsed into %q / %q", stateString(sentCopy, "folder"), stateString(inboxImported, "folder"))
	}
	if stateString(sentCopy, "id") == stateString(inboxImported, "id") {
		t.Fatal("Sent and INBOX copies must have distinct app ids")
	}
	if stateString(sentCopy, "threadId") != stateString(inboxImported, "threadId") {
		t.Fatal("self-send copies should share a conversation thread")
	}

	sentCopyFetched := inboxCopy
	sentCopyFetched.UID = 8
	sentCopyFetched.Folder = "sent"
	sentCopyFetched.Date = time.Now().Add(-time.Minute)
	imported, updated, removed := mergeFetchedMail(state, user, []fetchedMail{inboxCopy, sentCopyFetched}, t.TempDir(), map[string]bool{"inbox": true, "sent": true})
	if imported != 0 || updated != 2 || removed != 0 {
		t.Fatalf("full scan should refresh both copies, got imported=%d updated=%d removed=%d", imported, updated, removed)
	}
	if len(stateSlice(state, "messages")) != 2 {
		t.Fatalf("full scan duplicated a self-send copy: %d messages", len(stateSlice(state, "messages")))
	}
}

func TestMergeFetchedMailReconcilesOrphanSentCopy(t *testing.T) {
	oldDate := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	recentDate := time.Now().UTC().Format(time.RFC3339)
	state := map[string]any{"messages": []any{
		map[string]any{"id": "old_sent", "folder": "sent", "subject": "Old", "messageId": "<old@a8m.test>", "date": oldDate},
		map[string]any{"id": "new_sent", "folder": "sent", "subject": "New", "messageId": "<new@a8m.test>", "date": recentDate},
	}, "attachments": map[string]any{}, "people": []any{}}
	user := userRecord{Email: "avery.chen@a8m.test", Name: "Avery Chen"}
	_, _, removed := mergeFetchedMail(state, user, nil, t.TempDir(), map[string]bool{"sent": true})
	if removed != 1 || len(stateSlice(state, "messages")) != 1 {
		t.Fatalf("expected only the old orphan Sent copy to be removed, got removed=%d messages=%d", removed, len(stateSlice(state, "messages")))
	}
	remaining, _ := stateSlice(state, "messages")[0].(map[string]any)
	if stateString(remaining, "id") != "new_sent" {
		t.Fatalf("recent Sent copy was removed: %#v", remaining)
	}
}

func TestMergeFetchedMailUpdatesMovedMessageInPlace(t *testing.T) {
	state := map[string]any{"messages": []any{
		map[string]any{
			"id": "inbox_copy", "folder": "inbox", "subject": "Move me", "messageId": "<move@a8m.test>",
			"imapUid": 5, "imapFolder": "inbox", "imapUidValidity": 77, "from": map[string]any{"name": "Elena", "email": "elena.vasquez@a8m.test"},
			"to": []any{}, "cc": []any{}, "bcc": []any{}, "date": "2026-10-28T09:00:00Z", "sizeBytes": 90,
		},
	}, "attachments": map[string]any{}, "people": []any{}}
	user := userRecord{Email: "avery.chen@a8m.test", Name: "Avery Chen"}
	moved := fetchedMail{
		UID: 9, UIDValidity: 77, Folder: "archive", Subject: "Move me", MessageID: "<move@a8m.test>",
		Date: time.Now(), From: []map[string]any{{"name": "Elena", "email": "elena.vasquez@a8m.test"}},
		To:       []map[string]any{{"name": "Avery", "email": "avery.chen@a8m.test"}},
		BodyText: "Move me", BodyHTML: "<p>Move me</p>", Size: 90,
	}
	imported, updated, removed := mergeFetchedMail(state, user, []fetchedMail{moved}, t.TempDir(), map[string]bool{"inbox": true, "archive": true})
	if imported != 0 || updated != 1 || removed != 0 {
		t.Fatalf("move should update the existing app message, got imported=%d updated=%d removed=%d", imported, updated, removed)
	}
	messages := stateSlice(state, "messages")
	if len(messages) != 1 {
		t.Fatalf("move should not duplicate a message, got %d", len(messages))
	}
	message, _ := messages[0].(map[string]any)
	if stateString(message, "id") != "inbox_copy" || stateString(message, "folder") != "archive" || stateInt(message, "imapUid") != 9 {
		t.Fatalf("move was not applied in place: %#v", message)
	}
}

func TestMergeFetchedMailDeduplicatesAndSetsThread(t *testing.T) {
	state := map[string]any{"messages": []any{}, "attachments": map[string]any{}, "people": []any{}}
	user := userRecord{Email: "avery.chen@a8m.test", Name: "Avery Chen", Initials: "AC", Color: "#34557a"}
	fetched := []fetchedMail{
		{
			UID: 1, UIDValidity: 99, Folder: "inbox", Subject: "Hello", MessageID: "<one@a8m.test>",
			Date: time.Now(), From: []map[string]any{{"name": "Elena", "email": "elena.vasquez@a8m.test"}},
			To:       []map[string]any{{"name": "Avery", "email": "avery.chen@a8m.test"}},
			BodyText: "Hello", BodyHTML: "<p>Hello</p>", Size: 100,
		},
		{
			UID: 2, UIDValidity: 99, Folder: "inbox", Subject: "Re: Hello", MessageID: "<two@a8m.test>",
			InReplyTo: "<one@a8m.test>", References: []string{"<one@a8m.test>"},
			Date: time.Now().Add(time.Minute), From: []map[string]any{{"name": "Avery", "email": "avery.chen@a8m.test"}},
			To:       []map[string]any{{"name": "Elena", "email": "elena.vasquez@a8m.test"}},
			BodyText: "Reply", BodyHTML: "<p>Reply</p>", Size: 100,
		},
	}
	covered := map[string]bool{"inbox": true}
	imported, updated, removed := mergeFetchedMail(state, user, fetched, t.TempDir(), covered)
	if imported != 2 || updated != 0 || removed != 0 {
		t.Fatalf("expected 2 imported, got %d/%d/%d", imported, updated, removed)
	}
	messages := stateSlice(state, "messages")
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	second, _ := messages[1].(map[string]any)
	if stateString(first, "threadId") != stateString(second, "threadId") {
		t.Fatalf("reply does not share thread: %s vs %s", stateString(first, "threadId"), stateString(second, "threadId"))
	}
	importedAgain, updatedAgain, removedAgain := mergeFetchedMail(state, user, fetched, t.TempDir(), covered)
	if importedAgain != 0 || updatedAgain != 2 || removedAgain != 0 {
		t.Fatalf("dedup failed: imported=%d updated=%d removed=%d", importedAgain, updatedAgain, removedAgain)
	}
	fetched[0].Flags = []string{`\Seen`, `\Flagged`}
	_, updatedFlags, removedMissing := mergeFetchedMail(state, user, fetched[:1], t.TempDir(), covered)
	if updatedFlags != 1 || removedMissing != 1 {
		t.Fatalf("reconciliation failed: updated=%d removed=%d", updatedFlags, removedMissing)
	}
	remaining := stateSlice(state, "messages")
	if len(remaining) != 1 {
		t.Fatalf("expected absent IMAP message removed, got %d", len(remaining))
	}
	restored, _ := remaining[0].(map[string]any)
	if stateBool(restored, "unread") || !stateBool(restored, "starred") {
		t.Fatalf("IMAP flags were not reconciled: %#v", restored)
	}
}

func TestSearchStateMessages(t *testing.T) {
	state := map[string]any{"messages": []any{
		map[string]any{
			"id": "m1", "folder": "inbox", "subject": "Invoice #4821", "bodyText": "Please approve",
			"from": map[string]any{"name": "Elena Vasquez", "email": "elena.vasquez@a8m.test"},
			"to":   []any{}, "cc": []any{}, "date": "2026-10-27T10:00:00Z", "sizeBytes": 5000,
			"attachments": []any{map[string]any{"name": "invoice.pdf", "type": "PDF"}},
		},
		map[string]any{
			"id": "m2", "folder": "archive", "subject": "Roadmap", "bodyText": "Planning",
			"from": map[string]any{"name": "Nora Patel", "email": "nora.patel@a8m.test"},
			"to":   []any{}, "cc": []any{}, "date": "2026-10-20T10:00:00Z", "sizeBytes": 1000,
			"attachments": []any{},
		},
	}}
	if results := searchStateMessages(state, "from:elena has:attachment"); len(results) != 1 {
		t.Fatalf("expected one match, got %d", len(results))
	}
	if results := searchStateMessages(state, "in:archive"); len(results) != 1 {
		t.Fatalf("expected archive match, got %d", len(results))
	}
	if results := searchStateMessages(state, "invoice larger:1k"); len(results) != 1 {
		t.Fatalf("expected size-filtered match, got %d", len(results))
	}
}

func TestStateQuotaAndOutbound(t *testing.T) {
	state := map[string]any{
		"user": map[string]any{
			"quota":    map[string]any{"limitBytes": float64(1000), "baselineUsedBytes": float64(200)},
			"outbound": map[string]any{"limit": float64(10), "windowHours": float64(24), "baselineUsed": float64(2), "events": []any{}},
			"aliases":  []any{},
		},
		"messages": []any{
			map[string]any{"folder": "inbox", "sizeBytes": float64(100)},
			map[string]any{"folder": "sent", "sizeBytes": float64(50)},
		},
		"folders": []any{map[string]any{"id": "inbox", "label": "Inbox"}, map[string]any{"id": "sent", "label": "Sent"}},
		"outbox": []any{
			map[string]any{"id": "s1", "scheduled": true, "sendAt": time.Now().Add(time.Hour).Format(time.RFC3339)},
		},
	}
	quota := stateQuota(state)
	if stateInt(quota, "usedBytes") != 350 || stateInt(quota, "dynamicBytes") != 150 {
		t.Fatalf("unexpected quota: %#v", quota)
	}
	outbound := stateOutbound(state)
	if stateInt(outbound, "usedWithQueued") != 3 || stateInt(outbound, "scheduled") != 1 {
		t.Fatalf("unexpected outbound: %#v", outbound)
	}
}

func TestPublicOutboxItemHidesUndoBody(t *testing.T) {
	state := map[string]any{"outbox": []any{
		map[string]any{"id": "u1", "subject": "Hold", "bodyText": "secret", "sendAt": time.Now().Add(time.Second).Format(time.RFC3339), "scheduled": false},
		map[string]any{"id": "s1", "subject": "Later", "bodyText": "visible", "sendAt": time.Now().Add(time.Hour).Format(time.RFC3339), "scheduled": true},
	}}
	items := statePendingSends(state)
	if len(items) != 2 {
		t.Fatalf("expected two pending items, got %d", len(items))
	}
	undo := items[0].(map[string]any)
	scheduled := items[1].(map[string]any)
	if undo["bodyText"] != "" || scheduled["bodyText"] != "visible" {
		t.Fatalf("outbox body visibility incorrect: %#v / %#v", undo, scheduled)
	}
}
