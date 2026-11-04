package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"mime"
	"strings"
	"time"
)

// sendCalendarInvites sends one RFC 6047 iMIP message per attendee. Calendar
// mutations must never fail because a mailbox is offline, so delivery errors
// are logged and the in-app invitation remains available.
func (s *Server) sendCalendarInvites(ctx context.Context, organizer userRecord, eventID, method string) {
	events, err := s.loadCalendarEvents(ctx, organizer)
	if err != nil {
		return
	}
	for _, event := range events {
		if stateString(event, "id") == eventID {
			s.sendCalendarInvitePayload(ctx, organizer, event, method)
			return
		}
	}
}

// sendCalendarInvitePayload sends an invitation to every guest separately so
// each message can carry a signed RSVP link bound to that guest and sequence.
func (s *Server) sendCalendarInvitePayload(ctx context.Context, organizer userRecord, event map[string]any, method string) {
	attendees := stateSlice(event, "attendeeList")
	if len(attendees) == 0 {
		return
	}
	// A delegate may edit a shared calendar whose organizer is somebody else.
	// iMIP requires the From address and ORGANIZER property to describe the
	// same mailbox, so resolve the organizer before building any message.
	organizer = s.calendarOrganizerUser(ctx, organizer, event)
	if organizerEmail := strings.ToLower(strings.TrimSpace(stateString(event, "organizer"))); organizerEmail != "" &&
		!strings.EqualFold(organizerEmail, organizer.Email) {
		// Never send From a mailbox that does not own ORGANIZER. A mismatch is
		// the fastest way to make Gmail/Outlook hide RSVP controls or flag the
		// message as phishing.
		s.logger.Warn("calendar invite organizer is not a local mailbox",
			"event", stateString(event, "id"), "organizer", organizerEmail)
		return
	}
	sender := s.calendarSenderUser(ctx, organizer, event)
	senderHeaderName, senderHeaderEmail := "", ""
	if !strings.EqualFold(sender.Email, organizer.Email) {
		senderHeaderName, senderHeaderEmail = sender.Name, sender.Email
	}
	if stateString(event, "feedUrl") == "" {
		if feedURL := s.calendarFeedURL(ctx, event); feedURL != "" {
			event = deepCopyMap(event)
			event["feedUrl"] = feedURL
		}
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "REQUEST"
	}
	if stateString(event, "status") == "cancelled" && method == "REQUEST" {
		method = "CANCEL"
	}
	if method != "REQUEST" && method != "CANCEL" {
		return
	}
	ics := icsInviteEmail(event, method, s.cfg.PublicURL)
	if icsMethodValue(ics) != method {
		s.logger.Warn("calendar invite METHOD mismatch", "event", stateString(event, "id"), "method", method)
		return
	}
	sequence := int(stateInt(event, "sequence"))
	sent := 0
	for _, raw := range attendees {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(stateString(entry, "email")))
		if email == "" || email == strings.ToLower(organizer.Email) || stateBool(entry, "organizer") {
			continue
		}
		name := strings.TrimSpace(stateString(entry, "name"))
		subject, textBody, htmlBody := calendarInviteBodies(s, sender, organizer, event, entry, method)
		rawMessage, err := buildCalendarInviteMIME(organizer.Name, organizer.Email, organizer.Email, name, email, subject,
			textBody, htmlBody, ics, method,
			fmt.Sprintf("<%s@%s>", newID("calinvite"), s.cfg.MailDomain), senderHeaderName, senderHeaderEmail)
		if err != nil {
			s.logger.Warn("calendar invite MIME build failed", "err", err, "event", stateString(event, "id"))
			continue
		}
		if err := s.submitSMTPMessage(organizer.Email, []string{email}, rawMessage); err != nil {
			s.logger.Warn("calendar invite delivery failed", "err", err, "event", stateString(event, "id"), "attendee", email)
			_, _ = s.store.DB.Exec(ctx, `
				UPDATE calendar_attendees
				SET last_delivery_status='failed', last_delivery_at=now(), last_delivery_error=$3
				WHERE event_id=$1 AND lower(email)=lower($2)
			`, stateString(event, "id"), email, truncate(err.Error(), 300))
			continue
		}
		sent++
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE calendar_attendees
			SET last_invite_sequence=$3, last_invite_at=now(),
			    last_delivery_status='sent', last_delivery_at=now(), last_delivery_error=''
			WHERE event_id=$1 AND lower(email)=lower($2)
		`, stateString(event, "id"), email, sequence)
	}
	if sent > 0 {
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE calendar_events SET method_last_sent=$2, invite_sent_at=now() WHERE id=$1
		`, stateString(event, "id"), method)
		detail := fmt.Sprintf("Sent %d %s message(s) · sequence %d", sent, strings.ToLower(method), sequence)
		if !strings.EqualFold(sender.Email, organizer.Email) {
			detail += " · on behalf by " + sender.Email
		}
		s.insertAudit(organizer, "calendar.invite.sent", stateString(event, "title"), detail, "127.0.0.1", "calendar")
	}
}

