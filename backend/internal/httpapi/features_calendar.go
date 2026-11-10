package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
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
	ResourceIDs       []string          `json:"resourceIds"`
	Scope             string            `json:"scope"`
	OccurrenceDate    string            `json:"occurrenceDate"`
}

var calendarDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var calendarTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
var calendarEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var calendarHexPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type calendarRecord struct {
	ID, UID, Title, Description, Location, Timezone, Status, Visibility, RecurrenceRule string
	Organizer, OrganizerName, Color, CalendarID, CalendarName, CalendarColor            string
	CalendarKind, OwnerID, Transparency, MeetingURL, SourceID, SourceUID, Access        string
	SenderEmail, SenderName                                                             string
	StartsAt, EndsAt, CreatedAt, UpdatedAt                                              time.Time
	AllDay, ResponseRequested, Owned                                                    bool
	Sequence, ReminderMinutes, Attendees                                                int
	Metadata, Attachments                                                               []byte
	AttendeeNames                                                                       map[string]string
	MyStatus                                                                            string
	AttendeeRows, ExceptionRows, ProposalRows, ResourceRows                             []byte
}

type calendarResourcePublic struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Email            string `json:"email"`
	Kind             string `json:"kind"`
	Location         string `json:"location"`
	Capacity         int    `json:"capacity"`
	RequiresApproval bool   `json:"requiresApproval"`
	ApproverEmail    string `json:"approverEmail,omitempty"`
	Status           string `json:"status,omitempty"`
}

