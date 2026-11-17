package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/emersion/go-imap"
)

func TestParseIMAPMessageKeepsEncryptedMessageOpaque(t *testing.T) {
	recipient, err := mailcrypto.GenerateOpenPGPKey("Recipient", "recipient@example.test")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("Content-Type: text/plain; charset=UTF-8\r\n\r\nEncrypted body.\r\n")
	encrypted, err := mailcrypto.EncryptPGPMIME(plaintext, []*openpgp.Entity{recipient.Entity}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.Join([]string{
		"From: Sender <sender@example.test>",
		"To: Recipient <recipient@example.test>",
		"Subject: Encrypted test",
		"Message-ID: <encrypted-1@example.test>",
		"Date: Sun, 15 Nov 2026 00:00:00 +0000",
		"",
	}, "\r\n"))
	raw = append(raw, encrypted...)
	parsed := parseIMAPMessage(&imap.Message{Uid: 42, Size: uint32(len(raw)), InternalDate: time.Now()}, raw, "inbox")
	if !parsed.Crypto.Encrypted || parsed.Crypto.Protocol != mailcrypto.ProtocolOpenPGP {
		t.Fatalf("crypto protection = %#v", parsed.Crypto)
	}
	if parsed.BodyText != "" || parsed.BodyHTML != "" {
		t.Fatalf("encrypted body was parsed: %q / %q", parsed.BodyText, parsed.BodyHTML)
	}
	if len(parsed.Attachments) != 0 {
		t.Fatalf("encrypted attachments were extracted: %#v", parsed.Attachments)
	}
	state := map[string]any{"attachments": map[string]any{}, "messages": []any{}}
	user := userRecord{ID: "user-1", TenantID: "tenant-1", Email: "recipient@example.test", Name: "Recipient"}
	mergeFetchedMail(state, user, []fetchedMail{parsed}, t.TempDir(), map[string]bool{"inbox": true})
	messages := stateMessages(state)
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	message, _ := messages[0].(map[string]any)
	crypto := stateMap(message, "crypto")
	if !stateBool(crypto, "encrypted") || stateString(crypto, "protocol") != "openpgp" {
		t.Fatalf("message crypto = %#v", crypto)
	}
	if stateString(message, "bodyText") != "" || stateString(message, "bodyHtml") != "" {
		t.Fatal("plaintext body leaked into the encrypted message state")
	}
}
