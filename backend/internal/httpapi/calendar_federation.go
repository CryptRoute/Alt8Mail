package httpapi

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxExternalFreeBusyFeeds = 5

type externalFreeBusyFeed struct {
	Label string
	Busy  []map[string]any
	Error string
}

// externalFreeBusy fetches read-only iCalendar feeds for external
// counterparties. It exposes busy intervals only; summaries, descriptions,
// locations and attendees never leave the provider.
func (s *Server) externalFreeBusy(ctx context.Context, feeds []string, start, end time.Time) []externalFreeBusyFeed {
	results := []externalFreeBusyFeed{}
	for index, raw := range feeds {
		if index >= maxExternalFreeBusyFeeds {
			break
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			results = append(results, externalFreeBusyFeed{Label: "external feed", Error: "invalid calendar address"})
			continue
		}
		label := strings.ToLower(parsed.Hostname())
		data, _, err := safeFetchCalendarURL(ctx, raw)
		if err != nil {
			results = append(results, externalFreeBusyFeed{Label: label, Error: "calendar feed unavailable"})
			continue
		}
		events, err := parseICS(data)
		if err != nil {
			results = append(results, externalFreeBusyFeed{Label: label, Error: "calendar feed unreadable"})
			continue
		}
		busy := busyIntervalsFromICS(events, start, end)
		results = append(results, externalFreeBusyFeed{Label: label, Busy: busy})
	}
	return results
}

func busyIntervalsFromICS(events []parsedCalendarEvent, start, end time.Time) []map[string]any {
	busy := []map[string]any{}
	seen := map[string]bool{}
	appendInterval := func(from, to time.Time) {
		if !to.After(from) || !to.After(start) || !from.Before(end) {
			return
		}
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		key := from.UTC().Format(time.RFC3339Nano) + "/" + to.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			return
		}
		seen[key] = true
		busy = append(busy, map[string]any{
			"start": from.UTC().Format(time.RFC3339), "end": to.UTC().Format(time.RFC3339),
		})
	}
	for _, event := range events {
		if strings.EqualFold(event.Status, "cancelled") || strings.EqualFold(event.Transparency, "free") {
			continue
		}
		duration := event.EndsAt.Sub(event.StartsAt)
		if duration <= 0 {
			continue
		}
		for _, occurrence := range expandICSBusyOccurrences(event, start, end) {
			appendInterval(occurrence, occurrence.Add(duration))
			if len(busy) >= 5000 {
				return busy
			}
		}
	}
	return busy
}

type icsRecurrenceRule struct {
	Freq     string
	Interval int
	Count    int
	Until    time.Time
	ByDay    []time.Weekday
	FullDay  bool
}

func parseICSRecurrenceRule(raw string) (icsRecurrenceRule, bool) {
	rule := icsRecurrenceRule{Interval: 1}
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(raw)), "RRULE:"))
	if raw == "" {
		return rule, false
	}
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch key {
		case "FREQ":
			rule.Freq = value
		case "INTERVAL":
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 366 {
				rule.Interval = parsed
			}
		case "COUNT":
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 1000 {
				rule.Count = parsed
			}
		case "UNTIL":
			if parsed, _, err := parseICSTime(value, map[string]string{}); err == nil {
				rule.Until = parsed
			} else if parsed, err := time.Parse("20060102", value); err == nil {
				rule.Until = parsed
				rule.FullDay = true
			}
		case "BYDAY":
			for _, day := range strings.Split(value, ",") {
				day = strings.TrimSpace(day)
				day = strings.TrimLeft(day, "+-0123456789")
				if weekday, ok := icsWeekday(day); ok {
					rule.ByDay = append(rule.ByDay, weekday)
				}
			}
		}
	}
	switch rule.Freq {
	case "DAILY", "WEEKLY", "MONTHLY":
		return rule, true
	}
	return rule, false
}

func icsWeekday(value string) (time.Weekday, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "SU":
		return time.Sunday, true
	case "MO":
		return time.Monday, true
	case "TU":
		return time.Tuesday, true
	case "WE":
		return time.Wednesday, true
	case "TH":
		return time.Thursday, true
	case "FR":
		return time.Friday, true
	case "SA":
		return time.Saturday, true
	}
	return time.Sunday, false
}

// expandICSBusyOccurrences expands the common RRULE subset used by public
// calendar feeds (daily, weekly with BYDAY, monthly) inside the requested
// window. Unsupported rules fall back to the master event only.
func expandICSBusyOccurrences(event parsedCalendarEvent, start, end time.Time) []time.Time {
	if event.StartsAt.IsZero() {
		return nil
	}
	rule, ok := parseICSRecurrenceRule(event.RecurrenceRule)
	if !ok {
		return []time.Time{event.StartsAt}
	}
	occurrences := []time.Time{}
	count := 0
	limit := func(occurrence time.Time) bool {
		if occurrence.After(end) && count > 0 {
			return false
		}
		if rule.Count > 0 && count >= rule.Count {
			return false
		}
		if !rule.Until.IsZero() && occurrence.After(rule.Until) {
			return false
		}
		count++
		if occurrence.Add(event.EndsAt.Sub(event.StartsAt)).After(start) {
			occurrences = append(occurrences, occurrence)
		}
		return len(occurrences) < 500 && count < 1000
	}
	switch rule.Freq {
	case "DAILY":
		for occurrence := event.StartsAt; ; occurrence = occurrence.AddDate(0, 0, rule.Interval) {
			if !limit(occurrence) {
				break
			}
		}
	case "WEEKLY":
		byDay := rule.ByDay
		if len(byDay) == 0 {
			byDay = []time.Weekday{event.StartsAt.Weekday()}
		}
		allowed := map[time.Weekday]bool{}
		for _, day := range byDay {
			allowed[day] = true
		}
		anchor := event.StartsAt
		weekAnchor := anchor.AddDate(0, 0, -int(anchor.Weekday()))
		for day := anchor; !day.After(end.Add(24 * time.Hour)); day = day.AddDate(0, 0, 1) {
			if !allowed[day.Weekday()] {
				continue
			}
			weeks := (dayNumberUTC(day) - dayNumberUTC(weekAnchor)) / 7
			if weeks%rule.Interval != 0 {
				continue
			}
			if !limit(day) {
				break
			}
		}
	case "MONTHLY":
		for occurrence := event.StartsAt; ; occurrence = occurrence.AddDate(0, rule.Interval, 0) {
			if !limit(occurrence) {
				break
			}
		}
	}
	if len(occurrences) == 0 && event.StartsAt.Before(end) {
		return []time.Time{event.StartsAt}
	}
	return occurrences
}

func externalFeedQuery(r *url.URL) []string {
	feeds := append([]string{}, r.Query()["external"]...)
	for _, raw := range r.Query()["externalFeeds"] {
		for _, value := range strings.Split(raw, ",") {
			if strings.TrimSpace(value) != "" {
				feeds = append(feeds, strings.TrimSpace(value))
			}
		}
	}
	return feeds
}

func dayNumberUTC(value time.Time) int {
	year, month, day := value.Date()
	return int(time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}