type calendarExceptionPublic struct {
	ID              string `json:"id"`
	RecurrenceID    string `json:"recurrenceId"`
	RecurrenceDate  string `json:"recurrenceDate"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Location        string `json:"location"`
	StartsAt        string `json:"startsAt"`
	EndsAt          string `json:"endsAt"`
	Date            string `json:"date"`
	Start           string `json:"start"`
	End             string `json:"end"`
	AllDay          bool   `json:"allDay"`
	Timezone        string `json:"timezone"`
	Status          string `json:"status"`
	Sequence        int    `json:"sequence"`
	ReminderMinutes int    `json:"reminderMinutes"`
}

type calendarProposalPublic struct {
	ID               string `json:"id"`
	AttendeeEmail    string `json:"attendeeEmail"`
	AttendeeName     string `json:"attendeeName"`
	ProposedStartsAt string `json:"proposedStartsAt"`
	ProposedEndsAt   string `json:"proposedEndsAt"`
	Date             string `json:"date"`
	Start            string `json:"start"`
	End              string `json:"end"`
	Timezone         string `json:"timezone"`
	Note             string `json:"note"`
	Sequence         int    `json:"sequence"`
	Status           string `json:"status"`
	Source           string `json:"source"`
	CreatedAt        string `json:"createdAt"`
}

type calendarAttendeePublic struct {
	Email                string `json:"email"`
	Name                 string `json:"name"`
	Status               string `json:"status"`
	Organizer            bool   `json:"organizer"`
	UserID               string `json:"userId,omitempty"`
	Role                 string `json:"role,omitempty"`
	RSVP                 bool   `json:"rsvp"`
	RespondedAt          string `json:"respondedAt,omitempty"`
	LastInviteSequence   int    `json:"lastInviteSequence"`
	LastResponseSequence int    `json:"lastResponseSequence"`
	LastInviteAt         string `json:"lastInviteAt,omitempty"`
	LastResponseAt       string `json:"lastResponseAt,omitempty"`
	LastDeliveryStatus   string `json:"lastDeliveryStatus,omitempty"`
	LastDeliveryAt       string `json:"lastDeliveryAt,omitempty"`
	LastDeliveryError    string `json:"lastDeliveryError,omitempty"`
	Note                 string `json:"note,omitempty"`
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
	resources, _ := s.loadCalendarResources(r.Context(), sess.User.TenantID, sess.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events, "calendarEvents": events, "count": len(events),
		"calendars": calendars, "subscriptions": subscriptions,
		"addressBooks": s.mustAddressBooks(r.Context(), sess.User),
		"resources":    resources,
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
	       e.source_id, e.source_uid, e.sender_email::text, e.sender_name,
	       COALESCE((SELECT jsonb_agg(jsonb_build_object(
	          'id', x.id, 'recurrenceId', to_char(x.recurrence_id AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	          'recurrenceDate', to_char(x.recurrence_id AT TIME ZONE e.timezone, 'YYYY-MM-DD'),
	          'title', x.title, 'description', x.description, 'location', x.location,
	          'startsAt', to_char(x.starts_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	          'endsAt', to_char(x.ends_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	          'date', to_char(x.starts_at AT TIME ZONE x.timezone, 'YYYY-MM-DD'),
	          'start', to_char(x.starts_at AT TIME ZONE x.timezone, 'HH24:MI'),
	          'end', to_char(x.ends_at AT TIME ZONE x.timezone, 'HH24:MI'),
	          'allDay', x.all_day, 'timezone', x.timezone, 'status', x.status,
	          'sequence', x.sequence, 'reminderMinutes', x.reminder_minutes
	       ) ORDER BY x.recurrence_id) FROM calendar_event_exceptions x WHERE x.event_id=e.id), '[]'::jsonb) AS exceptions,
	       COALESCE((SELECT jsonb_agg(jsonb_build_object(
	          'id', p.id, 'attendeeEmail', p.attendee_email::text, 'attendeeName', p.attendee_name,
	          'proposedStartsAt', to_char(p.proposed_starts_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	          'proposedEndsAt', to_char(p.proposed_ends_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	          'date', to_char(p.proposed_starts_at AT TIME ZONE p.timezone, 'YYYY-MM-DD'),
	          'start', to_char(p.proposed_starts_at AT TIME ZONE p.timezone, 'HH24:MI'),
	          'end', to_char(p.proposed_ends_at AT TIME ZONE p.timezone, 'HH24:MI'),
	          'timezone', p.timezone, 'note', p.note, 'sequence', p.sequence, 'status', p.status,
	          'source', p.source, 'createdAt', to_char(p.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	       ) ORDER BY p.created_at DESC) FROM calendar_meeting_proposals p
	          WHERE p.event_id=e.id AND p.status='pending'), '[]'::jsonb) AS proposals,
	       COALESCE((SELECT jsonb_agg(jsonb_build_object(
	          'id', r.resource_id, 'name', r.name, 'email', r.email::text, 'status', r.status,
	          'kind', cr.kind, 'location', cr.location, 'capacity', cr.capacity
	       ) ORDER BY r.name) FROM calendar_event_resources r
	          JOIN calendar_resources cr ON cr.id=r.resource_id WHERE r.event_id=e.id), '[]'::jsonb) AS resources,
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
	          'userId', COALESCE(a.user_id::text, ''), 'note', a.note, 'role', a.role, 'rsvp', a.rsvp,
	          'lastInviteSequence', a.last_invite_sequence,
	          'lastResponseSequence', a.last_response_sequence,
	          'respondedAt', COALESCE(to_char(a.responded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
	          'lastInviteAt', COALESCE(to_char(a.last_invite_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
	          'lastResponseAt', COALESCE(to_char(a.last_response_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
	          'lastDeliveryStatus', a.last_delivery_status,
	          'lastDeliveryAt', COALESCE(to_char(a.last_delivery_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
	          'lastDeliveryError', a.last_delivery_error
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
			&event.SourceUID, &event.SenderEmail, &event.SenderName, &event.ExceptionRows,
			&event.ProposalRows, &event.ResourceRows, &event.Access, &event.MyStatus, &event.AttendeeRows) == nil {
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
			"organizer": attendee.Organizer, "userId": attendee.UserID, "role": attendee.Role,
			"rsvp": attendee.RSVP, "respondedAt": attendee.RespondedAt, "note": attendee.Note,
			"lastInviteSequence": attendee.LastInviteSequence, "lastResponseSequence": attendee.LastResponseSequence,
			"lastInviteAt": attendee.LastInviteAt, "lastResponseAt": attendee.LastResponseAt,
			"lastDeliveryStatus": attendee.LastDeliveryStatus, "lastDeliveryAt": attendee.LastDeliveryAt,
			"lastDeliveryError": attendee.LastDeliveryError,
		})
	}
	exceptions := []calendarExceptionPublic{}
	_ = json.Unmarshal(event.ExceptionRows, &exceptions)
	proposals := []calendarProposalPublic{}
	_ = json.Unmarshal(event.ProposalRows, &proposals)
	resources := []calendarResourcePublic{}
	_ = json.Unmarshal(event.ResourceRows, &resources)
	invited := event.Access == "attendee"
	canEdit := event.Owned || event.Access == "edit"
	result := map[string]any{
		"exceptions": exceptions, "proposals": proposals, "resourceList": resources,
		"senderEmail": event.SenderEmail, "senderName": event.SenderName,
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
		"ownerId":  event.OwnerID,
		"sequence": event.Sequence, "transparency": event.Transparency,
		"reminderMinutes": event.ReminderMinutes, "meetingUrl": event.MeetingURL,
		"responseRequested": event.ResponseRequested, "sourceId": event.SourceID,
		"attachments": attachments, "access": event.Access, "canEdit": canEdit,
		"owned": event.Owned, "invited": invited, "myStatus": event.MyStatus,
		"createdAt": event.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": event.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if analytics := calendarRSVPAnalytics(attendees, event.CreatedAt); analytics != nil {
		result["rsvpAnalytics"] = analytics
	}
	return result
}

func calendarRSVPAnalytics(attendees []calendarAttendeePublic, createdAt time.Time) map[string]any {
	guests := make([]calendarAttendeePublic, 0, len(attendees))
	for _, attendee := range attendees {
		if !attendee.Organizer {
			guests = append(guests, attendee)
		}
	}
	if len(guests) <= 10 {
		return nil
	}
	type domainStats struct {
		domain    string
		total     int
		responded int
		delivered int
		failed    int
		totalMins float64
		withMins  int
	}
	domains := map[string]*domainStats{}
	responded, delivered, failed := 0, 0, 0
	totalMins, withMins := 0.0, 0
	for _, guest := range guests {
		domain := domainOf(strings.ToLower(guest.Email))
		if domain == "" {
			domain = "unknown"
		}
		stats := domains[domain]
		if stats == nil {
			stats = &domainStats{domain: domain}
			domains[domain] = stats
		}
		stats.total++
		if guest.Status == "accepted" || guest.Status == "tentative" || guest.Status == "declined" {
			responded++
			stats.responded++
			start := createdAt
			if parsed, err := time.Parse(time.RFC3339, guest.LastInviteAt); err == nil {
				start = parsed
			}
			answered := time.Time{}
			if parsed, err := time.Parse(time.RFC3339, guest.LastResponseAt); err == nil {
				answered = parsed
			} else if parsed, err := time.Parse(time.RFC3339, guest.RespondedAt); err == nil {
				answered = parsed
			}
			if !answered.IsZero() && answered.After(start) {
				minutes := answered.Sub(start).Minutes()
				totalMins += minutes
				withMins++
				stats.totalMins += minutes
				stats.withMins++
			}
		}
		switch guest.LastDeliveryStatus {
		case "sent":
			delivered++
			stats.delivered++
		case "failed":
			failed++
			stats.failed++
		}
	}
	domainRows := make([]map[string]any, 0, len(domains))
	for _, stats := range domains {
		row := map[string]any{
			"domain": stats.domain, "total": stats.total, "responded": stats.responded,
			"delivered": stats.delivered, "failed": stats.failed,
			"responseRate": percentage(stats.responded, stats.total),
		}
		if stats.withMins > 0 {
			row["averageResponseMinutes"] = stats.totalMins / float64(stats.withMins)
		}
		domainRows = append(domainRows, row)
	}
	sort.Slice(domainRows, func(i, j int) bool { return fmt.Sprint(domainRows[i]["domain"]) < fmt.Sprint(domainRows[j]["domain"]) })
	out := map[string]any{
		"total": len(guests), "responded": responded,
		"responseRate": percentage(responded, len(guests)),
		"delivered":    delivered, "failed": failed, "domains": domainRows,
	}
	if withMins > 0 {
		out["averageResponseMinutes"] = totalMins / float64(withMins)
	}
	return out
}

func percentage(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(part)*1000/float64(total)) / 10
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
	organizerUser := sess.User
	if ownerID != sess.User.ID {
		if owner, lookupErr := s.userByID(r.Context(), ownerID); lookupErr == nil {
			// Creating on somebody else's shared calendar is a delegated send:
			// the calendar owner is the organizer and this user is the sender.
			organizerUser = owner
			record.Organizer = owner.Email
			record.OrganizerName = owner.Name
			record.SenderEmail = sess.User.Email
			record.SenderName = sess.User.Name
		}
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
		                            response_requested, sender_email, sender_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
		        $20, 0, $21, $22, $23, $24, $25, $26, $27)
	`, id, sess.User.TenantID, ownerID, record.CalendarID, record.UID, record.Title,
		record.Description, record.Location, record.StartsAt, record.EndsAt, record.AllDay, record.Timezone,
		record.Status, record.Visibility, record.RecurrenceRule, record.Organizer, record.OrganizerName,
		newID("etag"), id+".ics", fmt.Sprintf(`{"color":%q}`, record.Color), record.Transparency,
		record.ReminderMinutes, attachments, record.MeetingURL, record.ResponseRequested,
		record.SenderEmail, record.SenderName)
	if err == nil {
		err = s.replaceCalendarAttendees(r.Context(), tx, id, attendees, record.AttendeeNames, organizerUser, record.Organizer, false)
	}
	if err == nil {
		err = s.replaceCalendarEventResources(r.Context(), tx, id, sess.User.TenantID, sess.User.ID, body.ResourceIDs, record.StartsAt, record.EndsAt)
	}
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the event")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.created", record.Title,
		fmt.Sprintf("%s %s–%s", record.StartsAt.Format("2006-01-02"), record.StartsAt.Format("15:04"), record.EndsAt.Format("15:04")))
	s.publishRealtime("calendar")
	if err := s.queueCalendarInvites(context.Background(), sess.User, id, "REQUEST"); err != nil {
		s.logger.Warn("calendar invitation queue failed", "err", err, "event", id)
		go s.sendCalendarInvites(context.Background(), sess.User, id, "REQUEST")
	}
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
	if strings.EqualFold(strings.TrimSpace(body.Scope), "occurrence") {
		s.updateCalendarOccurrence(w, r, sess, id, existing, body)
		return
	}
	record, attendees, err := s.validateCalendarBody(r.Context(), body, existing, sess)
	if err != nil {
		writeFromError(w, err)
		return
	}
	ownerID := stateString(existing, "ownerId")
	if ownerID == "" {
		_ = s.store.DB.QueryRow(r.Context(), `SELECT user_id::text FROM calendar_events WHERE id=$1`, id).Scan(&ownerID)
	}
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
	record.SenderEmail, record.SenderName = "", ""
	if record.Organizer != "" && !strings.EqualFold(record.Organizer, sess.User.Email) {
		record.SenderEmail = sess.User.Email
		record.SenderName = sess.User.Name
	}
	organizerUser := s.calendarOrganizerUser(r.Context(), sess.User, existing)
	attachments := bodyAttachmentRaw(record, body, existing)
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start calendar transaction")
		return
	}
	defer tx.Rollback(r.Context())
	var updatedSequence int
	err = tx.QueryRow(r.Context(), `
		UPDATE calendar_events
		SET title=$2, description=$3, location=$4, starts_at=$5, ends_at=$6, all_day=$7,
		    timezone=$8, status=$9, visibility=$10, recurrence_rule=$11, organizer_email=$12,
		    organizer_name=$13, calendar_id=$14, etag=$15, metadata=$16, updated_at=now(),
		    sequence=sequence+1, transparency=$17, reminder_minutes=$18, attachments=$19,
		    meeting_url=$20, response_requested=$21, sender_email=$23, sender_name=$24
		WHERE id=$1 AND user_id=$22
		RETURNING sequence
	`, id, record.Title, record.Description, record.Location, record.StartsAt, record.EndsAt,
		record.AllDay, record.Timezone, record.Status, record.Visibility, record.RecurrenceRule,
		record.Organizer, record.OrganizerName, record.CalendarID, newID("etag"),
		fmt.Sprintf(`{"color":%q}`, record.Color), record.Transparency, record.ReminderMinutes,
		attachments, record.MeetingURL, record.ResponseRequested, ownerID,
		record.SenderEmail, record.SenderName).Scan(&updatedSequence)
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			UPDATE calendar_event_exceptions SET sequence=$2, updated_at=now()
			WHERE event_id=$1 AND sequence < $2
		`, id, updatedSequence)
	}
	if err == nil {
		err = s.replaceCalendarAttendees(r.Context(), tx, id, attendees, record.AttendeeNames, organizerUser, record.Organizer, true)
	}
	if err == nil {
		err = s.replaceCalendarEventResources(r.Context(), tx, id, sess.User.TenantID, sess.User.ID, body.ResourceIDs, record.StartsAt, record.EndsAt)
	}
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the event")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.updated", record.Title, "Event updated")
	s.publishRealtime("calendar")
	if err := s.queueCalendarInvites(context.Background(), sess.User, id, "REQUEST"); err != nil {
		s.logger.Warn("calendar invitation queue failed", "err", err, "event", id)
		go s.sendCalendarInvites(context.Background(), sess.User, id, "REQUEST")
	}
	event, _ := s.loadCalendarEvent(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"event": event})
}

func (s *Server) updateCalendarOccurrence(w http.ResponseWriter, r *http.Request, sess *sessionRecord, id string, existing map[string]any, body calendarEventBody) {
	event, sequence, err := s.applyCalendarOccurrence(r.Context(), sess, id, existing, body, "")
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": event, "sequence": sequence, "scope": "occurrence"})
}

func (s *Server) deleteCalendarOccurrence(w http.ResponseWriter, r *http.Request, sess *sessionRecord, id string, existing map[string]any, occurrenceDate string) {
	status := "cancelled"
	event, sequence, err := s.applyCalendarOccurrence(r.Context(), sess, id, existing,
		calendarEventBody{Scope: "occurrence", OccurrenceDate: occurrenceDate, Status: &status}, "cancelled")
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event, "sequence": sequence, "scope": "occurrence"})
}

func (s *Server) applyCalendarOccurrence(ctx context.Context, sess *sessionRecord, id string, existing map[string]any, body calendarEventBody, forceStatus string) (map[string]any, int, error) {
	if strings.TrimSpace(stateString(existing, "recurrenceRule")) == "" {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_SCOPE", "This event does not repeat")
	}
	occurrenceDate := strings.TrimSpace(body.OccurrenceDate)
	if !calendarDatePattern.MatchString(occurrenceDate) {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_OCCURRENCE", "Choose the occurrence to change")
	}
	zone := calendarEventLocation(existing)
	baseStart := stateString(existing, "start")
	if stateBool(existing, "allDay") {
		baseStart = "00:00"
	}
	recurrenceStart, err := time.ParseInLocation("2006-01-02 15:04", occurrenceDate+" "+baseStart, zone)
	if err != nil {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_OCCURRENCE", "The occurrence date is invalid")
	}
	recurrenceID := recurrenceStart.UTC()

	title := stateString(existing, "title")
	description := stateString(existing, "description")
	location := stateString(existing, "where")
	timezoneName := firstNonEmpty(stateString(existing, "timezone"), "UTC")
	status := firstNonEmpty(stateString(existing, "status"), "confirmed")
	allDay := stateBool(existing, "allDay")
	reminder := int(stateInt(existing, "reminderMinutes"))
	date := occurrenceDate
	start := stateString(existing, "start")
	end := stateString(existing, "end")
	var storedSequence int
	err = s.store.DB.QueryRow(ctx, `
		SELECT title, description, location, timezone, status, sequence, reminder_minutes, all_day,
		       to_char(starts_at AT TIME ZONE timezone, 'YYYY-MM-DD'),
		       to_char(starts_at AT TIME ZONE timezone, 'HH24:MI'),
		       to_char(ends_at AT TIME ZONE timezone, 'HH24:MI')
		FROM calendar_event_exceptions WHERE event_id=$1 AND recurrence_id=$2
	`, id, recurrenceID).Scan(&title, &description, &location, &timezoneName, &status,
		&storedSequence, &reminder, &allDay, &date, &start, &end)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, fail(http.StatusInternalServerError, "EVENT_OCCURRENCE", "Could not load the occurrence")
	}
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
	}
	if body.Description != nil {
		description = truncate(*body.Description, 5000)
	}
	if body.Location != nil {
		location = truncate(*body.Location, 240)
	}
	if body.Date != nil {
		date = strings.TrimSpace(*body.Date)
	}
	if body.Start != nil {
		start = strings.TrimSpace(*body.Start)
	}
	if body.End != nil {
		end = strings.TrimSpace(*body.End)
	}
	if body.AllDay != nil {
		allDay = *body.AllDay
	}
	if body.Timezone != nil {
		timezoneName = strings.TrimSpace(*body.Timezone)
	}
	if body.Status != nil {
		status = strings.TrimSpace(*body.Status)
	}
	if forceStatus != "" {
		status = forceStatus
	}
	if body.ReminderMinutes != nil {
		reminder = *body.ReminderMinutes
	}
	if title == "" || len([]rune(title)) > 240 {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_TITLE", "Event title is required")
	}
	if !calendarDatePattern.MatchString(date) {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_DATE", "Event date must be YYYY-MM-DD")
	}
	if allDay {
		start, end = "00:00", "23:59"
	}
	if !calendarTimePattern.MatchString(start) || !calendarTimePattern.MatchString(end) {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_TIME", "Event times must be HH:MM")
	}
	locationZone, err := time.LoadLocation(timezoneName)
	if err != nil {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_TIMEZONE", "Unknown event time zone")
	}
	startsAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+start, locationZone)
	if err != nil {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_TIME", "Invalid occurrence start")
	}
	endsAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+end, locationZone)
	if err != nil || !endsAt.After(startsAt) {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_TIME", "Occurrence end must be after its start")
	}
	if status != "confirmed" && status != "tentative" && status != "cancelled" {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_STATUS", "Invalid event status")
	}
	if reminder < 0 || reminder > 10080 {
		return nil, 0, fail(http.StatusBadRequest, "EVENT_REMINDER", "Choose a reminder between 0 and 10080 minutes")
	}
	var tenantID string
	if err := s.store.DB.QueryRow(ctx, `SELECT tenant_id::text FROM calendar_events WHERE id=$1`, id).Scan(&tenantID); err != nil {
		return nil, 0, fail(http.StatusNotFound, "NOT_FOUND", "Event not found")
	}
	senderEmail, senderName := "", ""
	if organizerEmail := stateString(existing, "organizer"); organizerEmail != "" && !strings.EqualFold(organizerEmail, sess.User.Email) {
		senderEmail, senderName = sess.User.Email, sess.User.Name
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return nil, 0, fail(http.StatusInternalServerError, "CALENDAR", "Could not start calendar transaction")
	}
	defer tx.Rollback(ctx)
	var nextSequence int
	if err := tx.QueryRow(ctx, `
		UPDATE calendar_events
		SET sequence=sequence+1, sender_email=$2, sender_name=$3, updated_at=now()
		WHERE id=$1 RETURNING sequence
	`, id, senderEmail, senderName).Scan(&nextSequence); err != nil {
		return nil, 0, fail(http.StatusNotFound, "NOT_FOUND", "Event not found")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE calendar_event_exceptions SET sequence=$2, updated_at=now()
		WHERE event_id=$1 AND sequence < $2
	`, id, nextSequence); err != nil {
		return nil, 0, fail(http.StatusInternalServerError, "CALENDAR", "Could not update the recurrence")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO calendar_event_exceptions
		(id, tenant_id, event_id, recurrence_id, title, description, location, starts_at, ends_at,
		 all_day, timezone, status, sequence, reminder_minutes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (event_id, recurrence_id) DO UPDATE
		SET title=EXCLUDED.title, description=EXCLUDED.description, location=EXCLUDED.location,
		    starts_at=EXCLUDED.starts_at, ends_at=EXCLUDED.ends_at, all_day=EXCLUDED.all_day,
		    timezone=EXCLUDED.timezone, status=EXCLUDED.status, sequence=EXCLUDED.sequence,
		    reminder_minutes=EXCLUDED.reminder_minutes, updated_at=now()
	`, newID("calexc"), tenantID, id, recurrenceID, title, description, location, startsAt, endsAt,
		allDay, timezoneName, status, nextSequence, reminder); err != nil {
		return nil, 0, fail(http.StatusInternalServerError, "CALENDAR", "Could not save the occurrence")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE calendar_attendees SET status='needs-action', responded_at=NULL
		WHERE event_id=$1 AND organizer=false
	`, id); err != nil {
		return nil, 0, fail(http.StatusInternalServerError, "CALENDAR", "Could not reset guest responses")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fail(http.StatusInternalServerError, "CALENDAR", "Could not commit the occurrence")
	}
	auditAction := "calendar.occurrence.updated"
	if forceStatus == "cancelled" {
		auditAction = "calendar.occurrence.cancelled"
	}
	s.recordWorkspaceAudit(ctx, sess, auditAction, title,
		fmt.Sprintf("Occurrence %s · %s %s–%s", occurrenceDate, date, start, end))
	if err := s.queueCalendarInvites(context.Background(), sess.User, id, "REQUEST"); err != nil {
		s.logger.Warn("calendar occurrence invitation queue failed", "err", err, "event", id)
		go s.sendCalendarInvites(context.Background(), sess.User, id, "REQUEST")
	}
	s.publishRealtime("calendar")
	event, loadErr := s.loadCalendarEvent(ctx, sess.User, id)
	if loadErr != nil {
		return nil, nextSequence, fail(http.StatusInternalServerError, "CALENDAR", "Could not load the event")
	}
	return event, nextSequence, nil
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
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("scope")), "occurrence") {
		s.deleteCalendarOccurrence(w, r, sess, id, existing, strings.TrimSpace(r.URL.Query().Get("occurrenceDate")))
		return
	}
	ownerID := stateString(existing, "ownerId")
	var nextSequence int
	// Bump SEQUENCE before removing the row. A cancellation for the old
	// sequence can be ignored by clients as stale.
	err = s.store.DB.QueryRow(r.Context(), `
		UPDATE calendar_events
		SET sequence=sequence+1, status='cancelled', updated_at=now()
		WHERE id=$1 AND user_id=$2
		RETURNING sequence
	`, id, ownerID).Scan(&nextSequence)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	existing["sequence"] = int64(nextSequence)
	existing["status"] = "cancelled"
	if _, err := s.store.DB.Exec(r.Context(), `DELETE FROM calendar_events WHERE id=$1 AND user_id=$2`, id, ownerID); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not remove the event")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.deleted", id, "Event removed · cancellation sent")
	s.publishRealtime("calendar")
	go s.sendCalendarInvitePayload(context.Background(), sess.User, existing, "CANCEL")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sequence": nextSequence})
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

