package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type parsedCalendarEvent struct {
	UID, Title, Description, Location, Timezone, RecurrenceRule, Status, Organizer, Transparency string
	StartsAt, EndsAt                                                                             time.Time
	AllDay                                                                                       bool
	Sequence                                                                                     int
	Attendees                                                                                    []string
}

func icsEscape(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", ";", "\\;", ",", "\\,", "\r\n", "\\n", "\n", "\\n", "\r", "\\n",
	)
	return replacer.Replace(value)
}

func icsUnescape(value string) string {
	value = strings.ReplaceAll(value, "\\n", "\n")
	value = strings.ReplaceAll(value, "\\N", "\n")
	value = strings.ReplaceAll(value, "\\,", ",")
	value = strings.ReplaceAll(value, "\\;", ";")
	value = strings.ReplaceAll(value, "\\\\", "\\")
	return value
}

func icsFold(line string) string {
	if len(line) <= 73 {
		return line
	}
	var out strings.Builder
	remaining := line
	first := true
	for len(remaining) > 0 {
		limit := 73
		if !first {
			limit = 72
		}
		if len(remaining) <= limit {
			if !first {
				out.WriteString("\r\n ")
			}
			out.WriteString(remaining)
			break
		}
		chunk := remaining[:limit]
		// Do not split a UTF-8 rune.
		for len(chunk) > 0 && chunk[len(chunk)-1]&0xC0 == 0x80 {
			chunk = chunk[:len(chunk)-1]
		}
		if len(chunk) == 0 {
			chunk = remaining[:limit]
		}
		if !first {
			out.WriteString("\r\n ")
		}
		out.WriteString(chunk)
		remaining = remaining[len(chunk):]
		first = false
	}
	return out.String()
}

func icsUTC(value time.Time) string {
	return value.UTC().Format("20060102T150405Z")
}

func calendarEventICS(event map[string]any, method string, publicURL string) string {
	var out strings.Builder
	line := func(value string) {
		out.WriteString(icsFold(value))
		out.WriteString("\r\n")
	}
	uid := stateString(event, "uid")
	if uid == "" {
		uid = stateString(event, "id")
	}
	allDay := stateBool(event, "allDay")
	starts, err := time.Parse(time.RFC3339, stateString(event, "startsAt"))
	if err != nil {
		starts = time.Now().UTC()
	}
	ends, err := time.Parse(time.RFC3339, stateString(event, "endsAt"))
	if err != nil {
		ends = starts.Add(time.Hour)
	}
	line("BEGIN:VEVENT")
	line("UID:" + icsEscape(uid))
	line("DTSTAMP:" + icsUTC(time.Now()))
	if allDay {
		line("DTSTART;VALUE=DATE:" + starts.UTC().Format("20060102"))
		line("DTEND;VALUE=DATE:" + ends.AddDate(0, 0, 1).UTC().Format("20060102"))
	} else {
		line("DTSTART:" + icsUTC(starts))
		line("DTEND:" + icsUTC(ends))
	}
	line("SUMMARY:" + icsEscape(stateString(event, "title")))
	if description := stateString(event, "description"); description != "" {
		line("DESCRIPTION:" + icsEscape(description))
	}
	if where := stateString(event, "where"); where != "" {
		line("LOCATION:" + icsEscape(where))
	}
	organizer := stateString(event, "organizer")
	organizerName := stateString(event, "organizerName")
	if organizer != "" {
		line("ORGANIZER" + icsParameter("CN", organizerName) + ":mailto:" + organizer)
	}
	for _, attendee := range stateSlice(event, "attendeeList") {
		entry, ok := attendee.(map[string]any)
		if !ok {
			continue
		}
		email := stateString(entry, "email")
		if email == "" {
			continue
		}
		status := "NEEDS-ACTION"
		switch stateString(entry, "status") {
		case "accepted":
			status = "ACCEPTED"
		case "declined":
			status = "DECLINED"
		case "tentative":
			status = "TENTATIVE"
		}
		line("ATTENDEE" + icsParameter("CN", stateString(entry, "name")) + ";CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=" + status + ";RSVP=TRUE:mailto:" + email)
	}
	if rule := stateString(event, "recurrenceRule"); rule != "" {
		line("RRULE:" + strings.TrimPrefix(rule, "RRULE:"))
	}
	status := "CONFIRMED"
	switch stateString(event, "status") {
	case "tentative":
		status = "TENTATIVE"
	case "cancelled":
		status = "CANCELLED"
	}
	line("STATUS:" + status)
	line("SEQUENCE:" + strconv.FormatInt(stateInt(event, "sequence"), 10))
	if stateString(event, "transparency") == "free" {
		line("TRANSP:TRANSPARENT")
	} else {
		line("TRANSP:OPAQUE")
	}
	if meeting := stateString(event, "meetingUrl"); meeting != "" {
		line("URL:" + icsEscape(meeting))
	}
	for _, attachment := range stateSlice(event, "attachments") {
		entry, ok := attachment.(map[string]any)
		if !ok || stateString(entry, "contentUrl") == "" {
			continue
		}
		line("ATTACH;FMTTYPE=" + icsEscape(firstNonEmpty(stateString(entry, "mime"), "application/octet-stream")) + ":" +
			icsEscape(strings.TrimRight(publicURL, "/")+stateString(entry, "contentUrl")))
	}
	if reminder := stateInt(event, "reminderMinutes"); reminder > 0 {
		line("BEGIN:VALARM")
		line("ACTION:DISPLAY")
		line("DESCRIPTION:" + icsEscape(stateString(event, "title")))
		line("TRIGGER:-PT" + strconv.FormatInt(reminder, 10) + "M")
		line("END:VALARM")
	}
	line("END:VEVENT")
	return out.String()
}

