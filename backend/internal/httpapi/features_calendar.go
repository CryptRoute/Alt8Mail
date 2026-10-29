package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type calendarEventBody struct {
	Title          *string  `json:"title"`
	Description    *string  `json:"description"`
	Location       *string  `json:"where"`
	Date           *string  `json:"date"`
	Start          *string  `json:"start"`
	End            *string  `json:"end"`
	AllDay         *bool    `json:"allDay"`
	Timezone       *string  `json:"timezone"`
	Status         *string  `json:"status"`
	Visibility     *string  `json:"visibility"`
	RecurrenceRule *string  `json:"recurrenceRule"`
	UID            *string  `json:"uid"`
	Color          *string  `json:"color"`
	Attendees      []string `json:"attendeeEmails"`
}

var calendarDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var calendarTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
var calendarEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type calendarRecord struct {
	ID, UID, Title, Description, Location, Timezone, Status, Visibility, RecurrenceRule, Organizer, Color string
	StartsAt, EndsAt, CreatedAt, UpdatedAt                                                                time.Time
	AllDay                                                                                                bool
	Attendees                                                                                             int
}

func (s *Server) getCalendarEvents(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	events, err := s.loadCalendarEvents(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load calendar events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "calendarEvents": events, "count": len(events)})
}

func (s *Server) loadCalendarEvents(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT e.id, e.uid, e.title, e.description, e.location, e.timezone, e.status, e.visibility,
		       e.recurrence_rule, e.organizer_email::text, e.starts_at, e.ends_at, e.all_day,
		       e.created_at, e.updated_at, COALESCE(e.metadata->>'color',''),
		       (SELECT count(*) FROM calendar_attendees a WHERE a.event_id = e.id)
		FROM calendar_events e
		WHERE e.user_id = $1
		ORDER BY e.starts_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []map[string]any{}
	for rows.Next() {
		var event calendarRecord
		if err := rows.Scan(&event.ID, &event.UID, &event.Title, &event.Description, &event.Location,
			&event.Timezone, &event.Status, &event.Visibility, &event.RecurrenceRule, &event.Organizer,
			&event.StartsAt, &event.EndsAt, &event.AllDay, &event.CreatedAt, &event.UpdatedAt,
			&event.Color, &event.Attendees); err == nil {
			events = append(events, publicCalendarEvent(event))
		}
	}
	return events, rows.Err()
}

