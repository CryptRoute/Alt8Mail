package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type calendarEventBody struct {
	Title             *string           `json:"title"`
	Description       *string           `json:"description"`
	Location          *string           `json:"where"`
	Date              *string           `json:"date"`
	Start             *string           `json:"start"`
	End               *string           `json:"end"`
	AllDay            *bool             `json:"allDay"`
	Timezone          *string           `json:"timezone"`
	Status            *string           `json:"status"`
	Visibility        *string           `json:"visibility"`
	RecurrenceRule    *string           `json:"recurrenceRule"`
	UID               *string           `json:"uid"`
	Color             *string           `json:"color"`
	CalendarID        *string           `json:"calendarId"`
	MeetingURL        *string           `json:"meetingUrl"`
	Transparency      *string           `json:"transparency"`
	ReminderMinutes   *int              `json:"reminderMinutes"`
	ResponseRequested *bool             `json:"responseRequested"`
	AttendeeEmails    []string          `json:"attendeeEmails"`
	AttendeeNames     map[string]string `json:"attendeeNames"`
	AttachmentIDs     []string          `json:"attachmentIds"`
}

var calendarDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var calendarTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
var calendarEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var calendarHexPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type calendarRecord struct {
	ID, UID, Title, Description, Location, Timezone, Status, Visibility, RecurrenceRule string
	Organizer, OrganizerName, Color, CalendarID, CalendarName, CalendarColor            string
	CalendarKind, OwnerID, Transparency, MeetingURL, SourceID, SourceUID, Access        string
	StartsAt, EndsAt, CreatedAt, UpdatedAt                                              time.Time
	AllDay, ResponseRequested, Owned                                                    bool
	Sequence, ReminderMinutes, Attendees                                                int
	Metadata, Attachments                                                               []byte
	AttendeeNames                                                                       map[string]string
	MyStatus                                                                            string
	AttendeeRows                                                                        []byte
}

type calendarAttendeePublic struct {
	Email       string `json:"email"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Organizer   bool   `json:"organizer"`
	UserID      string `json:"userId,omitempty"`
	RespondedAt string `json:"respondedAt,omitempty"`
	Note        string `json:"note,omitempty"`
}

func (s *Server) ensureDefaultCalendar(ctx context.Context, user userRecord) error {
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO calendars (id, tenant_id, user_id, name, color, kind, visible, is_default)
		VALUES ('default', $1, $2, 'Calendar', '#0f6cbd', 'personal', true, true)
		ON CONFLICT (user_id, id) DO NOTHING
	`, user.TenantID, user.ID)
	return err
}

func (s *Server) getCalendarEvents(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_ = s.ensureDefaultCalendar(r.Context(), sess.User)
	calendars, err := s.loadCalendars(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load calendars")
		return
	}
	events, err := s.loadCalendarEvents(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load calendar events")
		return
	}
	subscriptions, _ := s.loadCalendarSubscriptions(r.Context(), sess.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events, "calendarEvents": events, "count": len(events),
		"calendars": calendars, "subscriptions": subscriptions,
		"addressBooks": s.mustAddressBooks(r.Context(), sess.User),
	})
}

