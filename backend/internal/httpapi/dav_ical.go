package httpapi

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type icalProperty struct {
	Name   string
	Params map[string]string
	Value  string
}

// cleanICalToken removes CR/LF and control characters from token-like values
// (UID, RRULE) before they are emitted as their own content lines.
func cleanICalToken(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\r' || char == '\n' || char == 0 || char < 0x20 {
			return -1
		}
		return char
	}, value)
}

func unfoldContentLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parseContentLine(line string) (icalProperty, bool) {
	colon := -1
	quoted := false
	for index, char := range line {
		if char == '"' {
			quoted = !quoted
		}
		if char == ':' && !quoted {
			colon = index
			break
		}
	}
	if colon < 0 {
		return icalProperty{}, false
	}
	left := line[:colon]
	value := line[colon+1:]
	parts := splitContentParams(left)
	if len(parts) == 0 {
		return icalProperty{}, false
	}
	prop := icalProperty{Name: strings.ToUpper(strings.TrimSpace(parts[0])), Params: map[string]string{}, Value: value}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		prop.Params[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return prop, true
}

func splitContentParams(value string) []string {
	parts := []string{}
	var builder strings.Builder
	quoted := false
	for _, char := range value {
		if char == '"' {
			quoted = !quoted
			builder.WriteRune(char)
			continue
		}
		if char == ';' && !quoted {
			parts = append(parts, builder.String())
			builder.Reset()
			continue
		}
		builder.WriteRune(char)
	}
	parts = append(parts, builder.String())
	return parts
}

func unescapeICalText(value string) string {
	var builder strings.Builder
	escaped := false
	for _, char := range value {
		if escaped {
			switch char {
			case 'n', 'N':
				builder.WriteByte('\n')
			case 't', 'T':
				builder.WriteByte('\t')
			default:
				builder.WriteRune(char)
			}
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		builder.WriteRune(char)
	}
	if escaped {
		builder.WriteByte('\\')
	}
	return builder.String()
}

func escapeICalText(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\r\n", `\n`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\n`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	return value
}

func parseICalTime(prop icalProperty) (time.Time, bool, string, error) {
	value := strings.TrimSpace(prop.Value)
	location := time.UTC
	timezoneName := "UTC"
	if tzid := strings.TrimSpace(prop.Params["TZID"]); tzid != "" {
		timezoneName = tzid
		if loaded, err := time.LoadLocation(tzid); err == nil {
			location = loaded
		}
	}
	if len(value) == 8 {
		parsed, err := time.ParseInLocation("20060102", value, location)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid date %q", value)
		}
		return parsed, true, timezoneName, nil
	}
	if strings.HasSuffix(strings.ToUpper(value), "Z") {
		parsed, err := time.ParseInLocation("20060102T150405Z", strings.ToUpper(value), time.UTC)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid UTC date-time %q", value)
		}
		return parsed, false, "UTC", nil
	}
	parsed, err := time.ParseInLocation("20060102T150405", value, location)
	if err != nil {
		return time.Time{}, false, "", fmt.Errorf("invalid date-time %q", value)
	}
	return parsed, false, timezoneName, nil
}

