package office

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatsAndNames(t *testing.T) {
	for _, name := range []string{"Report.DOCX", "plan.xlsx", "deck.pptx", "notes.odt", "sheet.csv", "view.pdf"} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("format for %q was not found", name)
		}
	}
	if _, ok := Lookup("archive.zip"); ok {
		t.Error("zip was accepted as an Office format")
	}
	cases := map[string]string{
		"Report":        "Report.docx",
		"Report.pdf":    "Report.docx",
		"Q3 forecast":   "Q3 forecast.docx",
		"deck.pptx":     "deck.docx",
		"notes.txt.zip": "notes.txt.zip.docx",
	}
	for input, expected := range cases {
		if got := NameWithExtension(input, "docx"); got != expected {
			t.Errorf("NameWithExtension(%q) = %q, want %q", input, got, expected)
		}
	}
	if got := NameWithExtension("Q3 forecast", "xlsx"); got != "Q3 forecast.xlsx" {
		t.Errorf("spreadsheet name = %q", got)
	}
	if got := NameWithExtension("deck.pptx", "pptx"); got != "deck.pptx" {
		t.Errorf("presentation name = %q", got)
	}
	if formats := NewFormats(); len(formats) != 3 {
		t.Errorf("NewFormats() = %v", formats)
	}
	payload := SupportedPayload()
	if len(payload["word"]) == 0 || len(payload["cell"]) == 0 || len(payload["slide"]) == 0 {
		t.Errorf("supported payload is incomplete: %#v", payload)
	}
}

func TestTemplatesAreValidZipPackages(t *testing.T) {
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		data, err := Template(ext)
		if err != nil {
			t.Fatalf("template %s: %v", ext, err)
		}
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("template %s is not a zip: %v", ext, err)
		}
		if len(reader.File) < 3 {
			t.Fatalf("template %s has too few entries: %d", ext, len(reader.File))
		}
		found := false
		for _, entry := range reader.File {
			if entry.Name == "[Content_Types].xml" {
				found = true
			}
		}
		if !found {
			t.Fatalf("template %s has no [Content_Types].xml", ext)
		}
	}
	if _, err := Template("pdf"); err == nil {
		t.Error("pdf template should not exist")
	}
}

func TestURLTokenRoundTripAndTamper(t *testing.T) {
	secret := strings.Repeat("s", 48)
	now := time.Now()
	token, err := SignURLToken(secret, URLToken{
		Kind: TokenKindContent, FileID: "file_1", UserID: "user_1", TenantID: "tenant_1",
		ShareID: "share_1", GuestEmail: "guest@example.com",
		ExpiresAt: now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := VerifyURLToken(secret, token, now)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.FileID != "file_1" || parsed.ShareID != "share_1" || parsed.GuestEmail != "guest@example.com" || parsed.Kind != TokenKindContent {
		t.Fatalf("unexpected token: %#v", parsed)
	}
	if _, err := VerifyURLToken(secret+"x", token, now); err == nil {
		t.Error("token with the wrong secret was accepted")
	}
	parts := strings.Split(token, ".")
	parts[0] = parts[0][:len(parts[0])-1] + "A"
	if _, err := VerifyURLToken(secret, strings.Join(parts, "."), now); err == nil {
		t.Error("tampered token was accepted")
	}
	if _, err := VerifyURLToken(secret, token, now.Add(11*time.Minute)); err == nil {
		t.Error("expired token was accepted")
	}
}

func TestJWTAndDocumentKey(t *testing.T) {
	secret := strings.Repeat("j", 48)
	token, err := SignJWT(secret, map[string]any{"status": 2, "key": "doc-key"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyJWT(secret, token)
	if err != nil {
		t.Fatal(err)
	}
	if status, ok := ClaimInt(claims, "status"); !ok || status != 2 {
		t.Fatalf("status claim = %#v", claims["status"])
	}
	if value := ClaimString(claims, "key"); value != "doc-key" {
		t.Fatalf("key claim = %q", value)
	}
	if _, err := VerifyJWT(secret+"x", token); err == nil {
		t.Error("JWT with the wrong secret was accepted")
	}
	key := NewDocumentKey("odoc_123", 4)
	if !strings.HasPrefix(key, "odoc_123-v4-") || len(key) > 128 {
		t.Fatalf("unexpected document key %q", key)
	}
}