func (s *Server) loadCalendars(ctx context.Context, user userRecord) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT c.id, c.name, c.color, c.kind, c.visible, c.is_default, c.source_url,
		       c.created_at, c.updated_at, c.user_id::text, owner.email::text, owner.name,
		       CASE WHEN c.user_id=$1 THEN 'owner' ELSE COALESCE(sh.access, 'view') END AS access,
		       COALESCE(sh.access, ''), p.calendar_id IS NOT NULL,
		       (SELECT count(*) FROM calendar_events e WHERE e.user_id=c.user_id AND e.calendar_id=c.id)
		FROM calendars c
		JOIN users owner ON owner.id = c.user_id
		LEFT JOIN calendar_shares sh
		       ON sh.calendar_id=c.id AND sh.owner_user_id=c.user_id AND sh.grantee_user_id=$1
		LEFT JOIN calendar_publications p
		       ON p.calendar_id=c.id AND p.owner_user_id=c.user_id
		WHERE c.user_id=$1 OR sh.grantee_user_id=$1
		ORDER BY c.is_default DESC, c.kind, lower(c.name)
	`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calendars := []map[string]any{}
	for rows.Next() {
		var id, name, color, kind, ownerID, ownerEmail, ownerName, access, shareAccess string
		var visible, isDefault, published bool
		var sourceURL string
		var createdAt, updatedAt time.Time
		var count int64
		if rows.Scan(&id, &name, &color, &kind, &visible, &isDefault, &sourceURL,
			&createdAt, &updatedAt, &ownerID, &ownerEmail, &ownerName,
			&access, &shareAccess, &published, &count) != nil {
			continue
		}
		calendars = append(calendars, map[string]any{
			"id": id, "name": name, "color": color, "kind": kind, "visible": visible,
			"isDefault": isDefault, "sourceUrl": sourceURL, "owned": ownerID == user.ID,
			"ownerId": ownerID, "ownerEmail": ownerEmail, "ownerName": ownerName,
			"access": access, "shared": ownerID != user.ID, "published": published,
			"eventCount": count,
			"createdAt":  createdAt.UTC().Format(time.RFC3339),
			"updatedAt":  updatedAt.UTC().Format(time.RFC3339),
		})
	}
	return calendars, rows.Err()
}

func (s *Server) mustAddressBooks(ctx context.Context, user userRecord) []map[string]any {
	books, err := s.loadAddressBooks(ctx, user)
	if err != nil {
		return []map[string]any{}
	}
	return books
}

const calendarEventQuery = `
	SELECT e.id, e.uid, e.title, e.description, e.location, e.timezone, e.status, e.visibility,
	       e.recurrence_rule, e.organizer_email::text, e.organizer_name, e.starts_at, e.ends_at,
	       e.all_day, e.created_at, e.updated_at, e.metadata, e.attachments,
	       e.calendar_id, c.name, c.color, c.kind, e.user_id::text,
	       e.sequence, e.transparency, e.reminder_minutes, e.meeting_url, e.response_requested,
	       e.source_id, e.source_uid,
	       CASE WHEN e.user_id=$1 THEN 'owner'
	            WHEN EXISTS (SELECT 1 FROM calendar_shares s WHERE s.calendar_id=e.calendar_id
	                         AND s.owner_user_id=e.user_id AND s.grantee_user_id=$1 AND s.access='edit') THEN 'edit'
	            WHEN EXISTS (SELECT 1 FROM calendar_shares s WHERE s.calendar_id=e.calendar_id
	                         AND s.owner_user_id=e.user_id AND s.grantee_user_id=$1) THEN 'view'
	            WHEN EXISTS (SELECT 1 FROM calendar_attendees a WHERE a.event_id=e.id
	                         AND lower(a.email)=lower($2)) THEN 'attendee'
	            ELSE '' END AS access,
	       COALESCE((SELECT a.status FROM calendar_attendees a WHERE a.event_id=e.id
	                 AND lower(a.email)=lower($2) LIMIT 1), '') AS my_status,
	       COALESCE((SELECT jsonb_agg(jsonb_build_object(
	          'email', a.email::text, 'name', a.name, 'status', a.status, 'organizer', a.organizer,
	          'userId', COALESCE(a.user_id::text, ''), 'note', a.note,
	          'respondedAt', COALESCE(to_char(a.responded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
	       ) ORDER BY a.organizer DESC, a.email) FROM calendar_attendees a WHERE a.event_id=e.id), '[]'::jsonb)
	FROM calendar_events e
	JOIN calendars c ON c.id=e.calendar_id AND c.user_id=e.user_id
	WHERE e.user_id=$1
	   OR EXISTS (SELECT 1 FROM calendar_shares s WHERE s.calendar_id=e.calendar_id
	              AND s.owner_user_id=e.user_id AND s.grantee_user_id=$1)
	   OR EXISTS (SELECT 1 FROM calendar_attendees a WHERE a.event_id=e.id
	              AND lower(a.email)=lower($2))
	ORDER BY e.starts_at`

func (s *Server) loadCalendarEvents(ctx context.Context, user userRecord) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, calendarEventQuery, user.ID, user.Email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []map[string]any{}
	for rows.Next() {
		var event calendarRecord
		if rows.Scan(&event.ID, &event.UID, &event.Title, &event.Description, &event.Location,
			&event.Timezone, &event.Status, &event.Visibility, &event.RecurrenceRule, &event.Organizer,
			&event.OrganizerName, &event.StartsAt, &event.EndsAt, &event.AllDay, &event.CreatedAt,
			&event.UpdatedAt, &event.Metadata, &event.Attachments, &event.CalendarID, &event.CalendarName,
			&event.CalendarColor, &event.CalendarKind, &event.OwnerID, &event.Sequence, &event.Transparency,
			&event.ReminderMinutes, &event.MeetingURL, &event.ResponseRequested, &event.SourceID,
			&event.SourceUID, &event.Access, &event.MyStatus, &event.AttendeeRows) == nil {
			event.Owned = event.OwnerID == user.ID
			events = append(events, publicCalendarEvent(event))
		}
	}
	return events, rows.Err()
}

func (s *Server) loadCalendarEvent(ctx context.Context, user userRecord, id string) (map[string]any, error) {
	events, err := s.loadCalendarEvents(ctx, user)
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
	color := event.CalendarColor
	var metadata map[string]any
	if json.Unmarshal(event.Metadata, &metadata) == nil {
		if value, ok := metadata["color"].(string); ok && value != "" {
			color = value
		}
	}
	if color == "" {
		color = "#0f6cbd"
	}
	attachments := []any{}
	_ = json.Unmarshal(event.Attachments, &attachments)
	attendees := []calendarAttendeePublic{}
	_ = json.Unmarshal(event.AttendeeRows, &attendees)
	attendeePayload := make([]any, 0, len(attendees))
	for _, attendee := range attendees {
		attendeePayload = append(attendeePayload, map[string]any{
			"email": attendee.Email, "name": attendee.Name, "status": attendee.Status,
			"organizer": attendee.Organizer, "userId": attendee.UserID,
			"respondedAt": attendee.RespondedAt, "note": attendee.Note,
		})
	}
	invited := event.Access == "attendee"
	canEdit := event.Owned || event.Access == "edit"
	return map[string]any{
		"id": event.ID, "uid": event.UID, "title": event.Title, "description": event.Description,
		"where": event.Location, "date": startLocal.Format("2006-01-02"),
		"start": startLocal.Format("15:04"), "end": endLocal.Format("15:04"),
		"startsAt": event.StartsAt.UTC().Format(time.RFC3339), "endsAt": event.EndsAt.UTC().Format(time.RFC3339),
		"allDay": event.AllDay, "timezone": location, "attendees": len(attendees),
		"attendeeList": attendeePayload, "color": color, "status": event.Status,
		"visibility": event.Visibility, "recurrenceRule": event.RecurrenceRule,
		"organizer": event.Organizer, "organizerName": event.OrganizerName,
		"calendarId": event.CalendarID, "calendarName": event.CalendarName,
		"calendarColor": event.CalendarColor, "calendarKind": event.CalendarKind,
		"sequence": event.Sequence, "transparency": event.Transparency,
		"reminderMinutes": event.ReminderMinutes, "meetingUrl": event.MeetingURL,
		"responseRequested": event.ResponseRequested, "sourceId": event.SourceID,
		"attachments": attachments, "access": event.Access, "canEdit": canEdit,
		"owned": event.Owned, "invited": invited, "myStatus": event.MyStatus,
		"createdAt": event.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": event.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) calendarEditable(ctx context.Context, user userRecord, calendarID, ownerID string) bool {
	if ownerID == user.ID {
		return true
	}
	var access string
	err := s.store.DB.QueryRow(ctx, `
		SELECT access FROM calendar_shares
		WHERE calendar_id=$1 AND owner_user_id=$2 AND grantee_user_id=$3
	`, calendarID, ownerID, user.ID).Scan(&access)
	return err == nil && access == "edit"
}

func (s *Server) createCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_ = s.ensureDefaultCalendar(r.Context(), sess.User)
	var body calendarEventBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	record, attendees, err := s.validateCalendarBody(r.Context(), body, nil, sess)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if record.CalendarID == "" {
		record.CalendarID = "default"
	}
	var ownerID string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT c.user_id::text FROM calendars c
		WHERE c.id=$1 AND (c.user_id=$2 OR EXISTS (
			SELECT 1 FROM calendar_shares s
			WHERE s.calendar_id=c.id AND s.owner_user_id=c.user_id
			  AND s.grantee_user_id=$2 AND s.access='edit'
		))
	`, record.CalendarID, sess.User.ID).Scan(&ownerID); err != nil {
		writeError(w, http.StatusBadRequest, "EVENT_CALENDAR", "Choose a calendar you can edit")
		return
	}
	id := newID("cal")
	record.ID = id
	if record.UID == "" {
		record.UID = id
	}
	attachments := bodyAttachmentRaw(record, body, nil)
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start calendar transaction")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, location,
		                            starts_at, ends_at, all_day, timezone, status, visibility, recurrence_rule,
		                            organizer_email, organizer_name, etag, resource_name, metadata,
		                            sequence, transparency, reminder_minutes, attachments, meeting_url,
		                            response_requested)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
		        $20, 0, $21, $22, $23, $24, $25)
	`, id, sess.User.TenantID, ownerID, record.CalendarID, record.UID, record.Title,
		record.Description, record.Location, record.StartsAt, record.EndsAt, record.AllDay, record.Timezone,
		record.Status, record.Visibility, record.RecurrenceRule, record.Organizer, record.OrganizerName,
		newID("etag"), id+".ics", fmt.Sprintf(`{"color":%q}`, record.Color), record.Transparency,
		record.ReminderMinutes, attachments, record.MeetingURL, record.ResponseRequested)
	if err == nil {
		err = s.replaceCalendarAttendees(r.Context(), tx, id, attendees, record.AttendeeNames, sess.User, record.Organizer)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the event")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.created", record.Title,
		fmt.Sprintf("%s %s–%s", record.StartsAt.Format("2006-01-02"), record.StartsAt.Format("15:04"), record.EndsAt.Format("15:04")))
	s.publishRealtime("calendar")
	go s.sendCalendarInvites(context.Background(), sess.User, id, "REQUEST")
	event, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load the event")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"event": event})
}

func (s *Server) updateCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	existing, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	access := stateString(existing, "access")
	if access != "owner" && access != "edit" {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only the organizer or a calendar editor can change this event")
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
	ownerID := stateString(existing, "ownerId")
	targetCalendar := firstNonEmpty(record.CalendarID, stateString(existing, "calendarId"))
	if !s.calendarEditable(r.Context(), sess.User, targetCalendar, ownerID) {
		writeError(w, http.StatusForbidden, "EVENT_CALENDAR", "You cannot move the event to that calendar")
		return
	}
	var targetOwner string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT user_id::text FROM calendars WHERE id=$1 AND user_id=$2
	`, targetCalendar, ownerID).Scan(&targetOwner); err != nil {
		writeError(w, http.StatusBadRequest, "EVENT_CALENDAR", "Events cannot move between calendars owned by different people")
		return
	}
	record.CalendarID = targetCalendar
	attachments := bodyAttachmentRaw(record, body, existing)
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
		    organizer_name=$13, calendar_id=$14, etag=$15, metadata=$16, updated_at=now(),
		    sequence=sequence+1, transparency=$17, reminder_minutes=$18, attachments=$19,
		    meeting_url=$20, response_requested=$21
		WHERE id=$1 AND user_id=$22
	`, id, record.Title, record.Description, record.Location, record.StartsAt, record.EndsAt,
		record.AllDay, record.Timezone, record.Status, record.Visibility, record.RecurrenceRule,
		record.Organizer, record.OrganizerName, record.CalendarID, newID("etag"),
		fmt.Sprintf(`{"color":%q}`, record.Color), record.Transparency, record.ReminderMinutes,
		attachments, record.MeetingURL, record.ResponseRequested, ownerID)
	if err == nil && body.AttendeeEmails != nil {
		err = s.replaceCalendarAttendees(r.Context(), tx, id, attendees, record.AttendeeNames, sess.User, record.Organizer)
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
	s.publishRealtime("calendar")
	go s.sendCalendarInvites(context.Background(), sess.User, id, "REQUEST")
	event, _ := s.loadCalendarEvent(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"event": event})
}

func (s *Server) deleteCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	existing, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	access := stateString(existing, "access")
	if access != "owner" && access != "edit" {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only the organizer or a calendar editor can delete this event")
		return
	}
	ownerID := stateString(existing, "ownerId")
	result, err := s.store.DB.Exec(r.Context(), `DELETE FROM calendar_events WHERE id=$1 AND user_id=$2`, id, ownerID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.deleted", id, "Event removed")
	s.publishRealtime("calendar")
	go s.sendCalendarInvitePayload(context.Background(), sess.User, existing, "CANCEL")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func bodyAttachmentRaw(record calendarRecord, body calendarEventBody, existing map[string]any) []byte {
	if body.AttachmentIDs == nil {
		if existing != nil {
			if raw, err := json.Marshal(existing["attachments"]); err == nil {
				return raw
			}
		}
		return []byte("[]")
	}
	if record.Attachments != nil {
		return record.Attachments
	}
	return []byte("[]")
}

func (s *Server) validateCalendarBody(ctx context.Context, body calendarEventBody, existing map[string]any, sess *sessionRecord) (calendarRecord, []string, error) {
	record := calendarRecord{
		Timezone: "UTC", Status: "confirmed", Visibility: "default",
		Color:        firstNonEmpty(s.tenantPolicyFor(ctx, sess.User.TenantID).Branding.PrimaryColor, "#0f6cbd"),
		Transparency: "busy", ReminderMinutes: 15, ResponseRequested: true,
		CalendarID: "default",
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
		record.CalendarID = firstNonEmpty(stateString(existing, "calendarId"), "default")
		record.Transparency = firstNonEmpty(stateString(existing, "transparency"), "busy")
		record.ReminderMinutes = int(stateInt(existing, "reminderMinutes"))
		record.ResponseRequested = stateBool(existing, "responseRequested")
		record.MeetingURL = stateString(existing, "meetingUrl")
		record.Organizer = stateString(existing, "organizer")
		record.OrganizerName = stateString(existing, "organizerName")
		date = stateString(existing, "date")
		start = stateString(existing, "start")
		end = stateString(existing, "end")
		record.AttendeeNames = map[string]string{}
		for _, attendee := range stateSlice(existing, "attendeeList") {
			entry, ok := attendee.(map[string]any)
			if !ok {
				continue
			}
			if email := stateString(entry, "email"); email != "" {
				attendees = append(attendees, email)
				if name := stateString(entry, "name"); name != "" {
					record.AttendeeNames[strings.ToLower(email)] = name
				}
			}
		}
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
	record.RecurrenceRule = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(record.RecurrenceRule), "RRULE:"))
	if body.RecurrenceRule != nil {
		record.RecurrenceRule = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(*body.RecurrenceRule), "RRULE:"))
	}
	if len(record.RecurrenceRule) > 500 {
		return record, nil, fail(http.StatusBadRequest, "EVENT_RECURRENCE", "Recurrence rule is too long")
	}
	if record.RecurrenceRule != "" && cleanICalToken(record.RecurrenceRule) != record.RecurrenceRule {
		return record, nil, fail(http.StatusBadRequest, "EVENT_RECURRENCE", "Recurrence rule contains control characters")
	}
	if cleanICalToken(record.RecurrenceRule) != record.RecurrenceRule {
		return record, nil, fail(http.StatusBadRequest, "EVENT_RECURRENCE", "Recurrence rule contains control characters")
	}
	if body.UID != nil && strings.TrimSpace(*body.UID) != "" {
		record.UID = truncate(strings.TrimSpace(*body.UID), 200)
		if cleanICalToken(record.UID) != record.UID {
			return record, nil, fail(http.StatusBadRequest, "EVENT_UID", "Event UID contains control characters")
		}
	}
	if body.Color != nil && strings.HasPrefix(strings.TrimSpace(*body.Color), "#") {
		if !calendarHexPattern.MatchString(strings.TrimSpace(*body.Color)) {
			return record, nil, fail(http.StatusBadRequest, "EVENT_COLOR", "Choose a six-digit hex colour")
		}
		record.Color = strings.ToLower(strings.TrimSpace(*body.Color))
	}
	if body.CalendarID != nil && strings.TrimSpace(*body.CalendarID) != "" {
		record.CalendarID = truncate(strings.TrimSpace(*body.CalendarID), 120)
	}
	if body.Transparency != nil {
		record.Transparency = strings.ToLower(strings.TrimSpace(*body.Transparency))
	}
	if record.Transparency != "busy" && record.Transparency != "free" {
		return record, nil, fail(http.StatusBadRequest, "EVENT_TRANSPARENCY", "Show-as must be busy or free")
	}
	if body.ReminderMinutes != nil {
		record.ReminderMinutes = *body.ReminderMinutes
	}
	if record.ReminderMinutes < 0 || record.ReminderMinutes > 10080 {
		return record, nil, fail(http.StatusBadRequest, "EVENT_REMINDER", "Choose a reminder between 0 and 10080 minutes")
	}
	if body.ResponseRequested != nil {
		record.ResponseRequested = *body.ResponseRequested
	}
	if body.MeetingURL != nil {
		value := strings.TrimSpace(*body.MeetingURL)
		if value != "" && !strings.HasPrefix(strings.ToLower(value), "https://") && !strings.HasPrefix(strings.ToLower(value), "http://") {
			return record, nil, fail(http.StatusBadRequest, "EVENT_MEETING", "Meeting links must start with https://")
		}
		record.MeetingURL = truncate(value, 500)
	}
	if body.AttachmentIDs != nil {
		if len(body.AttachmentIDs) > 10 {
			return record, nil, fail(http.StatusBadRequest, "EVENT_ATTACHMENTS", "Attach up to 10 Cabinet files to an event")
		}
		raw, err := s.calendarAttachmentSnapshots(ctx, sess.User.ID, body.AttachmentIDs)
		if err != nil {
			return record, nil, fail(http.StatusBadRequest, "EVENT_ATTACHMENTS", err.Error())
		}
		record.Attachments = raw
	}
	if record.Organizer == "" {
		record.Organizer = sess.User.Email
	}
	if record.OrganizerName == "" {
		record.OrganizerName = sess.User.Name
	}
	if body.AttendeeEmails != nil {
		attendees = body.AttendeeEmails
	}
	if body.AttendeeNames != nil {
		if record.AttendeeNames == nil {
			record.AttendeeNames = map[string]string{}
		}
		for email, name := range body.AttendeeNames {
			record.AttendeeNames[strings.ToLower(strings.TrimSpace(email))] = truncate(strings.TrimSpace(name), 120)
		}
	}
	cleaned := []string{}
	seen := map[string]bool{}
	for _, email := range attendees {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !calendarEmailPattern.MatchString(email) || seen[email] {
			continue
		}
		seen[email] = true
		cleaned = append(cleaned, email)
		if len(cleaned) >= 100 {
			break
		}
	}
	return record, cleaned, nil
}

func (s *Server) calendarAttachmentSnapshots(ctx context.Context, userID string, ids []string) ([]byte, error) {
	cleaned := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			cleaned = append(cleaned, id)
		}
	}
	if len(cleaned) == 0 {
		return []byte("[]"), nil
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, name, mime, size_bytes, provider_id FROM files
		WHERE user_id=$1 AND id = ANY($2::text[])
	`, userID, cleaned)
	if err != nil {
		return nil, fmt.Errorf("could not load the selected files")
	}
	defer rows.Close()
	found := map[string]map[string]any{}
	for rows.Next() {
		var id, name, mime, providerID string
		var size int64
		if rows.Scan(&id, &name, &mime, &size, &providerID) == nil {
			found[id] = map[string]any{
				"id": id, "name": name, "mime": mime, "sizeBytes": size,
				"providerId": providerID, "contentUrl": "/api/files/" + id + "/content",
			}
		}
	}
	out := []map[string]any{}
	for _, id := range cleaned {
		if item, ok := found[id]; ok {
			out = append(out, item)
		}
	}
	encoded, _ := json.Marshal(out)
	return encoded, nil
}

