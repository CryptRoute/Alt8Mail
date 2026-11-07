package httpapi

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

func TestOfficeURLValidation(t *testing.T) {
	for _, value := range []string{"https://office.example.com", "http://localhost:8001"} {
		if !validOfficeBaseURL(value) {
			t.Errorf("valid base URL %q was rejected", value)
		}
	}
	for _, value := range []string{"", "not-a-url", "ftp://office.example.com", "https://mail.example.com/path", "https://mail.example.com?x=1"} {
		if validOfficeBaseURL(value) {
			t.Errorf("invalid base URL %q was accepted", value)
		}
	}
	for _, value := range []string{"https://office.example.com", "https://office.example.com/"} {
		if !validOfficeServerURL(value) {
			t.Errorf("valid Office URL %q was rejected", value)
		}
	}
	for _, value := range []string{"http://office.example.com", "https://office.example.com/path", "https://localhost", ""} {
		if validOfficeServerURL(value) {
			t.Errorf("invalid Office URL %q was accepted", value)
		}
	}
}

func TestOfficeCallbackEventKey(t *testing.T) {
	first := officeCallbackEventKey("doc-key", 2, "https://office.example.com/cache/1")
	second := officeCallbackEventKey("doc-key", 2, "https://office.example.com/cache/1")
	third := officeCallbackEventKey("doc-key", 2, "https://office.example.com/cache/2")
	if first != second {
		t.Fatal("identical callbacks produced different event keys")
	}
	if first == third {
		t.Fatal("different callback URLs produced the same event key")
	}
	if len(first) != 64 {
		t.Fatalf("event key length = %d", len(first))
	}
}

