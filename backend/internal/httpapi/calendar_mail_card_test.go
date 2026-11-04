package httpapi

import (
	"testing"
	"time"
)

func TestSameCalendarMoment(t *testing.T) {
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if !sameCalendarMoment(base, base.Add(30*time.Second)) {
		t.Fatal("times within a minute should match")
	}
	if sameCalendarMoment(base, base.Add(2*time.Minute)) {
		t.Fatal("times two minutes apart should not match")
	}
	if sameCalendarMoment(time.Time{}, base) {
		t.Fatal("a zero time never matches")
	}
}

func TestCalendarCardKind(t *testing.T) {
	cases := map[string]string{
		"CANCEL":  "cancelled",
		"REPLY":   "response",
		"COUNTER": "proposal",
		"REQUEST": "invitation",
		"":        "invitation",
	}
	for method, want := range cases {
		if got := calendarCardKind(method); got != want {
			t.Fatalf("calendarCardKind(%q) = %q, want %q", method, got, want)
		}
	}
}

func TestCalendarReplyStatus(t *testing.T) {
	event := parsedCalendarEvent{AttendeeDetails: []parsedCalendarAttendee{
		{Email: "guest@example.com", PartStat: "ACCEPTED"},
		{Email: "other@example.com", PartStat: "DECLINED"},
	}}
	if got := calendarReplyStatus(event, "guest@example.com"); got != "accepted" {
		t.Fatalf("reply status = %q", got)
	}
	if got := calendarReplyStatus(event, "missing@example.com"); got != "" {
		t.Fatalf("unknown attendee status = %q", got)
	}
}
