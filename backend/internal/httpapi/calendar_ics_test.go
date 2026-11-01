package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestHolidayCatalogCoverage(t *testing.T) {
	countries := holidayCatalog()
	if len(countries) < 140 {
		t.Fatalf("holiday catalogue has %d countries, want at least 140", len(countries))
	}
	seen := map[string]bool{}
	for _, country := range countries {
		if strings.TrimSpace(country.Code) == "" || strings.TrimSpace(country.Name) == "" {
			t.Fatalf("holiday country is missing a code or name: %#v", country)
		}
		if seen[country.Code] {
			t.Fatalf("duplicate holiday country code %q", country.Code)
		}
		seen[country.Code] = true
	}
	events := holidayEvents(holidayCountry{Code: "US", Name: "United States", Month: 7, Day: 4, Christmas: true}, 2026)
	if len(events) < 9 {
		t.Fatalf("expected holidays across three years, got %d", len(events))
	}
	found := false
	for _, event := range events {
		if event.StartsAt.Year() == 2026 && event.StartsAt.Month() == time.July && event.StartsAt.Day() == 4 {
			found = true
		}
	}
	if !found {
		t.Fatal("2026 US national day is missing")
	}
	easter := easterSunday(2026)
	if easter.Month() != time.April || easter.Day() != 5 {
		t.Fatalf("easterSunday(2026) = %s, want 2026-04-05", easter.Format("2006-01-02"))
	}
}

func TestICalendarRoundTrip(t *testing.T) {
	event := map[string]any{
		"id": "cal_test", "uid": "cal_test", "title": "Quarterly review, Europe; Q4",
		"description": "Agenda\n1. Numbers\n2. Next steps",
		"where":       "Room 4, HQ",
		"date":        "2026-11-04", "start": "14:00", "end": "15:30",
		"startsAt": "2026-11-04T14:00:00Z", "endsAt": "2026-11-04T15:30:00Z",
		"allDay": false, "timezone": "UTC", "color": "#0f6cbd", "status": "confirmed",
		"organizer": "owner@example.com", "organizerName": "Avery Chen",
		"recurrenceRule": "FREQ=WEEKLY;BYDAY=WE", "sequence": int64(2), "transparency": "busy",
		"reminderMinutes": int64(30),
		"attendeeList": []any{
			map[string]any{"email": "guest@example.com", "name": "Guest Person", "status": "accepted", "organizer": false},
		},
	}
	body := buildCalendarICS("Product", []map[string]any{event}, "REQUEST", "https://mail.example.com")
	if !strings.Contains(body, "BEGIN:VCALENDAR") || !strings.Contains(body, "ATTENDEE") || !strings.Contains(body, "RRULE:FREQ=WEEKLY") {
		t.Fatalf("calendar output is missing required fields:\n%s", body)
	}
	parsed, err := parseICS([]byte(body))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed %d events, want 1", len(parsed))
	}
	got := parsed[0]
	if got.Title != "Quarterly review, Europe; Q4" {
		t.Fatalf("title = %q", got.Title)
	}
	if !got.StartsAt.Equal(time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %s", got.StartsAt)
	}
	if len(got.Attendees) != 1 || got.Attendees[0] != "guest@example.com" {
		t.Fatalf("attendees = %#v", got.Attendees)
	}
	if got.Organizer != "owner@example.com" {
		t.Fatalf("organizer = %q", got.Organizer)
	}
	if got.RecurrenceRule != "FREQ=WEEKLY;BYDAY=WE" {
		t.Fatalf("recurrence = %q", got.RecurrenceRule)
	}
}

func TestAllDayICalendarImport(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:all-day-1\r\nSUMMARY:Holiday\r\nDTSTART;VALUE=DATE:20261101\r\nDTEND;VALUE=DATE:20261102\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := parseICS([]byte(body))
	if err != nil || len(events) != 1 {
		t.Fatalf("parseICS = %d events, %v", len(events), err)
	}
	event := events[0]
	if !event.AllDay {
		t.Fatal("all-day flag was not detected")
	}
	if event.StartsAt.Day() != 1 || event.EndsAt.Day() != 1 || event.EndsAt.Hour() != 23 {
		t.Fatalf("all-day range = %s – %s", event.StartsAt, event.EndsAt)
	}
}
