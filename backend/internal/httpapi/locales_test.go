package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	weblocales "github.com/CryptRoute/Alt8Mail/backend/internal/locales"
)

func TestPublicLocalesAppliesControlPlanePolicy(t *testing.T) {
	server := &Server{cfg: config.Config{PublicDir: filepath.Join("..", "..", "..", "public")}}
	server.localePolicyLoader = func(context.Context) (weblocales.Policy, error) {
		return weblocales.Policy{Default: "de-DE", Disabled: []string{"es-ES"}}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/locales", nil)
	rec := httptest.NewRecorder()
	server.publicLocales(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	var response weblocales.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Default != "de-DE" {
		t.Fatalf("default = %q, want de-DE", response.Default)
	}
	if len(response.Locales) < 30 {
		t.Fatalf("only %d locales returned", len(response.Locales))
	}
	seenPseudo := false
	for _, option := range response.Locales {
		if option.Code == "es-ES" && option.Enabled {
			t.Fatal("es-ES should be disabled")
		}
		if option.Code == "en-US" && !option.Enabled {
			t.Fatal("en-US must always be enabled")
		}
		if option.Code == "de-DE" && !option.Enabled {
			t.Fatal("the default locale must stay enabled")
		}
		if option.Code == "en-XA" {
			seenPseudo = true
		}
	}
	if !seenPseudo {
		t.Fatal("pseudo locale should remain available to the test harness")
	}
}

func TestWebmailLanguageEnabledHonorsPolicy(t *testing.T) {
	server := &Server{cfg: config.Config{PublicDir: filepath.Join("..", "..", "..", "public")}}
	server.localePolicyLoader = func(context.Context) (weblocales.Policy, error) {
		return weblocales.Policy{Default: "en-US", Disabled: []string{"es-ES"}}, nil
	}
	ctx := context.Background()
	if server.webmailLanguageEnabled(ctx, "es-ES") {
		t.Fatal("es-ES must be disabled")
	}
	if !server.webmailLanguageEnabled(ctx, "en-US") {
		t.Fatal("en-US must always be enabled")
	}
	if server.webmailLanguageEnabled(ctx, "xx-XX") {
		t.Fatal("unknown locales must be rejected")
	}
	if !server.webmailLanguageEnabled(ctx, "de-DE") {
		t.Fatal("de-DE should remain enabled")
	}
}

func TestApplyEffectiveLanguagePrecedence(t *testing.T) {
	server := &Server{cfg: config.Config{PublicDir: filepath.Join("..", "..", "..", "public")}}
	server.localePolicyLoader = func(context.Context) (weblocales.Policy, error) {
		return weblocales.Policy{Default: "de-DE", Disabled: []string{"fr-FR"}}, nil
	}
	ctx := context.Background()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"no stored preference follows the system default", ``, "de-DE"},
		{"empty preferences follow the system default", `{}`, "de-DE"},
		{"an explicit enabled preference wins", `{"language":"es-ES"}`, "es-ES"},
		{"an explicit disabled preference falls back to the default", `{"language":"fr-FR"}`, "de-DE"},
		{"an unknown preference falls back to the default", `{"language":"xx-XX"}`, "de-DE"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			prefs := map[string]any{"theme": "dark"}
			got := server.applyEffectiveLanguage(ctx, userRecord{Preferences: json.RawMessage(test.raw)}, prefs)
			if got["language"] != test.want {
				t.Fatalf("language = %v, want %s", got["language"], test.want)
			}
			if got["theme"] != "dark" {
				t.Fatalf("unrelated preferences must be preserved: %#v", got)
			}
		})
	}
}

func TestWebmailDefaultLocaleRejectsDisabledDefault(t *testing.T) {
	server := &Server{cfg: config.Config{PublicDir: filepath.Join("..", "..", "..", "public")}}
	server.localePolicyLoader = func(context.Context) (weblocales.Policy, error) {
		// A stale row can name a disabled default; English is the hard fallback.
		return weblocales.Policy{Default: "fr-FR", Disabled: []string{"fr-FR"}}, nil
	}
	if got := server.webmailDefaultLocale(context.Background()); got != "en-US" {
		t.Fatalf("default locale = %q, want en-US", got)
	}
}

func TestAdminUpdateOwnLanguageRejectsUnavailableLocale(t *testing.T) {
	server := &Server{cfg: config.Config{PublicDir: filepath.Join("..", "..", "..", "public")}}
	server.localePolicyLoader = func(context.Context) (weblocales.Policy, error) {
		return weblocales.Policy{Default: "en-US", Disabled: []string{"fr-FR"}}, nil
	}
	sess := &adminSessionRecord{User: adminUser{ID: "00000000-0000-0000-0000-000000000001", Email: "admin@example.test"}}
	for _, language := range []string{"xx-XX", "fr-FR"} {
		req := httptest.NewRequest(http.MethodPatch, "/admin/api/me/language", strings.NewReader(`{"language":"`+language+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), adminSessionContextKey{}, sess))
		rec := httptest.NewRecorder()
		server.adminUpdateOwnLanguage(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400: %s", language, rec.Code, rec.Body.String())
		}
	}
}

func TestWebmailLanguagePickerAndBootGuardAssets(t *testing.T) {
	admin, err := os.ReadFile(filepath.Join("..", "..", "..", "admin", "admin.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`data-settings-webmail="defaultLocale"`, `api("locales")`} {
		if !strings.Contains(string(admin), marker) {
			t.Fatalf("admin settings are missing %q", marker)
		}
	}
	index, err := os.ReadFile(filepath.Join("..", "..", "..", "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "/boot-guard.js") {
		t.Fatal("webmail shell does not load the boot watchdog")
	}
	if strings.Contains(string(index), `<option value="es-ES">`) {
		t.Fatal("the language picker must not ship a static partial locale list")
	}
}

func TestAdminWebmailDefaultLanguageReadsTenantSettings(t *testing.T) {
	if got := adminWebmailDefaultLanguage(json.RawMessage(`{"webmail":{"defaultLocale":"es-ES"}}`)); got != "es-ES" {
		t.Fatalf("default language = %q, want es-ES", got)
	}
	if got := adminWebmailDefaultLanguage(json.RawMessage(`{"branding":{}}`)); got != "" {
		t.Fatalf("default language = %q, want empty", got)
	}
}
