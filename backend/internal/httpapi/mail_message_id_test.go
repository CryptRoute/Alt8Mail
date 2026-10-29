package httpapi

import "testing"

func TestIMAPMessageIDIsUserAndFolderScoped(t *testing.T) {
	userA := "11111111-1111-1111-1111-111111111111"
	userB := "22222222-2222-2222-2222-222222222222"
	messageID := "<shared@example.test>"
	inboxA := imapMessageID(userA, messageID, "inbox")
	sentA := imapMessageID(userA, messageID, "sent")
	inboxB := imapMessageID(userB, messageID, "inbox")
	if inboxA == inboxB {
		t.Fatal("the same Message-ID must not share a row across users")
	}
	if inboxA == sentA {
		t.Fatal("INBOX and Sent copies must not share a row")
	}
	if again := imapMessageID(userA, messageID, "inbox"); again != inboxA {
		t.Fatal("message id is not deterministic")
	}
}
