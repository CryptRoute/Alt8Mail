package office

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractTextDOCX(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Project Atlas</w:t></w:r></w:p><w:p><w:r><w:t>Account number 1234</w:t></w:r></w:p></w:body></w:document>`,
		"word/header1.xml":  `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Confidential</w:t></w:r></w:p></w:hdr>`,
	})
	text, err := ExtractText("plan.docx", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Project Atlas", "Account number 1234", "Confidential"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestExtractTextXLSXSharedStrings(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"xl/sharedStrings.xml":     `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Customer list</t></si><si><t>social security</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c t="s"><v>0</v></c></row></sheetData></worksheet>`,
	})
	text, err := ExtractText("list.xlsx", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Customer list") || !strings.Contains(text, "social security") {
		t.Fatalf("unexpected text %q", text)
	}
}

func TestExtractTextODF(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:p>Quarterly forecast</text:p><text:p>Restricted figure</text:p></office:text></office:body></office:document-content>`,
	})
	text, err := ExtractText("forecast.odt", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Quarterly forecast") || !strings.Contains(text, "Restricted figure") {
		t.Fatalf("unexpected text %q", text)
	}
}

func TestExtractTextPlainAndRTF(t *testing.T) {
	text, err := ExtractText("notes.txt", []byte("hello\r\nworld"))
	if err != nil || !strings.Contains(text, "hello") || !strings.Contains(text, "world") {
		t.Fatalf("plain text: %q %v", text, err)
	}
	rtf := []byte(`{\rtf1\ansi{\fonttbl\f0 Arial;}\f0 Hello\par Account \'31\'32\'33\'34}`)
	text, err = ExtractText("notes.rtf", rtf)
	if err != nil || !strings.Contains(text, "Hello") || !strings.Contains(text, "1234") {
		t.Fatalf("rtf: %q %v", text, err)
	}
}

func TestExtractTextRejectsUnsupported(t *testing.T) {
	if _, err := ExtractText("photo.png", []byte("not an office file")); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

func TestExtractTextCapsOutput(t *testing.T) {
	huge := strings.Repeat("A", MaxExtractedText+500)
	text, err := ExtractText("huge.txt", []byte(huge))
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > MaxExtractedText {
		t.Fatalf("text was not capped: %d bytes", len(text))
	}
}
