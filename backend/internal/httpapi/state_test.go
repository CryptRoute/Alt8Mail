package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnippetTruncatesOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("a", 179) + "—tail"
	got := snippet(text)
	if !utf8.ValidString(got) {
		t.Fatalf("snippet split a UTF-8 sequence: %q", got)
	}
	if !utf8.ValidString(snippet(strings.Repeat("é", 200))) {
		t.Fatal("snippet split a multibyte rune")
	}
}
