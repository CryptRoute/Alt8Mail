package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"
)

const calendarPollTTL = 90 * 24 * time.Hour

type calendarPollClaims struct {
	PollID  string `json:"p"`
	Email   string `json:"e"`
	Expires int64  `json:"x"`
}

type calendarPollSlot struct {
	ID       string
	StartsAt time.Time
	EndsAt   time.Time
	Position int
	Yes      int
	Maybe    int
	No       int
}

type calendarPoll struct {
	ID             string
	Title          string
	Description    string
	Timezone       string
	Status         string
	SelectedSlotID string
	EventID        string
	CreatedAt      time.Time
	ClosesAt       *time.Time
	Slots          []calendarPollSlot
	Invitees       []calendarPollInvitee
}

type calendarPollInvitee struct {
	Email       string
	Name        string
	RespondedAt string
}

func (s *Server) calendarPollKey() []byte {
	sum := sha256.Sum256([]byte("alt8mail-calendar-poll\x00" + s.cfg.SessionSecret + "\x00" + s.cfg.CSRFSecret))
	return sum[:]
}

func (s *Server) signCalendarPollToken(claims calendarPollClaims) string {
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.calendarPollKey())
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) parseCalendarPollToken(token string) (calendarPollClaims, error) {
	var claims calendarPollClaims
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return claims, fmt.Errorf("invalid poll link")
	}
	mac := hmac.New(sha256.New, s.calendarPollKey())
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || subtle.ConstantTimeCompare(expected, actual) != 1 ||
		base64.RawURLEncoding.EncodeToString(actual) != parts[1] {
		return claims, fmt.Errorf("invalid poll link")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return claims, fmt.Errorf("invalid poll link")
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.PollID == "" || claims.Email == "" {
		return claims, fmt.Errorf("invalid poll link")
	}
	if time.Now().Unix() > claims.Expires {
		return claims, fmt.Errorf("this poll link has expired")
	}
	return claims, nil
}

func (s *Server) calendarPollURL(pollID, email string) string {
	token := s.signCalendarPollToken(calendarPollClaims{
		PollID: pollID, Email: strings.ToLower(email), Expires: time.Now().Add(calendarPollTTL).Unix(),
	})
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/calendar/poll/" + token
}

type calendarPollBody struct {
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Timezone      string            `json:"timezone"`
	InviteeEmails []string          `json:"inviteeEmails"`
	InviteeNames  map[string]string `json:"inviteeNames"`
	Slots         []struct {
		Date  string `json:"date"`
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"slots"`
}

func (s *Server) createCalendarPoll(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body calendarPollBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || len([]rune(title)) > 240 {
		writeError(w, http.StatusBadRequest, "POLL_TITLE", "Give the poll a title")
		return
	}
	timezone := firstNonEmpty(strings.TrimSpace(body.Timezone), "UTC")
	location, err := time.LoadLocation(timezone)
	if err != nil {
		writeError(w, http.StatusBadRequest, "POLL_TIMEZONE", "Unknown poll time zone")
		return
	}
	timezone = location.String()
	invitees := []string{}
	names := map[string]string{}
	seen := map[string]bool{}
	for _, raw := range body.InviteeEmails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if !calendarEmailPattern.MatchString(email) {
			writeError(w, http.StatusBadRequest, "POLL_INVITEE", "One of the invitee addresses is invalid")
			return
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		invitees = append(invitees, email)
		names[email] = strings.TrimSpace(body.InviteeNames[email])
	}
	if len(invitees) == 0 || len(invitees) > 50 {
		writeError(w, http.StatusBadRequest, "POLL_INVITEES", "Invite between 1 and 50 people")
		return
	}
	if len(body.Slots) == 0 || len(body.Slots) > 20 {
		writeError(w, http.StatusBadRequest, "POLL_SLOTS", "Add between 1 and 20 time options")
		return
	}
	type slotInput struct {
		start, end          time.Time
		date, begin, finish string
	}
	slots := []slotInput{}
	seenSlots := map[string]bool{}
	for _, raw := range body.Slots {
		start, end, parseErr := parseProposalTimes(raw.Date, raw.Start, raw.End, timezone)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "POLL_SLOT", "One of the poll time options is invalid")
			return
		}
		key := start.UTC().Format(time.RFC3339) + "/" + end.UTC().Format(time.RFC3339)
		if seenSlots[key] {
			continue
		}
		seenSlots[key] = true
		slots = append(slots, slotInput{start: start, end: end,
			date:  start.In(location).Format("2006-01-02"),
			begin: start.In(location).Format("15:04"), finish: end.In(location).Format("15:04")})
	}
	pollID := newID("poll")
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "POLL", "Could not start the poll")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO calendar_polls (id, tenant_id, user_id, title, description, timezone, status)
		VALUES ($1,$2,$3,$4,$5,$6,'open')
	`, pollID, sess.User.TenantID, sess.User.ID, title, truncate(body.Description, 5000), timezone); err != nil {
		writeError(w, http.StatusInternalServerError, "POLL", "Could not create the poll")
		return
	}
	for position, slot := range slots {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO calendar_poll_slots (id, poll_id, starts_at, ends_at, position)
			VALUES ($1,$2,$3,$4,$5)
		`, newID("pollslot"), pollID, slot.start.UTC(), slot.end.UTC(), position); err != nil {
			writeError(w, http.StatusInternalServerError, "POLL", "Could not create the poll slots")
			return
		}
	}
	for _, email := range invitees {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO calendar_poll_invitees (poll_id, email, name) VALUES ($1,$2,$3)
		`, pollID, email, names[email]); err != nil {
			writeError(w, http.StatusInternalServerError, "POLL", "Could not add the poll invitees")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "POLL", "Could not commit the poll")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.poll.created", title,
		fmt.Sprintf("%d time option(s) · %d invitee(s)", len(slots), len(invitees)))
	poll, err := s.loadCalendarPoll(r.Context(), sess.User.ID, pollID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "POLL", "The poll was created but could not be loaded")
		return
	}
	organizer := sess.User
	go s.sendCalendarPollEmails(context.Background(), organizer, poll)
	writeJSON(w, http.StatusCreated, map[string]any{"poll": publicCalendarPoll(poll)})
}

