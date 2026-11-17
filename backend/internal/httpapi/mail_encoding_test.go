package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap"
)

func TestSanitizeMailText(t *testing.T) {
	cases := []string{
		"plain ascii",
		"Acci\xf3n requerida",
		"METROPOLITAN\xa0POLICE",
		"before\x00after",
	}
	for _, input := range cases {
		got := sanitizeMailText(input)
		if !utf8.ValidString(got) {
			t.Fatalf("sanitizeMailText(%q) is not valid UTF-8: %q", input, got)
		}
		if strings.IndexByte(got, 0) >= 0 {
			t.Fatalf("sanitizeMailText(%q) retained NUL: %q", input, got)
		}
	}
	if got := sanitizeMailText("plain ascii"); got != "plain ascii" {
		t.Fatalf("valid text changed: %q", got)
	}
}

func TestSafeFileNameSanitizesRawWindows1252(t *testing.T) {
	got := safeFileName("Payment Confirmation_Swift \x96 MT103.7z")
	if !utf8.ValidString(got) {
		t.Fatalf("filename is not valid UTF-8: %q", got)
	}
	if strings.Contains(got, "\x96") {
		t.Fatalf("filename retained the raw byte: %q", got)
	}
}

func TestParseIMAPMessageSanitizesRawHeaderAndFilename(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"From: METROPOLITAN\xa0POLICE HEADQUARTERS <test@test.com>",
		"To: victim@example.test",
		"Subject: Acci\xf3n requerida: factura pendiente",
		"Message-ID: <raw-1@example.test>",
		"Date: Wed, 28 Oct 2026 09:00:00 +0000",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="mix"`,
		"",
		"--mix",
		`Content-Type: application/zip; name="SOA\xa0KAX 260716.zip"`,
		`Content-Disposition: attachment; filename="Confirmaci\xf3n de datos bancarios.rar"`,
		"Content-Transfer-Encoding: base64",
		"",
		"UEsDBA==",
		"--mix--",
		"",
	}, "\r\n"))
	message := &imap.Message{Uid: 9, Size: uint32(len(raw)), InternalDate: time.Now()}
	parsed := parseIMAPMessage(message, raw, "trash")
	if !utf8.ValidString(parsed.Subject) {
		t.Fatalf("subject is not valid UTF-8: %q", parsed.Subject)
	}
	if !strings.Contains(parsed.Subject, "�") {
		t.Fatalf("subject did not carry the replacement rune: %q", parsed.Subject)
	}
	if strings.Contains(parsed.Subject, "\xf3") {
		t.Fatalf("subject retained invalid UTF-8: %q", parsed.Subject)
	}
	if len(parsed.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %#v", parsed.Attachments)
	}
	name := parsed.Attachments[0].Name
	if !utf8.ValidString(name) {
		t.Fatalf("attachment name is not valid UTF-8: %q", name)
	}
	if strings.Contains(name, "\xa0") || strings.Contains(name, "\xf3") {
		t.Fatalf("attachment name retained raw bytes: %q", name)
	}
	state := map[string]any{"attachments": map[string]any{}, "messages": []any{}}
	user := userRecord{Email: "victim@example.test", Name: "Victim", Initials: "V", Color: "#34557a"}
	imported, _, _ := mergeFetchedMail(state, user, []fetchedMail{parsed}, t.TempDir(), map[string]bool{"trash": true})
	if imported != 1 {
		t.Fatalf("expected message import, got %d", imported)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("state JSON failed: %v", err)
	}
	if !utf8.Valid(encoded) {
		t.Fatal("state JSON is not valid UTF-8")
	}
}

func TestStoreImportedAttachmentSanitizesRecordAndFile(t *testing.T) {
	dir := t.TempDir()
	user := userRecord{Email: "victim@example.test", Name: "Victim", Initials: "V", Color: "#34557a"}
	record, err := storeImportedAttachment(dir, parsedMailAttachment{
		Name: "SOA\xa0KAX 260716.zip", MIMEType: "application/zip",
		ContentID: "cid\x00value", Disposition: "attachment\xf3", Data: []byte("PK\x03\x04"),
	}, user)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(stateString(record, "name")) || strings.Contains(stateString(record, "name"), "\xa0") {
		t.Fatalf("attachment record name was not sanitized: %q", stateString(record, "name"))
	}
	if strings.IndexByte(stateString(record, "contentId"), 0) >= 0 {
		t.Fatalf("content id retained NUL: %q", stateString(record, "contentId"))
	}
	if !utf8.ValidString(stateString(record, "disposition")) {
		t.Fatalf("disposition is not valid UTF-8: %q", stateString(record, "disposition"))
	}
}
