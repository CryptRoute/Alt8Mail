package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestCalendarICSRoundTrip(t *testing.T) {
	user := userRecord{Email: "avery@example.test", Name: "Avery Chen"}
	start := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	event := davCalendarEvent{
		UID: "event-123@example.test", Title: "Quarterly planning, with finance; review",
		Description: "Line one\nLine two", Location: "Board room", Timezone: "UTC",
		Status: "confirmed", Visibility: "private", StartsAt: start, EndsAt: start.Add(90 * time.Minute),
		UpdatedAt: start, Attendees: []davAttendee{{Email: "jordan@example.test", Name: "Jordan Lee", Status: "accepted"}},
	}
	ics := string(calendarToICS(event, user))
	if !strings.HasPrefix(ics, "BEGIN:VCALENDAR\r\n") || !strings.Contains(ics, "SUMMARY:Quarterly planning\\, with finance\\; review") {
		t.Fatalf("unexpected iCalendar output:\n%s", ics)
	}
	parsed, err := parseCalendarICS([]byte(ics), user, "#3d6b62")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.UID != event.UID || parsed.Title != event.Title || parsed.Description != event.Description ||
		parsed.Location != event.Location || parsed.Visibility != "private" ||
		!parsed.StartsAt.Equal(start) || !parsed.EndsAt.Equal(start.Add(90*time.Minute)) ||
		len(parsed.Attendees) != 1 || parsed.Attendees[0].Email != "jordan@example.test" {
		t.Fatalf("round trip mismatch: %#v", parsed)
	}
}

func TestCalendarICSAllDayAndFolding(t *testing.T) {
	user := userRecord{Email: "avery@example.test", Name: "Avery"}
	start := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	event := davCalendarEvent{
		UID: "all-day@example.test", Title: strings.Repeat("Long summary ", 16), AllDay: true,
		Timezone: "UTC", StartsAt: start, EndsAt: start, UpdatedAt: start,
	}
	ics := string(calendarToICS(event, user))
	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20261103") || !strings.Contains(ics, "DTEND;VALUE=DATE:20261104") {
		t.Fatalf("all-day dates are wrong:\n%s", ics)
	}
	for _, line := range strings.Split(ics, "\r\n") {
		if len([]byte(line)) > 75 {
			t.Fatalf("line exceeds 75 octets (%d): %q", len([]byte(line)), line)
		}
	}
	parsed, err := parseCalendarICS([]byte(ics), user, "")
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.AllDay || parsed.Title != event.Title || !parsed.StartsAt.Equal(start) {
		t.Fatalf("all-day round trip mismatch: %#v", parsed)
	}
}

func TestVCardRoundTrip(t *testing.T) {
	updated := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	contact := davContact{
		ID: "contact_1", UID: "contact-1@example.test", Email: "jordan@example.test",
		Name: "Jordan Lee", Company: "Northwind, Inc.", Title: "Buyer", Phone: "+1 555 0100",
		Location: "Seattle, WA", Notes: "Met at conference\nFollow up", UpdatedAt: updated,
	}
	data := string(contactToVCard(contact))
	if !strings.Contains(data, "BEGIN:VCARD") || !strings.Contains(data, "EMAIL;TYPE=INTERNET:jordan@example.test") {
		t.Fatalf("unexpected vCard:\n%s", data)
	}
	parsed, err := parseVCard([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.UID != contact.UID || parsed.Email != contact.Email || parsed.Name != contact.Name ||
		parsed.Company != contact.Company || parsed.Title != contact.Title || parsed.Phone != contact.Phone ||
		!strings.Contains(parsed.Location, "Seattle") || !strings.Contains(parsed.Notes, "conference") {
		t.Fatalf("vCard round trip mismatch: %#v", parsed)
	}
	if _, err := parseVCard([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:No Email\r\nEND:VCARD\r\n")); err == nil {
		t.Fatal("a contact without EMAIL must be rejected")
	}
}

func TestDAVSegmentsAndPreconditions(t *testing.T) {
	if parts, ok := davSegments("/dav/"); !ok || len(parts) != 0 {
		t.Fatalf("dav root: %#v %v", parts, ok)
	}
	if _, ok := davSegments("/dav/calendars/../secret"); ok {
		t.Fatal("path traversal must be rejected")
	}
	if _, ok := davSegments("/other/"); ok {
		t.Fatal("non-DAV path must be rejected")
	}
	parts, ok := davSegments("/dav/calendars/user-1/default/event.ics")
	if !ok || len(parts) != 4 || parts[3] != "event.ics" {
		t.Fatalf("unexpected segments: %#v", parts)
	}
	if !davETagListMatches(`"abc", "def"`, "def") || davETagListMatches(`"abc"`, "def") {
		t.Fatal("etag list matching is wrong")
	}
}
