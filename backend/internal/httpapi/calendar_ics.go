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

type parsedCalendarAttendee struct {
	Email    string
	Name     string
	PartStat string
	Role     string
	RSVP     bool
}

type parsedCalendarEvent struct {
	UID, Title, Description, Location, Timezone, RecurrenceRule, Status, Organizer, Transparency string
	StartsAt, EndsAt, RecurrenceID                                                               time.Time
	AllDay                                                                                       bool
	Sequence                                                                                     int
	Attendees                                                                                    []string
	AttendeeDetails                                                                              []parsedCalendarAttendee
}

func calendarEventLocation(event map[string]any) *time.Location {
	name := stateString(event, "timezone")
	if name == "" || strings.EqualFold(name, "UTC") || strings.EqualFold(name, "Etc/UTC") {
		return time.UTC
	}
	if normalized := normalizeTZID(name); normalized != "" {
		if location, err := time.LoadLocation(normalized); err == nil {
			return location
		}
	}
	return time.UTC
}

func calendarEventZoneName(event map[string]any) string {
	location := calendarEventLocation(event)
	if location == time.UTC {
		return "UTC"
	}
	return location.String()
}

// icsObservances emits the offset changes around the event's year. Clients need
// a VTIMEZONE when a DTSTART carries TZID; without it, Outlook in particular
// treats the wall-clock time as floating or UTC and moves the meeting.
func icsObservances(zoneName string, firstYear, lastYear int) []string {
	location, err := time.LoadLocation(zoneName)
	if err != nil || location == time.UTC {
		return nil
	}
	type observance struct {
		at        time.Time
		from, to  int
		daylight  bool
		nameAfter string
	}
	if firstYear == 0 {
		firstYear = time.Now().Year()
	}
	if lastYear < firstYear {
		lastYear = firstYear
	}
	changes := []observance{}
	cursor := time.Date(firstYear-1, time.December, 31, 0, 0, 0, 0, time.UTC)
	end := time.Date(lastYear+2, time.January, 2, 0, 0, 0, 0, time.UTC)
	_, previousOffset := cursor.In(location).Zone()
	for cursor.Before(end) {
		next := cursor.Add(24 * time.Hour)
		_, nextOffset := next.In(location).Zone()
		if nextOffset != previousOffset {
			// Refine the transition hour within the day.
			low, high := cursor, next
			for high.Sub(low) > time.Minute {
				mid := low.Add(high.Sub(low) / 2)
				_, midOffset := mid.In(location).Zone()
				if midOffset == previousOffset {
					low = mid
				} else {
					high = mid
				}
			}
			at := high.Truncate(time.Minute)
			_, newOffset := at.In(location).Zone()
			name := ""
			if after := at.Add(time.Hour).In(location); after.Year() >= 0 {
				name, _ = after.Zone()
			}
			changes = append(changes, observance{
				at: at, from: previousOffset, to: newOffset,
				daylight: newOffset > previousOffset, nameAfter: name,
			})
			previousOffset = newOffset
		}
		cursor = next
	}
	if len(changes) == 0 {
		// A zone without transitions in this window still needs one observance.
		_, offset := time.Date(firstYear, time.January, 1, 12, 0, 0, 0, location).Zone()
		changes = append(changes, observance{
			at:   time.Date(firstYear, time.January, 1, 0, 0, 0, 0, time.UTC),
			from: offset, to: offset, nameAfter: zoneName,
		})
	}
	out := []string{}
	for _, change := range changes {
		kind := "STANDARD"
		if change.daylight {
			kind = "DAYLIGHT"
		}
		localStart := change.at.Add(time.Duration(change.from) * time.Second)
		offset := func(seconds int) string {
			sign := "+"
			if seconds < 0 {
				sign = "-"
				seconds = -seconds
			}
			return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, (seconds%3600)/60)
		}
		out = append(out, "BEGIN:"+kind,
			"DTSTART:"+localStart.Format("20060102T150405"),
			"TZOFFSETFROM:"+offset(change.from),
			"TZOFFSETTO:"+offset(change.to),
			"TZNAME:"+icsEscape(firstNonEmpty(change.nameAfter, zoneName)),
			"END:"+kind)
	}
	return out
}

