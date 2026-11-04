package httpapi

import "testing"

func TestReactionCatalogCoversFullPicker(t *testing.T) {
	if len(reactionEmojiCategories) < 5 {
		t.Fatalf("expected a multi-category emoji picker, got %d categories", len(reactionEmojiCategories))
	}
	for _, emoji := range []string{"😀", "👎", "💙", "🎊", "🔒", "🍕", "✈️", "👍"} {
		if _, _, ok := validReactionEmoji(emoji); !ok {
			t.Fatalf("expected %q to be a valid reaction", emoji)
		}
	}
	if len(quickReactionEmojis) != 6 {
		t.Fatalf("quick reaction row changed: %#v", quickReactionEmojis)
	}
	payload := reactionCatalogPayload()
	categories, _ := payload["categories"].([]map[string]any)
	if len(categories) == 0 {
		t.Fatal("catalog payload has no categories")
	}
}

func TestReactionUnavailableReason(t *testing.T) {
	if reason := reactionUnavailableReason(map[string]any{"folder": "inbox"}); reason != "" {
		t.Fatalf("inbox should be reactable: %q", reason)
	}
	for _, message := range []map[string]any{
		{"folder": "sent"},
		{"folder": "drafts", "draft": true},
		{"folder": "spam"},
		{"folder": "inbox", "unsubscribe": map[string]any{"url": "https://example.test/unsub"}},
	} {
		if reason := reactionUnavailableReason(message); reason == "" {
			t.Fatalf("expected %#v to be unavailable", message)
		}
	}
}

func TestStickyNoteNormalization(t *testing.T) {
	note := normalizeStickyNote(nil)
	if note["color"] != "yellow" || note["enabled"] != true || note["collapsed"] != false {
		t.Fatalf("unexpected defaults: %#v", note)
	}
	note = normalizeStickyNote(map[string]any{
		"content": "Dentist Friday **3pm**", "color": "pink", "collapsed": true,
		"enabled": false, "links": []any{map[string]any{"threadId": "thread-1"}},
		"position": map[string]any{"x": -1.0, "y": 4.0},
	})
	if note["color"] != "pink" || note["collapsed"] != true || note["enabled"] != false {
		t.Fatalf("stored fields were not normalized: %#v", note)
	}
	position, _ := note["position"].(map[string]any)
	if position["x"] != 0.0 || position["y"] != 1.0 {
		t.Fatalf("position was not clamped: %#v", position)
	}
	if note["content"] != "Dentist Friday **3pm**" {
		t.Fatalf("content changed: %#v", note["content"])
	}
}

func TestTenantFeatureDefaults(t *testing.T) {
	policy := defaultTenantPolicy()
	if !policy.Features.StickyNotes || !policy.Features.Reactions {
		t.Fatalf("optional features should default on: %#v", policy.Features)
	}
	disabled := tenantPolicyFromSettings([]byte(`{"features":{"stickyNotes":false,"reactions":false}}`))
	if disabled.Features.StickyNotes || disabled.Features.Reactions {
		t.Fatalf("tenant feature switches were not applied: %#v", disabled.Features)
	}
}
