package httpapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStickyNoteTitleDerivesFromContent(t *testing.T) {
	cases := map[string]string{
		"Dentist Friday 3pm\n- [ ] Send deck": "Dentist Friday 3pm",
		"- [ ] Send deck\nSecond line":        "Send deck",
		"- Buy milk":                          "Buy milk",
		"":                                    "Untitled note",
		"\n\n  ":                              "Untitled note",
	}
	for input, expected := range cases {
		if got := stickyNoteTitle(input); got != expected {
			t.Fatalf("stickyNoteTitle(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestStickyNoteDefaultPositionStaysInsideHome(t *testing.T) {
	for index := 0; index < 40; index++ {
		position := defaultNotePosition(index)
		x, _ := position["x"].(float64)
		y, _ := position["y"].(float64)
		if x < 0 || x > 1 || y < 0 || y > 1 {
			t.Fatalf("position %d = %#v", index, position)
		}
	}
}

func TestStickyNoteLinksAcceptLegacyAndTypedShapes(t *testing.T) {
	var refs []stickyNoteLinkRef
	if err := json.Unmarshal([]byte(`["thread-1",{"type":"event","id":"event-1"},{"threadId":"thread-2"},{"fileId":"file-1"}]`), &refs); err != nil {
		t.Fatalf("decode links: %v", err)
	}
	expected := []struct{ typ, id string }{
		{"thread", "thread-1"}, {"event", "event-1"}, {"thread", "thread-2"}, {"file", "file-1"},
	}
	if len(refs) != len(expected) {
		t.Fatalf("decoded %d links, want %d", len(refs), len(expected))
	}
	for index, want := range expected {
		typ, id, ok := normalizeStickyNoteLinkRef(refs[index])
		if !ok || typ != want.typ || id != want.id {
			t.Fatalf("link %d = %q:%q (%v), want %q:%q", index, typ, id, ok, want.typ, want.id)
		}
	}
	if _, _, ok := normalizeStickyNoteLinkRef(stickyNoteLinkRef{Type: "contact", ID: "x"}); ok {
		t.Fatal("unsupported link type was accepted")
	}
}

func TestStickyNotePublicNormalizesLegacyLinks(t *testing.T) {
	links, _ := json.Marshal([]any{
		map[string]any{"threadId": "thread-1", "subject": "Legacy"},
		map[string]any{"type": "event", "id": "event-1", "title": "Review"},
		map[string]any{"type": "unknown", "id": "x"},
	})
	row := stickyNoteRow{ID: "note-1", Title: "Desk", Content: "Line", Links: links}
	public := stickyNotePublic(row, false)
	normalized := public["links"].([]any)
	if len(normalized) != 2 {
		t.Fatalf("normalized links = %#v", normalized)
	}
	first := normalized[0].(map[string]any)
	if first["type"] != "thread" || first["id"] != "thread-1" || first["threadId"] != "thread-1" {
		t.Fatalf("legacy thread link = %#v", first)
	}
}

func TestStickyNoteArchiveTimestampIsStableWhileArchived(t *testing.T) {
	first := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	if got := noteArchivedAt(true, &first); got != first {
		t.Fatalf("archived note timestamp = %#v, want %s", got, first)
	}
	if got := noteArchivedAt(true, nil); got == nil {
		t.Fatal("new archive should get a timestamp")
	}
	if got := noteArchivedAt(false, &first); got != nil {
		t.Fatalf("restored note timestamp = %#v, want nil", got)
	}
}

func TestStickyNotePublicParsesPositionAndLinks(t *testing.T) {
	position, _ := json.Marshal(map[string]any{"x": 0.2, "y": 0.4})
	links, _ := json.Marshal([]any{map[string]any{"threadId": "thread-1", "subject": "Hello"}})
	row := stickyNoteRow{
		ID: "note-1", Title: "Desk", Content: "Line", Color: "yellow",
		InboxPinned: true, Position: position, Links: links,
		UpdatedAt: time.Now(), CreatedAt: time.Now(),
	}
	public := stickyNotePublic(row, false)
	if public["inboxPinned"] != true || public["archived"] != false {
		t.Fatalf("public note = %#v", public)
	}
	if public["position"].(map[string]any)["x"] != 0.2 {
		t.Fatalf("position = %#v", public["position"])
	}
	if len(public["links"].([]any)) != 1 {
		t.Fatalf("links = %#v", public["links"])
	}
}
