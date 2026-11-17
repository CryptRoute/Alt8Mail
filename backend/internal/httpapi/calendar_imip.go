package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// processInboundCalendarReplies applies RFC 6047 attendee responses to the
// organizer's event state. It understands METHOD:REPLY, METHOD:COUNTER and the
// REQUEST-shaped counter-proposals some clients (notably Google Calendar) send
// when an attendee proposes another time. A proposal is only accepted when the
// sender is an attendee on the event, the UID belongs to an event organized by
// the local user and the proposed time differs from the current one.
func (s *Server) localMailIdentity(ctx context.Context, user userRecord, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	if strings.EqualFold(email, user.Email) {
		return true
	}
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.mail_accounts
			WHERE user_id=$1 AND kind='external' AND status <> 'disabled'
			  AND lower(email::text)=lower($2)
		)
	`, user.ID, email).Scan(&exists)
	return exists
}

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
			if method != "REPLY" && method != "COUNTER" && method != "REQUEST" {
				continue
			}
			replies, err := parseICSReplies(part.Data)
			if err != nil {
				continue
			}
			for _, reply := range replies {
				if method == "REPLY" {
					if s.applyCalendarReply(ctx, user, sender, message, part.Data, reply) {
						applied++
					}
					continue
				}
				if s.applyCalendarCounter(ctx, user, sender, message, part.Data, reply, method) {
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
		!s.localMailIdentity(ctx, organizer, organizerEmail) {
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
	var eventID, eventOrganizer, title, tenantID, eventTimezone string
	var eventSequence, lastResponseSequence int
	var eventStartsAt, eventEndsAt time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT e.id, e.sequence, e.organizer_email::text, e.title, e.tenant_id::text,
		       e.starts_at, e.ends_at, e.timezone, a.last_response_sequence
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE lower(e.uid)=lower($1) AND lower(a.email)=lower($2) AND e.tenant_id=$3
		LIMIT 1
	`, uid, sender, organizer.TenantID).Scan(&eventID, &eventSequence, &eventOrganizer, &title, &tenantID,
		&eventStartsAt, &eventEndsAt, &eventTimezone, &lastResponseSequence)
	if err != nil {
		return false
	}
	if !s.localMailIdentity(ctx, organizer, eventOrganizer) {
		return false
	}
	// A reply must answer the invitation currently on record. Google and some
	// desktop clients echo an older SEQUENCE, so clamp the reply to the event's
	// sequence instead of rejecting an otherwise valid response. A sender
	// cannot pin a future sequence and lock out later legitimate replies.
	effectiveSequence := reply.Sequence
	if effectiveSequence > eventSequence {
		effectiveSequence = eventSequence
	}
	stale := effectiveSequence < lastResponseSequence
	dedupSource := firstNonEmpty(strings.TrimSpace(message.MessageID), string(ics))
	if dedupSource == "" {
		dedupSource = fmt.Sprintf("%s\x00%s\x00%d\x00%s", uid, sender, effectiveSequence, partstat)
	}
	sum := sha256.Sum256([]byte(dedupSource + "\x00" + uid + "\x00" + sender + "\x00" + fmt.Sprint(effectiveSequence) + "\x00" + partstat))
	dedupKey := "imip:" + hex.EncodeToString(sum[:])
	var replyID int64
	err = s.store.DB.QueryRow(ctx, `
		INSERT INTO calendar_invite_replies
		(event_id, tenant_id, event_uid, attendee_email, sequence, partstat, method,
		 message_id, dedup_key, source, applied, stale, detail)
		VALUES ($1,$2,$3,$4,$5,$6,'REPLY',$7,$8,'imip',$9,$10,$11)
		ON CONFLICT (event_id, dedup_key) DO NOTHING
		RETURNING id
	`, eventID, tenantID, uid, sender, effectiveSequence, strings.ToUpper(partstat),
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
		WHERE a.event_id=e.id AND e.id=$3 AND e.sequence=$4
		  AND lower(a.email)=lower($5) AND a.last_response_sequence <= $2
	`, partstat, effectiveSequence, eventID, eventSequence, sender)
	if err != nil || result.RowsAffected() == 0 {
		return false
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE calendar_invite_replies SET applied=true, stale=false WHERE id=$1`, replyID)
	s.insertAudit(organizer, "calendar.rsvp-received", title,
		fmt.Sprintf("%s replied %s · sequence %d · iMIP", sender, partstat, effectiveSequence),
		"127.0.0.1", "iMIP")
	senderName := firstNonEmpty(calendarAttendeeName(reply.AttendeeDetails, sender), sender)
	responseLabel := map[string]string{"accepted": "Yes", "tentative": "Maybe", "declined": "No"}[partstat]
	if responseLabel == "" {
		responseLabel = partstat
	}
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.mail_notifications
		  (id, tenant_id, user_id, kind, title, body, detail, actor_user_id, actor_name, actor_email,
		   thread_id, message_id, owner_user_id)
		VALUES ($1,$2,$3,'calendar',$4,$5,$6,NULL,$7,$8,$9,'',$3)
	`, newID("notification"), tenantID, organizer.ID,
		senderName+" responded "+responseLabel, title,
		calendarInviteWhen(map[string]any{
			"startsAt": eventStartsAt.UTC().Format(time.RFC3339),
			"endsAt":   eventEndsAt.UTC().Format(time.RFC3339),
			"timezone": eventTimezone, "title": title,
		}, "en"), senderName, sender, eventID)
	s.publishRealtimeTo(organizer.ID, "calendar")
	return true
}

func (s *Server) applyCalendarCounter(ctx context.Context, organizer userRecord, sender string, message fetchedMail, ics []byte, counter parsedCalendarEvent, method string) bool {
	uid := strings.TrimSpace(counter.UID)
	if uid == "" || counter.StartsAt.IsZero() || counter.EndsAt.IsZero() || !counter.EndsAt.After(counter.StartsAt) {
		return false
	}
	if organizerEmail := strings.ToLower(strings.TrimSpace(counter.Organizer)); organizerEmail != "" &&
		!s.localMailIdentity(ctx, organizer, organizerEmail) {
		return false
	}
	var eventID, eventOrganizer, eventTimezone, title, tenantID string
	var eventSequence int
	var eventStartsAt, eventEndsAt time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT e.id, e.sequence, e.organizer_email::text, e.timezone, e.title, e.tenant_id::text,
		       e.starts_at, e.ends_at
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE lower(e.uid)=lower($1) AND lower(a.email)=lower($2) AND e.tenant_id=$3
		LIMIT 1
	`, uid, sender, organizer.TenantID).Scan(&eventID, &eventSequence, &eventOrganizer, &eventTimezone, &title, &tenantID, &eventStartsAt, &eventEndsAt)
	if err != nil || !s.localMailIdentity(ctx, organizer, eventOrganizer) {
		return false
	}
	// The organizer's own address can also appear as an attendee. An update
	// sent from that mailbox is not a guest proposal.
	if strings.EqualFold(eventOrganizer, sender) {
		return false
	}
	// Google Calendar has shipped proposals as METHOD:REQUEST as well as
	// METHOD:COUNTER. A REQUEST only becomes a proposal when its time differs
	// from the event on record; otherwise it is an echo of the invitation.
	if strings.EqualFold(method, "REQUEST") && sameCalendarMoment(counter.StartsAt, eventStartsAt) &&
		sameCalendarMoment(counter.EndsAt, eventEndsAt) {
		return false
	}
	timezone := firstNonEmpty(counter.Timezone, eventTimezone, "UTC")
	sum := sha256.Sum256([]byte(firstNonEmpty(message.MessageID, string(ics)) + "\x00" + uid + "\x00" + sender + "\x00" + method))
	dedupKey := "counter:" + hex.EncodeToString(sum[:])
	var alreadyRecorded int
	if err := s.store.DB.QueryRow(ctx, `
		SELECT 1 FROM calendar_invite_replies WHERE event_id=$1 AND dedup_key=$2 LIMIT 1
	`, eventID, dedupKey).Scan(&alreadyRecorded); err == nil {
		return false
	}
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
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO calendar_invite_replies
		(event_id, tenant_id, event_uid, attendee_email, sequence, partstat, method, message_id,
		 dedup_key, source, applied, detail)
		VALUES ($1,$2,$3,$4,$5,'TENTATIVE',$6,$7,$8,'imip',true,$9)
		ON CONFLICT (event_id, dedup_key) DO NOTHING
	`, eventID, tenantID, uid, sender, eventSequence, method, truncate(message.MessageID, 400),
		dedupKey, "Proposed time received · "+proposalID)
	return true
}

func sameCalendarMoment(left, right time.Time) bool {
	if left.IsZero() || right.IsZero() {
		return false
	}
	delta := left.Sub(right)
	return delta > -time.Minute && delta < time.Minute
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
