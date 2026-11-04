package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCalendarCardBytes = 1 << 20

// messageCalendarCard returns the structured calendar data carried by an email
// attachment. The webmail uses it to render the invitation/proposal card right
// under the message header, the same way a calendar-aware mail client does.
func (s *Server) messageCalendarCard(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	lookupUser := sess.User
	messageID := r.PathValue("id")
	if ownerID, originalID, delegated := parseDelegatedMessageID(messageID); delegated {
		if _, allowed := s.delegationFor(r.Context(), sess.User.TenantID, ownerID, sess.User.ID); !allowed {
			writeError(w, http.StatusForbidden, "DELEGATE_FORBIDDEN", "You do not have access to this mailbox")
			return
		}
		owner, err := s.userByID(r.Context(), ownerID)
		if err != nil || owner.TenantID != sess.User.TenantID {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Delegated mailbox not found")
			return
		}
		lookupUser = owner
		messageID = originalID
	} else if ownerID := strings.TrimSpace(r.URL.Query().Get("owner")); ownerID != "" {
		if _, allowed := s.delegationFor(r.Context(), sess.User.TenantID, ownerID, sess.User.ID); !allowed {
			writeError(w, http.StatusForbidden, "DELEGATE_FORBIDDEN", "You do not have access to this mailbox")
			return
		}
		owner, err := s.userByID(r.Context(), ownerID)
		if err != nil || owner.TenantID != sess.User.TenantID {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Delegated mailbox not found")
			return
		}
		lookupUser = owner
	}

	var subject, fromEmail, folder, name, mime, storedPath string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT m.subject, m.from_email::text, m.folder, a.name, a.mime, a.stored_path
		FROM mail_messages m
		JOIN mail_message_attachments ma ON ma.message_id=m.id
		JOIN attachments a ON a.id=ma.attachment_id AND a.user_id=m.user_id
		WHERE m.id=$1 AND m.user_id=$2
		  AND (lower(a.mime) LIKE 'text/calendar%' OR lower(a.name) LIKE '%.ics')
		ORDER BY ma.position
		LIMIT 1
	`, messageID, lookupUser.ID).Scan(&subject, &fromEmail, &folder, &name, &mime, &storedPath)
	if err != nil || folder == "spam" {
		writeJSON(w, http.StatusOK, map[string]any{"calendar": nil})
		return
	}
	if storedPath == "" || strings.ContainsAny(storedPath, `/\`) || storedPath == "." || storedPath == ".." {
		writeJSON(w, http.StatusOK, map[string]any{"calendar": nil})
		return
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, storedPath))
	if err != nil || len(data) == 0 || len(data) > maxCalendarCardBytes {
		writeJSON(w, http.StatusOK, map[string]any{"calendar": nil})
		return
	}
	events, err := parseICSReplies(data)
	if err != nil || len(events) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"calendar": nil})
		return
	}
	calendar := events[0]
	for _, candidate := range events {
		if candidate.RecurrenceID.IsZero() {
			calendar = candidate
			break
		}
	}
	method := strings.ToUpper(strings.TrimSpace(icsMethodValue(string(data))))

	card := map[string]any{
		"method":        method,
		"kind":          calendarCardKind(method),
		"title":         truncate(firstNonEmpty(calendar.Title, subject, "Untitled event"), 240),
		"description":   calendar.Description,
		"location":      calendar.Location,
		"timezone":      firstNonEmpty(calendar.Timezone, "UTC"),
		"uid":           calendar.UID,
		"sequence":      calendar.Sequence,
		"status":        firstNonEmpty(calendar.Status, "confirmed"),
		"allDay":        calendar.AllDay,
		"organizer":     calendar.Organizer,
		"organizerName": "",
		"fromEmail":     strings.ToLower(strings.TrimSpace(fromEmail)),
		"attachment":    map[string]any{"name": name, "mime": mime},
	}

	attendees := make([]map[string]any, 0, len(calendar.AttendeeDetails))
	for _, attendee := range calendar.AttendeeDetails {
		attendees = append(attendees, map[string]any{
			"email": attendee.Email, "name": attendee.Name,
			"status": strings.ToLower(firstNonEmpty(attendee.PartStat, "NEEDS-ACTION")),
			"role":   attendee.Role, "rsvp": attendee.RSVP,
		})
	}
	card["attendees"] = attendees
	if !calendar.StartsAt.IsZero() {
		card["startsAt"] = calendar.StartsAt.UTC().Format(time.RFC3339)
		card["endsAt"] = calendar.EndsAt.UTC().Format(time.RFC3339)
	}

	senderEmail := strings.ToLower(strings.TrimSpace(fromEmail))
	role := "other"
	if strings.EqualFold(calendar.Organizer, lookupUser.Email) {
		role = "organizer"
	} else if calendarHasAttendee(calendar, lookupUser.Email) {
		role = "attendee"
	}

	var eventID, eventTitle, eventOrganizer, eventOrganizerName, eventStatus, eventTimezone, myStatus string
	var eventSequence int
	var eventStartsAt, eventEndsAt time.Time
	var eventDescription, eventLocation string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT e.id, e.title, e.organizer_email::text, e.organizer_name, e.status, e.timezone,
		       e.sequence, e.starts_at, e.ends_at, e.description, e.location,
		       COALESCE((SELECT a.status FROM calendar_attendees a
		                 WHERE a.event_id=e.id AND lower(a.email)=lower($3) LIMIT 1), '')
		FROM calendar_events e
		WHERE e.tenant_id=$1 AND lower(e.uid)=lower($2)
		  AND (e.user_id=$4
		       OR EXISTS (SELECT 1 FROM calendar_shares s WHERE s.calendar_id=e.calendar_id
		                  AND s.owner_user_id=e.user_id AND s.grantee_user_id=$4)
		       OR EXISTS (SELECT 1 FROM calendar_attendees a WHERE a.event_id=e.id
		                  AND lower(a.email)=lower($3)))
		ORDER BY CASE WHEN lower(e.organizer_email)=lower($3) THEN 0 ELSE 1 END
		LIMIT 1
	`, lookupUser.TenantID, calendar.UID, lookupUser.Email, lookupUser.ID).Scan(
		&eventID, &eventTitle, &eventOrganizer, &eventOrganizerName, &eventStatus, &eventTimezone,
		&eventSequence, &eventStartsAt, &eventEndsAt, &eventDescription, &eventLocation, &myStatus)
	if err == nil {
		if strings.EqualFold(eventOrganizer, lookupUser.Email) {
			role = "organizer"
		} else if myStatus != "" {
			role = "attendee"
		}
		card["eventId"] = eventID
		card["eventTitle"] = eventTitle
		card["eventStartsAt"] = eventStartsAt.UTC().Format(time.RFC3339)
		card["eventEndsAt"] = eventEndsAt.UTC().Format(time.RFC3339)
		card["eventTimezone"] = firstNonEmpty(eventTimezone, "UTC")
		card["eventStatus"] = eventStatus
		card["eventSequence"] = eventSequence
		card["myStatus"] = strings.ToLower(myStatus)
		card["organizer"] = eventOrganizer
		card["organizerName"] = eventOrganizerName
		if calendar.Title == "" {
			card["title"] = eventTitle
		}
		if calendar.Description == "" {
			card["description"] = eventDescription
		}
		if calendar.Location == "" {
			card["location"] = eventLocation
		}
		if calendar.StartsAt.IsZero() {
			card["startsAt"] = eventStartsAt.UTC().Format(time.RFC3339)
			card["endsAt"] = eventEndsAt.UTC().Format(time.RFC3339)
			card["timezone"] = firstNonEmpty(eventTimezone, "UTC")
		}
		card["attendees"] = s.calendarCardAttendees(r, eventID, attendees)
		if role == "organizer" && method != "REPLY" && method != "CANCEL" {
			if proposal := s.calendarCardProposal(r, eventID, senderEmail); proposal != nil {
				card["proposal"] = proposal
				card["kind"] = "proposal"
			} else if !sameCalendarMoment(calendar.StartsAt, eventStartsAt) ||
				!sameCalendarMoment(calendar.EndsAt, eventEndsAt) {
				card["kind"] = "proposal"
				card["proposalUnavailable"] = true
			}
		}
	}
	card["role"] = role
	if method == "REPLY" {
		card["kind"] = "response"
		card["responseStatus"] = calendarReplyStatus(calendar, senderEmail)
	}
	if method == "REQUEST" && role == "organizer" && calendar.StartsAt.IsZero() {
		card["kind"] = "invitation"
	}
	card["canOpenEvent"] = eventID != ""
	card["canRespond"] = role == "attendee" && eventID != "" && !strings.EqualFold(eventStatus, "cancelled")
	card["canManageProposal"] = role == "organizer" && card["proposal"] != nil
	writeJSON(w, http.StatusOK, map[string]any{"calendar": card})
}