func writeICSVTIMEZONE(out *strings.Builder, zoneName string, firstYear, lastYear int) {
	if zoneName == "" || strings.EqualFold(zoneName, "UTC") {
		return
	}
	line := func(value string) {
		out.WriteString(icsFold(value))
		out.WriteString("\r\n")
	}
	line("BEGIN:VTIMEZONE")
	line("TZID:" + icsEscape(zoneName))
	if firstYear == 0 {
		firstYear = time.Now().Year()
	}
	if lastYear < firstYear {
		lastYear = firstYear
	}
	if lastYear-firstYear > 10 {
		lastYear = firstYear + 10
	}
	for _, observance := range icsObservances(zoneName, firstYear, lastYear) {
		line(observance)
	}
	line("END:VTIMEZONE")
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
	zone := calendarEventLocation(event)
	zoneName := calendarEventZoneName(event)
	localStart := starts.In(zone)
	localEnd := ends.In(zone)
	line("BEGIN:VEVENT")
	line("UID:" + icsEscape(uid))
	line("DTSTAMP:" + icsUTC(time.Now()))
	if allDay {
		line("DTSTART;VALUE=DATE:" + localStart.Format("20060102"))
		line("DTEND;VALUE=DATE:" + localEnd.AddDate(0, 0, 1).Format("20060102"))
	} else if zone == time.UTC {
		line("DTSTART:" + icsUTC(starts))
		line("DTEND:" + icsUTC(ends))
	} else {
		line("DTSTART;TZID=" + icsParameterValue(zoneName) + ":" + localStart.Format("20060102T150405"))
		line("DTEND;TZID=" + icsParameterValue(zoneName) + ":" + localEnd.Format("20060102T150405"))
	}
	recurrenceID := stateString(event, "recurrenceId")
	if recurrenceID != "" {
		if parsed, err := time.Parse(time.RFC3339, recurrenceID); err == nil {
			recurrenceZoneName := firstNonEmpty(stateString(event, "recurrenceTimezone"), zoneName)
			recurrenceZone := calendarEventLocation(map[string]any{"timezone": recurrenceZoneName})
			original := parsed.In(recurrenceZone)
			if recurrenceAllDay := stateBool(event, "recurrenceAllDay"); recurrenceAllDay {
				line("RECURRENCE-ID;VALUE=DATE:" + original.Format("20060102"))
			} else if recurrenceZone == time.UTC {
				line("RECURRENCE-ID:" + icsUTC(parsed))
			} else {
				line("RECURRENCE-ID;TZID=" + icsParameterValue(recurrenceZoneName) + ":" + original.Format("20060102T150405"))
			}
		}
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
	if sender := strings.ToLower(strings.TrimSpace(stateString(event, "senderEmail"))); sender != "" && sender != organizer {
		line("SENDER" + icsParameter("CN", stateString(event, "senderName")) + ":mailto:" + sender)
	}
	responseRequested := stateBool(event, "responseRequested")
	for _, attendee := range stateSlice(event, "attendeeList") {
		entry, ok := attendee.(map[string]any)
		if !ok {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(stateString(entry, "email")))
		if email == "" {
			continue
		}
		isOrganizer := stateBool(entry, "organizer") || strings.EqualFold(email, organizer)
		status := "NEEDS-ACTION"
		switch stateString(entry, "status") {
		case "accepted":
			status = "ACCEPTED"
		case "declined":
			status = "DECLINED"
		case "tentative":
			status = "TENTATIVE"
		}
		role := strings.ToUpper(strings.TrimSpace(stateString(entry, "role")))
		if role == "" {
			role = "REQ-PARTICIPANT"
		}
		if isOrganizer {
			role = "CHAIR"
			status = "ACCEPTED"
		}
		rsvp := responseRequested && !isOrganizer && method == "REQUEST"
		line("ATTENDEE" + icsParameter("CN", stateString(entry, "name")) +
			";CUTYPE=INDIVIDUAL;ROLE=" + role +
			";PARTSTAT=" + status +
			";RSVP=" + map[bool]string{true: "TRUE", false: "FALSE"}[rsvp] + ":mailto:" + email)
	}
	resourceNames := []string{}
	for _, raw := range stateSlice(event, "resourceList") {
		resource := calendarResourceMap(raw)
		if resource == nil {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(stateString(resource, "email")))
		if email == "" {
			continue
		}
		name := stateString(resource, "name")
		line("ATTENDEE" + icsParameter("CN", name) + ";CUTYPE=RESOURCE;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED;RSVP=FALSE:mailto:" + email)
		if name != "" {
			resourceNames = append(resourceNames, icsEscape(name))
		}
	}
	if len(resourceNames) > 0 {
		line("RESOURCES:" + strings.Join(resourceNames, ","))
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
	return ";" + name + "=" + icsParameterValue(value)
}

func icsParameterValue(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(icsEscape(value), ";", ","), "\n", " ")
}

func calendarResourceMap(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return value
	case calendarResourcePublic:
		return map[string]any{
			"id": value.ID, "name": value.Name, "email": value.Email, "kind": value.Kind,
			"location": value.Location, "capacity": value.Capacity, "status": value.Status,
		}
	}
	return nil
}

