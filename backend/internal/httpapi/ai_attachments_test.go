package httpapi

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipTestDocument(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestAIAttachmentKind(t *testing.T) {
	cases := []struct {
		name string
		mime string
		want string
	}{
		{"quarterly.pdf", "application/pdf", "pdf"},
		{"plan.docx", "application/octet-stream", "docx"},
		{"budget.xls", "", "xls"},
		{"metrics.csv", "text/csv", "csv"},
		{"photo.PNG", "image/png", "image"},
		{"logo.svg", "image/svg+xml", ""},
		{"slides.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", ""},
		{"archive.zip", "application/zip", ""},
	}
	for _, test := range cases {
		if got := aiAttachmentKind(test.name, test.mime); got != test.want {
			t.Fatalf("aiAttachmentKind(%q, %q) = %q, want %q", test.name, test.mime, got, test.want)
		}
	}
}

func TestExtractDOCXText(t *testing.T) {
	data := zipTestDocument(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Quarterly plan</w:t></w:r></w:p><w:p><w:r><w:t>Ship the beta in November.</w:t></w:r></w:p></w:body></w:document>`,
	})
	text := extractDOCXText(data)
	if !strings.Contains(text, "Quarterly plan") || !strings.Contains(text, "Ship the beta in November.") {
		t.Fatalf("unexpected DOCX text: %q", text)
	}
}

func TestExtractXLSXText(t *testing.T) {
	data := zipTestDocument(t, map[string]string{
		"xl/sharedStrings.xml": `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Revenue</t></si><si><t>Forecast</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
			<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
			<row r="2"><c r="A2"><v>42</v></c></row>
		</sheetData></worksheet>`,
	})
	text := extractXLSXText(data)
	for _, expected := range []string{"A1: Revenue", "B1: Forecast", "A2: 42"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("XLSX text missing %q: %q", expected, text)
		}
	}
}

func TestCleanExtractedTextStripsControls(t *testing.T) {
	got := cleanExtractedText("Hello\x00  world\r\n\tNext\x07 line")
	if got != "Hello world\n\tNext line" {
		t.Fatalf("cleanExtractedText = %q", got)
	}
}