func parseMailtoValue(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.Index(strings.ToLower(value), "mailto:"); index >= 0 {
		value = value[index+len("mailto:"):]
	}
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func parseCalendarICS(data []byte, user userRecord, defaultColor string) (davCalendarEvent, error) {
	lines := unfoldContentLines(data)
	event := davCalendarEvent{
		Title: "(no title)", Status: "confirmed", Visibility: "default",
		Timezone: "UTC", Organizer: user.Email, Attendees: []davAttendee{},
	}
	inEvent := false
	sawEvent := false
	endsSet := false
	startSet := false
	for _, line := range lines {
		prop, ok := parseContentLine(line)
		if !ok {
			continue
		}
		if prop.Name == "BEGIN" && strings.EqualFold(prop.Value, "VEVENT") {
			inEvent = true
			sawEvent = true
			continue
		}
		if prop.Name == "END" && strings.EqualFold(prop.Value, "VEVENT") {
			inEvent = false
			continue
		}
		if !inEvent {
			continue
		}
		switch prop.Name {
		case "UID":
			event.UID = strings.TrimSpace(prop.Value)
			if cleanICalToken(event.UID) != event.UID {
				return event, fmt.Errorf("UID contains control characters")
			}
		case "SUMMARY":
			event.Title = truncate(unescapeICalText(prop.Value), 240)
		case "DESCRIPTION":
			event.Description = truncate(unescapeICalText(prop.Value), 5000)
		case "LOCATION":
			event.Location = truncate(unescapeICalText(prop.Value), 240)
		case "DTSTART":
			start, allDay, timezone, err := parseICalTime(prop)
			if err != nil {
				return event, err
			}
			event.StartsAt = start
			event.AllDay = allDay
			event.Timezone = timezone
			startSet = true
		case "DTEND":
			end, _, timezone, err := parseICalTime(prop)
			if err != nil {
				return event, err
			}
			event.EndsAt = end
			if strings.TrimSpace(prop.Params["TZID"]) != "" {
				event.Timezone = timezone
			}
			endsSet = true
		case "RRULE":
			event.RecurrenceRule = truncate(strings.TrimSpace(prop.Value), 1000)
			if cleanICalToken(event.RecurrenceRule) != event.RecurrenceRule {
				return event, fmt.Errorf("RRULE contains control characters")
			}
		case "STATUS":
			switch strings.ToLower(strings.TrimSpace(prop.Value)) {
			case "tentative":
				event.Status = "tentative"
			case "cancelled", "canceled":
				event.Status = "cancelled"
			default:
				event.Status = "confirmed"
			}
		case "CLASS":
			switch strings.ToUpper(strings.TrimSpace(prop.Value)) {
			case "PRIVATE":
				event.Visibility = "private"
			case "CONFIDENTIAL":
				event.Visibility = "confidential"
			default:
				event.Visibility = "default"
			}
		case "ORGANIZER":
			if email := parseMailtoValue(prop.Value); calendarEmailPattern.MatchString(email) {
				event.Organizer = email
			}
		case "ATTENDEE":
			email := parseMailtoValue(prop.Value)
			if !calendarEmailPattern.MatchString(email) {
				continue
			}
			name := unescapeICalText(prop.Params["CN"])
			if name == "" {
				name = email
			}
			event.Attendees = append(event.Attendees, davAttendee{
				Email: email, Name: name,
				Status: firstNonEmpty(strings.ToLower(prop.Params["PARTSTAT"]), "needs-action"),
			})
		}
	}
	if !sawEvent {
		return event, fmt.Errorf("the request does not contain a VEVENT")
	}
	if !startSet {
		now := time.Now().UTC()
		event.StartsAt = now.Truncate(time.Minute)
	}
	if !endsSet || !event.EndsAt.After(event.StartsAt) {
		if event.AllDay {
			event.EndsAt = event.StartsAt.Add(24 * time.Hour)
		} else {
			event.EndsAt = event.StartsAt.Add(time.Hour)
		}
	}
	if event.UID == "" {
		event.UID = newID("uid") + "@a8m"
	}
	if event.Title == "" {
		event.Title = "(no title)"
	}
	_ = defaultColor
	return event, nil
}

func calendarToICS(event davCalendarEvent, user userRecord) []byte {
	location := time.UTC
	if event.Timezone != "" {
		if loaded, err := time.LoadLocation(event.Timezone); err == nil {
			location = loaded
		}
	}
	start := event.StartsAt.In(location)
	end := event.EndsAt.In(location)
	stamp := event.UpdatedAt
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Alt8Mail//CalDAV 1.0//EN",
		"CALSCALE:GREGORIAN",
		"BEGIN:VEVENT",
		"UID:" + cleanICalToken(event.UID),
		"DTSTAMP:" + stamp.UTC().Format("20060102T150405Z"),
		"CREATED:" + event.StartsAt.UTC().Format("20060102T150405Z"),
		"LAST-MODIFIED:" + event.UpdatedAt.UTC().Format("20060102T150405Z"),
		"SEQUENCE:0",
		"SUMMARY:" + escapeICalText(event.Title),
	}
	if event.Description != "" {
		lines = append(lines, "DESCRIPTION:"+escapeICalText(event.Description))
	}
	if event.Location != "" {
		lines = append(lines, "LOCATION:"+escapeICalText(event.Location))
	}
	if event.AllDay {
		startDate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, location)
		endDate := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, location)
		if !endDate.After(startDate) {
			endDate = startDate.AddDate(0, 0, 1)
		}
		lines = append(lines, "DTSTART;VALUE=DATE:"+startDate.Format("20060102"))
		lines = append(lines, "DTEND;VALUE=DATE:"+endDate.Format("20060102"))
	} else {
		lines = append(lines, "DTSTART:"+start.UTC().Format("20060102T150405Z"))
		lines = append(lines, "DTEND:"+end.UTC().Format("20060102T150405Z"))
	}
	switch strings.ToLower(event.Status) {
	case "tentative":
		lines = append(lines, "STATUS:TENTATIVE")
	case "cancelled", "canceled":
		lines = append(lines, "STATUS:CANCELLED")
	default:
		lines = append(lines, "STATUS:CONFIRMED")
	}
	switch strings.ToLower(event.Visibility) {
	case "private":
		lines = append(lines, "CLASS:PRIVATE")
	case "confidential":
		lines = append(lines, "CLASS:CONFIDENTIAL")
	default:
		lines = append(lines, "CLASS:PUBLIC")
	}
	lines = append(lines, "TRANSP:OPAQUE")
	if rule := cleanICalToken(event.RecurrenceRule); rule != "" {
		lines = append(lines, "RRULE:"+rule)
	}
	organizer := cleanICalToken(firstNonEmpty(event.Organizer, user.Email))
	lines = append(lines, "ORGANIZER;CN="+escapeICalParam(user.Name)+":mailto:"+organizer)
	for _, attendee := range event.Attendees {
		status := strings.ToUpper(firstNonEmpty(attendee.Status, "NEEDS-ACTION"))
		lines = append(lines, "ATTENDEE;CN="+escapeICalParam(attendee.Name)+";PARTSTAT="+status+":mailto:"+cleanICalToken(attendee.Email))
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	return []byte(foldICalLines(lines))
}

