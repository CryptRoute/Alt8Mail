package httpapi

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

type calendarProposalInput struct {
	EventID       string
	AttendeeEmail string
	AttendeeName  string
	StartsAt      time.Time
	EndsAt        time.Time
	Timezone      string
	Note          string
	Sequence      int
	RecurrenceID  *time.Time
	Source        string
	MessageID     string
	ActorUserID   string
}

type calendarProposalRecord struct {
	ID            string
	EventID       string
	AttendeeEmail string
	AttendeeName  string
	Timezone      string
	Note          string
	Status        string
	Source        string
	StartsAt      time.Time
	EndsAt        time.Time
	Sequence      int
	RecurrenceID  *time.Time
}

func (s *Server) saveCalendarProposal(ctx context.Context, input calendarProposalInput) (string, error) {
	var tenantID, title, organizerEmail string
	var currentSequence int
	if err := s.store.DB.QueryRow(ctx, `
		SELECT tenant_id::text, title, organizer_email::text, sequence
		FROM calendar_events WHERE id=$1
	`, input.EventID).Scan(&tenantID, &title, &organizerEmail, &currentSequence); err != nil {
		return "", fail(http.StatusNotFound, "NOT_FOUND", "Event not found")
	}
	if input.Sequence != currentSequence {
		return "", fail(http.StatusConflict, "RSVP_STALE", "This invitation changed. Open the newest invitation and try again")
	}
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return "", fail(http.StatusBadRequest, "EVENT_TIMEZONE", "Unknown time zone")
	}
	input.Timezone = location.String()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return "", fail(http.StatusInternalServerError, "CALENDAR", "Could not start proposal transaction")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE calendar_meeting_proposals SET status='superseded'
		WHERE event_id=$1 AND lower(attendee_email)=lower($2) AND status='pending'
	`, input.EventID, input.AttendeeEmail); err != nil {
		return "", err
	}
	proposalID := newID("calprop")
	var recurrence any
	if input.RecurrenceID != nil {
		recurrence = input.RecurrenceID.UTC()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO calendar_meeting_proposals
		(id, tenant_id, event_id, attendee_email, attendee_name, proposed_starts_at, proposed_ends_at,
		 timezone, note, sequence, recurrence_id, status, source, message_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending',$12,$13)
	`, proposalID, tenantID, input.EventID, input.AttendeeEmail, input.AttendeeName,
		input.StartsAt.UTC(), input.EndsAt.UTC(), input.Timezone, truncate(input.Note, 500),
		input.Sequence, recurrence, input.Source, truncate(input.MessageID, 400)); err != nil {
		return "", err
	}
	result, err := tx.Exec(ctx, `
		UPDATE calendar_attendees a
		SET status='tentative', responded_at=now(), last_response_sequence=$2,
		    last_response_at=now(), note=$3
		FROM calendar_events e
		WHERE a.event_id=e.id AND e.id=$1 AND e.sequence=$2
		  AND lower(a.email)=lower($4)
	`, input.EventID, input.Sequence, "Proposed a new time", input.AttendeeEmail)
	if err != nil || result.RowsAffected() == 0 {
		return "", fail(http.StatusConflict, "RSVP_STALE", "This invitation changed. Open the newest invitation and try again")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if organizer := s.calendarUserByEmail(ctx, tenantID, organizerEmail, userRecord{TenantID: tenantID}); organizer.ID != "" {
		when := input.StartsAt.In(mustLocation(input.Timezone)).Format("Mon, Jan 2, 2006 · 15:04") +
			" – " + input.EndsAt.In(mustLocation(input.Timezone)).Format("15:04")
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_notifications
			  (id, tenant_id, user_id, kind, title, body, detail, actor_user_id, actor_name, actor_email,
			   thread_id, message_id, owner_user_id)
			VALUES ($1,$2,$3,'calendar',$4,$5,$6,$7,$8,$9,$11,'',$10)
		`, newID("notification"), tenantID, organizer.ID,
			firstNonEmpty(input.AttendeeName, input.AttendeeEmail)+" proposed a new time",
			title, when, nullableUUID(input.ActorUserID), input.AttendeeName,
			input.AttendeeEmail, organizer.ID, input.EventID)
		s.insertAudit(organizer, "calendar.counter.received", title,
			fmt.Sprintf("%s proposed %s · sequence %d", input.AttendeeEmail, when, input.Sequence),
			"127.0.0.1", "calendar")
		s.publishRealtimeTo(organizer.ID, "calendar")
	}
	return proposalID, nil
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func nullableUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Server) createCalendarCounter(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	event, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	if stateString(event, "status") == "cancelled" {
		writeError(w, http.StatusConflict, "EVENT_CANCELLED", "This event has been cancelled")
		return
	}
	if !stateBool(event, "invited") {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only invited guests can propose a new time")
		return
	}
	var body struct {
		Date           string `json:"date"`
		Start          string `json:"start"`
		End            string `json:"end"`
		Timezone       string `json:"timezone"`
		Note           string `json:"note"`
		OccurrenceDate string `json:"occurrenceDate"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	date := firstNonEmpty(strings.TrimSpace(body.Date), stateString(event, "date"))
	start := firstNonEmpty(strings.TrimSpace(body.Start), stateString(event, "start"))
	end := firstNonEmpty(strings.TrimSpace(body.End), stateString(event, "end"))
	timezone := firstNonEmpty(strings.TrimSpace(body.Timezone), stateString(event, "timezone"), "UTC")
	if stateBool(event, "allDay") && body.Start == "" {
		start, end = "00:00", "23:59"
	}
	startsAt, endsAt, err := parseProposalTimes(date, start, end, timezone)
	if err != nil {
		writeFromError(w, err)
		return
	}
	var recurrenceID *time.Time
	if value := strings.TrimSpace(body.OccurrenceDate); value != "" && stateString(event, "recurrenceRule") != "" {
		if parsed, ok := calendarOccurrenceUTC(event, value); ok {
			recurrenceID = &parsed
		}
	}
	proposalID, err := s.saveCalendarProposal(r.Context(), calendarProposalInput{
		EventID: id, AttendeeEmail: sess.User.Email, AttendeeName: sess.User.Name,
		StartsAt: startsAt, EndsAt: endsAt, Timezone: timezone, Note: body.Note,
		Sequence: int(stateInt(event, "sequence")), RecurrenceID: recurrenceID,
		Source: "app", ActorUserID: sess.User.ID,
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.counter.proposed", stateString(event, "title"),
		fmt.Sprintf("Proposed %s %s–%s · %s", date, start, end, timezone))
	attendee := sess.User
	go s.sendCalendarCounterEmail(context.Background(), attendee, event, startsAt, endsAt, timezone, body.Note, recurrenceID)
	event, _ = s.loadCalendarEvent(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proposalId": proposalID, "event": event})
}

func parseProposalTimes(date, start, end, timezone string) (time.Time, time.Time, error) {
	if !calendarDatePattern.MatchString(strings.TrimSpace(date)) {
		return time.Time{}, time.Time{}, fail(http.StatusBadRequest, "EVENT_DATE", "Choose a valid date")
	}
	if !calendarTimePattern.MatchString(strings.TrimSpace(start)) || !calendarTimePattern.MatchString(strings.TrimSpace(end)) {
		return time.Time{}, time.Time{}, fail(http.StatusBadRequest, "EVENT_TIME", "Choose a valid start and end time")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return time.Time{}, time.Time{}, fail(http.StatusBadRequest, "EVENT_TIMEZONE", "Unknown time zone")
	}
	startsAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+start, location)
	if err != nil {
		return time.Time{}, time.Time{}, fail(http.StatusBadRequest, "EVENT_TIME", "Invalid proposed start")
	}
	endsAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+end, location)
	if err != nil || !endsAt.After(startsAt) {
		return time.Time{}, time.Time{}, fail(http.StatusBadRequest, "EVENT_TIME", "The proposed end must be after the start")
	}
	return startsAt, endsAt, nil
}

func calendarOccurrenceUTC(event map[string]any, occurrenceDate string) (time.Time, bool) {
	if !calendarDatePattern.MatchString(strings.TrimSpace(occurrenceDate)) {
		return time.Time{}, false
	}
	zone := calendarEventLocation(event)
	start := stateString(event, "start")
	if stateBool(event, "allDay") {
		start = "00:00"
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", occurrenceDate+" "+start, zone)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func (s *Server) sendCalendarCounterEmail(ctx context.Context, attendee userRecord, event map[string]any, startsAt, endsAt time.Time, timezone, note string, recurrenceID *time.Time) {
	organizerEmail := strings.ToLower(strings.TrimSpace(stateString(event, "organizer")))
	if organizerEmail == "" || strings.EqualFold(domainOf(organizerEmail), domainOf(attendee.Email)) {
		return
	}
	copyEvent := deepCopyMap(event)
	copyEvent["startsAt"] = startsAt.UTC().Format(time.RFC3339)
	copyEvent["endsAt"] = endsAt.UTC().Format(time.RFC3339)
	copyEvent["timezone"] = timezone
	copyEvent["date"] = startsAt.In(mustLocation(timezone)).Format("2006-01-02")
	copyEvent["start"] = startsAt.In(mustLocation(timezone)).Format("15:04")
	copyEvent["end"] = endsAt.In(mustLocation(timezone)).Format("15:04")
	copyEvent["senderEmail"] = ""
	copyEvent["senderName"] = ""
	if recurrenceID != nil {
		copyEvent["recurrenceId"] = recurrenceID.UTC().Format(time.RFC3339)
		copyEvent["recurrenceTimezone"] = stateString(event, "timezone")
		copyEvent["recurrenceAllDay"] = stateBool(event, "allDay")
		copyEvent["recurrenceRule"] = ""
	} else {
		copyEvent["recurrenceId"] = ""
	}
	copyEvent["exceptions"] = nil
	copyEvent["attendeeList"] = []any{map[string]any{
		"email": attendee.Email, "name": attendee.Name, "status": "tentative",
		"organizer": false, "role": "REQ-PARTICIPANT",
	}}
	ics := buildCalendarICS("Alt8Mail counter", []map[string]any{copyEvent}, "COUNTER", s.cfg.PublicURL)
	title := stateString(event, "title")
	when := calendarInviteWhen(copyEvent, "en")
	subject := "New time proposed: " + title
	bodyText := attendee.Name + " proposed a new time for “" + title + "”.\n\n" + when + "\n"
	if strings.TrimSpace(note) != "" {
		bodyText += "\n" + truncate(note, 500) + "\n"
	}
	bodyHTML := `<p style="font-family:Arial,sans-serif;font-size:14px;color:#263445">` +
		html.EscapeString(attendee.Name+" proposed a new time for “"+title+"”.") +
		`</p><p style="font-family:Arial,sans-serif;font-size:14px;color:#263445">` + html.EscapeString(when) + `</p>`
	raw, err := buildCalendarInviteMIME(attendee.Name, attendee.Email, attendee.Email, "", organizerEmail, subject,
		bodyText, bodyHTML, ics, "COUNTER", fmt.Sprintf("<%s@%s>", newID("calcounter"), s.cfg.MailDomain), "", "")
	if err != nil {
		s.logger.Warn("calendar counter MIME build failed", "err", err, "event", stateString(event, "id"))
		return
	}
	if err := s.submitSMTPMessage(attendee.Email, []string{organizerEmail}, raw); err != nil {
		s.logger.Warn("calendar counter delivery failed", "err", err, "event", stateString(event, "id"))
	}
}

func (s *Server) acceptCalendarProposal(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	proposalID := r.PathValue("proposalId")
	event, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	access := stateString(event, "access")
	if access != "owner" && access != "edit" {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only the organizer or a calendar editor can decide a proposed time")
		return
	}
	proposal, err := s.loadPendingProposal(r.Context(), id, proposalID)
	if err != nil {
		writeError(w, http.StatusNotFound, "PROPOSAL_NOT_FOUND", "That proposed time is no longer available")
		return
	}
	applyException := proposal.RecurrenceID != nil && stateString(event, "recurrenceRule") != ""
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start proposal transaction")
		return
	}
	defer tx.Rollback(r.Context())
	senderEmail, senderName := "", ""
	if organizerEmail := stateString(event, "organizer"); organizerEmail != "" && !strings.EqualFold(organizerEmail, sess.User.Email) {
		senderEmail, senderName = sess.User.Email, sess.User.Name
	}
	var sequence int
	if err := tx.QueryRow(r.Context(), `
		UPDATE calendar_events SET sequence=sequence+1, sender_email=$2, sender_name=$3, updated_at=now()
		WHERE id=$1 RETURNING sequence
	`, id, senderEmail, senderName).Scan(&sequence); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE calendar_event_exceptions SET sequence=$2, updated_at=now()
		WHERE event_id=$1 AND sequence < $2
	`, id, sequence); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update recurrence exceptions")
		return
	}
	timezone := firstNonEmpty(proposal.Timezone, stateString(event, "timezone"), "UTC")
	if applyException {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO calendar_event_exceptions
			(id, tenant_id, event_id, recurrence_id, title, description, location, starts_at, ends_at,
			 all_day, timezone, status, sequence, reminder_minutes)
			SELECT $1, e.tenant_id, e.id, $2, e.title, e.description, e.location, $3, $4,
			       false, $5, 'confirmed', $6, e.reminder_minutes
			FROM calendar_events e WHERE e.id=$7
			ON CONFLICT (event_id, recurrence_id) DO UPDATE
			SET starts_at=EXCLUDED.starts_at, ends_at=EXCLUDED.ends_at, timezone=EXCLUDED.timezone,
			    all_day=false, status='confirmed', sequence=EXCLUDED.sequence, updated_at=now()
		`, newID("calexc"), *proposal.RecurrenceID, proposal.StartsAt, proposal.EndsAt, timezone, sequence, id); err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not apply the proposed time")
			return
		}
	} else {
		if _, err := tx.Exec(r.Context(), `
			UPDATE calendar_events SET starts_at=$2, ends_at=$3, timezone=$4, all_day=false, status='confirmed', updated_at=now()
			WHERE id=$1
		`, id, proposal.StartsAt, proposal.EndsAt, timezone); err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not apply the proposed time")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE calendar_attendees SET status='needs-action', responded_at=NULL, note=''
		WHERE event_id=$1 AND organizer=false
	`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not reset guest responses")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE calendar_attendees a
		SET status='accepted', responded_at=now(), last_response_sequence=$2,
		    last_response_at=now(), note='Proposed time accepted'
		WHERE a.event_id=$1 AND lower(a.email)=lower($3)
	`, id, sequence, proposal.AttendeeEmail); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update the guest response")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE calendar_meeting_proposals
		SET status=CASE WHEN id=$2 THEN 'accepted' ELSE 'superseded' END,
		    decided_by=$3, decided_at=now()
		WHERE event_id=$1 AND status='pending'
	`, id, proposalID, sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update the proposal")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the proposal")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.counter.accepted", stateString(event, "title"),
		fmt.Sprintf("Accepted %s's proposed time · sequence %d", proposal.AttendeeEmail, sequence))
	if err := s.queueCalendarInvites(context.Background(), sess.User, id, "REQUEST"); err != nil {
		s.logger.Warn("accepted proposal invitation queue failed", "err", err, "event", id)
		go s.sendCalendarInvites(context.Background(), sess.User, id, "REQUEST")
	}
	s.publishRealtime("calendar")
	event, _ = s.loadCalendarEvent(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event, "sequence": sequence})
}

func (s *Server) declineCalendarProposal(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	proposalID := r.PathValue("proposalId")
	event, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	access := stateString(event, "access")
	if access != "owner" && access != "edit" {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only the organizer or a calendar editor can decide a proposed time")
		return
	}
	proposal, err := s.loadPendingProposal(r.Context(), id, proposalID)
	if err != nil {
		writeError(w, http.StatusNotFound, "PROPOSAL_NOT_FOUND", "That proposed time is no longer available")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not start proposal transaction")
		return
	}
	defer tx.Rollback(r.Context())
	result, err := tx.Exec(r.Context(), `
		UPDATE calendar_meeting_proposals
		SET status='declined', decided_by=$3, decided_at=now()
		WHERE event_id=$1 AND id=$2 AND status='pending'
	`, id, proposalID, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "PROPOSAL_DECIDED", "That proposed time has already been decided")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE calendar_attendees SET status='needs-action', responded_at=NULL, note='Proposed time declined'
		WHERE event_id=$1 AND lower(email)=lower($2)
	`, id, proposal.AttendeeEmail); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update the guest response")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the decision")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.counter.declined", stateString(event, "title"),
		"Declined "+proposal.AttendeeEmail+"'s proposed time")
	s.publishRealtime("calendar")
	go s.sendCalendarProposalDeclineEmail(context.Background(), event, proposal)
	event, _ = s.loadCalendarEvent(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event})
}

