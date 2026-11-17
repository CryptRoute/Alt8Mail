package httpapi

import "testing"

func TestAIJSONPayload(t *testing.T) {
	cases := []string{
		`{"items":[]}`,
		"```json\n{\"items\":[]}\n```",
		"Here is the result:\n{\"available\":true}\nThanks.",
	}
	for _, raw := range cases {
		payload := aiJSONPayload(raw)
		if payload == "" || payload[0] != '{' || payload[len(payload)-1] != '}' {
			t.Fatalf("aiJSONPayload(%q) = %q", raw, payload)
		}
	}
	if got := aiJSONPayload("not json"); got != "" {
		t.Fatalf("expected empty payload, got %q", got)
	}
}

func TestCommitmentMessageForQuote(t *testing.T) {
	messages := []map[string]any{
		{"id": "m1", "bodyText": "I will send the revised deck by Thursday. Thanks."},
		{"id": "m2", "bodyText": "Could you confirm the final budget before Friday?"},
	}
	if got := commitmentMessageForQuote(messages, "send the revised deck"); got == nil || stateString(got, "id") != "m1" {
		t.Fatalf("expected m1, got %#v", got)
	}
	if got := commitmentMessageForQuote(messages, "confirm the final budget"); got == nil || stateString(got, "id") != "m2" {
		t.Fatalf("expected m2, got %#v", got)
	}
	if got := commitmentMessageForQuote(messages, "not present"); got != nil {
		t.Fatalf("unexpected match: %#v", got)
	}
}
