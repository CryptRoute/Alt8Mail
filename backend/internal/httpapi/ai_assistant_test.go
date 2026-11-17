package httpapi

import (
	"strings"
	"testing"
)

func TestAIEndpointNormalization(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com":                     "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1":                  "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1/chat/completions": "https://api.openai.com/v1/chat/completions",
		"https://api.deepseek.com/v1":                "https://api.deepseek.com/v1/chat/completions",
	}
	for input, expected := range cases {
		if got := openAIEndpoint(input); got != expected {
			t.Fatalf("openAIEndpoint(%q) = %q, want %q", input, got, expected)
		}
	}
	if got := anthropicEndpoint("https://api.anthropic.com"); got != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("anthropic base = %q", got)
	}
	if got := anthropicEndpoint("https://api.anthropic.com/v1"); got != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("anthropic v1 = %q", got)
	}
}

func TestAIConversationTranscriptKeepsRecentThreadContext(t *testing.T) {
	transcript := aiConversationTranscript([]map[string]any{
		{
			"fromEmail": "one@example.com", "subject": "Kickoff", "date": "2026-10-28T09:00:00Z",
			"toText": "avery@example.com", "bodyText": "First message",
		},
		{
			"fromEmail": "two@example.com", "subject": "Re: Kickoff", "date": "2026-10-28T10:00:00Z",
			"toText": "avery@example.com", "ccText": "team@example.com", "bodyText": "Latest message",
		},
	}, 4000)
	for _, expected := range []string{"one@example.com", "First message", "two@example.com", "Latest message", "Cc: team@example.com"} {
		if !strings.Contains(transcript, expected) {
			t.Fatalf("transcript missing %q:\n%s", expected, transcript)
		}
	}
	if strings.Index(transcript, "First message") > strings.Index(transcript, "Latest message") {
		t.Fatal("transcript is not oldest-to-newest")
	}
}