func TestOfficeDocumentFetchURL(t *testing.T) {
	base := "https://office.example.com"
	cases := map[string]string{
		"http://127.0.0.1:8082/cache/files/1": "https://office.example.com/cache/files/1",
		"http://onlyoffice/cache/files/1":     "https://office.example.com/cache/files/1",
		"/cache/files/1":                      "https://office.example.com/cache/files/1",
		"https://office.example.com/cache/1":  "https://office.example.com/cache/1",
		"https://cdn.example.com/cache/1":     "https://cdn.example.com/cache/1",
	}
	for input, expected := range cases {
		if got := officeDocumentFetchURL(input, base); got != expected {
			t.Errorf("officeDocumentFetchURL(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestOfficeValidDocumentBytes(t *testing.T) {
	if !officeValidDocumentBytes("docx", []byte("PK\x03\x04rest")) {
		t.Error("valid docx magic was rejected")
	}
	if officeValidDocumentBytes("docx", []byte("<html>error</html>")) {
		t.Error("HTML was accepted as a docx")
	}
	if !officeValidDocumentBytes("pdf", []byte("%PDF-1.7\n")) {
		t.Error("valid PDF magic was rejected")
	}
	if !officeValidDocumentBytes("csv", []byte("a,b\n1,2\n")) {
		t.Error("CSV content was rejected")
	}
	if officeValidDocumentBytes("doc", []byte("not an OLE file")) {
		t.Error("invalid legacy document was accepted")
	}
}

func TestOfficeEditorConfig(t *testing.T) {
	runtime := officeRuntime{Secret: strings.Repeat("s", 48)}
	format, _ := office.Lookup("Report.docx")
	config, err := (&Server{}).officeEditorConfig(runtime, "Report.docx", format, "doc-key", "edit", "theme-dark", "user_1", "Avery", "https://mail.example.com/content", "https://mail.example.com/callback")
	if err != nil {
		t.Fatal(err)
	}
	editorConfig, _ := config["editorConfig"].(map[string]any)
	coEditing, _ := editorConfig["coEditing"].(map[string]any)
	if coEditing["mode"] != "fast" || coEditing["change"] != true || editorConfig["mode"] != "edit" {
		t.Fatalf("editor config = %#v", editorConfig)
	}
	user, _ := editorConfig["user"].(map[string]any)
	if user["id"] != "user_1" || user["name"] != "Avery" {
		t.Fatalf("editor user = %#v", user)
	}
	if token, _ := config["token"].(string); token == "" {
		t.Fatal("editor config token is empty")
	}
	document, _ := config["document"].(map[string]any)
	permissions, _ := document["permissions"].(map[string]any)
	if permissions["edit"] != true {
		t.Fatalf("permissions = %#v", permissions)
	}
}

func TestOfficeAIPrompt(t *testing.T) {
	system, user, err := officeAIPrompt("rewrite", "Old wording here.", "", "")
	if err != nil || !strings.Contains(user, "Old wording here.") || !strings.Contains(system, "plain text") {
		t.Fatalf("rewrite prompt = %q / %q / %v", system, user, err)
	}
	if _, user, err := officeAIPrompt("translate", "Hello", "", "French"); err != nil || !strings.Contains(user, "French") {
		t.Fatalf("translate prompt = %q / %v", user, err)
	}
	if _, _, err := officeAIPrompt("translate", "Hello", "", ""); err == nil {
		t.Fatal("empty target language was accepted")
	}
	if _, _, err := officeAIPrompt("custom", "", "Write a summary", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := officeAIPrompt("bogus", "text", "", ""); err == nil {
		t.Fatal("unsupported action was accepted")
	}
}

func TestOfficeGuestUserID(t *testing.T) {
	first := officeGuestUserID("share_1", "Guest@Example.com")
	second := officeGuestUserID("share_1", "guest@example.com")
	if first != second || !strings.HasPrefix(first, "guest_") {
		t.Fatalf("guest IDs = %q / %q", first, second)
	}
	if first == officeGuestUserID("share_2", "guest@example.com") {
		t.Fatal("guest IDs ignore the share")
	}
	if officeGuestUserID("share_1", "") == officeGuestUserID("share_2", "") {
		t.Fatal("anonymous guest IDs should be unique per open")
	}
}

func TestOfficeDocumentPayloadEditability(t *testing.T) {
	if payload := officeDocumentPayload(officeDocumentRecord{Format: "docx", DocumentType: "word"}); payload["editable"] != true {
		t.Fatalf("docx payload = %#v", payload)
	}
	if payload := officeDocumentPayload(officeDocumentRecord{Format: "doc", DocumentType: "word"}); payload["editable"] != false {
		t.Fatalf("legacy doc payload = %#v", payload)
	}
	if payload := officeDocumentPayload(officeDocumentRecord{Format: "pdf", DocumentType: "pdf"}); payload["editable"] != false {
		t.Fatalf("pdf payload = %#v", payload)
	}
}

func TestOfficeStorageHelpers(t *testing.T) {
	server := &Server{}
	server.cfg.UploadDir = t.TempDir()
	for _, key := range []string{"", ".", "..", "../escape", "dir/file", `dir\file`} {
		if _, ok := server.officeStoragePath(key); ok {
			t.Errorf("unsafe storage key %q was accepted", key)
		}
	}
	key, checksum, err := server.officeStoreBytes([]byte("document bytes"), "docx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "ofv_") || !strings.HasSuffix(key, ".docx") || len(checksum) != 64 {
		t.Fatalf("unexpected stored key %q / checksum %q", key, checksum)
	}
	data, err := server.officeReadStorage(key)
	if err != nil || string(data) != "document bytes" {
		t.Fatalf("stored content was not readable: %q, %v", data, err)
	}
	server.officeRemoveStorage(key)
	if _, err := server.officeReadStorage(key); err == nil {
		t.Fatal("removed version content is still readable")
	}
}

func TestOfficeEditorAssetsEmbedded(t *testing.T) {
	if !bytes.Contains(officeEditorHTML, []byte("/office-editor.js")) {
		t.Error("editor HTML does not load its script")
	}
	if !bytes.Contains(officeEditorJS, []byte("/api/office/documents/")) {
		t.Error("editor script does not call the open endpoint")
	}
	if !strings.Contains(string(officeEditorJS), "a8m_system_csrf") {
		t.Error("editor script does not read the CSRF cookie")
	}
	if !bytes.Contains(officeEditorHTML, []byte("aiPanel")) || !bytes.Contains(officeEditorHTML, []byte("aiFab")) {
		t.Error("editor HTML is missing the AI assistant panel")
	}
	if !bytes.Contains(officeEditorHTML, []byte("[hidden] { display: none !important; }")) {
		t.Error("editor HTML is missing the hidden-element guard")
	}
	if !bytes.Contains(officeEditorJS, []byte("Alt8OfficeAI")) || !bytes.Contains(officeEditorJS, []byte("/api/office/ai")) {
		t.Error("editor script is missing the AI assistant bridge")
	}
	if !bytes.Contains(officeEditorJS, []byte("/api/public/shares/")) {
		t.Error("editor script is missing guest share mode")
	}
}
