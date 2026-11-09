package httpapi

import (
	"context"
	"testing"
)

func TestIMAPActionSourceFolderResolution(t *testing.T) {
	native := imapActionForMessage(map[string]any{
		"folder": "spam", "imapFolder": "spam",
		"messageId": "<spam-1@example.test>", "imapUid": 42,
	})
	if native.SourceFolder != "spam" || native.SourceIMAPFolder != "spam" {
		t.Fatalf("native action should retain the app folder and stored IMAP value: %#v", native)
	}
	resolved := (&Server{}).actionIMAPFolder(context.Background(), mailAccountRecord{Kind: mailAccountKindNative}, nil, native.SourceFolder)
	if got := sourceIMAPFolderForAction(mailAccountKindNative, native, resolved); got != "Junk" {
		t.Fatalf("native spam must resolve to the Junk mailbox, got %q", got)
	}

	external := imapActionForMessage(map[string]any{
		"folder": "archive", "imapFolder": "[Gmail]/All Mail",
		"messageId": "<gmail-1@example.test>", "imapUid": 7,
	})
	if external.SourceFolder != "archive" || external.SourceIMAPFolder != "[Gmail]/All Mail" {
		t.Fatalf("external action should keep the app folder and remote label: %#v", external)
	}
	if got := sourceIMAPFolderForAction(mailAccountKindExternal, external, "archive"); got != "[Gmail]/All Mail" {
		t.Fatalf("connected accounts must use the provider label, got %q", got)
	}
}