// replaceCalendarAttendees preserves RSVP responses for attendees that were
// already on the event and always records the organizer.
func (s *Server) replaceCalendarAttendees(ctx context.Context, tx pgx.Tx, eventID string, attendees []string, names map[string]string, organizer userRecord, organizerEmail string) error {
	existing := map[string]calendarAttendeePublic{}
	rows, err := tx.Query(ctx, `SELECT email::text, name, status, responded_at FROM calendar_attendees WHERE event_id=$1`, eventID)
	if err == nil {
		for rows.Next() {
			var email, name, status string
			var responded *time.Time
			if rows.Scan(&email, &name, &status, &responded) == nil {
				entry := calendarAttendeePublic{Email: email, Name: name, Status: status}
				if responded != nil {
					entry.RespondedAt = responded.UTC().Format(time.RFC3339)
				}
				existing[strings.ToLower(email)] = entry
			}
		}
		rows.Close()
	}
	if _, err := tx.Exec(ctx, `DELETE FROM calendar_attendees WHERE event_id=$1`, eventID); err != nil {
		return err
	}
	organizerEmail = strings.ToLower(strings.TrimSpace(organizerEmail))
	organizerName := organizer.Name
	if organizerEmail != "" {
		_ = tx.QueryRow(ctx, `SELECT name FROM users WHERE lower(email)=lower($1)`, organizerEmail).Scan(&organizerName)
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_attendees (event_id, email, name, status, user_id, organizer, responded_at)
			VALUES ($1, $2, $3, 'accepted', $4, true, now())
			ON CONFLICT (event_id, email) DO NOTHING
		`, eventID, organizerEmail, organizerName, organizer.ID); err != nil {
			return err
		}
	}
	for _, email := range attendees {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || email == organizerEmail {
			continue
		}
		status := "needs-action"
		responded := ""
		name := strings.TrimSpace(names[email])
		if previous, ok := existing[email]; ok {
			status = previous.Status
			responded = previous.RespondedAt
			if name == "" {
				name = previous.Name
			}
		}
		var respondedAt any
		if responded != "" {
			if parsed, err := time.Parse(time.RFC3339, responded); err == nil {
				respondedAt = parsed
			}
		}
		var userID any
		var tenantID string
		if err := tx.QueryRow(ctx, `SELECT tenant_id::text FROM users WHERE id=$1`, organizer.ID).Scan(&tenantID); err == nil {
			var candidate string
			if err := tx.QueryRow(ctx, `
				SELECT id::text FROM users WHERE tenant_id=$1 AND lower(email)=lower($2) AND deleted_at IS NULL
			`, tenantID, email).Scan(&candidate); err == nil {
				userID = candidate
			}
		}
		if name == "" && userID != nil {
			_ = tx.QueryRow(ctx, `SELECT name FROM users WHERE id=$1`, userID).Scan(&name)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_attendees (event_id, email, name, status, user_id, organizer, responded_at)
			VALUES ($1, $2, $3, $4, $5, false, $6)
			ON CONFLICT (event_id, email) DO UPDATE
			SET name=EXCLUDED.name, user_id=EXCLUDED.user_id
		`, eventID, email, name, status, userID, respondedAt); err != nil {
			return err
		}
	}
	return nil
}