func (s *Server) calendarFeedURL(ctx context.Context, event map[string]any) string {
	if event == nil {
		return ""
	}
	var sealedToken string
	err := s.store.DB.QueryRow(ctx, `
		SELECT p.token_enc
		FROM calendar_events e
		JOIN calendar_publications p ON p.calendar_id=e.calendar_id AND p.owner_user_id=e.user_id
		WHERE e.id=$1
	`, stateString(event, "id")).Scan(&sealedToken)
	if err != nil {
		return ""
	}
	token, err := s.openSecuritySecret(sealedToken)
	if err != nil || token == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/calendar/feed/" + token + ".ics"
}

func (s *Server) calendarOrganizerUser(ctx context.Context, fallback userRecord, event map[string]any) userRecord {
	email := strings.ToLower(strings.TrimSpace(stateString(event, "organizer")))
	if email == "" || strings.EqualFold(email, fallback.Email) {
		return fallback
	}
	return s.calendarUserByEmail(ctx, fallback.TenantID, email, fallback)
}

func (s *Server) calendarSenderUser(ctx context.Context, organizer userRecord, event map[string]any) userRecord {
	email := strings.ToLower(strings.TrimSpace(stateString(event, "senderEmail")))
	if email == "" || strings.EqualFold(email, organizer.Email) {
		return organizer
	}
	return s.calendarUserByEmail(ctx, organizer.TenantID, email, organizer)
}

