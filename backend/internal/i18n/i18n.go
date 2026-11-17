// Package i18n localizes server-generated messages: push notifications,
// notification-center templates and feature emails. User-interface strings are
// owned by the webmail catalogs; this package only carries server surfaces.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

//go:embed catalogs/*.json
var catalogFS embed.FS

const fallbackCatalog = "en"

// Value is either a plain string or a map of CLDR plural forms.
type Value struct {
	Text  string
	Forms map[string]string
}

// Bundle is an immutable set of server-message catalogs.
type Bundle struct {
	catalogs map[string]map[string]Value
	tags     []language.Tag
	names    []string
	matcher  language.Matcher
}

// New loads the embedded catalogs. The catalogs are validated at build time by
// the package tests, so a load error means the binary is damaged.
func New() (*Bundle, error) {
	entries, err := catalogFS.ReadDir("catalogs")
	if err != nil {
		return nil, fmt.Errorf("read server catalogs: %w", err)
	}
	bundle := &Bundle{catalogs: map[string]map[string]Value{}}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		raw, err := catalogFS.ReadFile(path.Join("catalogs", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read catalog %s: %w", name, err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("parse catalog %s: %w", name, err)
		}
		catalog := make(map[string]Value, len(decoded))
		for key, value := range decoded {
			parsed, err := parseValue(value)
			if err != nil {
				return nil, fmt.Errorf("catalog %s key %s: %w", name, key, err)
			}
			catalog[key] = parsed
		}
		bundle.catalogs[name] = catalog
	}
	if _, ok := bundle.catalogs[fallbackCatalog]; !ok {
		return nil, fmt.Errorf("server catalogs are missing %s.json", fallbackCatalog)
	}
	for name := range bundle.catalogs {
		tag, err := language.Parse(name)
		if err != nil {
			continue
		}
		bundle.tags = append(bundle.tags, tag)
		bundle.names = append(bundle.names, name)
	}
	slices.Sort(bundle.names)
	sort.Slice(bundle.tags, func(i, j int) bool { return bundle.tags[i].String() < bundle.tags[j].String() })
	bundle.matcher = language.NewMatcher(bundle.tags)
	return bundle, nil
}

func parseValue(raw json.RawMessage) (Value, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return Value{Text: text}, nil
	}
	var forms map[string]string
	if err := json.Unmarshal(raw, &forms); err != nil {
		return Value{}, fmt.Errorf("value must be a string or plural object")
	}
	if _, ok := forms["other"]; !ok {
		return Value{}, fmt.Errorf("plural object is missing other")
	}
	return Value{Forms: forms}, nil
}

// Match resolves a requested locale to the closest catalog. Unknown or empty
// values resolve to English.

// parseAcceptLanguageLoose accepts individual well-formed entries and ignores
// unknown ones, so one bad subtag does not discard the whole header.
func parseAcceptLanguageLoose(header string) []language.Tag {
	var tags []language.Tag
	for _, part := range strings.Split(header, ",") {
		value := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if value == "" {
			continue
		}
		if tag, err := language.Parse(value); err == nil {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (b *Bundle) Match(preferred ...string) string {
	if b == nil {
		return fallbackCatalog
	}
	var tags []language.Tag
	for _, value := range preferred {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, ",;") {
			tags = append(tags, parseAcceptLanguageLoose(value)...)
			continue
		}
		if tag, err := language.Parse(value); err == nil {
			tags = append(tags, tag)
		}
	}
	for _, tag := range tags {
		base, _ := tag.Base()
		if _, ok := b.catalogs[base.String()]; ok {
			return base.String()
		}
		if _, ok := b.catalogs[tag.String()]; ok {
			return tag.String()
		}
	}
	if len(tags) == 0 {
		return fallbackCatalog
	}
	matched, _, confidence := b.matcher.Match(tags...)
	if confidence < language.High {
		return fallbackCatalog
	}
	name := matched.String()
	if _, ok := b.catalogs[name]; ok {
		return name
	}
	base, _ := matched.Base()
	if _, ok := b.catalogs[base.String()]; ok {
		return base.String()
	}
	return fallbackCatalog
}

// Translator returns a translator for the requested locale.
func (b *Bundle) Translator(preferred ...string) *Translator {
	name := b.Match(preferred...)
	return &Translator{bundle: b, name: name}
}

// Translator resolves a locale and exposes string and plural lookup.
type Translator struct {
	bundle *Bundle
	name   string
}

// Locale returns the resolved catalog name, such as "de" or "zh-Hans".
func (t *Translator) Locale() string {
	if t == nil || t.name == "" {
		return fallbackCatalog
	}
	return t.name
}

// Dir returns the text direction for the locale.
func (t *Translator) Dir() string {
	switch strings.ToLower(strings.SplitN(t.Locale(), "-", 2)[0]) {
	case "ar", "he", "fa", "ur":
		return "rtl"
	default:
		return "ltr"
	}
}

// T returns the translated string for key, or an empty string when the key is
// not present. Callers keep their existing English message as the fallback.
func (t *Translator) T(key string, vars map[string]any) string {
	if value, ok := t.lookup(key); ok {
		if value.Text != "" {
			return interpolate(value.Text, vars)
		}
		if text, ok := value.Forms["other"]; ok {
			return interpolate(text, vars)
		}
	}
	return ""
}

// N returns the plural form for count, falling back to other.
func (t *Translator) N(key string, count int, vars map[string]any) string {
	if value, ok := t.lookup(key); ok {
		if value.Text != "" {
			if vars == nil {
				vars = map[string]any{}
			}
			vars["count"] = count
			return interpolate(value.Text, vars)
		}
		form := pluralForm(t.Locale(), count)
		text, ok := value.Forms[form]
		if !ok {
			text = value.Forms["other"]
		}
		if vars == nil {
			vars = map[string]any{}
		}
		vars["count"] = count
		return interpolate(text, vars)
	}
	return ""
}

func (t *Translator) lookup(key string) (Value, bool) {
	if t == nil || t.bundle == nil {
		return Value{}, false
	}
	if catalog, ok := t.bundle.catalogs[t.name]; ok {
		if value, ok := catalog[key]; ok {
			return value, true
		}
	}
	if t.name != fallbackCatalog {
		if value, ok := t.bundle.catalogs[fallbackCatalog][key]; ok {
			return value, true
		}
	}
	return Value{}, false
}

func pluralForm(locale string, count int) string {
	tag, err := language.Parse(locale)
	if err != nil {
		return "other"
	}
	if count < 0 {
		count = -count
	}
	switch plural.Cardinal.MatchPlural(tag, count, 0, 0, 0, 0) {
	case plural.Zero:
		return "zero"
	case plural.One:
		return "one"
	case plural.Two:
		return "two"
	case plural.Few:
		return "few"
	case plural.Many:
		return "many"
	default:
		return "other"
	}
}

func interpolate(text string, vars map[string]any) string {
	if len(vars) == 0 {
		return text
	}
	result := text
	for key, value := range vars {
		result = strings.ReplaceAll(result, "{"+key+"}", formatValue(value))
	}
	return result
}

func formatValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}
