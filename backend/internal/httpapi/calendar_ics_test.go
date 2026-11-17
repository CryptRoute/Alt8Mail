package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
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

func TestStateSliceHandlesTypedSlices(t *testing.T) {
	value := stateSlice(map[string]any{
		"attendees": []calendarAttendeePublic{{Email: "guest@example.com", Name: "Guest"}},
	}, "attendees")
	if len(value) != 1 {
		t.Fatalf("stateSlice returned %d items", len(value))
	}
	entry, ok := value[0].(calendarAttendeePublic)
	if !ok || entry.Email != "guest@example.com" {
		t.Fatalf("stateSlice item = %#v", value[0])
	}
	if got := stateSlice(map[string]any{"items": []string{"a", "b"}}, "items"); len(got) != 2 {
		t.Fatalf("string slice length = %d", len(got))
	}
}

func TestCalendarICSIncludesTZIDVTIMEZONEAndSequence(t *testing.T) {
	event := map[string]any{
		"id": "cal_tz", "uid": "cal_tz@example.com", "title": "Board review",
		"date": "2026-11-04", "start": "09:00", "end": "10:30",
		"startsAt": "2026-11-04T14:00:00Z", "endsAt": "2026-11-04T15:30:00Z",
		"allDay": false, "timezone": "America/New_York", "status": "confirmed",
		"organizer": "owner@example.com", "organizerName": "Avery Chen",
		"sequence": int64(3), "responseRequested": true, "transparency": "busy",
		"attendeeList": []any{
			map[string]any{"email": "owner@example.com", "name": "Avery Chen", "status": "accepted", "organizer": true},
			map[string]any{"email": "guest@example.com", "name": "Guest Person", "status": "needs-action", "organizer": false},
		},
	}
	body := buildCalendarICS("Product", []map[string]any{event}, "REQUEST", "https://mail.example.com")
	for _, want := range []string{
		"METHOD:REQUEST", "BEGIN:VTIMEZONE", "TZID:America/New_York",
		"DTSTART;TZID=America/New_York:20261104T090000",
		"DTEND;TZID=America/New_York:20261104T103000",
		"SEQUENCE:3", "UID:cal_tz@example.com", "RSVP=TRUE", "ROLE=CHAIR",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("calendar output is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "DTSTART:20261104T140000Z") {
		t.Fatal("timed event was sent as floating UTC instead of organizer TZID")
	}
	if strings.Count(body, "BEGIN:STANDARD") != strings.Count(body, "END:STANDARD") ||
		strings.Count(body, "BEGIN:DAYLIGHT") != strings.Count(body, "END:DAYLIGHT") ||
		!strings.Contains(body, "BEGIN:DAYLIGHT") || !strings.Contains(body, "BEGIN:STANDARD") {
		t.Fatalf("VTIMEZONE observances are unbalanced:\n%s", body)
	}
}

func TestCalendarCancelUsesCancelMethodAndRSVPFalse(t *testing.T) {
	event := map[string]any{
		"id": "cal_cancel", "uid": "cal_cancel", "title": "Cancelled review",
		"date": "2026-11-04", "start": "09:00", "end": "10:00",
		"startsAt": "2026-11-04T09:00:00Z", "endsAt": "2026-11-04T10:00:00Z",
		"timezone": "UTC", "status": "cancelled", "organizer": "owner@example.com",
		"sequence": int64(4), "responseRequested": true,
		"attendeeList": []any{
			map[string]any{"email": "guest@example.com", "name": "Guest", "status": "accepted"},
		},
	}
	body := buildCalendarICS("Alt8Mail invitation", []map[string]any{event}, "CANCEL", "https://mail.example.com")
	if !strings.Contains(body, "METHOD:CANCEL") || !strings.Contains(body, "STATUS:CANCELLED") {
		t.Fatalf("cancel calendar is missing CANCEL/CANCELLED:\n%s", body)
	}
	if !strings.Contains(body, "SEQUENCE:4") || !strings.Contains(body, "RSVP=FALSE") {
		t.Fatalf("cancel calendar has wrong sequence or RSVP:\n%s", body)
	}
}

func TestBuildCalendarInviteMIMEKeepsMethodAligned(t *testing.T) {
	event := map[string]any{
		"id": "cal_mime", "uid": "cal_mime@example.com", "title": "MIME review",
		"date": "2026-11-04", "start": "09:00", "end": "10:00",
		"startsAt": "2026-11-04T09:00:00Z", "endsAt": "2026-11-04T10:00:00Z",
		"timezone": "UTC", "status": "confirmed", "organizer": "owner@example.com",
		"sequence": int64(1), "responseRequested": true,
		"attendeeList": []any{map[string]any{"email": "guest@example.com", "name": "Guest", "status": "needs-action"}},
	}
	ics := icsInviteEmail(event, "REQUEST", "https://mail.example.com")
	raw, err := buildCalendarInviteMIME("Owner", "owner@example.com", "owner@example.com", "Guest", "guest@example.com",
		"Invitation: MIME review", "You are invited", "<p>You are invited</p>", ics, "REQUEST", "<mime@example.com>", "EA", "ea@example.com")
	if err != nil {
		t.Fatalf("buildCalendarInviteMIME: %v", err)
	}
	message := string(raw)
	if strings.Count(message, "Content-Type: text/calendar") != 1 {
		t.Fatalf("invite MIME must contain exactly one calendar part:\n%s", message)
	}
	for _, want := range []string{
		"Content-Type: multipart/mixed", "multipart/alternative", "Sender: \"EA\" <ea@example.com>",
		"Content-Type: text/calendar; charset=UTF-8; method=REQUEST; name=invite.ics",
		"Content-Disposition: attachment; filename=invite.ics",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("invite MIME is missing %q:\n%s", want, message)
		}
	}
	if _, err := buildCalendarInviteMIME("Owner", "owner@example.com", "owner@example.com", "Guest", "guest@example.com",
		"Invitation", "text", "<p>text</p>", ics, "CANCEL", "<mime2@example.com>", "", ""); err == nil {
		t.Fatal("MIME builder accepted a CANCEL parameter with REQUEST calendar data")
	}
}

