package httpapi

import (
	"encoding/json"
	"testing"

	serveri18n "github.com/CryptRoute/Alt8Mail/backend/internal/i18n"
)

func TestLocalizedNotificationTitle(t *testing.T) {
	bundle, err := serveri18n.New()
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	params, _ := json.Marshal(map[string]any{"actor": "Mira"})
	fallback := "Mira assigned you a conversation"
	if got := localizedNotificationTitle(bundle.Translator("de-DE"), fallback, "server.notify.assignment", params); got != "Mira hat Ihnen ein Gespräch zugewiesen" {
		t.Errorf("German notification = %q", got)
	}
	if got := localizedNotificationTitle(bundle.Translator("en"), fallback, "server.notify.assignment", params); got != "Mira assigned you a conversation" {
		t.Errorf("English notification = %q", got)
	}
	if got := localizedNotificationTitle(bundle.Translator("de"), fallback, "", nil); got != fallback {
		t.Errorf("legacy fallback = %q", got)
	}
	if got := localizedNotificationTitle(bundle.Translator("de"), fallback, "server.notify.missing", params); got != fallback {
		t.Errorf("missing template fallback = %q", got)
	}
}