func icsParameter(name, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return ";" + name + "=" + strings.ReplaceAll(strings.ReplaceAll(icsEscape(value), ";", ","), "\n", " ")
}

func buildCalendarICS(calendarName string, events []map[string]any, method string, publicURL string) string {
	var out strings.Builder
	line := func(value string) {
		out.WriteString(icsFold(value))
		out.WriteString("\r\n")
	}
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Alt8Mail//Calendar 1.0//EN")
	line("CALSCALE:GREGORIAN")
	line("X-WR-CALNAME:" + icsEscape(calendarName))
	if method != "" {
		line("METHOD:" + method)
	}
	for _, event := range events {
		out.WriteString(calendarEventICS(event, method, publicURL))
	}
	line("END:VCALENDAR")
	return out.String()
}

func icsInviteEmail(event map[string]any, method string, publicURL string) string {
	if method == "CANCEL" {
		return buildCalendarICS("Alt8Mail invitation", []map[string]any{event}, "CANCEL", publicURL)
	}
	return buildCalendarICS("Alt8Mail invitation", []map[string]any{event}, "REQUEST", publicURL)
}

// sendCalendarInvites sends a best-effort MIME invitation to every attendee.
// Calendar mutations must never fail because a mailbox is offline, so errors
// are logged and the in-app invitation remains available.
func (s *Server) sendCalendarInvites(ctx context.Context, organizer userRecord, eventID, method string) {
	events, err := s.loadCalendarEvents(ctx, organizer)
	if err != nil {
		return
	}
	for _, event := range events {
		if stateString(event, "id") == eventID {
			s.sendCalendarInvitePayload(ctx, organizer, event, method)
			return
		}
	}
}

func (s *Server) sendCalendarInvitePayload(ctx context.Context, organizer userRecord, event map[string]any, method string) {
	if len(stateSlice(event, "attendeeList")) == 0 {
		return
	}
	recipients := []string{}
	for _, attendee := range stateSlice(event, "attendeeList") {
		entry, ok := attendee.(map[string]any)
		if !ok {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(stateString(entry, "email")))
		if email == "" || email == strings.ToLower(organizer.Email) {
			continue
		}
		recipients = append(recipients, email)
	}
	if len(recipients) == 0 {
		return
	}
	title := stateString(event, "title")
	subject := "Invitation: " + title
	if method == "CANCEL" {
		subject = "Cancelled: " + title
	}
	when := stateString(event, "date") + " " + stateString(event, "start") + "–" + stateString(event, "end")
	body := fmt.Sprintf("%s\n\n%s\n%s\n\nOrganizer: %s\n", title, when,
		firstNonEmpty(stateString(event, "where"), "No location"), organizer.Email)
	if method == "CANCEL" {
		body = "This event has been cancelled.\n\n" + body
	}
	ics := icsInviteEmail(event, method, s.cfg.PublicURL)
	item := map[string]any{
		"from":        map[string]any{"name": organizer.Name, "email": organizer.Email},
		"to":          peopleForEmails(recipients),
		"subject":     subject,
		"bodyText":    body,
		"bodyHtml":    textToHTML(body),
		"trustedHtml": true,
	}
	raw, err := buildMIMEMessage(item, fmt.Sprintf("<%s@%s>", newID("calinvite"), s.cfg.MailDomain),
		[]outboundAttachment{{Name: "invite.ics", MIMEType: "text/calendar; charset=UTF-8; method=" + method, Data: []byte(ics)}})
	if err != nil {
		s.logger.Warn("calendar invite MIME build failed", "err", err, "event", stateString(event, "id"))
		return
	}
	if err := s.submitSMTPMessage(organizer.Email, recipients, raw); err != nil {
		s.logger.Warn("calendar invite delivery failed", "err", err, "event", stateString(event, "id"))
	}
}

