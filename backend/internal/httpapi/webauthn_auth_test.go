package httpapi

import (
	"strings"
	"testing"
)

func TestPasskeyNameForUserAgent(t *testing.T) {
	chromeWindows := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	if got := passkeyNameForUserAgent(chromeWindows, ""); got != "Passkey · Chrome on Windows" {
		t.Fatalf("default name = %q", got)
	}
	if got := passkeyNameForUserAgent(chromeWindows, "  Work \n laptop  "); got != "Work laptop" {
		t.Fatalf("trimmed name = %q", got)
	}
	if got := passkeyNameForUserAgent("", strings.Repeat("x", 100)); len([]rune(got)) != 80 {
		t.Fatalf("long name length = %d", len([]rune(got)))
	}
	if got := passkeyNameForUserAgent("", "   "); !strings.HasPrefix(got, "Passkey · ") {
		t.Fatalf("blank name should use the default, got %q", got)
	}
}
