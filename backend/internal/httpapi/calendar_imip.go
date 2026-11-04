package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// processInboundCalendarReplies applies RFC 6047 METHOD:REPLY messages to the
// organizer's attendee state. A reply is only accepted when it came from the
// attendee address recorded on the event, matches the event UID and does not
// belong to an older SEQUENCE.
func (s *Server) processInboundCalendarReplies(ctx context.Context, user userRecord, fetched []fetchedMail) int {
	applied := 0
	for _, message := range fetched {
		if len(message.From) == 0 || len(message.CalendarParts) == 0 {
			continue
		}
		sender := strings.ToLower(strings.TrimSpace(stateString(message.From[0], "email")))
		if !calendarEmailPattern.MatchString(sender) {
			continue
		}
		for _, part := range message.CalendarParts {
			method := strings.ToUpper(strings.TrimSpace(part.Method))
			if method == "" {
				method = icsMethodValue(string(part.Data))
			}
			if method != "REPLY" && method != "COUNTER" {
				continue
			}
			replies, err := parseICSReplies(part.Data)
			if err != nil {
				continue
			}
			for _, reply := range replies {
				if method == "COUNTER" {
					if s.applyCalendarCounter(ctx, user, sender, message, part.Data, reply) {
						applied++
					}
					continue
				}
				if s.applyCalendarReply(ctx, user, sender, message, part.Data, reply) {
					applied++
				}
			}
		}
	}
	return applied
}