func publicCalendarEvent(event calendarRecord) map[string]any {
	location := event.Timezone
	if location == "" {
		location = "UTC"
	}
	zone, err := time.LoadLocation(location)
	if err != nil {
		zone = time.UTC
	}
	startLocal := event.StartsAt.In(zone)
	endLocal := event.EndsAt.In(zone)
	return map[string]any{
		"id": event.ID, "uid": event.UID, "title": event.Title, "description": event.Description,
		"where": event.Location, "date": startLocal.Format("2006-01-02"),
		"start": startLocal.Format("15:04"), "end": endLocal.Format("15:04"),
		"allDay": event.AllDay, "timezone": location, "attendees": event.Attendees,
		"color": firstNonEmpty(event.Color, "#3d6b62"), "status": event.Status,
		"visibility": event.Visibility, "recurrenceRule": event.RecurrenceRule,
		"organizer": event.Organizer, "createdAt": event.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": event.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) createCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body calendarEventBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	record, attendees, err := s.validateCalendarBody(r.Context(), body, nil, sess)
	if err != nil {
		writeFromError(w, err)
		return
	}
	id := newID("cal")
	record.ID = id
	if record.UID == "" {
		record.UID = id
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start calendar transaction")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, location,
		                            starts_at, ends_at, all_day, timezone, status, visibility, recurrence_rule,
		                            organizer_email, etag, resource_name, metadata)
		VALUES ($1, $2, $3, 'default', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`, id, sess.User.TenantID, sess.User.ID, record.UID, record.Title, record.Description, record.Location,
		record.StartsAt, record.EndsAt, record.AllDay, record.Timezone, record.Status, record.Visibility,
		record.RecurrenceRule, record.Organizer, newID("etag"), id+".ics", fmt.Sprintf(`{"color":%q}`, record.Color))
	if err == nil {
		err = replaceCalendarAttendees(r.Context(), tx, id, attendees)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the event")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.created", record.Title, fmt.Sprintf("%s %s–%s", record.StartsAt.Format("2006-01-02"), record.StartsAt.Format("15:04"), record.EndsAt.Format("15:04")))
	event, err := s.loadCalendarEvent(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load the event")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"event": event})
}

func (s *Server) updateCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	existing, err := s.loadCalendarEvent(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	var body calendarEventBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	record, attendees, err := s.validateCalendarBody(r.Context(), body, existing, sess)
	if err != nil {
		writeFromError(w, err)
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start calendar transaction")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		UPDATE calendar_events
		SET title=$2, description=$3, location=$4, starts_at=$5, ends_at=$6, all_day=$7,
		    timezone=$8, status=$9, visibility=$10, recurrence_rule=$11, organizer_email=$12,
		    etag=$13, metadata=$14, updated_at=now()
		WHERE id=$1 AND user_id=$15
	`, id, record.Title, record.Description, record.Location, record.StartsAt, record.EndsAt, record.AllDay,
		record.Timezone, record.Status, record.Visibility, record.RecurrenceRule, record.Organizer,
		newID("etag"), fmt.Sprintf(`{"color":%q}`, record.Color), sess.User.ID)
	if err == nil && body.Attendees != nil {
		err = replaceCalendarAttendees(r.Context(), tx, id, attendees)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update the event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the event")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.updated", record.Title, "Event updated")
	event, _ := s.loadCalendarEvent(r.Context(), sess.User.ID, id)
	writeJSON(w, http.StatusOK, map[string]any{"event": event})
}

func (s *Server) deleteCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	result, err := s.store.DB.Exec(r.Context(), `DELETE FROM calendar_events WHERE id=$1 AND user_id=$2`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.deleted", id, "Event removed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) loadCalendarEvent(ctx context.Context, userID, id string) (map[string]any, error) {
	events, err := s.loadCalendarEvents(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if stateString(event, "id") == id {
			return event, nil
		}
	}
	return nil, fmt.Errorf("event not found")
}

func (s *Server) validateCalendarBody(ctx context.Context, body calendarEventBody, existing map[string]any, sess *sessionRecord) (calendarRecord, []string, error) {
	record := calendarRecord{
		Timezone: "UTC", Status: "confirmed", Visibility: "default",
		Color: firstNonEmpty(s.tenantPolicyFor(ctx, sess.User.TenantID).Branding.PrimaryColor, "#3d6b62"),
	}
	date := time.Now().UTC().Format("2006-01-02")
	start, end := "09:00", "10:00"
	attendees := []string{}
	if existing != nil {
		record.Title = stateString(existing, "title")
		record.Description = stateString(existing, "description")
		record.Location = stateString(existing, "where")
		record.Timezone = firstNonEmpty(stateString(existing, "timezone"), "UTC")
		record.Status = firstNonEmpty(stateString(existing, "status"), "confirmed")
		record.Visibility = firstNonEmpty(stateString(existing, "visibility"), "default")
		record.RecurrenceRule = stateString(existing, "recurrenceRule")
		record.Color = firstNonEmpty(stateString(existing, "color"), record.Color)
		record.UID = stateString(existing, "uid")
		record.AllDay = stateBool(existing, "allDay")
		date = stateString(existing, "date")
		start = stateString(existing, "start")
		end = stateString(existing, "end")
	}
	if body.Title != nil {
		record.Title = strings.TrimSpace(*body.Title)
	}
	if record.Title == "" || len([]rune(record.Title)) > 240 {
		return record, nil, fail(http.StatusBadRequest, "EVENT_TITLE", "Event title is required")
	}
	if body.Description != nil {
		record.Description = truncate(*body.Description, 5000)
	}
	if body.Location != nil {
		record.Location = truncate(*body.Location, 240)
	}
	if body.Date != nil {
		date = strings.TrimSpace(*body.Date)
	}
	if !calendarDatePattern.MatchString(date) {
		return record, nil, fail(http.StatusBadRequest, "EVENT_DATE", "Event date must be YYYY-MM-DD")
	}
	if body.Start != nil {
		start = strings.TrimSpace(*body.Start)
	}
	if body.End != nil {
		end = strings.TrimSpace(*body.End)
	}
	if body.AllDay != nil {
		record.AllDay = *body.AllDay
	}
	if record.AllDay {
		start, end = "00:00", "23:59"
	}
	if !calendarTimePattern.MatchString(start) || !calendarTimePattern.MatchString(end) {
		return record, nil, fail(http.StatusBadRequest, "EVENT_TIME", "Event times must be HH:MM")
	}
	if body.Timezone != nil {
		record.Timezone = strings.TrimSpace(*body.Timezone)
	}
	if record.Timezone == "" {
		record.Timezone = "UTC"
	}
	location, err := time.LoadLocation(record.Timezone)
	if err != nil {
		return record, nil, fail(http.StatusBadRequest, "EVENT_TIMEZONE", "Unknown event time zone")
	}
	record.StartsAt, err = time.ParseInLocation("2006-01-02 15:04", date+" "+start, location)
	if err != nil {
		return record, nil, fail(http.StatusBadRequest, "EVENT_TIME", "Invalid event start")
	}
	record.EndsAt, err = time.ParseInLocation("2006-01-02 15:04", date+" "+end, location)
	if err != nil || !record.EndsAt.After(record.StartsAt) {
		return record, nil, fail(http.StatusBadRequest, "EVENT_TIME", "Event end must be after its start")
	}
	if body.Status != nil {
		record.Status = *body.Status
	}
	if record.Status != "confirmed" && record.Status != "tentative" && record.Status != "cancelled" {
		return record, nil, fail(http.StatusBadRequest, "EVENT_STATUS", "Invalid event status")
	}
	if body.Visibility != nil {
		record.Visibility = *body.Visibility
	}
	if body.RecurrenceRule != nil {
		record.RecurrenceRule = truncate(strings.TrimSpace(*body.RecurrenceRule), 500)
		if cleanICalToken(record.RecurrenceRule) != record.RecurrenceRule {
			return record, nil, fail(http.StatusBadRequest, "EVENT_RECURRENCE", "Recurrence rule contains control characters")
		}
	}
	if body.UID != nil && strings.TrimSpace(*body.UID) != "" {
		record.UID = truncate(strings.TrimSpace(*body.UID), 200)
		if cleanICalToken(record.UID) != record.UID {
			return record, nil, fail(http.StatusBadRequest, "EVENT_UID", "Event UID contains control characters")
		}
	}
	if body.Color != nil && strings.HasPrefix(strings.TrimSpace(*body.Color), "#") {
		record.Color = truncate(strings.TrimSpace(*body.Color), 20)
	}
	record.Organizer = sess.User.Email
	if body.Attendees != nil {
		seen := map[string]bool{}
		for _, email := range body.Attendees {
			email = strings.ToLower(strings.TrimSpace(email))
			if email == "" || !calendarEmailPattern.MatchString(email) || seen[email] {
				continue
			}
			seen[email] = true
			attendees = append(attendees, email)
		}
	}
	return record, attendees, nil
}

func replaceCalendarAttendees(ctx context.Context, tx pgx.Tx, eventID string, attendees []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM calendar_attendees WHERE event_id = $1`, eventID); err != nil {
		return err
	}
	for _, email := range attendees {
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_attendees (event_id, email, name, status)
			VALUES ($1, $2, '', 'needs-action')
			ON CONFLICT (event_id, email) DO NOTHING
		`, eventID, email); err != nil {
			return err
		}
	}
	return nil
}