func (s *Server) getCalendarPolls(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	polls, err := s.loadCalendarPolls(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "POLL", "Could not load meeting polls")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"polls": polls, "count": len(polls)})
}

func (s *Server) loadCalendarPolls(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id FROM calendar_polls WHERE user_id=$1 AND status <> 'cancelled'
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []map[string]any{}
	for _, id := range ids {
		poll, err := s.loadCalendarPoll(ctx, userID, id)
		if err == nil {
			out = append(out, publicCalendarPoll(poll))
		}
	}
	return out, nil
}

func (s *Server) loadCalendarPoll(ctx context.Context, userID, pollID string) (calendarPoll, error) {
	var poll calendarPoll
	var closes *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, title, description, timezone, status, COALESCE(selected_slot_id,''),
		       COALESCE(event_id,''), created_at, closes_at
		FROM calendar_polls WHERE id=$1 AND user_id=$2
	`, pollID, userID).Scan(&poll.ID, &poll.Title, &poll.Description, &poll.Timezone,
		&poll.Status, &poll.SelectedSlotID, &poll.EventID, &poll.CreatedAt, &closes)
	if err != nil {
		return poll, err
	}
	poll.ClosesAt = closes
	rows, err := s.store.DB.Query(ctx, `
		SELECT s.id, s.starts_at, s.ends_at, s.position,
		       count(*) FILTER (WHERE r.response='yes'),
		       count(*) FILTER (WHERE r.response='maybe'),
		       count(*) FILTER (WHERE r.response='no')
		FROM calendar_poll_slots s
		LEFT JOIN calendar_poll_responses r ON r.slot_id=s.id AND r.poll_id=s.poll_id
		WHERE s.poll_id=$1 GROUP BY s.id ORDER BY s.position
	`, pollID)
	if err != nil {
		return poll, err
	}
	for rows.Next() {
		var slot calendarPollSlot
		if rows.Scan(&slot.ID, &slot.StartsAt, &slot.EndsAt, &slot.Position, &slot.Yes, &slot.Maybe, &slot.No) == nil {
			poll.Slots = append(poll.Slots, slot)
		}
	}
	rows.Close()
	rows, err = s.store.DB.Query(ctx, `
		SELECT i.email::text, i.name,
		       COALESCE(to_char(max(r.responded_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
		FROM calendar_poll_invitees i
		LEFT JOIN calendar_poll_responses r ON r.poll_id=i.poll_id AND lower(r.invitee_email)=lower(i.email)
		WHERE i.poll_id=$1 GROUP BY i.email, i.name ORDER BY i.name, i.email
	`, pollID)
	if err != nil {
		return poll, err
	}
	for rows.Next() {
		var invitee calendarPollInvitee
		if rows.Scan(&invitee.Email, &invitee.Name, &invitee.RespondedAt) == nil {
			poll.Invitees = append(poll.Invitees, invitee)
		}
	}
	rows.Close()
	return poll, nil
}

func publicCalendarPoll(poll calendarPoll) map[string]any {
	location := mustLocation(poll.Timezone)
	slots := make([]any, 0, len(poll.Slots))
	for _, slot := range poll.Slots {
		localStart := slot.StartsAt.In(location)
		localEnd := slot.EndsAt.In(location)
		slots = append(slots, map[string]any{
			"id": slot.ID, "date": localStart.Format("2006-01-02"), "start": localStart.Format("15:04"),
			"end": localEnd.Format("15:04"), "startsAt": slot.StartsAt.UTC().Format(time.RFC3339),
			"endsAt": slot.EndsAt.UTC().Format(time.RFC3339), "position": slot.Position,
			"yes": slot.Yes, "maybe": slot.Maybe, "no": slot.No,
		})
	}
	invitees := make([]any, 0, len(poll.Invitees))
	for _, invitee := range poll.Invitees {
		invitees = append(invitees, map[string]any{
			"email": invitee.Email, "name": invitee.Name, "respondedAt": invitee.RespondedAt,
		})
	}
	out := map[string]any{
		"id": poll.ID, "title": poll.Title, "description": poll.Description,
		"timezone": poll.Timezone, "status": poll.Status, "selectedSlotId": poll.SelectedSlotID,
		"eventId": poll.EventID, "createdAt": poll.CreatedAt.UTC().Format(time.RFC3339),
		"slots": slots, "invitees": invitees,
	}
	if poll.ClosesAt != nil {
		out["closesAt"] = poll.ClosesAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Server) closeCalendarPoll(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE calendar_polls SET status='closed', updated_at=now() WHERE id=$1 AND user_id=$2 AND status='open'
	`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "POLL_NOT_FOUND", "Open poll not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.poll.closed", id, "Meeting poll closed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteCalendarPoll(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE calendar_polls SET status='cancelled', updated_at=now() WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID)
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.poll.deleted", id, "Meeting poll cancelled")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) scheduleCalendarPoll(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body struct {
		SlotID string `json:"slotId"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	poll, err := s.loadCalendarPoll(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "POLL_NOT_FOUND", "Meeting poll not found")
		return
	}
	if poll.Status == "scheduled" {
		writeError(w, http.StatusConflict, "POLL_DONE", "This poll already has a scheduled event")
		return
	}
	if poll.Status != "open" && poll.Status != "closed" {
		writeError(w, http.StatusConflict, "POLL_DONE", "This poll cannot be scheduled")
		return
	}
	var slot calendarPollSlot
	found := false
	for _, candidate := range poll.Slots {
		if candidate.ID == body.SlotID {
			slot, found = candidate, true
			break
		}
	}
	if !found {
		writeError(w, http.StatusBadRequest, "POLL_SLOT", "Choose a time option")
		return
	}
	eventID, err := s.createEventFromPoll(r.Context(), sess, poll, slot)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.poll.scheduled", poll.Title,
		"Meeting poll scheduled as an event")
	if err := s.queueCalendarInvites(context.Background(), sess.User, eventID, "REQUEST"); err != nil {
		go s.sendCalendarInvites(context.Background(), sess.User, eventID, "REQUEST")
	}
	event, _ := s.loadCalendarEvent(r.Context(), sess.User, eventID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "eventId": eventID, "event": event})
}

func (s *Server) createEventFromPoll(ctx context.Context, sess *sessionRecord, poll calendarPoll, slot calendarPollSlot) (string, error) {
	if err := s.ensureDefaultCalendar(ctx, sess.User); err != nil {
		return "", err
	}
	eventID := newID("cal")
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	title := poll.Title
	description := poll.Description
	organizerName := sess.User.Name
	_, err = tx.Exec(ctx, `
		INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, location,
		                            starts_at, ends_at, all_day, timezone, status, visibility, recurrence_rule,
		                            organizer_email, organizer_name, etag, resource_name, metadata,
		                            sequence, transparency, reminder_minutes, attachments, meeting_url,
		                            response_requested, sender_email, sender_name)
		VALUES ($1,$2,$3,'default',$4,$5,$6,'',$7,$8,false,$9,'confirmed','default','',$10,$11,$12,$13,
		        '{"color":"#0f6cbd"}',0,'busy',15,'[]','',true,'','')
	`, eventID, sess.User.TenantID, sess.User.ID, eventID, title, description,
		slot.StartsAt.UTC(), slot.EndsAt.UTC(), poll.Timezone, sess.User.Email, organizerName,
		newID("etag"), eventID+".ics")
	if err == nil {
		emails := make([]string, 0, len(poll.Invitees))
		names := map[string]string{}
		for _, invitee := range poll.Invitees {
			emails = append(emails, invitee.Email)
			names[invitee.Email] = invitee.Name
		}
		err = s.replaceCalendarAttendees(ctx, tx, eventID, emails, names, sess.User, sess.User.Email, false)
	}
	if err != nil {
		return "", fail(http.StatusInternalServerError, "POLL", "Could not create the poll event")
	}
	result, err := tx.Exec(ctx, `
		UPDATE calendar_polls SET status='scheduled', selected_slot_id=$2, event_id=$3, updated_at=now()
		WHERE id=$1 AND user_id=$4 AND status IN ('open','closed')
	`, poll.ID, slot.ID, eventID, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		return "", fail(http.StatusConflict, "POLL_DONE", "This poll has already been scheduled")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fail(http.StatusInternalServerError, "POLL", "Could not commit the poll event")
	}
	return eventID, nil
}

func (s *Server) sendCalendarPollEmails(ctx context.Context, organizer userRecord, poll calendarPoll) {
	location := mustLocation(poll.Timezone)
	for _, invitee := range poll.Invitees {
		link := s.calendarPollURL(poll.ID, invitee.Email)
		var text strings.Builder
		fmt.Fprintf(&text, "You are invited to help find a time for %s.\n\n", poll.Title)
		if poll.Description != "" {
			fmt.Fprintf(&text, "%s\n\n", poll.Description)
		}
		for _, slot := range poll.Slots {
			fmt.Fprintf(&text, "- %s, %s–%s (%s)\n", slot.StartsAt.In(location).Format("Mon, Jan 2, 2006"),
				slot.StartsAt.In(location).Format("15:04"), slot.EndsAt.In(location).Format("15:04"), poll.Timezone)
		}
		fmt.Fprintf(&text, "\nOpen the poll to choose your availability:\n%s\n", link)
		bodyHTML := `<div style="font-family:Arial,sans-serif;color:#263445;line-height:1.6;max-width:620px">` +
			`<p>You are invited to help find a time for <strong>` + html.EscapeString(poll.Title) + `</strong>.</p>` +
			`<ul>` + func() string {
			var slots strings.Builder
			for _, slot := range poll.Slots {
				slots.WriteString(`<li>` + html.EscapeString(slot.StartsAt.In(location).Format("Mon, Jan 2, 2006")) + `, ` +
					html.EscapeString(slot.StartsAt.In(location).Format("15:04")+"–"+slot.EndsAt.In(location).Format("15:04")+" ("+poll.Timezone+")") + `</li>`)
			}
			return slots.String()
		}() + `</ul>` +
			`<p><a href="` + html.EscapeString(link) + `" style="display:inline-block;padding:11px 18px;border-radius:8px;background:#0f6cbd;color:#fff;text-decoration:none;font-weight:600">Choose availability</a></p>` +
			`<p style="font-size:12px;color:#7a8797">This link is unique to you and expires automatically.</p></div>`
		item := map[string]any{
			"from":    map[string]any{"name": organizer.Name, "email": organizer.Email},
			"to":      []any{map[string]any{"name": invitee.Name, "email": invitee.Email}},
			"subject": "Find a time: " + poll.Title, "bodyText": text.String(),
			"bodyHtml": bodyHTML, "trustedHtml": true,
		}
		raw, err := buildMIMEMessage(item, fmt.Sprintf("<%s@%s>", newID("poll"), s.cfg.MailDomain), nil)
		if err != nil {
			continue
		}
		_ = s.submitSMTPMessage(organizer.Email, []string{invitee.Email}, raw)
	}
}

// ---------------------------------------------------------------------------
// Public poll page
// ---------------------------------------------------------------------------

type calendarPollPageData struct {
	Title       string
	Description string
	Timezone    string
	Error       string
	Notice      string
	Closed      bool
	Token       string
	Slots       []calendarPollPageSlot
}

type calendarPollPageSlot struct {
	ID, Date, Start, End, Selected string
	Yes, Maybe, No                 int
}

var calendarPollPageTemplate = template.Must(template.New("poll").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex,nofollow"><meta name="referrer" content="no-referrer">
<title>{{.Title}} · Alt8Mail poll</title>
<style>
:root{color-scheme:light dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;background:#eef2f8;color:#17212f;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;display:grid;place-items:center;padding:24px 16px}
.card{width:min(680px,100%);background:#fff;border:1px solid #dce4ef;border-radius:18px;overflow:hidden;box-shadow:0 20px 60px rgba(23,33,47,.12)}
.inner{padding:30px 32px 32px}.eyebrow{margin:0 0 8px;color:#0f6cbd;font-size:11px;font-weight:800;letter-spacing:.12em;text-transform:uppercase}
h1{margin:0 0 10px;font-size:25px;line-height:1.3}.desc{white-space:pre-wrap;color:#526071;line-height:1.65;font-size:14px}
.slot{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;padding:12px;margin-top:9px;border:1px solid #dce4ef;border-radius:10px}
.slot-time strong{display:block;font-size:14px}.slot-time span{font-size:12px;color:#7a8797}.choices{display:flex;gap:5px;flex-wrap:wrap}
.choices label{display:inline-flex;align-items:center;gap:4px;padding:7px 10px;border:1px solid #d6dfea;border-radius:8px;font-size:12px;font-weight:600;cursor:pointer;background:#f8fafc}
.choices input{accent-color:#0f6cbd}.actions{margin-top:20px}.submit{border:0;border-radius:9px;padding:11px 18px;background:#0f6cbd;color:#fff;font:inherit;font-size:14px;font-weight:700;cursor:pointer}
.notice{margin:0 0 16px;padding:12px 14px;border-radius:10px;font-size:13px;line-height:1.55}.notice.ok{background:#edf7f1;border:1px solid #c5e3d2;color:#286347}.notice.bad{background:#fff0f2;border:1px solid #f0c7cf;color:#8a3946}
.footer{margin-top:22px;padding-top:16px;border-top:1px solid #e3e9f1;color:#8592a3;font-size:12px;line-height:1.55}
@media(prefers-color-scheme:dark){body{background:#10151d;color:#e8edf5}.card{background:#19202a;border-color:#2c3644}.slot{border-color:#2c3644}.choices label{background:#202936;border-color:#344153;color:#d5deea}.desc{color:#b9c5d4}.footer{border-color:#2c3644}}
</style></head><body><main class="card"><div class="inner">
<p class="eyebrow">Alt8Mail meeting poll</p><h1>{{.Title}}</h1>
{{if .Error}}<div class="notice bad">{{.Error}}</div>{{end}}{{if .Notice}}<div class="notice ok">{{.Notice}}</div>{{end}}
{{if .Description}}<div class="desc">{{.Description}}</div>{{end}}
{{if .Closed}}<div class="notice bad">This poll is closed. Ask the organizer if you still need to respond.</div>{{else}}
<form method="post" action="{{.Token}}">
{{range .Slots}}<div class="slot"><div class="slot-time"><strong>{{.Date}} · {{.Start}}–{{.End}}</strong><span>{{$.Timezone}}</span></div>
<div class="choices">
<label><input type="radio" name="slot_{{.ID}}" value="yes" {{if eq .Selected "yes"}}checked{{end}}> Yes</label>
<label><input type="radio" name="slot_{{.ID}}" value="maybe" {{if eq .Selected "maybe"}}checked{{end}}> Maybe</label>
<label><input type="radio" name="slot_{{.ID}}" value="no" {{if eq .Selected "no"}}checked{{end}}> No</label>
</div></div>{{end}}
<div class="actions"><button class="submit" type="submit">Send availability</button></div></form>{{end}}
<div class="footer">This link is unique to you. Responses can be updated until the organizer closes the poll.</div>
</div></main></body></html>`))

func (s *Server) publicCalendarPoll(w http.ResponseWriter, r *http.Request) {
	s.calendarPollPage(w, r, http.StatusOK, "")
}

func (s *Server) publicCalendarPollSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil {
		s.calendarPollPage(w, r, http.StatusBadRequest, "Your availability could not be read.")
		return
	}
	token := r.PathValue("token")
	claims, err := s.parseCalendarPollToken(token)
	if err != nil {
		s.calendarPollPage(w, r, http.StatusBadRequest, err.Error())
		return
	}
	var status string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT p.status FROM calendar_polls p JOIN calendar_poll_invitees i ON i.poll_id=p.id
		WHERE p.id=$1 AND lower(i.email)=lower($2)
	`, claims.PollID, claims.Email).Scan(&status); err != nil {
		http.NotFound(w, r)
		return
	}
	if status != "open" {
		s.calendarPollPage(w, r, http.StatusConflict, "This poll is closed. Ask the organizer if you still need to respond.")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `SELECT id FROM calendar_poll_slots WHERE poll_id=$1`, claims.PollID)
	if err != nil {
		s.calendarPollPage(w, r, http.StatusInternalServerError, "The poll could not be saved.")
		return
	}
	slotIDs := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			slotIDs = append(slotIDs, id)
		}
	}
	rows.Close()
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		s.calendarPollPage(w, r, http.StatusInternalServerError, "The poll could not be saved.")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `DELETE FROM calendar_poll_responses WHERE poll_id=$1 AND lower(invitee_email)=lower($2)`,
		claims.PollID, claims.Email); err != nil {
		s.calendarPollPage(w, r, http.StatusInternalServerError, "The poll could not be saved.")
		return
	}
	for _, slotID := range slotIDs {
		response := strings.ToLower(strings.TrimSpace(r.FormValue("slot_" + slotID)))
		if response != "yes" && response != "maybe" && response != "no" {
			continue
		}
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO calendar_poll_responses (poll_id, invitee_email, slot_id, response, responded_at)
			VALUES ($1,$2,$3,$4,now())
		`, claims.PollID, claims.Email, slotID, response); err != nil {
			s.calendarPollPage(w, r, http.StatusInternalServerError, "The poll could not be saved.")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.calendarPollPage(w, r, http.StatusInternalServerError, "The poll could not be saved.")
		return
	}
	s.calendarPollPage(w, r, http.StatusOK, "Your availability was saved. You can update it until the poll closes.")
}

