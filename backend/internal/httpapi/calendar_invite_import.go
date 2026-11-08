package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// importMessageCalendar adds an invitation received by email to the user's
// default calendar. It is user-triggered from the reader card, so the local
// attendee is added even when the sender omitted them from ATTENDEE.
func (s *Server) importMessageCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	messageID := r.PathValue("id")
	if _, _, delegated := parseDelegatedMessageID(messageID); delegated {
		writeError(w, http.StatusForbidden, "DELEGATE_READ_ONLY", "Open the invitation in the mailbox owner's calendar")
		return
	}
	var subject, fromEmail, folder, name, mime, storedPath, accountEmail string
	var messageIDHeader, threadID string
	var sentAt time.Time
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT m.subject, m.from_email::text, m.folder, m.message_id, m.thread_id, m.sent_at,
		       a.name, a.mime, a.stored_path, COALESCE(ma2.email::text, '')
		FROM mail_messages m
		JOIN mail_message_attachments ma ON ma.message_id=m.id
		JOIN attachments a ON a.id=ma.attachment_id AND a.user_id=m.user_id
		LEFT JOIN mail_accounts ma2 ON ma2.id=m.account_id
		WHERE m.id=$1 AND m.user_id=$2
		  AND (lower(a.mime) LIKE 'text/calendar%' OR lower(a.name) LIKE '%.ics')
		ORDER BY ma.position
		LIMIT 1
	`, messageID, sess.User.ID).Scan(&subject, &fromEmail, &folder, &messageIDHeader, &threadID, &sentAt, &name, &mime, &storedPath, &accountEmail)
	if err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_INVITE", "This message does not contain a calendar invitation")
		return
	}
	if folder == "spam" {
		writeError(w, http.StatusForbidden, "SPAM_INVITE_BLOCKED", "Move this message to Inbox before adding the invitation")
		return
	}
	if storedPath == "" || strings.ContainsAny(storedPath, `/\`) || storedPath == "." || storedPath == ".." {
		writeError(w, http.StatusNotFound, "CALENDAR_INVITE", "The calendar file is no longer available")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, storedPath))
	if err != nil || len(data) == 0 || len(data) > maxCalendarCardBytes {
		writeError(w, http.StatusNotFound, "CALENDAR_INVITE", "The calendar file is no longer available")
		return
	}
	method := strings.ToUpper(strings.TrimSpace(icsMethodValue(string(data))))
	if method != "" && method != "REQUEST" && method != "PUBLISH" {
		writeError(w, http.StatusBadRequest, "CALENDAR_INVITE", "This file is not a meeting invitation")
		return
	}
	events, err := parseICSReplies(data)
	if err != nil || len(events) == 0 {
		writeError(w, http.StatusBadRequest, "CALENDAR_INVITE", "The calendar file could not be read")
		return
	}
	event := events[0]
	for _, candidate := range events {
		if candidate.RecurrenceID.IsZero() {
			event = candidate
			break
		}
	}
	if strings.TrimSpace(event.UID) == "" || event.StartsAt.IsZero() || event.EndsAt.IsZero() {
		writeError(w, http.StatusBadRequest, "CALENDAR_INVITE", "The invitation is missing a meeting time")
		return
	}
	if s.localMailIdentity(r.Context(), sess.User, event.Organizer) {
		writeError(w, http.StatusConflict, "CALENDAR_INVITE", "You organized this meeting; open it from your calendar")
		return
	}
	if err := s.ensureDefaultCalendar(r.Context(), sess.User); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not prepare your calendar")
		return
	}
	var existingID string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT id FROM calendar_events
		WHERE user_id=$1 AND calendar_id='default' AND lower(uid)=lower($2)
		LIMIT 1
	`, sess.User.ID, event.UID).Scan(&existingID); err == nil {
		payload, _ := s.loadCalendarEvent(r.Context(), sess.User, existingID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alreadyPresent": true, "event": payload})
		return
	}
	timezone := firstNonEmpty(event.Timezone, "UTC")
	if _, err := time.LoadLocation(timezone); err != nil {
		timezone = "UTC"
	}
	eventID := newID("cal")
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start the calendar import")
		return
	}
	defer tx.Rollback(r.Context())
	title := truncate(firstNonEmpty(event.Title, subject, "Invitation"), 240)
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO calendar_events
		(id, tenant_id, user_id, calendar_id, uid, title, description, location, starts_at, ends_at,
		 all_day, timezone, status, visibility, organizer_email, organizer_name, etag, metadata,
		 sequence, transparency, reminder_minutes, response_requested, sender_email, sender_name)
		VALUES ($1,$2,$3,'default',$4,$5,$6,$7,$8,$9,$10,$11,$12,'default',$13,'',$14,$15,$16,'busy',15,true,$17,'')
	`, eventID, sess.User.TenantID, sess.User.ID, event.UID, title,
		truncate(event.Description, 5000), truncate(event.Location, 240),
		event.StartsAt.UTC(), event.EndsAt.UTC(), event.AllDay, timezone,
		firstNonEmpty(event.Status, "confirmed"), event.Organizer, newID("etag"),
		`{"color":"#0f6cbd","importedFrom":"email"}`, event.Sequence,
		firstNonEmpty(strings.ToLower(strings.TrimSpace(fromEmail)), event.Organizer)); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not save the invitation")
		return
	}
	if err := s.insertImportedCalendarAttendees(r.Context(), tx, eventID, sess.User, firstNonEmpty(accountEmail, sess.User.Email), event); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not save the invitation guests")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not finish the calendar import")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_notifications
		  (id, tenant_id, user_id, kind, title, body, detail, actor_user_id, actor_name, actor_email,
		   thread_id, message_id, owner_user_id)
		VALUES ($1,$2,$3,'calendar',$4,$5,$6,NULL,'',$7,$8,$9,$3)
	`, newID("notification"), sess.User.TenantID, sess.User.ID,
		"Calendar invitation added", title, calendarInviteWhen(map[string]any{
			"startsAt": event.StartsAt.UTC().Format(time.RFC3339),
			"endsAt":   event.EndsAt.UTC().Format(time.RFC3339),
			"allDay":   event.AllDay, "timezone": timezone, "title": title,
		}, "en"), event.Organizer, threadID, messageIDHeader)
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.invite-imported", title, "Imported an email invitation")
	s.publishRealtime("calendar")
	payload, _ := s.loadCalendarEvent(r.Context(), sess.User, eventID)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "event": payload})
}

