package locales

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppliesControlPlaneDisabledList(t *testing.T) {
	manifest, err := Load(filepath.Join("..", "..", "..", "public", "locales", "manifest.json"))
	if err != nil {
		t.Fatalf("load shipped manifest: %v", err)
	}
	response := manifest.Resolve(Policy{Default: "de-DE", Disabled: []string{"es-ES", "fr-FR", "es-ES"}})
	if response.Default != "de-DE" {
		t.Fatalf("default = %q, want de-DE", response.Default)
	}
	if got := optionEnabled(response, "es-ES"); got {
		t.Fatal("es-ES should be disabled")
	}
	if got := optionEnabled(response, "fr-FR"); got {
		t.Fatal("fr-FR should be disabled")
	}
	if !optionEnabled(response, "de-DE") {
		t.Fatal("the default locale must stay enabled")
	}
	if !optionEnabled(response, "en-US") {
		t.Fatal("English (United States) must always stay enabled")
	}
	if len(response.Disabled) != 2 {
		t.Fatalf("disabled list = %#v, want two de-duplicated entries", response.Disabled)
	}
}

func TestResolveFallsBackWhenDefaultIsNotShipped(t *testing.T) {
	manifest := Manifest{Default: "en-US", Locales: []Entry{
		{Code: "en-US", Catalog: "en", Label: "English (United States)"},
		{Code: "de-DE", Catalog: "de", Label: "Deutsch"},
	}}
	response := manifest.Resolve(Policy{Default: "xx-XX", Disabled: []string{"en-US"}})
	if response.Default != "en-US" {
		t.Fatalf("default = %q, want en-US fallback", response.Default)
	}
	if !optionEnabled(response, "en-US") {
		t.Fatal("English fallback must be force-enabled")
	}
}

func TestNormalizeDisabledRejectsFallbackAndDefault(t *testing.T) {
	manifest := Fallback()
	if _, err := manifest.NormalizeDisabled([]string{"en-US"}, "en-US"); err == nil {
		t.Fatal("disabling en-US must fail")
	}
	if _, err := manifest.NormalizeDisabled([]string{"es-ES"}, "es-ES"); err == nil {
		t.Fatal("disabling the current default must fail")
	}
	if _, err := manifest.NormalizeDisabled([]string{"xx-XX"}, "en-US"); err == nil {
		t.Fatal("unknown locales must fail")
	}
	normalized, err := manifest.NormalizeDisabled([]string{"es-ES", "es-ES", "fr-FR"}, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 2 {
		t.Fatalf("normalized = %#v, want two entries", normalized)
	}
}

func TestLoadOrDefaultUsesCompiledManifest(t *testing.T) {
	manifest := LoadOrDefault(filepath.Join(t.TempDir(), "missing"))
	if len(manifest.Locales) != len(Fallback().Locales) {
		t.Fatalf("fallback has %d locales, want %d", len(manifest.Locales), len(Fallback().Locales))
	}
	if manifest.Default != DefaultCode {
		t.Fatalf("fallback default = %q, want %q", manifest.Default, DefaultCode)
	}
}

func TestCatalogLanguageMapsServerCatalogs(t *testing.T) {
	cases := map[string]string{
		"es": "es-ES", "de": "de-DE", "pt": "pt-BR", "zh-Hans": "zh-Hans", "en": "en-US",
	}
	for catalog, want := range cases {
		if got := CatalogLanguage(catalog); got != want {
			t.Errorf("CatalogLanguage(%q) = %q, want %q", catalog, got, want)
		}
	}
}

func TestLoadRejectsDuplicateCodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	raw := `{"default":"en-US","locales":[{"code":"en-US"},{"code":"en-US"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("duplicate locale code must fail validation")
	}
}

func optionEnabled(response Response, code string) bool {
	for _, option := range response.Locales {
		if option.Code == code {
			return option.Enabled
		}
	}
	return false
}