func calendarCardKind(method string) string {
	switch method {
	case "CANCEL":
		return "cancelled"
	case "REPLY":
		return "response"
	case "COUNTER":
		return "proposal"
	}
	return "invitation"
}

func calendarHasAttendee(event parsedCalendarEvent, email string) bool {
	for _, attendee := range event.AttendeeDetails {
		if strings.EqualFold(attendee.Email, email) {
			return true
		}
	}
	return false
}

func calendarReplyStatus(event parsedCalendarEvent, email string) string {
	for _, attendee := range event.AttendeeDetails {
		if strings.EqualFold(attendee.Email, email) {
			return strings.ToLower(firstNonEmpty(attendee.PartStat, "NEEDS-ACTION"))
		}
	}
	return ""
}

func (s *Server) calendarCardAttendees(r *http.Request, eventID string, fallback []map[string]any) []map[string]any {
	list := []map[string]any{}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT a.email::text, a.name, a.status, a.organizer, a.role
		FROM calendar_attendees a WHERE a.event_id=$1
		ORDER BY a.organizer DESC, a.email
	`, eventID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var email, name, status, role string
			var organizer bool
			if rows.Scan(&email, &name, &status, &organizer, &role) == nil {
				list = append(list, map[string]any{
					"email": email, "name": name, "status": status,
					"organizer": organizer, "role": role,
				})
			}
		}
	}
	if len(list) > 0 {
		return list
	}
	return fallback
}

func (s *Server) calendarCardProposal(r *http.Request, eventID, attendeeEmail string) map[string]any {
	var id, email, name, timezone, note, status string
	var startsAt, endsAt time.Time
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id, attendee_email::text, attendee_name, proposed_starts_at, proposed_ends_at,
		       timezone, note, status
		FROM calendar_meeting_proposals
		WHERE event_id=$1 AND status='pending' AND lower(attendee_email)=lower($2)
		ORDER BY created_at DESC
		LIMIT 1
	`, eventID, attendeeEmail).Scan(&id, &email, &name, &startsAt, &endsAt, &timezone, &note, &status)
	if err != nil {
		return nil
	}
	return map[string]any{
		"id": id, "attendeeEmail": email, "attendeeName": name,
		"startsAt": startsAt.UTC().Format(time.RFC3339), "endsAt": endsAt.UTC().Format(time.RFC3339),
		"timezone": timezone, "note": note, "status": status,
	}
}