func (s *Server) insertImportedCalendarAttendees(ctx context.Context, tx pgx.Tx, eventID string, user userRecord, localEmail string, event parsedCalendarEvent) error {
	seen := map[string]bool{}
	localEmail = strings.ToLower(strings.TrimSpace(firstNonEmpty(localEmail, user.Email)))
	organizerEmail := strings.ToLower(strings.TrimSpace(event.Organizer))
	insert := func(email, name, status, role string, organizer bool, userID any, respondedAt any) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO calendar_attendees
			(event_id, email, name, status, user_id, organizer, responded_at, role, rsvp,
			 last_invite_sequence, last_response_sequence)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true,$9,-1)
			ON CONFLICT (event_id, email) DO UPDATE
			SET name=EXCLUDED.name, status=EXCLUDED.status, user_id=EXCLUDED.user_id,
			    organizer=EXCLUDED.organizer, responded_at=EXCLUDED.responded_at,
			    role=EXCLUDED.role, rsvp=EXCLUDED.rsvp, last_invite_sequence=EXCLUDED.last_invite_sequence
		`, eventID, email, name, status, userID, organizer, respondedAt, role, event.Sequence)
		return err
	}
	for _, attendee := range event.AttendeeDetails {
		email := strings.ToLower(strings.TrimSpace(attendee.Email))
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		isOrganizer := email == organizerEmail
		status := calendarPartStat(attendee.PartStat)
		if status == "" {
			status = "needs-action"
		}
		if isOrganizer {
			status = "accepted"
		}
		role := strings.TrimSpace(attendee.Role)
		if role == "" {
			role = "REQ-PARTICIPANT"
		}
		if isOrganizer {
			role = "CHAIR"
		}
		name := attendee.Name
		var userID any
		if local := s.calendarUserByEmail(ctx, user.TenantID, email, userRecord{}); local.ID != "" {
			userID = local.ID
			if name == "" {
				name = local.Name
			}
		} else if email == localEmail {
			userID = user.ID
			if name == "" {
				name = user.Name
			}
		}
		var respondedAt any
		if status != "needs-action" {
			respondedAt = time.Now().UTC()
		}
		if err := insert(email, name, status, role, isOrganizer, userID, respondedAt); err != nil {
			return err
		}
	}
	email := localEmail
	if email != "" && !seen[email] {
		if err := insert(email, user.Name, "needs-action", "REQ-PARTICIPANT", false, user.ID, nil); err != nil {
			return err
		}
	}
	return nil
}