// replaceCalendarAttendees always records the organizer. On a new event it
// keeps existing responses; on an update every guest response is reset to
// NEEDS-ACTION because the invitation SEQUENCE has advanced.
func (s *Server) replaceCalendarEventResources(ctx context.Context, tx pgx.Tx, eventID, tenantID, userID string, resourceIDs []string, startsAt, endsAt time.Time) error {
	if resourceIDs == nil {
		rows, err := tx.Query(ctx, `SELECT resource_id FROM calendar_event_resources WHERE event_id=$1`, eventID)
		if err != nil {
			return err
		}
		resourceIDs = []string{}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				resourceIDs = append(resourceIDs, id)
			}
		}
		rows.Close()
	}
	if len(resourceIDs) > 20 {
		return fail(http.StatusBadRequest, "EVENT_RESOURCES", "An event can book up to 20 rooms or resources")
	}
	unique := make([]string, 0, len(resourceIDs))
	seen := map[string]bool{}
	for _, id := range resourceIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		_, err := tx.Exec(ctx, `DELETE FROM calendar_event_resources WHERE event_id=$1`, eventID)
		return err
	}
	type resourceRow struct {
		id, name, email, approver string
		requiresApproval          bool
	}
	found := map[string]resourceRow{}
	rows, err := tx.Query(ctx, `
		SELECT id, name, email::text, requires_approval,
		       COALESCE(approver_user_id::text, created_by::text, '')
		FROM calendar_resources
		WHERE tenant_id=$1 AND id = ANY($2::text[])
		  AND ((created_by=$3 AND active=true)
		       OR id IN (SELECT resource_id FROM calendar_event_resources WHERE event_id=$4))
	`, tenantID, unique, userID, eventID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item resourceRow
		if rows.Scan(&item.id, &item.name, &item.email, &item.requiresApproval, &item.approver) == nil {
			found[item.id] = item
		}
	}
	rows.Close()
	if len(found) != len(unique) {
		return fail(http.StatusBadRequest, "EVENT_RESOURCES", "One of those rooms or resources is unavailable")
	}
	sort.Strings(unique)
	for _, id := range unique {
		// Serialize bookings for the same resource so two organizers cannot
		// double-book a room between the conflict check and the insert.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "calendar-resource:"+id); err != nil {
			return err
		}
	}
	var busy string
	if err := tx.QueryRow(ctx, `
		SELECT cr.name
		FROM calendar_event_resources er
		JOIN calendar_events e ON e.id=er.event_id
		JOIN calendar_resources cr ON cr.id=er.resource_id
		WHERE er.resource_id = ANY($1::text[]) AND e.id <> $2
		  AND e.status <> 'cancelled' AND e.starts_at < $4 AND e.ends_at > $3
		LIMIT 1
	`, unique, eventID, startsAt, endsAt).Scan(&busy); err == nil && busy != "" {
		return fail(http.StatusConflict, "RESOURCE_BUSY", busy+" is already booked for that time")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM calendar_event_resources WHERE event_id=$1`, eventID); err != nil {
		return err
	}
	for _, id := range unique {
		item := found[id]
		status := "accepted"
		if item.requiresApproval {
			status = "pending"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_event_resources (event_id, resource_id, name, email, status)
			VALUES ($1,$2,$3,$4,$5)
		`, eventID, id, item.name, item.email, status); err != nil {
			return err
		}
		if status == "pending" && item.approver != "" {
			_, _ = tx.Exec(ctx, `
				INSERT INTO public.mail_notifications (id, tenant_id, user_id, kind, title, body, detail)
				SELECT $1,$2,$3,'resource_approval',$4,COALESCE(e.title,'Calendar event'),
				       'Event ' || $5 || ' · resource ' || $6
				FROM public.calendar_events e WHERE e.id=$5
				  AND NOT EXISTS (SELECT 1 FROM public.mail_notifications n WHERE n.user_id=$3 AND n.kind='resource_approval' AND n.detail LIKE '%' || $5 || '%' AND n.title=$4)
			`, newID("notification"), tenantID, item.approver, truncate("Room approval: "+item.name, 240), eventID, id)
		}
	}
	return nil
}

