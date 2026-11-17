package control

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	weblocales "github.com/CryptRoute/Alt8Mail/backend/internal/locales"
)

func TestLanguagePolicyHandlerAppliesAndPersists(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "public", "locales", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "locales", "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	config := webmailLocaleConfig{Default: "en-US"}
	server := &Server{cfg: Config{WebRoot: root}}
	server.localeConfigLoader = func(context.Context) (webmailLocaleConfig, error) { return config, nil }
	server.localeConfigSaver = func(_ context.Context, next webmailLocaleConfig, _ string) error {
		config = next
		return nil
	}
	server.auditSink = func(context.Context, *adminUser, string, string, string, net.IP, string, any) {}
	sess := &sessionRecord{User: adminUser{ID: "00000000-0000-0000-0000-000000000001", Email: "operator@example.test"}}

	get := httptest.NewRequest(http.MethodGet, "/control/api/languages", nil)
	get.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	server.handleLanguages(rec, get, sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", rec.Code, rec.Body.String())
	}
	var initial weblocales.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Default != "en-US" || len(initial.Locales) < 30 {
		t.Fatalf("unexpected initial policy: %#v", initial)
	}

	body := `{"default":"de-DE","disabled":["es-ES","fr-FR"]}`
	put := httptest.NewRequest(http.MethodPut, "/control/api/languages", strings.NewReader(body))
	put.RemoteAddr = "127.0.0.1:1234"
	rec = httptest.NewRecorder()
	server.handleSaveLanguages(rec, put, sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	if config.Default != "de-DE" || len(config.Disabled) != 2 {
		t.Fatalf("saved config = %#v", config)
	}
	var updated weblocales.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Default != "de-DE" {
		t.Fatalf("updated default = %q, want de-DE", updated.Default)
	}
	for _, option := range updated.Locales {
		if option.Code == "es-ES" && option.Enabled {
			t.Fatal("es-ES should be disabled in the response")
		}
	}
}

func TestLanguagePolicyHandlerRejectsDisablingEnglish(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "public", "locales", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "locales", "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{WebRoot: root}}
	server.localeConfigLoader = func(context.Context) (webmailLocaleConfig, error) {
		return webmailLocaleConfig{Default: "en-US"}, nil
	}
	server.localeConfigSaver = func(context.Context, webmailLocaleConfig, string) error {
		t.Fatal("invalid policy must not be saved")
		return nil
	}
	server.auditSink = func(context.Context, *adminUser, string, string, string, net.IP, string, any) {}
	sess := &sessionRecord{User: adminUser{ID: "00000000-0000-0000-0000-000000000001"}}
	req := httptest.NewRequest(http.MethodPut, "/control/api/languages", strings.NewReader(`{"default":"en-US","disabled":["en-US"]}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	server.handleSaveLanguages(rec, req, sess)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestLanguageControlAssetsAreEmbedded(t *testing.T) {
	view, err := webFiles.ReadFile("web/views-languages.js")
	if err != nil {
		t.Fatalf("languages view is not embedded: %v", err)
	}
	for _, marker := range []string{"renderLanguages", "/control/api/languages", "data-locale-code", "Default system language"} {
		if !strings.Contains(string(view), marker) {
			t.Fatalf("languages view is missing %q", marker)
		}
	}
	app, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("control app is not embedded: %v", err)
	}
	if !strings.Contains(string(app), "renderLanguages") || !strings.Contains(string(app), "#/languages") {
		t.Fatal("control app does not route the Languages view")
	}
}

func TestOwnLanguageRejectsUnavailableLocale(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "public", "locales", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "locales", "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{WebRoot: root}}
	server.localeConfigLoader = func(context.Context) (webmailLocaleConfig, error) {
		return webmailLocaleConfig{Default: "en-US", Disabled: []string{"fr-FR"}}, nil
	}
	sess := &sessionRecord{User: adminUser{ID: "00000000-0000-0000-0000-000000000001", Email: "operator@example.test"}}
	for _, language := range []string{"xx-XX", "fr-FR"} {
		req := httptest.NewRequest(http.MethodPatch, "/control/api/me/language", strings.NewReader(`{"language":"`+language+`"}`))
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		server.handleOwnLanguage(rec, req, sess)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400: %s", language, rec.Code, rec.Body.String())
		}
	}
}

func TestPublicLocalesReturnsSystemDefaultAndEnabledList(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "public", "locales", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "locales", "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{WebRoot: root}}
	server.localeConfigLoader = func(context.Context) (webmailLocaleConfig, error) {
		return webmailLocaleConfig{Default: "de-DE", Disabled: []string{"es-ES"}}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/control/api/locales", nil)
	rec := httptest.NewRecorder()
	server.handlePublicLocales(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var response weblocales.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Default != "de-DE" {
		t.Fatalf("default = %q, want de-DE", response.Default)
	}
	for _, option := range response.Locales {
		if option.Code == "es-ES" && option.Enabled {
			t.Fatal("es-ES should be disabled for the control console too")
		}
	}
}