func (s *Server) calendarUserByEmail(ctx context.Context, tenantID, email string, fallback userRecord) userRecord {
	var id string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT id::text FROM users
		WHERE tenant_id=$1 AND lower(email)=lower($2) AND deleted_at IS NULL AND status='active'
	`, tenantID, email).Scan(&id); err != nil {
		return fallback
	}
	user, err := s.userByID(ctx, id)
	if err != nil {
		return fallback
	}
	return user
}

// sendCalendarReplyEmail delivers a METHOD:REPLY to an external organizer.
// Local organizers already see the attendee row update in the database.
func (s *Server) sendCalendarReplyEmail(ctx context.Context, attendee userRecord, event map[string]any, status string) {
	organizer := strings.ToLower(strings.TrimSpace(stateString(event, "organizer")))
	if organizer == "" || !calendarEmailPattern.MatchString(organizer) ||
		strings.EqualFold(organizer, attendee.Email) ||
		strings.EqualFold(domainOf(organizer), domainOf(attendee.Email)) {
		return
	}
	partstat := map[string]string{"accepted": "ACCEPTED", "tentative": "TENTATIVE", "declined": "DECLINED"}[status]
	if partstat == "" {
		return
	}
	replyEvent := deepCopyMap(event)
	replyEvent["attendeeList"] = []any{map[string]any{
		"email": attendee.Email, "name": attendee.Name, "status": status,
		"organizer": false, "role": "REQ-PARTICIPANT",
	}}
	replyEvent["status"] = "confirmed"
	replyEvent["exceptions"] = nil
	ics := buildCalendarICS("Alt8Mail reply", []map[string]any{replyEvent}, "REPLY", s.cfg.PublicURL)
	title := stateString(event, "title")
	subject := map[string]string{
		"accepted": "Accepted: ", "tentative": "Tentative: ", "declined": "Declined: ",
	}[status] + title
	bodyText := attendee.Name + " (" + attendee.Email + ") responded " + status + " to “" + title + "”.\n"
	bodyHTML := `<p style="font-family:Arial,sans-serif;font-size:14px;color:#263445">` +
		html.EscapeString(attendee.Name+" ("+attendee.Email+") responded "+status+" to “"+title+"”.") + `</p>`
	raw, err := buildCalendarInviteMIME(attendee.Name, attendee.Email, attendee.Email, "", organizer, subject,
		bodyText, bodyHTML, ics, "REPLY", fmt.Sprintf("<%s@%s>", newID("calreply"), s.cfg.MailDomain), "", "")
	if err != nil {
		s.logger.Warn("calendar reply MIME build failed", "err", err, "event", stateString(event, "id"))
		return
	}
	if err := s.submitSMTPMessage(attendee.Email, []string{organizer}, raw); err != nil {
		s.logger.Warn("calendar reply delivery failed", "err", err, "event", stateString(event, "id"))
	}
}

func calendarInviteBodies(s *Server, sender, organizer userRecord, event, attendee map[string]any, method string) (string, string, string) {
	labels := calendarInviteStringsFor(sender)
	title := truncate(firstNonEmpty(stateString(event, "title"), "Untitled event"), 240)
	when := calendarInviteWhen(event, labels.Lang)
	where := stateString(event, "where")
	meeting := stateString(event, "meetingUrl")
	organizerLabel := firstNonEmpty(stateString(event, "organizerName"), organizer.Name)
	organizerEmail := firstNonEmpty(stateString(event, "organizer"), organizer.Email)
	senderLabel := firstNonEmpty(sender.Name, stateString(event, "senderName"))
	senderEmail := firstNonEmpty(sender.Email, stateString(event, "senderEmail"))
	onBehalf := senderEmail != "" && !strings.EqualFold(senderEmail, organizerEmail)
	sequence := int(stateInt(event, "sequence"))
	updated := sequence > 0
	feedURL := stateString(event, "feedUrl")
	rsvpURL := ""
	if method == "REQUEST" && stateBool(event, "responseRequested") {
		rsvpURL = s.calendarRSVPURL(event, stateString(attendee, "email"))
	}

	subjectPrefix := labels.SubjectInvitation
	switch {
	case method == "CANCEL":
		subjectPrefix = labels.SubjectCancelled
	case updated:
		subjectPrefix = labels.SubjectUpdated
	}
	subject := subjectPrefix + ": " + title + " @ " + calendarInviteSubjectDate(event, labels.Lang)

	var text strings.Builder
	if method == "CANCEL" {
		fmt.Fprintf(&text, "%s\n\n%s\n%s\n", labels.CancelledIntro, title, when)
		if where != "" {
			fmt.Fprintf(&text, "%s: %s\n", labels.WhereLabel, where)
		}
		fmt.Fprintf(&text, "%s: %s <%s>\n", labels.OrganizerLabel, organizerLabel, organizerEmail)
		if onBehalf {
			fmt.Fprintf(&text, "%s\n", fmt.Sprintf(labels.OnBehalf, senderLabel, organizerLabel))
		}
		fmt.Fprintf(&text, "\n%s\n", labels.NoAction)
		return subject, text.String(), calendarInviteCancelHTML(labels, title, when, where, organizerLabel, organizerEmail, onBehalf, senderLabel, senderEmail, feedURL)
	}
	if updated {
		fmt.Fprintf(&text, "%s\n\n", labels.UpdatedIntro)
	} else {
		fmt.Fprintf(&text, "%s\n\n", labels.InvitedIntro)
	}
	fmt.Fprintf(&text, "%s\n%s\n", title, when)
	if where != "" {
		fmt.Fprintf(&text, "%s: %s\n", labels.WhereLabel, where)
	}
	if meeting != "" {
		fmt.Fprintf(&text, "%s: %s\n", labels.JoinLabel, meeting)
	}
	fmt.Fprintf(&text, "%s: %s <%s>\n", labels.OrganizerLabel, organizerLabel, organizerEmail)
	if onBehalf {
		fmt.Fprintf(&text, "%s\n", fmt.Sprintf(labels.OnBehalf, senderLabel, organizerLabel))
	}
	if description := truncate(stateString(event, "description"), 2000); description != "" {
		fmt.Fprintf(&text, "\n%s\n", description)
	}
	if rsvpURL != "" {
		fmt.Fprintf(&text, "\n%s: %s?response=accepted\n%s: %s?response=tentative\n%s: %s?response=declined\n",
			labels.YesButton, rsvpURL, labels.MaybeButton, rsvpURL, labels.NoButton, rsvpURL)
	} else {
		fmt.Fprintf(&text, "\n%s\n", labels.OpenAttached)
	}
	if feedURL != "" {
		fmt.Fprintf(&text, "\n%s: %s\n", labels.FeedLabel, feedURL)
	}
	return subject, text.String(), calendarInviteRequestHTML(labels, event, organizerLabel, organizerEmail,
		onBehalf, senderLabel, senderEmail, when, where, meeting, rsvpURL, feedURL, updated)
}

func calendarInviteWhen(event map[string]any, language string) string {
	zone := calendarEventLocation(event)
	labels := calendarInviteCatalog[language]
	if labels.Lang == "" {
		labels = calendarInviteCatalog["en"]
	}
	starts, err := time.Parse(time.RFC3339, stateString(event, "startsAt"))
	if err != nil {
		if value := strings.TrimSpace(stateString(event, "date") + " " + stateString(event, "start")); value != " " {
			return value
		}
		return labels.TimeToBeConfirmed
	}
	ends, err := time.Parse(time.RFC3339, stateString(event, "endsAt"))
	if err != nil {
		ends = starts.Add(time.Hour)
	}
	localStart := starts.In(zone)
	localEnd := ends.In(zone)
	if language == "en" || language == "" {
		if stateBool(event, "allDay") {
			return localStart.Format("Monday, January 2, 2006") + " (" + labels.AllDay + ")"
		}
		if localStart.Format("2006-01-02") == localEnd.Format("2006-01-02") {
			return localStart.Format("Monday, January 2, 2006, 3:04 PM") + " – " + localEnd.Format("3:04 PM") +
				" (" + calendarEventZoneName(event) + ")"
		}
		return localStart.Format("Monday, January 2, 2006, 3:04 PM") + " – " +
			localEnd.Format("Monday, January 2, 2006, 3:04 PM") + " (" + calendarEventZoneName(event) + ")"
	}
	if stateBool(event, "allDay") {
		return localStart.Format("02.01.2006") + " (" + labels.AllDay + ")"
	}
	if localStart.Format("2006-01-02") == localEnd.Format("2006-01-02") {
		return localStart.Format("02.01.2006 · 15:04") + " – " + localEnd.Format("15:04") +
			" (" + calendarEventZoneName(event) + ")"
	}
	return localStart.Format("02.01.2006 · 15:04") + " – " +
		localEnd.Format("02.01.2006 · 15:04") + " (" + calendarEventZoneName(event) + ")"
}

func calendarInviteSubjectDate(event map[string]any, language string) string {
	zone := calendarEventLocation(event)
	starts, err := time.Parse(time.RFC3339, stateString(event, "startsAt"))
	if err != nil {
		return stateString(event, "date")
	}
	local := starts.In(zone)
	if language == "en" || language == "" {
		return local.Format("Jan 2, 2006 · 3:04 PM")
	}
	return local.Format("02.01.2006 · 15:04")
}

func calendarInviteRequestHTML(labels calendarInviteStrings, event map[string]any, organizerLabel, organizerEmail string,
	onBehalf bool, senderLabel, senderEmail, when, where, meeting, rsvpURL, feedURL string, updated bool) string {
	title := html.EscapeString(truncate(firstNonEmpty(stateString(event, "title"), "Untitled event"), 240))
	color := stateString(event, "color")
	if !calendarHexPattern.MatchString(color) {
		color = "#0f6cbd"
	}
	var detail strings.Builder
	detail.WriteString(calendarInviteDetailRow(labels.WhenLabel, when))
	if where != "" {
		detail.WriteString(calendarInviteDetailRow(labels.WhereLabel, where))
	}
	detail.WriteString(calendarInviteDetailRow(labels.OrganizerLabel, organizerLabel+" · "+organizerEmail))
	if meeting != "" {
		detail.WriteString(calendarInviteDetailRow(labels.JoinLabel, meeting))
	}
	if onBehalf {
		detail.WriteString(calendarInviteDetailRow(labels.SentBy, senderLabel+" · "+senderEmail))
	}
	description := truncate(stateString(event, "description"), 2000)
	descriptionHTML := ""
	if description != "" {
		descriptionHTML = `<div style="margin:18px 0 0;padding-top:18px;border-top:1px solid #e5eaf2;color:#3e4c5f;font-size:14px;line-height:1.65;white-space:pre-wrap">` + html.EscapeString(description) + `</div>`
	}
	buttons := ""
	note := ""
	if updated {
		note = `<p style="margin:0 0 16px;color:#7a5a16;background:#fff8e6;border:1px solid #f0dfb0;border-radius:10px;padding:11px 13px;font-size:13px;line-height:1.55">` + html.EscapeString(labels.UpdatedNotice) + `</p>`
	}
	if rsvpURL != "" {
		escaped := html.EscapeString(rsvpURL)
		button := func(response, label, background string) string {
			return `<a href="` + escaped + `?response=` + response + `" style="display:inline-block;margin:0 8px 8px 0;padding:12px 18px;border-radius:9px;background:` + background + `;color:#ffffff;text-decoration:none;font-size:14px;font-weight:600">` + html.EscapeString(label) + `</a>`
		}
		buttons = `<div style="margin:22px 0 6px">` +
			button("accepted", labels.YesButton, color) +
			button("tentative", labels.MaybeButton, "#5b6675") +
			button("declined", labels.NoButton, "#a94452") +
			`</div><p style="margin:8px 0 0;color:#6a7686;font-size:12px;line-height:1.5">` + html.EscapeString(labels.RsvpHint) + `</p>`
	}
	feedHTML := ""
	if feedURL != "" {
		feedHTML = `<p style="margin:14px 0 0;font-size:12px;line-height:1.55"><a href="` + html.EscapeString(feedURL) + `" style="color:#3d6b9a;text-decoration:none">` + html.EscapeString(labels.FeedLabel) + `</a></p>`
	}
	return `<!doctype html><html lang="` + labels.Lang + `" dir="` + labels.Dir + `"><body style="margin:0;padding:0;background:#f2f5fa;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#17212f;direction:` + labels.Dir + `">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f2f5fa;padding:28px 12px"><tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:620px;background:#ffffff;border:1px solid #dfe6f0;border-radius:16px;overflow:hidden">
<tr><td style="height:5px;background:` + color + `"></td></tr>
<tr><td style="padding:30px 32px 28px">
<p style="margin:0 0 8px;color:` + color + `;font-size:11px;font-weight:700;letter-spacing:.11em;text-transform:uppercase">` + html.EscapeString(labels.Eyebrow) + `</p>
<h1 style="margin:0 0 8px;font-size:24px;line-height:1.3;color:#17212f">` + title + `</h1>
` + note + `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:16px 0 0">` + detail.String() + `</table>` + descriptionHTML + buttons + feedHTML + `
<div style="margin-top:24px;padding-top:16px;border-top:1px solid #e5eaf2;color:#7a8797;font-size:12px;line-height:1.55">` + html.EscapeString(labels.AttachedNote) + `</div>
<div style="margin-top:8px;color:#9aa6b5;font-size:11px">` + html.EscapeString(labels.Footer) + `</div>
</td></tr></table></td></tr></table></body></html>`
}

func calendarInviteDetailRow(label, value string) string {
	return `<tr><td valign="top" style="padding:4px 14px 8px 0;color:#7a8797;font-size:13px;white-space:nowrap">` + html.EscapeString(label) +
		`</td><td style="padding:4px 0 8px;color:#263445;font-size:14px;line-height:1.45">` + html.EscapeString(value) + `</td></tr>`
}

func calendarInviteCancelHTML(labels calendarInviteStrings, title, when, where, organizerLabel, organizerEmail string,
	onBehalf bool, senderLabel, senderEmail, feedURL string) string {
	var detail strings.Builder
	detail.WriteString(calendarInviteDetailRow(labels.WhenLabel, when))
	if where != "" {
		detail.WriteString(calendarInviteDetailRow(labels.WhereLabel, where))
	}
	detail.WriteString(calendarInviteDetailRow(labels.OrganizerLabel, organizerLabel+" · "+organizerEmail))
	if onBehalf {
		detail.WriteString(calendarInviteDetailRow(labels.SentBy, senderLabel+" · "+senderEmail))
	}
	feedHTML := ""
	if feedURL != "" {
		feedHTML = `<p style="margin:14px 0 0;font-size:12px"><a href="` + html.EscapeString(feedURL) + `" style="color:#3d6b9a;text-decoration:none">` + html.EscapeString(labels.FeedLabel) + `</a></p>`
	}
	return `<!doctype html><html lang="` + labels.Lang + `" dir="` + labels.Dir + `"><body style="margin:0;padding:0;background:#f2f5fa;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;direction:` + labels.Dir + `">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f2f5fa;padding:28px 12px"><tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:620px;background:#ffffff;border:1px solid #dfe6f0;border-radius:16px;overflow:hidden">
<tr><td style="height:5px;background:#8a3946"></td></tr>
<tr><td style="padding:30px 32px 28px">
<p style="margin:0 0 8px;color:#8a3946;font-size:11px;font-weight:700;letter-spacing:.11em;text-transform:uppercase">` + html.EscapeString(labels.CancelledHeading) + `</p>
<h1 style="margin:0 0 8px;font-size:24px;line-height:1.3">` + html.EscapeString(title) + `</h1>
<p style="margin:0 0 18px;color:#526071;font-size:15px;line-height:1.55">` + html.EscapeString(labels.CancelledIntro) + `</p>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0">` + detail.String() + `</table>` + feedHTML + `
<div style="margin-top:24px;padding-top:16px;border-top:1px solid #e5eaf2;color:#7a8797;font-size:12px;line-height:1.55">` + html.EscapeString(labels.CancelledIntro) + `</div>
<div style="margin-top:8px;color:#9aa6b5;font-size:11px">` + html.EscapeString(labels.Footer) + `</div>
</td></tr></table></td></tr></table></body></html>`
}

func icsMethodValue(body string) string {
	for _, line := range unfoldICS([]byte(body)) {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "METHOD:") {
			return strings.ToUpper(strings.TrimSpace(line[len("METHOD:"):]))
		}
	}
	return ""
}

// buildCalendarInviteMIME emits multipart/mixed with an alternative text+HTML
// branch and one text/calendar body part. The method parameter on the MIME
// part always matches METHOD inside the iCalendar object.
func buildCalendarInviteMIME(fromName, fromEmail, replyTo, toName, toEmail, subject, bodyText, bodyHTML, ics, method, messageID, senderHeaderName, senderHeaderEmail string) ([]byte, error) {
	if strings.ToUpper(icsMethodValue(ics)) != strings.ToUpper(method) {
		return nil, fmt.Errorf("calendar MIME method does not match iCalendar METHOD")
	}
	var buffer bytes.Buffer
	writeHeaderLine(&buffer, "From", formatMailbox(fromName, fromEmail))
	if strings.TrimSpace(replyTo) != "" && !strings.EqualFold(replyTo, fromEmail) {
		writeHeaderLine(&buffer, "Reply-To", formatMailbox("", replyTo))
	}
	if strings.TrimSpace(senderHeaderEmail) != "" && !strings.EqualFold(senderHeaderEmail, fromEmail) {
		writeHeaderLine(&buffer, "Sender", formatMailbox(senderHeaderName, senderHeaderEmail))
	}
	writeHeaderLine(&buffer, "To", formatMailbox(toName, toEmail))
	writeHeaderLine(&buffer, "Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeaderLine(&buffer, "Date", time.Now().Format(time.RFC1123Z))
	writeHeaderLine(&buffer, "Message-ID", messageID)
	writeHeaderLine(&buffer, "MIME-Version", "1.0")
	writeHeaderLine(&buffer, "Auto-Submitted", "auto-generated")
	writeHeaderLine(&buffer, "Content-Class", "urn:content-classes:calendarmessage")
	mixedBoundary := "mix_" + randomToken(12)
	altBoundary := "alt_" + randomToken(12)
	writeHeaderLine(&buffer, "Content-Type", `multipart/mixed; boundary="`+mixedBoundary+`"`)
	buffer.WriteString("\r\n")
	buffer.WriteString("--" + mixedBoundary + "\r\n")
	writeAlternativePart(&buffer, altBoundary, bodyText, bodyHTML)
	buffer.WriteString("--" + mixedBoundary + "\r\n")
	calendarType := mime.FormatMediaType("text/calendar", map[string]string{
		"charset": "UTF-8", "method": strings.ToUpper(method), "name": "invite.ics",
	})
	writeHeaderLine(&buffer, "Content-Type", calendarType)
	writeHeaderLine(&buffer, "Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "invite.ics"}))
	writeHeaderLine(&buffer, "Content-Transfer-Encoding", "base64")
	buffer.WriteString("\r\n")
	writeBase64Lines(&buffer, []byte(ics))
	buffer.WriteString("\r\n")
	buffer.WriteString("--" + mixedBoundary + "--\r\n")
	return buffer.Bytes(), nil
}