func (s *Server) loadPendingProposal(ctx context.Context, eventID, proposalID string) (calendarProposalRecord, error) {
	var item calendarProposalRecord
	var recurrence *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, event_id, attendee_email::text, attendee_name, timezone, note, status, source,
		       proposed_starts_at, proposed_ends_at, sequence, recurrence_id
		FROM calendar_meeting_proposals
		WHERE id=$1 AND event_id=$2 AND status='pending'
	`, proposalID, eventID).Scan(&item.ID, &item.EventID, &item.AttendeeEmail, &item.AttendeeName,
		&item.Timezone, &item.Note, &item.Status, &item.Source, &item.StartsAt, &item.EndsAt,
		&item.Sequence, &recurrence)
	if err != nil {
		return item, err
	}
	item.RecurrenceID = recurrence
	return item, nil
}

func (s *Server) sendCalendarProposalDeclineEmail(ctx context.Context, event map[string]any, proposal calendarProposalRecord) {
	var organizerEmail, organizerName string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT organizer_email::text, organizer_name FROM calendar_events WHERE id=$1
	`, proposal.EventID).Scan(&organizerEmail, &organizerName); err != nil {
		return
	}
	if organizerEmail == "" {
		return
	}
	title := stateString(event, "title")
	when := calendarInviteWhen(event, "en")
	subject := "Proposed time declined: " + title
	bodyText := "The organizer declined the proposed new time for “" + title + "”.\n\nThe original invitation still stands:\n" + when + "\n"
	bodyHTML := `<p style="font-family:Arial,sans-serif;font-size:14px;color:#263445">The organizer declined the proposed new time for “` +
		html.EscapeString(title) + `”.</p><p style="font-family:Arial,sans-serif;font-size:14px;color:#263445">The original invitation still stands:<br>` +
		html.EscapeString(when) + `</p>`
	item := map[string]any{
		"from":    map[string]any{"name": organizerName, "email": organizerEmail},
		"to":      []any{map[string]any{"name": proposal.AttendeeName, "email": proposal.AttendeeEmail}},
		"subject": subject, "bodyText": bodyText, "bodyHtml": bodyHTML, "trustedHtml": true,
	}
	raw, err := buildMIMEMessage(item, fmt.Sprintf("<%s@%s>", newID("caldecision"), s.cfg.MailDomain), nil)
	if err != nil {
		return
	}
	_ = s.submitSMTPMessage(organizerEmail, []string{proposal.AttendeeEmail}, raw)
}
