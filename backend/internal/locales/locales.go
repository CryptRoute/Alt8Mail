// Package locales reads the webmail locale manifest and applies the Control
// Plane's enabled-language policy.
//
// The manifest in public/locales/manifest.json is the single source of truth
// for locale codes, catalog names, native labels and text direction. The
// Control Plane persists only the operator's default locale and the list of
// languages they have disabled; the manifest itself stays immutable release
// content so an update can add catalogs without a database migration.
package locales

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultCode is the documented hard fallback. The Control Plane may choose a
// different default, but English (United States) can never be disabled.
const DefaultCode = "en-US"

// Entry is one locale row in public/locales/manifest.json. Fields not used by
// the language policy are ignored by the response types below.
type Entry struct {
	Code       string `json:"code"`
	Catalog    string `json:"catalog"`
	Label      string `json:"label"`
	Dir        string `json:"dir"`
	Status     string `json:"status"`
	Enabled    *bool  `json:"enabled"`
	Hidden     bool   `json:"hidden"`
	Wave       int    `json:"wave,omitempty"`
	DraftReady bool   `json:"draftReady,omitempty"`
	ReviewedAt string `json:"reviewedAt,omitempty"`
	ReviewedBy string `json:"reviewedBy,omitempty"`
	SourceHash string `json:"sourceHash,omitempty"`
}

// IsEnabled treats an omitted enabled flag as enabled, matching the webmail
// runtime's historical behavior.
func (e Entry) IsEnabled() bool {
	return e.Enabled == nil || *e.Enabled
}

// IsPseudo reports whether the entry is the pseudo-localization test locale.
// It is selectable only by explicitly stored values and never shown in a
// language picker.
func (e Entry) IsPseudo() bool {
	return e.Status == "pseudo" || strings.EqualFold(strings.TrimSpace(e.Code), "en-XA")
}

// Manifest mirrors the published manifest shape.
type Manifest struct {
	Default string  `json:"default"`
	Locales []Entry `json:"locales"`
}

// Policy is the operator-managed part of language availability.
type Policy struct {
	Default  string   `json:"default"`
	Disabled []string `json:"disabled"`
}

// Option is a manifest entry with the effective enabled flag applied. It is
// intentionally smaller than Entry so control and webmail UIs get a stable
// JSON contract.
type Option struct {
	Code    string `json:"code"`
	Catalog string `json:"catalog"`
	Label   string `json:"label"`
	Dir     string `json:"dir"`
	Status  string `json:"status"`
	Hidden  bool   `json:"hidden"`
	Enabled bool   `json:"enabled"`
}

// Response is returned by GET /api/locales in webmail and by the Control
// Plane's language endpoints.
type Response struct {
	Default  string   `json:"default"`
	Locales  []Option `json:"locales"`
	Disabled []string `json:"disabled"`
}

// ManifestPath returns the release-owned manifest path for a public web root.
func ManifestPath(publicDir string) string {
	return filepath.Join(publicDir, "locales", "manifest.json")
}

// Load reads and validates a manifest file.
func Load(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse locale manifest: %w", err)
	}
	if len(manifest.Locales) == 0 {
		return Manifest{}, fmt.Errorf("locale manifest %s has no locales", path)
	}
	seen := make(map[string]bool, len(manifest.Locales))
	for _, entry := range manifest.Locales {
		code := strings.TrimSpace(entry.Code)
		if code == "" {
			return Manifest{}, fmt.Errorf("locale manifest %s contains an empty code", path)
		}
		if seen[strings.ToLower(code)] {
			return Manifest{}, fmt.Errorf("locale manifest %s repeats %s", path, code)
		}
		seen[strings.ToLower(code)] = true
	}
	return manifest, nil
}

// LoadOrDefault falls back to the compiled manifest when the release asset is
// missing or damaged. A broken static file must never reduce the language
// picker to English only.
func LoadOrDefault(publicDir string) Manifest {
	manifest, err := Load(ManifestPath(publicDir))
	if err == nil {
		return manifest
	}
	return Fallback()
}