func escapeICalParam(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `'`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return `"` + value + `"`
}

// foldICalLines folds content lines at 75 octets without splitting a UTF-8
// code point, as required by RFC 5545.
func foldICalLines(lines []string) string {
	var builder strings.Builder
	for _, line := range lines {
		remaining := line
		first := true
		for len(remaining) > 0 {
			limit := 75
			if !first {
				limit = 74
			}
			if len(remaining) <= limit {
				if !first {
					builder.WriteString(" ")
				}
				builder.WriteString(remaining)
				builder.WriteString("\r\n")
				break
			}
			cut := limit
			for cut > 0 && !utf8.RuneStart(remaining[cut]) {
				cut--
			}
			if cut == 0 {
				cut = limit
			}
			if !first {
				builder.WriteString(" ")
			}
			builder.WriteString(remaining[:cut])
			builder.WriteString("\r\n")
			remaining = remaining[cut:]
			first = false
		}
		if line == "" {
			builder.WriteString("\r\n")
		}
	}
	return builder.String()
}

func contactToVCard(contact davContact) []byte {
	name := firstNonEmpty(contact.Name, contact.Email)
	family, given := "", ""
	parts := strings.Fields(name)
	if len(parts) > 1 {
		given = strings.Join(parts[:len(parts)-1], " ")
		family = parts[len(parts)-1]
	} else {
		family = name
	}
	lines := []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"UID:" + cleanICalToken(firstNonEmpty(contact.UID, contact.ID)),
		"FN:" + escapeVCardText(name),
		"N:" + escapeVCardText(family) + ";" + escapeVCardText(given) + ";;;",
		"EMAIL;TYPE=INTERNET:" + escapeVCardText(contact.Email),
	}
	if contact.Company != "" {
		lines = append(lines, "ORG:"+escapeVCardText(contact.Company))
	}
	if contact.Title != "" {
		lines = append(lines, "TITLE:"+escapeVCardText(contact.Title))
	}
	if contact.Phone != "" {
		lines = append(lines, "TEL;TYPE=WORK,VOICE:"+escapeVCardText(contact.Phone))
	}
	if contact.Location != "" {
		lines = append(lines, "ADR;TYPE=WORK:;;;"+escapeVCardText(contact.Location)+";;;")
	}
	if contact.Notes != "" {
		lines = append(lines, "NOTE:"+escapeVCardText(contact.Notes))
	}
	lines = append(lines, "REV:"+contact.UpdatedAt.UTC().Format("20060102T150405Z"), "END:VCARD")
	return []byte(foldICalLines(lines))
}

