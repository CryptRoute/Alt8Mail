package httpapi

import (
	"net/url"
	"testing"
	"time"
)

func TestICSRecurrenceExpansion(t *testing.T) {
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // Monday
	end := start.AddDate(0, 0, 14)
	daily := parsedCalendarEvent{
		StartsAt: start, EndsAt: start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}
	occurrences := expandICSBusyOccurrences(daily, start, end)
	if len(occurrences) != 3 {
		t.Fatalf("daily occurrences = %d", len(occurrences))
	}
	weekly := parsedCalendarEvent{
		StartsAt: start, EndsAt: start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=4",
	}
	occurrences = expandICSBusyOccurrences(weekly, start, end)
	if len(occurrences) != 4 {
		t.Fatalf("weekly occurrences = %d", len(occurrences))
	}
	if occurrences[0].Weekday() != time.Monday || occurrences[1].Weekday() != time.Wednesday {
		t.Fatalf("weekly weekdays = %v %v", occurrences[0].Weekday(), occurrences[1].Weekday())
	}
	monthly := parsedCalendarEvent{
		StartsAt: start, EndsAt: start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=MONTHLY;COUNT=2",
	}
	if occurrences := expandICSBusyOccurrences(monthly, start, start.AddDate(0, 2, 0)); len(occurrences) != 2 {
		t.Fatalf("monthly occurrences = %d", len(occurrences))
	}
	if occurrences := expandICSBusyOccurrences(parsedCalendarEvent{StartsAt: start, EndsAt: start.Add(time.Hour)}, start, end); len(occurrences) != 1 {
		t.Fatalf("single occurrence = %d", len(occurrences))
	}
}

func TestBusyIntervalsSkipTransparentAndCancelled(t *testing.T) {
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	events := []parsedCalendarEvent{
		{StartsAt: start, EndsAt: start.Add(time.Hour), Status: "confirmed", Transparency: "busy"},
		{StartsAt: start.Add(2 * time.Hour), EndsAt: start.Add(3 * time.Hour), Status: "cancelled"},
		{StartsAt: start.Add(4 * time.Hour), EndsAt: start.Add(5 * time.Hour), Status: "confirmed", Transparency: "free"},
		{StartsAt: start.Add(6 * time.Hour), EndsAt: start.Add(7 * time.Hour), Status: "tentative", Transparency: "busy"},
	}
	busy := busyIntervalsFromICS(events, start, start.Add(24*time.Hour))
	if len(busy) != 2 {
		t.Fatalf("busy intervals = %d (%v)", len(busy), busy)
	}
	for _, item := range busy {
		if item["start"] == "" || item["end"] == "" {
			t.Fatalf("interval missing bounds: %v", item)
		}
	}
}

func TestExternalFeedQuery(t *testing.T) {
	parsed, _ := url.Parse("https://mail.example.test/api/calendar/free-busy?external=https%3A%2F%2Fa.example%2Ffeed.ics&externalFeeds=https%3A%2F%2Fb.example%2Ffeed.ics,https%3A%2F%2Fc.example%2Ffeed.ics")
	feeds := externalFeedQuery(parsed)
	if len(feeds) != 3 {
		t.Fatalf("feeds = %v", feeds)
	}
}
