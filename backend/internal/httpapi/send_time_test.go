package httpapi

import (
	"testing"
	"time"
)

func TestNextRecipientWeekdaySlot(t *testing.T) {
	// Friday after the recipient's working window rolls to Monday 09:30.
	friday := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	got := nextRecipientWeekdaySlot(friday, 0, 9, 17)
	want := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Friday rollover = %s, want %s", got, want)
	}
	// A known +05:30 recipient sees 09:30 local, not 09:30 UTC.
	got = nextRecipientWeekdaySlot(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 330, 9, 17)
	want = time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("offset slot = %s, want %s", got, want)
	}
	// Within the window but past the first slot, the next hour is used.
	got = nextRecipientWeekdaySlot(time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), 0, 9, 17)
	want = time.Date(2026, 11, 16, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next hour slot = %s, want %s", got, want)
	}
	// Weekend candidates are skipped.
	saturday := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	got = nextRecipientWeekdaySlot(saturday, 0, 9, 17)
	want = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("weekend slot = %s, want %s", got, want)
	}
}