func TestCalendarRSVPTokenPinsAttendeeSequenceAndExpiry(t *testing.T) {
	server := &Server{cfg: config.Config{
		PublicURL: "https://mail.example.com", SessionSecret: "session-secret",
		CSRFSecret: "csrf-secret", MailDomain: "example.com",
	}}
	event := map[string]any{
		"id": "cal_token", "uid": "cal_token@example.com", "sequence": int64(2),
		"endsAt": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}
	link := server.calendarRSVPURL(event, "Guest@Example.com")
	if !strings.HasPrefix(link, "https://mail.example.com/calendar/rsvp/") {
		t.Fatalf("RSVP link = %q", link)
	}
	token := strings.TrimPrefix(link, "https://mail.example.com/calendar/rsvp/")
	claims, err := server.parseCalendarRSVPToken(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.EventID != "cal_token" || claims.UID != "cal_token@example.com" ||
		claims.Email != "guest@example.com" || claims.Sequence != 2 {
		t.Fatalf("claims = %#v", claims)
	}
	replacement := byte('A')
	if token[0] == 'A' {
		replacement = 'B'
	}
	tampered := string(replacement) + token[1:]
	if _, err := server.parseCalendarRSVPToken(tampered); err == nil {
		t.Fatal("tampered RSVP token was accepted")
	}
	expired := server.signCalendarRSVPToken(calendarRSVPClaims{
		EventID: "cal_token", UID: "cal_token@example.com", Email: "guest@example.com",
		Sequence: 2, Expires: time.Now().Add(-time.Minute).Unix(),
	})
	if _, err := server.parseCalendarRSVPToken(expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestCalendarRSVPPageTemplateEscapesAndPosts(t *testing.T) {
	server := &Server{cfg: config.Config{
		PublicURL: "https://mail.example.com", SessionSecret: "session-secret",
		CSRFSecret: "csrf-secret", MailDomain: "example.com",
	}}
	event := map[string]any{
		"id": "cal_page", "uid": "cal_page@example.com", "title": `Quarterly <review>`,
		"sequence": int64(1), "endsAt": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}
	token := strings.TrimPrefix(server.calendarRSVPURL(event, "guest@example.com"),
		"https://mail.example.com/calendar/rsvp/")
	var rendered bytes.Buffer
	err := calendarRSVPPageTemplate.Execute(&rendered, calendarRSVPPageData{
		Title: "Quarterly <review>", When: "Wednesday", Organizer: "owner@example.com",
		CanRespond: true, Token: "/calendar/rsvp/" + token,
	})
	if err != nil {
		t.Fatalf("render RSVP page: %v", err)
	}
	page := rendered.String()
	if strings.Contains(page, "ZgotmplZ") {
		t.Fatalf("signed token was rejected by html/template:\n%s", page)
	}
	if !strings.Contains(page, `action="/calendar/rsvp/`+token+`"`) {
		t.Fatalf("RSVP form action is missing the signed token:\n%s", page)
	}
	if !strings.Contains(page, "Propose a new time") || !strings.Contains(page, "proposedTimezone") {
		t.Fatalf("RSVP page is missing the proposed-time form:\n%s", page)
	}
	if !strings.Contains(page, "Quarterly &lt;review&gt;") {
		t.Fatalf("event title was not escaped:\n%s", page)
	}
}

func TestParseIMIPReplyWithoutDTSTART(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\n" +
		"BEGIN:VEVENT\r\nUID:invite-123@example.com\r\nSEQUENCE:3\r\n" +
		"ORGANIZER:mailto:owner@example.com\r\n" +
		"ATTENDEE;CN=Guest;PARTSTAT=DECLINED;ROLE=REQ-PARTICIPANT:mailto:guest@example.com\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := parseICSReplies([]byte(body))
	if err != nil || len(events) != 1 {
		t.Fatalf("parseICSReplies = %d events, %v", len(events), err)
	}
	reply := events[0]
	if reply.UID != "invite-123@example.com" || reply.Sequence != 3 || reply.Organizer != "owner@example.com" {
		t.Fatalf("reply = %#v", reply)
	}
	if len(reply.AttendeeDetails) != 1 || reply.AttendeeDetails[0].PartStat != "DECLINED" {
		t.Fatalf("attendee details = %#v", reply.AttendeeDetails)
	}
	if icsMethodValue(body) != "REPLY" {
		t.Fatalf("METHOD = %q", icsMethodValue(body))
	}
	if _, err := parseICS([]byte(body)); err == nil {
		t.Fatal("strict parser accepted a VEVENT without DTSTART")
	}
}

func TestCalendarICSIncludesRecurrenceExceptionAndSender(t *testing.T) {
	event := map[string]any{
		"id": "cal_rec", "uid": "cal_rec@example.com", "title": "Weekly sync",
		"date": "2026-11-04", "start": "15:00", "end": "16:00",
		"startsAt": "2026-11-04T14:00:00Z", "endsAt": "2026-11-04T15:00:00Z",
		"timezone": "Europe/Berlin", "status": "confirmed", "organizer": "owner@example.com",
		"organizerName": "Owner", "senderEmail": "ea@example.com", "senderName": "EA",
		"recurrenceRule": "FREQ=WEEKLY;BYDAY=WE", "sequence": int64(5), "feedUrl": "https://mail.example.com/calendar/feed/token.ics",
		"responseRequested": true, "reminderMinutes": int64(15),
		"attendeeList": []any{map[string]any{"email": "guest@example.com", "name": "Guest", "status": "accepted"}},
		"exceptions": []any{map[string]any{
			"id": "exc1", "recurrenceId": "2026-11-11T14:00:00Z", "recurrenceDate": "2026-11-11",
			"title": "Weekly sync moved", "description": "", "location": "Room 2",
			"startsAt": "2026-11-11T16:00:00Z", "endsAt": "2026-11-11T17:00:00Z",
			"date": "2026-11-11", "start": "17:00", "end": "18:00", "allDay": false,
			"timezone": "Europe/Berlin", "status": "confirmed", "sequence": int64(5),
			"reminderMinutes": int64(15),
		}},
	}
	body := buildCalendarICS("Product", []map[string]any{event}, "REQUEST", "https://mail.example.com")
	if strings.Count(body, "BEGIN:VEVENT") != 2 {
		t.Fatalf("expected master + exception VEVENT:\n%s", body)
	}
	for _, want := range []string{
		"SENDER;CN=EA:mailto:ea@example.com",
		"SOURCE;VALUE=URI:https://mail.example.com/calendar/feed/token.ics",
		"RECURRENCE-ID;TZID=Europe/Berlin:20261111T150000",
		"DTSTART;TZID=Europe/Berlin:20261111T170000",
		"SUMMARY:Weekly sync moved",
		"BEGIN:VALARM",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("exception calendar is missing %q:\n%s", want, body)
		}
	}
}

func TestCalendarInviteLocalizesBodyAndSubject(t *testing.T) {
	server := &Server{cfg: config.Config{
		PublicURL: "https://mail.example.com", SessionSecret: "session-secret",
		CSRFSecret: "csrf-secret", MailDomain: "example.com",
	}}
	organizer := userRecord{Email: "owner@example.com", Name: "Owner", TenantID: "tenant"}
	sender := userRecord{Email: "owner@example.com", Name: "Owner", TenantID: "tenant",
		Preferences: json.RawMessage(`{"language":"de-DE"}`)}
	event := map[string]any{
		"id": "cal_de", "uid": "cal_de@example.com", "title": "Quartalsprüfung",
		"date": "2026-11-04", "start": "09:00", "end": "10:00",
		"startsAt": "2026-11-04T09:00:00Z", "endsAt": "2026-11-04T10:00:00Z",
		"timezone": "Europe/Berlin", "status": "confirmed", "organizer": "owner@example.com",
		"organizerName": "Owner", "sequence": int64(0), "responseRequested": true,
	}
	attendee := map[string]any{"email": "guest@example.com", "name": "Guest"}
	subject, textBody, htmlBody := calendarInviteBodies(server, sender, organizer, event, attendee, "REQUEST")
	if !strings.HasPrefix(subject, "Einladung:") {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.Contains(textBody, "Sie wurden zu einem Termin eingeladen") || !strings.Contains(textBody, "Ja, ich nehme teil") {
		t.Fatalf("German body missing:\n%s", textBody)
	}
	if !strings.Contains(htmlBody, `lang="de"`) || !strings.Contains(htmlBody, "Ja, ich nehme teil") {
		t.Fatalf("German HTML missing:\n%s", htmlBody)
	}
}

func TestParseIMIPCounter(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:COUNTER\r\n" +
		"BEGIN:VEVENT\r\nUID:invite-123@example.com\r\nSEQUENCE:4\r\n" +
		"ORGANIZER:mailto:owner@example.com\r\n" +
		"ATTENDEE;CN=Guest;PARTSTAT=TENTATIVE:mailto:guest@example.com\r\n" +
		"DTSTART;TZID=America/New_York:20261105T130000\r\n" +
		"DTEND;TZID=America/New_York:20261105T140000\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := parseICSReplies([]byte(body))
	if err != nil || len(events) != 1 {
		t.Fatalf("parseICSReplies = %d events, %v", len(events), err)
	}
	counter := events[0]
	if icsMethodValue(body) != "COUNTER" || counter.Sequence != 4 {
		t.Fatalf("counter = %#v", counter)
	}
	if counter.StartsAt.IsZero() || counter.Timezone != "America/New_York" {
		t.Fatalf("counter time = %s %q", counter.StartsAt, counter.Timezone)
	}
}

func TestExpandCalendarEventsHandlesTypedExceptionRows(t *testing.T) {
	event := map[string]any{
		"id": "cal_typed", "uid": "cal_typed", "title": "Master", "startsAt": "2026-11-04T09:00:00Z",
		"endsAt": "2026-11-04T10:00:00Z", "timezone": "UTC", "date": "2026-11-04",
		"start": "09:00", "end": "10:00", "recurrenceRule": "FREQ=DAILY",
		"exceptions": []calendarExceptionPublic{{
			ID: "exc_typed", RecurrenceID: "2026-11-05T09:00:00Z", RecurrenceDate: "2026-11-05",
			Title: "Exception", StartsAt: "2026-11-05T11:00:00Z", EndsAt: "2026-11-05T12:00:00Z",
			Date: "2026-11-05", Start: "11:00", End: "12:00", Timezone: "UTC",
			Status: "confirmed", Sequence: 2, ReminderMinutes: 15,
		}},
	}
	expanded := expandCalendarEvents([]map[string]any{event})
	if len(expanded) != 2 {
		t.Fatalf("expanded %d events, want 2", len(expanded))
	}
	if expanded[1]["recurrenceId"] != "2026-11-05T09:00:00Z" || expanded[1]["title"] != "Exception" {
		t.Fatalf("exception expansion = %#v", expanded[1])
	}
}

func TestCalendarRSVPAnalyticsForLargeMeeting(t *testing.T) {
	invited := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	answered := time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	attendees := []calendarAttendeePublic{{Email: "owner@example.com", Organizer: true, Status: "accepted"}}
	for index := 0; index < 11; index++ {
		domain := "example.com"
		if index%2 == 1 {
			domain = "partner.example"
		}
		attendees = append(attendees, calendarAttendeePublic{
			Email: "guest" + string(rune('a'+index)) + "@" + domain, Status: "accepted",
			LastInviteAt: invited, LastResponseAt: answered, LastDeliveryStatus: "sent",
		})
	}
	analytics := calendarRSVPAnalytics(attendees, time.Now().Add(-3*time.Hour))
	if analytics == nil {
		t.Fatal("expected analytics for more than ten guests")
	}
	if analytics["total"] != 11 || analytics["responded"] != 11 || analytics["delivered"] != 11 {
		t.Fatalf("analytics = %#v", analytics)
	}
	domains, _ := analytics["domains"].([]map[string]any)
	if len(domains) != 2 {
		t.Fatalf("domains = %#v", analytics["domains"])
	}
	if analytics["responseRate"] != float64(100) {
		t.Fatalf("response rate = %#v", analytics["responseRate"])
	}
	if analytics["averageResponseMinutes"] == nil {
		t.Fatal("average response time was not calculated")
	}
}

func TestCalendarICSIncludesBookedResources(t *testing.T) {
	event := map[string]any{
		"id": "cal_res", "uid": "cal_res", "title": "Room booking",
		"date": "2026-11-04", "start": "09:00", "end": "10:00",
		"startsAt": "2026-11-04T09:00:00Z", "endsAt": "2026-11-04T10:00:00Z",
		"timezone": "UTC", "status": "confirmed", "organizer": "owner@example.com",
		"sequence": int64(0), "responseRequested": true,
		"resourceList": []any{map[string]any{"id": "room-a", "name": "Conference Room A", "email": "room-a@example.com"}},
	}
	body := buildCalendarICS("Product", []map[string]any{event}, "REQUEST", "https://mail.example.com")
	for _, want := range []string{"CUTYPE=RESOURCE", "RESOURCES:Conference Room A", "mailto:room-a@example.com"} {
		if !strings.Contains(body, want) {
			t.Fatalf("resource calendar is missing %q:\n%s", want, body)
		}
	}
}

func TestCalendarPollTokenPinsInvitee(t *testing.T) {
	server := &Server{cfg: config.Config{
		PublicURL: "https://mail.example.com", SessionSecret: "session-secret",
		CSRFSecret: "csrf-secret", MailDomain: "example.com",
	}}
	link := server.calendarPollURL("poll-1", "Guest@Example.com")
	token := strings.TrimPrefix(link, "https://mail.example.com/calendar/poll/")
	claims, err := server.parseCalendarPollToken(token)
	if err != nil {
		t.Fatalf("parse poll token: %v", err)
	}
	if claims.PollID != "poll-1" || claims.Email != "guest@example.com" {
		t.Fatalf("claims = %#v", claims)
	}
	replacement := byte('A')
	if token[0] == 'A' {
		replacement = 'B'
	}
	if _, err := server.parseCalendarPollToken(string(replacement) + token[1:]); err == nil {
		t.Fatal("tampered poll token was accepted")
	}
}

func TestCalendarICSFoldsLongLinesAtSeventyFiveOctets(t *testing.T) {
	event := map[string]any{
		"id": "cal_fold", "uid": "cal_fold", "title": strings.Repeat("Quarterly planning with international guests — ", 5),
		"date": "2026-11-04", "start": "09:00", "end": "10:00",
		"startsAt": "2026-11-04T09:00:00Z", "endsAt": "2026-11-04T10:00:00Z",
		"timezone": "Asia/Dubai", "status": "confirmed", "organizer": "owner@example.com",
		"sequence": int64(1), "responseRequested": true,
	}
	body := buildCalendarICS("Product", []map[string]any{event}, "REQUEST", "https://mail.example.com")
	for _, line := range strings.Split(body, "\r\n") {
		if len([]byte(line)) > 75 {
			t.Fatalf("unfolded ICS line is %d octets: %q", len([]byte(line)), line)
		}
	}
	if !strings.Contains(body, "\r\n ") {
		t.Fatal("long ICS content was not folded with CRLF + space")
	}
}

func TestCalendarPollPageTemplateEscapesAndSubmits(t *testing.T) {
	server := &Server{cfg: config.Config{
		PublicURL: "https://mail.example.com", SessionSecret: "session-secret",
		CSRFSecret: "csrf-secret", MailDomain: "example.com",
	}}
	link := server.calendarPollURL("poll-1", "guest@example.com")
	token := strings.TrimPrefix(link, "https://mail.example.com/calendar/poll/")
	var rendered bytes.Buffer
	err := calendarPollPageTemplate.Execute(&rendered, calendarPollPageData{
		Title: "Partner <kickoff>", Timezone: "UTC", Token: "/calendar/poll/" + token,
		Slots: []calendarPollPageSlot{{ID: "slot-1", Date: "Thu, Nov 13, 2026", Start: "09:00", End: "10:00"}},
	})
	if err != nil {
		t.Fatalf("render poll page: %v", err)
	}
	page := rendered.String()
	if strings.Contains(page, "ZgotmplZ") {
		t.Fatalf("poll token was rejected by html/template:\n%s", page)
	}
	if !strings.Contains(page, "slot_slot-1") || !strings.Contains(page, "Send availability") {
		t.Fatalf("poll form is incomplete:\n%s", page)
	}
	if !strings.Contains(page, "Partner &lt;kickoff&gt;") {
		t.Fatalf("poll title was not escaped:\n%s", page)
	}
}

func TestCalendarAnalyticsSkipsSmallMeetings(t *testing.T) {
	attendees := []calendarAttendeePublic{{Email: "owner@example.com", Organizer: true}}
	for index := 0; index < 10; index++ {
		attendees = append(attendees, calendarAttendeePublic{Email: "guest" + string(rune('a'+index)) + "@example.com"})
	}
	if analytics := calendarRSVPAnalytics(attendees, time.Now()); analytics != nil {
		t.Fatalf("analytics should start above ten guests: %#v", analytics)
	}
}