// Entry looks up a code case-insensitively.
func (m Manifest) Entry(code string) (Entry, bool) {
	code = strings.TrimSpace(code)
	for _, entry := range m.Locales {
		if strings.EqualFold(entry.Code, code) {
			return entry, true
		}
	}
	return Entry{}, false
}

// Resolve applies an operator policy to the manifest. English (United States)
// and the active default stay enabled even if a stale database row or a race
// says otherwise; the UI must always have a working fallback.
func (m Manifest) Resolve(policy Policy) Response {
	disabled := normalizeDisabled(policy.Disabled)
	blocked := make(map[string]bool, len(disabled))
	for _, code := range disabled {
		blocked[strings.ToLower(code)] = true
	}

	defaultCode := strings.TrimSpace(policy.Default)
	if defaultCode == "" {
		defaultCode = strings.TrimSpace(m.Default)
	}
	if entry, ok := m.Entry(defaultCode); !ok || entry.IsPseudo() || entry.Hidden || !entry.IsEnabled() || blocked[strings.ToLower(entry.Code)] {
		defaultCode = DefaultCode
	}
	if _, ok := m.Entry(defaultCode); !ok {
		defaultCode = m.firstEnabled()
	}
	if defaultCode == "" {
		defaultCode = DefaultCode
	}

	options := make([]Option, 0, len(m.Locales))
	for _, entry := range m.Locales {
		code := strings.TrimSpace(entry.Code)
		enabled := entry.IsEnabled() && !blocked[strings.ToLower(code)]
		if strings.EqualFold(code, DefaultCode) || strings.EqualFold(code, defaultCode) {
			enabled = true
		}
		options = append(options, Option{
			Code:    code,
			Catalog: entry.Catalog,
			Label:   entry.Label,
			Dir:     entry.Dir,
			Status:  entry.Status,
			Hidden:  entry.Hidden,
			Enabled: enabled,
		})
	}
	return Response{Default: defaultCode, Locales: options, Disabled: disabled}
}

func (m Manifest) firstEnabled() string {
	for _, entry := range m.Locales {
		if entry.IsEnabled() && !entry.Hidden && !entry.IsPseudo() {
			return entry.Code
		}
	}
	return ""
}

// NormalizeDefault validates an operator-selected default against a manifest.
func (m Manifest) NormalizeDefault(code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("default locale is required")
	}
	entry, ok := m.Entry(code)
	if !ok {
		return "", fmt.Errorf("%s is not a shipped locale", code)
	}
	if entry.IsPseudo() || entry.Hidden {
		return "", fmt.Errorf("%s is not available to webmail users", code)
	}
	if !entry.IsEnabled() {
		return "", fmt.Errorf("%s is not enabled in the locale manifest", code)
	}
	return entry.Code, nil
}

// NormalizeDisabled validates, de-duplicates and canonicalizes a disabled
// list. The default locale and English (United States) can never be disabled.
func (m Manifest) NormalizeDisabled(codes []string, defaultCode string) ([]string, error) {
	defaultCode = strings.TrimSpace(defaultCode)
	seen := map[string]bool{}
	out := make([]string, 0, len(codes))
	for _, raw := range codes {
		code := strings.TrimSpace(raw)
		if code == "" {
			continue
		}
		entry, ok := m.Entry(code)
		if !ok {
			return nil, fmt.Errorf("%s is not a shipped locale", code)
		}
		canonical := entry.Code
		if entry.IsPseudo() || entry.Hidden {
			return nil, fmt.Errorf("%s cannot be disabled", canonical)
		}
		if strings.EqualFold(canonical, DefaultCode) {
			return nil, fmt.Errorf("%s must stay enabled as the fallback language", canonical)
		}
		if defaultCode != "" && strings.EqualFold(canonical, defaultCode) {
			return nil, fmt.Errorf("%s is the default language; choose another default before disabling it", canonical)
		}
		if seen[strings.ToLower(canonical)] {
			continue
		}
		seen[strings.ToLower(canonical)] = true
		out = append(out, canonical)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out, nil
}

func normalizeDisabled(codes []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(codes))
	for _, raw := range codes {
		code := strings.TrimSpace(raw)
		if code == "" || seen[strings.ToLower(code)] {
			continue
		}
		seen[strings.ToLower(code)] = true
		out = append(out, code)
	}
	return out
}