func peopleForEmails(emails []string) []any {
	people := make([]any, 0, len(emails))
	for _, email := range emails {
		people = append(people, map[string]any{"name": "", "email": email})
	}
	return people
}

// ---------------------------------------------------------------------------
// ICS parsing (subscriptions, file import)
// ---------------------------------------------------------------------------

func unfoldICS(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(lines) > 0 {
				lines[len(lines)-1] += line[1:]
			}
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parseICSProperty(line string) (name string, params map[string]string, value string) {
	params = map[string]string{}
	index := strings.Index(line, ":")
	if index < 0 {
		return "", params, ""
	}
	head, value := line[:index], line[index+1:]
	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	for _, part := range parts[1:] {
		if eq := strings.Index(part, "="); eq > 0 {
			params[strings.ToUpper(part[:eq])] = strings.Trim(part[eq+1:], `"`)
		}
	}
	return name, params, value
}

var windowsTimeZones = map[string]string{
	"Eastern Standard Time": "America/New_York", "Central Standard Time": "America/Chicago",
	"Mountain Standard Time": "America/Denver", "Pacific Standard Time": "America/Los_Angeles",
	"GMT Standard Time": "Europe/London", "W. Europe Standard Time": "Europe/Berlin",
	"Romance Standard Time": "Europe/Paris", "Central Europe Standard Time": "Europe/Budapest",
	"E. Europe Standard Time": "Europe/Chisinau", "FLE Standard Time": "Europe/Kyiv",
	"Russian Standard Time": "Europe/Moscow", "Turkey Standard Time": "Europe/Istanbul",
	"Arabian Standard Time": "Asia/Dubai", "India Standard Time": "Asia/Kolkata",
	"China Standard Time": "Asia/Shanghai", "Tokyo Standard Time": "Asia/Tokyo",
	"Korea Standard Time": "Asia/Seoul", "AUS Eastern Standard Time": "Australia/Sydney",
	"New Zealand Standard Time": "Pacific/Auckland", "South Africa Standard Time": "Africa/Johannesburg",
	"E. Africa Standard Time": "Africa/Nairobi",
}

func normalizeTZID(tzid string) string {
	tzid = strings.TrimSpace(tzid)
	if loaded, err := time.LoadLocation(tzid); err == nil {
		return loaded.String()
	}
	if mapped, ok := windowsTimeZones[tzid]; ok {
		return mapped
	}
	return ""
}

func parseICSTime(value string, params map[string]string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, fmt.Errorf("empty date")
	}
	allDay := strings.EqualFold(params["VALUE"], "DATE") || (len(value) == 8 && !strings.Contains(value, "T"))
	if allDay {
		parsed, err := time.ParseInLocation("20060102", value, time.UTC)
		return parsed, true, err
	}
	zone := time.UTC
	if tzid := params["TZID"]; tzid != "" {
		if normalized := normalizeTZID(tzid); normalized != "" {
			zone, _ = time.LoadLocation(normalized)
		}
	}
	if strings.HasSuffix(value, "Z") {
		parsed, err := time.Parse("20060102T150405Z", value)
		return parsed.UTC(), false, err
	}
	parsed, err := time.ParseInLocation("20060102T150405", value, zone)
	return parsed, false, err
}