func calendarExceptionMap(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return value
	case calendarExceptionPublic:
		return map[string]any{
			"id": value.ID, "recurrenceId": value.RecurrenceID, "recurrenceDate": value.RecurrenceDate,
			"title": value.Title, "description": value.Description, "location": value.Location,
			"startsAt": value.StartsAt, "endsAt": value.EndsAt, "date": value.Date,
			"start": value.Start, "end": value.End, "allDay": value.AllDay,
			"timezone": value.Timezone, "status": value.Status, "sequence": value.Sequence,
			"reminderMinutes": value.ReminderMinutes,
		}
	}
	return nil
}

func expandCalendarEvents(events []map[string]any) []map[string]any {
	expanded := make([]map[string]any, 0, len(events))
	for _, event := range events {
		expanded = append(expanded, event)
		for _, raw := range stateSlice(event, "exceptions") {
			override := calendarExceptionMap(raw)
			if override == nil {
				continue
			}
			copy := deepCopyMap(event)
			for _, key := range []string{"id", "recurrenceId", "title", "description",
				"startsAt", "endsAt", "allDay", "timezone", "status", "sequence", "reminderMinutes"} {
				if value, exists := override[key]; exists {
					copy[key] = value
				}
			}
			copy["where"] = stateString(override, "location")
			copy["date"] = stateString(override, "date")
			copy["start"] = stateString(override, "start")
			copy["end"] = stateString(override, "end")
			copy["recurrenceRule"] = ""
			copy["exceptions"] = nil
			copy["recurrenceTimezone"] = stateString(event, "timezone")
			copy["recurrenceAllDay"] = stateBool(event, "allDay")
			expanded = append(expanded, copy)
		}
	}
	return expanded
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
	feedURL := ""
	for _, event := range events {
		if value := stateString(event, "feedUrl"); value != "" {
			feedURL = value
			break
		}
	}
	if feedURL != "" {
		line("SOURCE;VALUE=URI:" + icsEscape(feedURL))
	}
	expanded := expandCalendarEvents(events)
	zones := map[string][2]int{}
	for _, event := range expanded {
		zoneName := calendarEventZoneName(event)
		if zoneName == "" || strings.EqualFold(zoneName, "UTC") {
			continue
		}
		year := time.Now().Year()
		if starts, err := time.Parse(time.RFC3339, stateString(event, "startsAt")); err == nil {
			year = starts.In(calendarEventLocation(event)).Year()
		}
		window, ok := zones[zoneName]
		if !ok {
			zones[zoneName] = [2]int{year, year}
		} else {
			if year < window[0] {
				window[0] = year
			}
			if year > window[1] {
				window[1] = year
			}
			zones[zoneName] = window
		}
	}
	for zoneName, window := range zones {
		writeICSVTIMEZONE(&out, zoneName, window[0], window[1])
	}
	for _, event := range expanded {
		out.WriteString(calendarEventICS(event, method, publicURL))
	}
	line("END:VCALENDAR")
	return out.String()
}

func icsInviteEmail(event map[string]any, method string, publicURL string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "REQUEST"
	}
	return buildCalendarICS("Alt8Mail invitation", []map[string]any{event}, method, publicURL)
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
	return parseICSDocument(data, false)
}

// parseICSReplies keeps events that carry UID/SEQUENCE/PARTSTAT but omit
// DTSTART, which is common in short iTIP REPLY messages.
func parseICSReplies(data []byte) ([]parsedCalendarEvent, error) {
	return parseICSDocument(data, true)
}

func parseICSDocument(data []byte, allowMissingStart bool) ([]parsedCalendarEvent, error) {
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
			} else if allowMissingStart && current.UID != "" {
				events = append(events, *current)
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
			current.Organizer = strings.ToLower(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "mailto:"))
		case "ATTENDEE":
			email := strings.ToLower(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "mailto:"))
			if calendarEmailPattern.MatchString(email) {
				current.Attendees = append(current.Attendees, email)
				current.AttendeeDetails = append(current.AttendeeDetails, parsedCalendarAttendee{
					Email: email, Name: icsUnescape(params["CN"]),
					PartStat: strings.ToUpper(strings.TrimSpace(params["PARTSTAT"])),
					Role:     strings.ToUpper(strings.TrimSpace(params["ROLE"])),
					RSVP:     strings.EqualFold(strings.TrimSpace(params["RSVP"]), "TRUE"),
				})
			}
		case "SEQUENCE":
			current.Sequence, _ = strconv.Atoi(strings.TrimSpace(value))
		case "RECURRENCE-ID":
			if parsed, _, err := parseICSTime(value, params); err == nil {
				current.RecurrenceID = parsed.UTC()
			}
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
