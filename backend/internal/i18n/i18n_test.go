package i18n

import (
	"sort"
	"strings"
	"testing"
)

func TestBundleMatch(t *testing.T) {
	bundle, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := map[string]string{
		"de-DE":   "de",
		"de":      "de",
		"fr-CA":   "fr",
		"pt-BR":   "pt",
		"zh-Hans": "zh-Hans",
		"ja-JP":   "ja",
		"ru-RU":   "ru",
		"xx-YY":   "en",
		"":        "en",
	}
	for input, want := range cases {
		if got := bundle.Match(input); got != want {
			t.Errorf("Match(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAcceptLanguageNegotiation(t *testing.T) {
	bundle, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := bundle.Match("fr-FR,fr;q=0.9,en;q=0.8"); got != "fr" {
		t.Errorf("Accept-Language match = %q, want fr", got)
	}
	if got := bundle.Match("xx;q=0.9,ja;q=0.8"); got != "ja" {
		t.Errorf("weighted match = %q, want ja", got)
	}
}

func TestTranslatorFallbackAndInterpolation(t *testing.T) {
	bundle, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := bundle.Translator("de-DE").T("server.push.via", map[string]any{"account": "Work"}); got != "über Work" {
		t.Errorf("German interpolation = %q", got)
	}
	if got := bundle.Translator("en").T("server.push.via", map[string]any{"account": "Work"}); got != "via Work" {
		t.Errorf("English interpolation = %q", got)
	}
	if got := bundle.Translator("de").T("missing.key", nil); got != "" {
		t.Errorf("missing key = %q, want empty fallback", got)
	}
	if got := bundle.Translator("de").Dir(); got != "ltr" {
		t.Errorf("German dir = %q", got)
	}
	if got := bundle.Translator("ar").Dir(); got != "rtl" {
		t.Errorf("Arabic dir = %q", got)
	}
}

func TestPluralSelection(t *testing.T) {
	bundle, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		locale string
		count  int
		want   string
	}{
		{"en", 1, "1 new message"},
		{"en", 4, "4 new messages"},
		{"ru", 1, "1 новое сообщение"},
		{"ru", 3, "3 новых сообщения"},
		{"ru", 9, "9 новых сообщений"},
		{"ja", 7, "7 件の新着メッセージ"},
	}
	for _, test := range cases {
		if got := bundle.Translator(test.locale).N("server.push.newMessages", test.count, nil); got != test.want {
			t.Errorf("%s N(%d) = %q, want %q", test.locale, test.count, got, test.want)
		}
	}
	if got := bundle.Translator("ar").N("server.push.newMessages", 2, nil); got != "رسالتان جديدتان" {
		t.Errorf("Arabic dual = %q", got)
	}
	if got := bundle.Translator("en").N("server.push.newMessages", 2, map[string]any{"count": 2}); got != "2 new messages" {
		t.Errorf("count interpolation = %q", got)
	}
}

func TestEveryCatalogHasEveryEnglishKey(t *testing.T) {
	bundle, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	english := bundle.catalogs[fallbackCatalog]
	for name, catalog := range bundle.catalogs {
		for key := range english {
			if _, ok := catalog[key]; !ok {
				t.Errorf("%s is missing %s", name, key)
			}
		}
	}
}

func TestCatalogPlaceholdersMatchEnglish(t *testing.T) {
	bundle, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	english := bundle.catalogs[fallbackCatalog]
	placeholder := func(text, form string) string {
		var names []string
		for i := 0; i < len(text); i++ {
			if text[i] != '{' {
				continue
			}
			end := strings.IndexByte(text[i:], '}')
			if end < 0 {
				continue
			}
			name := text[i+1 : i+end]
			if name != "count" || (form != "zero" && form != "one" && form != "two") {
				names = append(names, name)
			}
			i += end
		}
		sort.Strings(names)
		return strings.Join(names, ",")
	}
	sourceFor := func(value Value, form string) string {
		if value.Text != "" {
			return value.Text
		}
		if text, ok := value.Forms[form]; ok {
			return text
		}
		return value.Forms["other"]
	}
	for name, catalog := range bundle.catalogs {
		for key, sourceValue := range english {
			translated := catalog[key]
			if translated.Text != "" {
				if got, want := placeholder(translated.Text, ""), placeholder(sourceFor(sourceValue, ""), ""); got != want {
					t.Errorf("%s %s placeholders = %q, want %q", name, key, got, want)
				}
			}
			for form, text := range translated.Forms {
				if got, want := placeholder(text, form), placeholder(sourceFor(sourceValue, form), form); got != want {
					t.Errorf("%s %s.%s placeholders = %q, want %q", name, key, form, got, want)
				}
			}
		}
	}
}
