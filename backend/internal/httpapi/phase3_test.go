package httpapi

import "testing"

func TestParseListUnsubscribe(t *testing.T) {
	meta := parseListUnsubscribe("<mailto:unsubscribe@example.test>, <https://example.test/unsub>", "List-Unsubscribe=One-Click")
	if meta == nil {
		t.Fatal("expected unsubscribe metadata")
	}
	if stateString(meta, "mailto") != "unsubscribe@example.test" || stateString(meta, "url") != "https://example.test/unsub" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if !stateBool(meta, "oneClick") {
		t.Fatal("expected one-click support")
	}
	if parseListUnsubscribe("", "List-Unsubscribe=One-Click") != nil {
		t.Fatal("empty header should not produce metadata")
	}
	if parseListUnsubscribe("mailto:not-an-email", "") != nil {
		t.Fatal("invalid mailto should be discarded")
	}
	plain := parseListUnsubscribe("<https://example.test/unsub>", "")
	if plain == nil || stateBool(plain, "oneClick") {
		t.Fatalf("plain link should not be marked one-click: %#v", plain)
	}
}

func TestValidateUnsubscribeURLRejectsLocalTargets(t *testing.T) {
	cases := []string{
		"http://localhost/unsub",
		"http://127.0.0.1/unsub",
		"http://10.0.0.4/unsub",
		"ftp://example.test/unsub",
		"https://example.test:8443/unsub",
		"https://user:pass@example.test/unsub",
	}
	for _, value := range cases {
		if _, err := validateUnsubscribeURL(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
	if _, err := validateUnsubscribeURL("https://example.test/unsub"); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
}

func TestReadingTimeForText(t *testing.T) {
	if _, minutes := readingTimeForText(""); minutes != 0 {
		t.Fatalf("empty text should be 0 minutes, got %d", minutes)
	}
	if _, minutes := readingTimeForText("one two three"); minutes != 1 {
		t.Fatalf("short text should be 1 minute, got %d", minutes)
	}
	words := ""
	for index := 0; index < 441; index++ {
		words += "word "
	}
	if count, minutes := readingTimeForText(words); count != 441 || minutes != 3 {
		t.Fatalf("got %d words / %d minutes", count, minutes)
	}
}

func TestReactionSuggestions(t *testing.T) {
	if emoji, semantic, ok := validReactionEmoji("✅"); !ok || emoji != "✅" || semantic != "approval" {
		t.Fatalf("approval emoji not recognized: %q %q %v", emoji, semantic, ok)
	}
	if _, _, ok := validReactionEmoji("💣"); ok {
		t.Fatal("unsupported emoji should be rejected")
	}
	suggestions := parseReactionSuggestions(`{"reactions":[{"emoji":"✅","semantic":"approval","label":"Approve"},{"emoji":"💣","semantic":"bad","label":"Bad"},{"emoji":"👍","semantic":"acknowledgement","label":"Acknowledge"}]}`)
	if len(suggestions) != 2 || suggestions[0].Emoji != "✅" || suggestions[1].Emoji != "👍" {
		t.Fatalf("unexpected suggestions: %#v", suggestions)
	}
	heuristic := heuristicReactionSuggestions(map[string]any{
		"subject": "Please approve the order form", "bodyText": "Could you confirm before Friday?",
	})
	if len(heuristic) == 0 || heuristic[0].Emoji != "✅" {
		t.Fatalf("expected an approval suggestion: %#v", heuristic)
	}
}