func (s *Server) calendarPollPage(w http.ResponseWriter, r *http.Request, code int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	token := r.PathValue("token")
	claims, err := s.parseCalendarPollToken(token)
	if err != nil {
		if code == http.StatusOK {
			code = http.StatusBadRequest
		}
		w.WriteHeader(code)
		_ = calendarPollPageTemplate.Execute(w, calendarPollPageData{Title: "Meeting poll", Error: err.Error()})
		return
	}
	var poll calendarPoll
	var closes *time.Time
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT p.id, p.title, p.description, p.timezone, p.status, p.created_at, p.closes_at
		FROM calendar_polls p JOIN calendar_poll_invitees i ON i.poll_id=p.id
		WHERE p.id=$1 AND lower(i.email)=lower($2)
	`, claims.PollID, claims.Email).Scan(&poll.ID, &poll.Title, &poll.Description, &poll.Timezone,
		&poll.Status, &poll.CreatedAt, &closes)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = calendarPollPageTemplate.Execute(w, calendarPollPageData{Title: "Meeting poll", Error: "This poll is no longer available."})
		return
	}
	data := calendarPollPageData{
		Title: poll.Title, Description: poll.Description, Timezone: poll.Timezone,
		Token: "/calendar/poll/" + token, Closed: poll.Status != "open",
	}
	if message != "" {
		if code < http.StatusBadRequest {
			data.Notice = message
		} else {
			data.Error = message
		}
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT s.id, s.starts_at, s.ends_at, COALESCE(r.response,'')
		FROM calendar_poll_slots s
		LEFT JOIN calendar_poll_responses r ON r.slot_id=s.id AND r.poll_id=s.poll_id
		     AND lower(r.invitee_email)=lower($2)
		WHERE s.poll_id=$1 ORDER BY s.position
	`, claims.PollID, claims.Email)
	if err == nil {
		location := mustLocation(poll.Timezone)
		for rows.Next() {
			var start, end time.Time
			var item calendarPollPageSlot
			if rows.Scan(&item.ID, &start, &end, &item.Selected) == nil {
				item.Date = start.In(location).Format("Mon, Jan 2, 2006")
				item.Start = start.In(location).Format("15:04")
				item.End = end.In(location).Format("15:04")
				data.Slots = append(data.Slots, item)
			}
		}
		rows.Close()
	}
	w.WriteHeader(code)
	_ = calendarPollPageTemplate.Execute(w, data)
}
