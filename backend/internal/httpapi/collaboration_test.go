package httpapi

import "testing"

func TestSanitizeMentionInput(t *testing.T) {
	input := []any{
		"Priya.Shah@A8M.example",
		map[string]any{"email": " priya.shah@a8m.example "},
		map[string]any{"email": "elena.vasquez@a8m.example"},
		"not-an-address",
		"",
		map[string]any{"name": "No email"},
		"marcus.webb@a8m.example",
	}
	got := sanitizeMentionInput(input)
	want := []string{"priya.shah@a8m.example", "elena.vasquez@a8m.example", "marcus.webb@a8m.example"}
	if len(got) != len(want) {
		t.Fatalf("sanitizeMentionInput returned %d entries, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("entry %d = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestSanitizeMentionInputCapsList(t *testing.T) {
	input := make([]any, 0, 25)
	for index := 0; index < 25; index++ {
		input = append(input, "person"+string(rune('a'+index))+"@a8m.example")
	}
	if got := sanitizeMentionInput(input); len(got) != 20 {
		t.Fatalf("expected mention list to cap at 20, got %d", len(got))
	}
}

func TestStateStringOf(t *testing.T) {
	if got := stateStringOf("user-1"); got != "user-1" {
		t.Fatalf("stateStringOf(string) = %q", got)
	}
	if got := stateStringOf(nil); got != "" {
		t.Fatalf("stateStringOf(nil) = %q", got)
	}
	if got := stateStringOf(42); got != "" {
		t.Fatalf("stateStringOf(int) = %q", got)
	}
}