func (s *Server) replaceCalendarAttendees(ctx context.Context, tx pgx.Tx, eventID string, attendees []string, names map[string]string, organizer userRecord, organizerEmail string, resetResponses bool) error {
	existing := map[string]calendarAttendeePublic{}
	rows, err := tx.Query(ctx, `
		SELECT email::text, name, status, responded_at, role, rsvp,
		       last_invite_sequence, last_response_sequence, last_invite_at, last_response_at
		FROM calendar_attendees WHERE event_id=$1
	`, eventID)
	if err == nil {
		for rows.Next() {
			var email, name, status, role string
			var rsvp bool
			var responded, invitedAt, respondedAt *time.Time
			var lastInviteSequence, lastResponseSequence int
			if rows.Scan(&email, &name, &status, &responded, &role, &rsvp,
				&lastInviteSequence, &lastResponseSequence, &invitedAt, &respondedAt) == nil {
				entry := calendarAttendeePublic{
					Email: email, Name: name, Status: status, Role: role, RSVP: rsvp,
					LastInviteSequence: lastInviteSequence, LastResponseSequence: lastResponseSequence,
				}
				if responded != nil {
					entry.RespondedAt = responded.UTC().Format(time.RFC3339)
				}
				if invitedAt != nil {
					entry.LastInviteAt = invitedAt.UTC().Format(time.RFC3339)
				}
				if respondedAt != nil {
					entry.LastResponseAt = respondedAt.UTC().Format(time.RFC3339)
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
			INSERT INTO calendar_attendees
			(event_id, email, name, status, user_id, organizer, responded_at, role, rsvp)
			VALUES ($1, $2, $3, 'accepted', $4, true, now(), 'CHAIR', false)
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
		name := strings.TrimSpace(names[email])
		lastInviteSequence, lastResponseSequence := -1, -1
		var respondedAt, lastInviteAt, lastResponseAt any
		role := "REQ-PARTICIPANT"
		rsvp := true
		if previous, ok := existing[email]; ok {
			if name == "" {
				name = previous.Name
			}
			lastInviteSequence = previous.LastInviteSequence
			lastResponseSequence = previous.LastResponseSequence
			if previous.Role != "" {
				role = previous.Role
			}
			// A new SEQUENCE invalidates the answer that applied to the old
			// invitation. The previous response details stay available for the
			// organizer, but the guest must respond again.
			if !resetResponses {
				status = previous.Status
				if parsed, err := time.Parse(time.RFC3339, previous.RespondedAt); err == nil {
					respondedAt = parsed
				}
			}
			if parsed, err := time.Parse(time.RFC3339, previous.LastInviteAt); err == nil {
				lastInviteAt = parsed
			}
			if parsed, err := time.Parse(time.RFC3339, previous.LastResponseAt); err == nil {
				lastResponseAt = parsed
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
			INSERT INTO calendar_attendees
			(event_id, email, name, status, user_id, organizer, responded_at, role, rsvp,
			 last_invite_sequence, last_response_sequence, last_invite_at, last_response_at)
			VALUES ($1, $2, $3, $4, $5, false, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (event_id, email) DO UPDATE
			SET name=EXCLUDED.name, user_id=EXCLUDED.user_id, status=EXCLUDED.status,
			    responded_at=EXCLUDED.responded_at, role=EXCLUDED.role, rsvp=EXCLUDED.rsvp,
			    last_invite_sequence=EXCLUDED.last_invite_sequence,
			    last_response_sequence=EXCLUDED.last_response_sequence,
			    last_invite_at=EXCLUDED.last_invite_at, last_response_at=EXCLUDED.last_response_at
		`, eventID, email, name, status, userID, respondedAt, role, rsvp,
			lastInviteSequence, lastResponseSequence, lastInviteAt, lastResponseAt); err != nil {
			return err
		}
	}
	return nil
}