func (s *Server) applyCalendarReply(ctx context.Context, organizer userRecord, sender string, message fetchedMail, ics []byte, reply parsedCalendarEvent) bool {
	uid := strings.TrimSpace(reply.UID)
	if uid == "" {
		return false
	}
	if organizerEmail := strings.ToLower(strings.TrimSpace(reply.Organizer)); organizerEmail != "" &&
		!strings.EqualFold(organizerEmail, organizer.Email) {
		return false
	}
	var attendee parsedCalendarAttendee
	for _, detail := range reply.AttendeeDetails {
		if strings.EqualFold(detail.Email, sender) {
			attendee = detail
			break
		}
	}
	if attendee.Email == "" {
		return false
	}
	partstat := calendarPartStat(attendee.PartStat)
	if partstat == "" {
		partstat = "needs-action"
	}
	var eventID, eventOrganizer, title, tenantID, currentStatus string
	var eventSequence, lastResponseSequence int
	err := s.store.DB.QueryRow(ctx, `
		SELECT e.id, e.sequence, e.organizer_email::text, e.title, e.tenant_id::text,
		       a.status, a.last_response_sequence
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE lower(e.uid)=lower($1) AND lower(a.email)=lower($2) AND e.tenant_id=$3
		LIMIT 1
	`, uid, sender, organizer.TenantID).Scan(&eventID, &eventSequence, &eventOrganizer, &title, &tenantID, &currentStatus, &lastResponseSequence)
	if err != nil {
		return false
	}
	if !strings.EqualFold(eventOrganizer, organizer.Email) {
		return false
	}
	// A reply must answer the invitation currently on record. Accepting a
	// larger number would let a sender pin a high sequence and lock out later
	// legitimate replies.
	stale := reply.Sequence != eventSequence || reply.Sequence < lastResponseSequence
	dedupSource := firstNonEmpty(strings.TrimSpace(message.MessageID), string(ics))
	if dedupSource == "" {
		dedupSource = fmt.Sprintf("%s\x00%s\x00%d\x00%s", uid, sender, reply.Sequence, partstat)
	}
	sum := sha256.Sum256([]byte(dedupSource + "\x00" + uid + "\x00" + sender + "\x00" + fmt.Sprint(reply.Sequence) + "\x00" + partstat))
	dedupKey := "imip:" + hex.EncodeToString(sum[:])
	var replyID int64
	err = s.store.DB.QueryRow(ctx, `
		INSERT INTO calendar_invite_replies
		(event_id, tenant_id, event_uid, attendee_email, sequence, partstat, method,
		 message_id, dedup_key, source, applied, stale, detail)
		VALUES ($1,$2,$3,$4,$5,$6,'REPLY',$7,$8,'imip',$9,$10,$11)
		ON CONFLICT (event_id, dedup_key) DO NOTHING
		RETURNING id
	`, eventID, tenantID, uid, sender, reply.Sequence, strings.ToUpper(partstat),
		truncate(strings.TrimSpace(message.MessageID), 400), dedupKey, false, stale,
		firstNonEmpty(title, "Meeting response")).Scan(&replyID)
	if err != nil {
		return false
	}
	if stale || partstat == "needs-action" {
		return false
	}
	result, err := s.store.DB.Exec(ctx, `
		UPDATE calendar_attendees a
		SET status=$1, responded_at=now(), last_response_sequence=$2, last_response_at=now()
		FROM calendar_events e
		WHERE a.event_id=e.id AND e.id=$3 AND e.sequence=$2
		  AND lower(a.email)=lower($4) AND a.last_response_sequence <= $2
	`, partstat, reply.Sequence, eventID, sender)
	if err != nil || result.RowsAffected() == 0 {
		return false
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE calendar_invite_replies SET applied=true, stale=false WHERE id=$1`, replyID)
	s.insertAudit(organizer, "calendar.rsvp-received", title,
		fmt.Sprintf("%s replied %s · sequence %d · iMIP", sender, partstat, reply.Sequence),
		"127.0.0.1", "iMIP")
	s.publishRealtimeTo(organizer.ID, "calendar")
	return true
}

func (s *Server) applyCalendarCounter(ctx context.Context, organizer userRecord, sender string, message fetchedMail, ics []byte, counter parsedCalendarEvent) bool {
	uid := strings.TrimSpace(counter.UID)
	if uid == "" || counter.StartsAt.IsZero() || counter.EndsAt.IsZero() || !counter.EndsAt.After(counter.StartsAt) {
		return false
	}
	if organizerEmail := strings.ToLower(strings.TrimSpace(counter.Organizer)); organizerEmail != "" &&
		!strings.EqualFold(organizerEmail, organizer.Email) {
		return false
	}
	if !containsCalendarAttendee(counter.AttendeeDetails, sender) {
		return false
	}
	var eventID, eventOrganizer, eventTimezone, title, tenantID string
	var eventSequence int
	err := s.store.DB.QueryRow(ctx, `
		SELECT e.id, e.sequence, e.organizer_email::text, e.timezone, e.title, e.tenant_id::text
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE lower(e.uid)=lower($1) AND lower(a.email)=lower($2) AND e.tenant_id=$3
		LIMIT 1
	`, uid, sender, organizer.TenantID).Scan(&eventID, &eventSequence, &eventOrganizer, &eventTimezone, &title, &tenantID)
	if err != nil || !strings.EqualFold(eventOrganizer, organizer.Email) {
		return false
	}
	if counter.Sequence != eventSequence {
		return false
	}
	timezone := firstNonEmpty(counter.Timezone, eventTimezone, "UTC")
	var recurrence *time.Time
	if !counter.RecurrenceID.IsZero() {
		value := counter.RecurrenceID.UTC()
		recurrence = &value
	}
	proposalID, err := s.saveCalendarProposal(ctx, calendarProposalInput{
		EventID: eventID, AttendeeEmail: sender, AttendeeName: calendarAttendeeName(counter.AttendeeDetails, sender),
		StartsAt: counter.StartsAt, EndsAt: counter.EndsAt, Timezone: timezone,
		Sequence: eventSequence, RecurrenceID: recurrence, Source: "imip",
		MessageID: sanitizeMailText(strings.TrimSpace(message.MessageID)),
	})
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(firstNonEmpty(message.MessageID, string(ics)) + "\x00" + uid + "\x00" + sender + "\x00COUNTER"))
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO calendar_invite_replies
		(event_id, tenant_id, event_uid, attendee_email, sequence, partstat, method, message_id,
		 dedup_key, source, applied, detail)
		VALUES ($1,$2,$3,$4,$5,'TENTATIVE','COUNTER',$6,$7,'imip',true,$8)
		ON CONFLICT (event_id, dedup_key) DO NOTHING
	`, eventID, tenantID, uid, sender, eventSequence, truncate(message.MessageID, 400),
		"counter:"+hex.EncodeToString(sum[:]), "Proposed time received · "+proposalID)
	return true
}

func containsCalendarAttendee(attendees []parsedCalendarAttendee, email string) bool {
	for _, attendee := range attendees {
		if strings.EqualFold(attendee.Email, email) {
			return true
		}
	}
	return false
}

func calendarAttendeeName(attendees []parsedCalendarAttendee, email string) string {
	for _, attendee := range attendees {
		if strings.EqualFold(attendee.Email, email) {
			return attendee.Name
		}
	}
	return ""
}

func calendarPartStat(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "ACCEPTED":
		return "accepted"
	case "TENTATIVE":
		return "tentative"
	case "DECLINED":
		return "declined"
	case "NEEDS-ACTION", "":
		return "needs-action"
	}
	return ""
}