func escapeVCardText(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\r\n", `\n`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\n`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	return value
}

func parseVCard(data []byte) (davContact, error) {
	lines := unfoldContentLines(data)
	contact := davContact{}
	sawCard := false
	emails := []string{}
	for _, line := range lines {
		prop, ok := parseContentLine(line)
		if !ok {
			continue
		}
		if prop.Name == "BEGIN" && strings.EqualFold(prop.Value, "VCARD") {
			sawCard = true
			continue
		}
		switch prop.Name {
		case "UID":
			contact.UID = strings.TrimSpace(prop.Value)
			if cleanICalToken(contact.UID) != contact.UID {
				return contact, fmt.Errorf("contact UID contains control characters")
			}
		case "FN":
			contact.Name = truncate(strings.TrimSpace(unescapeVCardText(prop.Value)), 240)
		case "N":
			if contact.Name == "" {
				parts := strings.Split(prop.Value, ";")
				nameParts := []string{}
				for _, part := range parts {
					if part = strings.TrimSpace(unescapeVCardText(part)); part != "" {
						nameParts = append(nameParts, part)
					}
				}
				contact.Name = truncate(strings.Join(nameParts, " "), 240)
			}
		case "EMAIL":
			if email := strings.ToLower(strings.TrimSpace(unescapeVCardText(prop.Value))); email != "" {
				emails = append(emails, email)
			}
		case "ORG":
			contact.Company = truncate(strings.TrimSpace(unescapeVCardText(prop.Value)), 160)
		case "TITLE":
			contact.Title = truncate(strings.TrimSpace(unescapeVCardText(prop.Value)), 160)
		case "TEL":
			if contact.Phone == "" {
				contact.Phone = truncate(strings.TrimSpace(unescapeVCardText(prop.Value)), 80)
			}
		case "ADR":
			parts := strings.Split(prop.Value, ";")
			locationParts := []string{}
			for _, part := range parts {
				if part = strings.TrimSpace(unescapeVCardText(part)); part != "" {
					locationParts = append(locationParts, part)
				}
			}
			contact.Location = truncate(strings.Join(locationParts, ", "), 160)
		case "NOTE":
			contact.Notes = truncate(strings.TrimSpace(unescapeVCardText(prop.Value)), 4000)
		}
	}
	if !sawCard {
		return contact, fmt.Errorf("the request does not contain a VCARD")
	}
	if len(emails) == 0 {
		return contact, fmt.Errorf("a contact must include an EMAIL property")
	}
	contact.Email = emails[0]
	if contact.Name == "" {
		contact.Name = contact.Email
	}
	if contact.UID == "" {
		contact.UID = newID("uid") + "@a8m"
	}
	return contact, nil
}

func unescapeVCardText(value string) string {
	return unescapeICalText(value)
}