// CatalogLanguage maps a server catalog name ("es", "zh-Hans") to the locale
// code used by the Control Plane policy ("es-ES", "zh-Hans").
func CatalogLanguage(catalog string) string {
	catalog = strings.TrimSpace(catalog)
	if catalog == "" {
		return ""
	}
	for _, entry := range Fallback().Locales {
		if strings.EqualFold(entry.Catalog, catalog) && !entry.IsPseudo() {
			return entry.Code
		}
	}
	return ""
}

// Fallback is a compiled copy of the shipped manifest. It keeps webmail and
// the Control Plane working if the static manifest cannot be read on a fresh
// or partially updated host.
func Fallback() Manifest {
	return Manifest{Default: DefaultCode, Locales: []Entry{
		{Code: "en-US", Catalog: "en", Label: "English (United States)", Dir: "ltr", Status: "source", Enabled: boolPtr(true)},
		{Code: "en-GB", Catalog: "en", Label: "English (United Kingdom)", Dir: "ltr", Status: "source", Enabled: boolPtr(true)},
		{Code: "ar", Catalog: "ar", Label: "العربية", Dir: "rtl", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "de-DE", Catalog: "de", Label: "Deutsch", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "fr-FR", Catalog: "fr", Label: "Français", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "es-ES", Catalog: "es", Label: "Español", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "pt-BR", Catalog: "pt", Label: "Português (Brasil)", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "nl-NL", Catalog: "nl", Label: "Nederlands", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "it-IT", Catalog: "it", Label: "Italiano", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "bg", Catalog: "bg", Label: "Български", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "cs", Catalog: "cs", Label: "Čeština", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "da", Catalog: "da", Label: "Dansk", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "el", Catalog: "el", Label: "Ελληνικά", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "et", Catalog: "et", Label: "Eesti", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "fi", Catalog: "fi", Label: "Suomi", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "he", Catalog: "he", Label: "עברית", Dir: "rtl", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "hi", Catalog: "hi", Label: "हिन्दी", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "hr", Catalog: "hr", Label: "Hrvatski", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "hu", Catalog: "hu", Label: "Magyar", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "id", Catalog: "id", Label: "Bahasa Indonesia", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "ja", Catalog: "ja", Label: "日本語", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "ko", Catalog: "ko", Label: "한국어", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "lt", Catalog: "lt", Label: "Lietuvių", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "lv", Catalog: "lv", Label: "Latviešu", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "ms", Catalog: "ms", Label: "Bahasa Melayu", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "nb", Catalog: "nb", Label: "Norsk bokmål", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "pl", Catalog: "pl", Label: "Polski", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "ro", Catalog: "ro", Label: "Română", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "ru", Catalog: "ru", Label: "Русский", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "sk", Catalog: "sk", Label: "Slovenčina", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "sl", Catalog: "sl", Label: "Slovenščina", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "sv", Catalog: "sv", Label: "Svenska", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "tr", Catalog: "tr", Label: "Türkçe", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "uk", Catalog: "uk", Label: "Українська", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "zh-Hans", Catalog: "zh-Hans", Label: "简体中文", Dir: "ltr", Status: "reviewed", Enabled: boolPtr(true)},
		{Code: "en-XA", Catalog: "en-XA", Label: "English (Pseudo)", Dir: "ltr", Status: "pseudo", Enabled: boolPtr(false)},
	}}
}

func boolPtr(value bool) *bool { return &value }