func parseICS(data []byte) ([]parsedCalendarEvent, error) {
	lines := unfoldICS(data)
	events := []parsedCalendarEvent{}
	var current *parsedCalendarEvent
	var startValue, endValue string
	var startParams, endParams map[string]string
	for _, line := range lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case upper == "BEGIN:VEVENT":
			current = &parsedCalendarEvent{Status: "confirmed", Transparency: "busy"}
			startValue, endValue = "", ""
			startParams, endParams = map[string]string{}, map[string]string{}
			continue
		case upper == "END:VEVENT":
			if current == nil {
				continue
			}
			if startValue != "" {
				if parsed, allDay, err := parseICSTime(startValue, startParams); err == nil {
					current.StartsAt, current.AllDay = parsed, allDay
					if endValue != "" {
						if end, _, err := parseICSTime(endValue, endParams); err == nil {
							current.EndsAt = end
						}
					}
					if current.EndsAt.IsZero() || !current.EndsAt.After(current.StartsAt) {
						if current.AllDay {
							current.EndsAt = current.StartsAt.AddDate(0, 0, 1).Add(-time.Second)
						} else {
							current.EndsAt = current.StartsAt.Add(time.Hour)
						}
					} else if current.AllDay {
						// DTEND is exclusive for all-day events.
						current.EndsAt = current.EndsAt.Add(-time.Second)
					}
					if current.Title == "" {
						current.Title = "Imported event"
					}
					events = append(events, *current)
				}
			}
			current = nil
			continue
		}
		if current == nil {
			continue
		}
		name, params, value := parseICSProperty(strings.TrimSpace(line))
		switch name {
		case "UID":
			current.UID = strings.TrimSpace(value)
		case "SUMMARY":
			current.Title = icsUnescape(value)
		case "DESCRIPTION":
			current.Description = truncate(icsUnescape(value), 5000)
		case "LOCATION":
			current.Location = truncate(icsUnescape(value), 240)
		case "DTSTART":
			startValue, startParams = value, params
			if tzid := params["TZID"]; tzid != "" {
				if normalized := normalizeTZID(tzid); normalized != "" {
					current.Timezone = normalized
				}
			}
		case "DTEND":
			endValue, endParams = value, params
		case "RRULE":
			current.RecurrenceRule = truncate(strings.TrimSpace(value), 500)
		case "STATUS":
			status := strings.ToLower(strings.TrimSpace(value))
			if status == "tentative" || status == "cancelled" || status == "confirmed" {
				current.Status = status
			}
		case "ORGANIZER":
			current.Organizer = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "mailto:"))
		case "ATTENDEE":
			email := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "mailto:"))
			if calendarEmailPattern.MatchString(email) {
				current.Attendees = append(current.Attendees, email)
			}
		case "SEQUENCE":
			current.Sequence, _ = strconv.Atoi(strings.TrimSpace(value))
		case "TRANSP":
			if strings.EqualFold(strings.TrimSpace(value), "TRANSPARENT") {
				current.Transparency = "free"
			}
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("no calendar events were found in this file")
	}
	sort.Slice(events, func(i, j int) bool { return events[i].StartsAt.Before(events[j].StartsAt) })
	if len(events) > 5000 {
		events = events[:5000]
	}
	return events, nil
}

// safeFetchCalendarURL prevents the subscription importer from being used to
// reach internal services. HTTPS only, public addresses only, bounded body.
func safeFetchCalendarURL(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, "", fmt.Errorf("calendar subscriptions require an https:// address")
	}
	if parsed.User != nil {
		return nil, "", fmt.Errorf("calendar subscriptions cannot include credentials in the URL")
	}
	host := parsed.Hostname()
	if err := ensurePublicHost(host); err != nil {
		return nil, "", err
	}
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
	}
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("calendar redirects must stay on https")
			}
			if err := ensurePublicHost(req.URL.Hostname()); err != nil {
				return err
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Accept", "text/calendar, text/plain;q=0.8, */*;q=0.5")
	request.Header.Set("User-Agent", "Alt8Mail-Calendar/1.0")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("could not reach the calendar feed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the calendar feed returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return nil, "", fmt.Errorf("could not read the calendar feed")
	}
	if len(data) > 8*1024*1024 {
		return nil, "", fmt.Errorf("the calendar feed is larger than 8 MB")
	}
	if !bytes.Contains(bytes.ToUpper(data), []byte("BEGIN:VCALENDAR")) {
		return nil, "", fmt.Errorf("that address did not return an iCalendar feed")
	}
	return data, parsed.Host, nil
}

func ensurePublicHost(host string) error {
	if host == "" || strings.EqualFold(host, "localhost") {
		return fmt.Errorf("that calendar address is not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("the calendar host could not be resolved")
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("that calendar address resolves to a private network and is not allowed")
		}
	}
	return nil
}

func calendarFeedTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
