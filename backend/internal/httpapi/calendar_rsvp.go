package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

const calendarRSVPMaxTTL = 365 * 24 * time.Hour

type calendarRSVPClaims struct {
	EventID  string `json:"e"`
	UID      string `json:"u"`
	Email    string `json:"a"`
	Sequence int    `json:"s"`
	Expires  int64  `json:"x"`
}

func (s *Server) calendarRSVPKey() []byte {
	sum := sha256.Sum256([]byte("alt8mail-calendar-rsvp\x00" + s.cfg.SessionSecret + "\x00" + s.cfg.CSRFSecret))
	return sum[:]
}

func (s *Server) signCalendarRSVPToken(claims calendarRSVPClaims) string {
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.calendarRSVPKey())
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) parseCalendarRSVPToken(token string) (calendarRSVPClaims, error) {
	var claims calendarRSVPClaims
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return claims, fmt.Errorf("invalid invitation link")
	}
	mac := hmac.New(sha256.New, s.calendarRSVPKey())
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || subtle.ConstantTimeCompare(expected, actual) != 1 ||
		base64.RawURLEncoding.EncodeToString(actual) != parts[1] {
		return claims, fmt.Errorf("invalid invitation link")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return claims, fmt.Errorf("invalid invitation link")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, fmt.Errorf("invalid invitation link")
	}
	if claims.EventID == "" || claims.UID == "" || claims.Email == "" || claims.Expires <= 0 {
		return claims, fmt.Errorf("invalid invitation link")
	}
	if time.Now().Unix() > claims.Expires {
		return claims, fmt.Errorf("this invitation link has expired")
	}
	return claims, nil
}

func (s *Server) calendarRSVPURL(event map[string]any, attendeeEmail string) string {
	sequence := int(stateInt(event, "sequence"))
	expires := time.Now().Add(90 * 24 * time.Hour)
	if ends, err := time.Parse(time.RFC3339, stateString(event, "endsAt")); err == nil {
		if candidate := ends.Add(30 * 24 * time.Hour); candidate.After(expires) && candidate.Before(time.Now().Add(calendarRSVPMaxTTL)) {
			expires = candidate
		}
	}
	claims := calendarRSVPClaims{
		EventID: stateString(event, "id"), UID: stateString(event, "uid"),
		Email: strings.ToLower(strings.TrimSpace(attendeeEmail)), Sequence: sequence,
		Expires: expires.Unix(),
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/calendar/rsvp/" + s.signCalendarRSVPToken(claims)
}

type calendarRSVPPageData struct {
	Title            string
	When             string
	Where            string
	Meeting          string
	Organizer        string
	Description      string
	Color            string
	Status           string
	Selected         string
	SelectedLabel    string
	Pending          bool
	Prompt           string
	Error            string
	Notice           string
	CanRespond       bool
	Stale            bool
	Cancelled        bool
	Token            string
	Updated          bool
	ProposedDate     string
	ProposedStart    string
	ProposedEnd      string
	ProposedTimezone string
	DateMonth        string
	DateDay          string
	TimeZone         string
}

var calendarRSVPPageTemplate = template.Must(template.New("rsvp").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex,nofollow"><meta name="referrer" content="no-referrer">
<title>{{.Title}} · Alt8Mail Calendar</title>
<style>
:root{color-scheme:light dark}*{box-sizing:border-box}
body{margin:0;min-height:100vh;padding:38px 16px;background:radial-gradient(circle at 8% 0%,rgba(15,108,189,.15),transparent 36%),radial-gradient(circle at 94% 100%,rgba(47,125,91,.12),transparent 34%),#edf2f9;color:#17212f;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;display:grid;place-items:center;position:relative}
body::before{content:"";position:fixed;inset:0;pointer-events:none;background-image:linear-gradient(rgba(23,33,47,.035) 1px,transparent 1px),linear-gradient(90deg,rgba(23,33,47,.035) 1px,transparent 1px);background-size:44px 44px;mask-image:linear-gradient(180deg,rgba(0,0,0,.6),transparent 78%)}
.shell{width:min(660px,100%);position:relative}
.brand{display:flex;align-items:center;gap:9px;margin:0 0 11px 3px;color:#5a6879;font-size:11px;font-weight:800;letter-spacing:.1em;text-transform:uppercase}
.brand-mark{width:24px;height:24px;display:grid;place-items:center;border-radius:7px;background:var(--accent,#0f6cbd);color:#fff;box-shadow:0 5px 14px color-mix(in srgb,var(--accent,#0f6cbd) 30%,transparent)}
.brand-mark svg{width:14px;height:14px}
.card{background:#fff;border:1px solid #dce4ef;border-radius:20px;overflow:hidden;box-shadow:0 26px 70px rgba(23,33,47,.15)}
.accent{height:6px;background:linear-gradient(90deg,var(--accent,#0f6cbd),color-mix(in srgb,var(--accent,#0f6cbd) 55%,#7fb0d9))}
.inner{padding:28px 30px 30px}
.eyebrow{margin:0 0 12px;color:var(--accent,#0f6cbd);font-size:10px;font-weight:800;letter-spacing:.13em;text-transform:uppercase}
.event-head{display:flex;align-items:flex-start;gap:16px;margin-bottom:20px}
.date-tile{width:70px;height:76px;flex:none;display:flex;flex-direction:column;align-items:center;justify-content:center;border:1px solid color-mix(in srgb,var(--accent,#0f6cbd) 22%,#dce4ef);border-radius:14px;background:linear-gradient(160deg,color-mix(in srgb,var(--accent,#0f6cbd) 12%,#fff),#fff)}
.date-tile span{color:var(--accent,#0f6cbd);font-size:10px;font-weight:800;letter-spacing:.1em;text-transform:uppercase}
.date-tile b{color:#17212f;font-size:29px;line-height:1.05;font-weight:760}
.event-heading{flex:1;min-width:0;padding-top:3px}
h1{margin:0 0 8px;font-size:25px;line-height:1.25;letter-spacing:-.025em;overflow-wrap:anywhere}
.event-status{display:flex;flex-wrap:wrap;align-items:center;gap:6px}
.pill{display:inline-flex;align-items:center;gap:5px;padding:4px 9px;border-radius:999px;font-size:10px;font-weight:800;letter-spacing:.05em;text-transform:uppercase}
.pill.answered{background:color-mix(in srgb,var(--accent,#0f6cbd) 12%,#fff);color:var(--accent,#0f6cbd)}
.pill.cancelled{background:#fff0f2;color:#8a3946}
.pill.neutral{background:#eef2f7;color:#5a6879}
.notice{margin:0 0 18px;padding:12px 14px;border-radius:11px;font-size:13px;line-height:1.55;display:flex;gap:8px;align-items:flex-start}
.notice svg{width:15px;height:15px;flex:none;margin-top:1px}
.notice.warn{background:#fff7e6;border:1px solid #f0dfb0;color:#765817}
.notice.bad{background:#fff0f2;border:1px solid #f0c7cf;color:#8a3946}
.notice.ok{background:#edf7f1;border:1px solid #c5e3d2;color:#286347}
.meta{display:grid;gap:2px;margin:0 0 20px;padding:14px 16px;border:1px solid #e2e8f1;border-radius:13px;background:#fafbfd}
.row{display:grid;grid-template-columns:96px 1fr;gap:12px;padding:5px 0;font-size:14px;line-height:1.5}
.row dt{color:#718096;font-weight:650}
.row dd{margin:0;color:#253346;overflow-wrap:anywhere}
.row dd a{color:var(--accent,#0f6cbd);text-decoration:none}
.desc{padding:16px 0 0;margin-top:2px;border-top:1px solid #e3e9f1;white-space:pre-wrap;font-size:14px;line-height:1.65;color:#435166}
.actions-label{margin:22px 0 8px;color:#718096;font-size:10px;font-weight:800;letter-spacing:.09em;text-transform:uppercase}
.actions{display:flex;flex-wrap:wrap;gap:9px;margin:0 0 8px}
.actions button{border:1px solid transparent;border-radius:11px;padding:12px 18px;font:inherit;font-size:14px;font-weight:700;cursor:pointer;display:inline-flex;align-items:center;gap:7px;transition:transform .12s ease,box-shadow .12s ease}
.actions button svg{width:15px;height:15px}
.actions button:hover:not(:disabled){transform:translateY(-1px)}
.yes{background:var(--accent,#0f6cbd);color:#fff}.maybe{background:#5a6678;color:#fff}.no{background:#a94452;color:#fff}
.actions button.is-selected{outline:3px solid color-mix(in srgb,var(--accent,#0f6cbd) 32%,#fff);outline-offset:2px;box-shadow:0 8px 20px rgba(23,33,47,.15)}
.actions button:disabled{opacity:.5;cursor:not-allowed}
.hint{margin:10px 0 0;color:#718096;font-size:12px;line-height:1.55}
.propose{margin-top:18px;border-top:1px solid #e3e9f1;padding-top:14px}
.propose summary{cursor:pointer;color:#435166;font-size:13px;font-weight:700}
.propose-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;margin-top:12px}
.propose label{display:flex;flex-direction:column;gap:4px;color:#718096;font-size:11px;font-weight:600}
.propose input,.propose textarea{width:100%;border:1px solid #d6dfea;border-radius:8px;padding:9px 10px;font:inherit;font-size:13px;background:#fff;color:#17212f}
.propose textarea{grid-column:1/-1;min-height:64px;resize:vertical}
.propose button{margin-top:10px;border:1px solid #c9d4e2;border-radius:9px;background:#f6f8fb;color:#253346;padding:10px 16px;font:inherit;font-size:13px;font-weight:700;cursor:pointer}
.footer{margin-top:22px;padding-top:16px;border-top:1px solid #e3e9f1;color:#8592a3;font-size:11px;line-height:1.6}
@media(max-width:520px){.inner{padding:22px 18px 24px}.event-head{gap:12px}.date-tile{width:58px;height:64px}.date-tile b{font-size:24px}h1{font-size:21px}.row{grid-template-columns:1fr;gap:1px}.actions button{flex:1;justify-content:center}}
@media(prefers-color-scheme:dark){body{background:radial-gradient(circle at 8% 0%,rgba(15,108,189,.2),transparent 38%),radial-gradient(circle at 94% 100%,rgba(47,125,91,.14),transparent 36%),#10151d;color:#e8edf5}body::before{background-image:linear-gradient(rgba(255,255,255,.035) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.035) 1px,transparent 1px)}.card{background:#19202a;border-color:#2c3644}.date-tile{border-color:#334054;background:linear-gradient(160deg,#223043,#19202a)}.date-tile b{color:#e8edf5}.row dd{color:#d5deea}.row dt,.hint,.footer{color:#94a1b2}.meta{background:#1d2531;border-color:#2c3644}.desc{color:#b9c5d4;border-color:#2c3644}.footer{border-color:#2c3644}.pill.answered{background:#203247;color:#8fbdec}.pill.neutral{background:#242d3a;color:#aab7c8}}
</style></head><body><div class="shell"><div class="brand"><span class="brand-mark"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><rect x="3.5" y="5" width="17" height="15.5" rx="2"/><path d="M8 3v4M16 3v4M3.5 9.5h17"/></svg></span>Alt8Mail Calendar</div>
<main class="card" style="--accent:{{.Color}}"><div class="accent"></div><div class="inner">
<p class="eyebrow">{{if .Cancelled}}Meeting cancelled{{else if .Updated}}Updated invitation{{else}}Calendar invitation{{end}}</p>
<div class="event-head">
  {{if .DateMonth}}<div class="date-tile"><span>{{.DateMonth}}</span><b>{{.DateDay}}</b></div>{{end}}
  <div class="event-heading">
    <h1>{{.Title}}</h1>
    <div class="event-status"><span class="pill {{if .Cancelled}}cancelled{{else if .Pending}}neutral{{else if .Selected}}answered{{else}}neutral{{end}}">{{if .Cancelled}}Cancelled{{else if .Pending}}Confirm your response{{else if .Selected}}You chose {{.SelectedLabel}}{{else}}Your response is requested{{end}}</span></div>
  </div>
</div>
{{if .Error}}<div class="notice bad"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5v5"/><circle cx="12" cy="16.2" r=".9" fill="currentColor" stroke="none"/></svg><span>{{.Error}}</span></div>{{end}}
{{if .Notice}}<div class="notice ok"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="8.5"/><path d="m8.2 12.2 2.6 2.6 5.2-5.4"/></svg><span>{{.Notice}}</span></div>{{end}}
{{if .Prompt}}<div class="notice warn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M12 7v5l3 2"/><circle cx="12" cy="12" r="8.5"/></svg><span>{{.Prompt}}</span></div>{{end}}
{{if .Stale}}<div class="notice warn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5v5"/><circle cx="12" cy="16.2" r=".9" fill="currentColor" stroke="none"/></svg><span>This invitation was updated after this link was sent. Open the newest invitation email to respond to the current time.</span></div>{{end}}
<dl class="meta"><div class="row"><dt>When</dt><dd>{{.When}}</dd></div>{{if .Where}}<div class="row"><dt>Where</dt><dd>{{.Where}}</dd></div>{{end}}{{if .Meeting}}<div class="row"><dt>Join</dt><dd><a href="{{.Meeting}}" rel="noreferrer">{{.Meeting}}</a></dd></div>{{end}}<div class="row"><dt>Organizer</dt><dd>{{.Organizer}}</dd></div></dl>
{{if .Description}}<div class="desc">{{.Description}}</div>{{end}}
{{if .CanRespond}}<form method="post" action="{{.Token}}">
<p class="actions-label">Can you make it?</p>
<div class="actions">
<button class="yes{{if eq .Selected "accepted"}} is-selected{{end}}" type="submit" name="response" value="accepted" aria-pressed="{{if eq .Selected "accepted"}}true{{else}}false{{end}}" {{if .Stale}}disabled{{end}}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>Yes, I'll attend</button>
<button class="maybe{{if eq .Selected "tentative"}} is-selected{{end}}" type="submit" name="response" value="tentative" aria-pressed="{{if eq .Selected "tentative"}}true{{else}}false{{end}}" {{if .Stale}}disabled{{end}}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 7v5l3 2"/><circle cx="12" cy="12" r="8"/></svg>Maybe</button>
<button class="no{{if eq .Selected "declined"}} is-selected{{end}}" type="submit" name="response" value="declined" aria-pressed="{{if eq .Selected "declined"}}true{{else}}false{{end}}" {{if .Stale}}disabled{{end}}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M7 7l10 10M17 7 7 17"/></svg>No, I can't</button>
</div>
<p class="hint">Your response is saved instantly and can be changed any time from this page. The organizer sees it on their Alt8Mail calendar.</p></form>
<details class="propose"><summary>Propose a new time</summary>
<form method="post" action="{{.Token}}"><input type="hidden" name="action" value="propose">
<div class="propose-grid">
<label>Date<input type="date" name="proposedDate" value="{{.ProposedDate}}" required></label>
<label>Start<input type="time" name="proposedStart" value="{{.ProposedStart}}" required></label>
<label>End<input type="time" name="proposedEnd" value="{{.ProposedEnd}}" required></label>
<label>Time zone<input type="text" name="proposedTimezone" value="{{.ProposedTimezone}}" required></label>
<label>Note<textarea name="proposedNote" maxlength="500" placeholder="Optional note for the organizer"></textarea></label>
</div><button type="submit">Send proposed time</button></form></details>
{{end}}
<div class="footer">Signed for {{.Organizer}} by Alt8Mail Calendar. This page does not show the event to anyone without the signed link.</div>
</div></main></div></body></html>`))

func rsvpStatusLabel(status string) string {
	switch strings.ToLower(status) {
	case "accepted":
		return "accepted"
	case "tentative":
		return "tentative"
	case "declined":
		return "declined"
	}
	return ""
}

func calendarRSVPSelectedLabel(status string) string {
	switch strings.ToLower(status) {
	case "accepted":
		return "Yes, I'll attend"
	case "tentative":
		return "Maybe"
	case "declined":
		return "No, I can't"
	}
	return ""
}

func (s *Server) publicCalendarRSVP(w http.ResponseWriter, r *http.Request) {
	s.calendarRSVPPage(w, r, http.StatusOK, "", "")
}

func (s *Server) publicCalendarRSVPSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	if err := r.ParseForm(); err != nil {
		s.calendarRSVPPage(w, r, http.StatusBadRequest, "", "Your response could not be read. Please try again.")
		return
	}
	token := r.PathValue("token")
	claims, err := s.parseCalendarRSVPToken(token)
	if err != nil {
		s.calendarRSVPPage(w, r, http.StatusBadRequest, "", err.Error())
		return
	}
	var eventID, uid, tenantID, organizerID, organizerEmail, title, status, eventTimezone, attendeeName string
	var sequence int
	var attendeeStatus string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT e.id, e.uid, e.tenant_id::text, e.user_id::text, e.organizer_email::text,
		       e.title, e.status, e.sequence, a.status, e.timezone, a.name
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE e.id=$1 AND lower(e.uid)=lower($2) AND lower(a.email)=lower($3)
	`, claims.EventID, claims.UID, claims.Email).Scan(&eventID, &uid, &tenantID, &organizerID,
		&organizerEmail, &title, &status, &sequence, &attendeeStatus, &eventTimezone, &attendeeName)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if organizerEmail != "" {
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT id::text FROM users WHERE tenant_id=$1 AND lower(email)=lower($2) AND deleted_at IS NULL
		`, tenantID, organizerEmail).Scan(&organizerID)
	}
	if status == "cancelled" {
		s.calendarRSVPPage(w, r, http.StatusConflict, "", "This meeting has been cancelled. No response is needed.")
		return
	}
	if sequence != claims.Sequence {
		s.calendarRSVPPage(w, r, http.StatusConflict, "", "This invitation link is out of date. Open the newest invitation email to respond.")
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.FormValue("action")), "propose") {
		startsAt, endsAt, parseErr := parseProposalTimes(
			r.FormValue("proposedDate"), r.FormValue("proposedStart"), r.FormValue("proposedEnd"),
			firstNonEmpty(r.FormValue("proposedTimezone"), eventTimezone, "UTC"),
		)
		if parseErr != nil {
			s.calendarRSVPPage(w, r, http.StatusBadRequest, "", parseErr.Error())
			return
		}
		_, saveErr := s.saveCalendarProposal(r.Context(), calendarProposalInput{
			EventID: eventID, AttendeeEmail: claims.Email, AttendeeName: attendeeName,
			StartsAt: startsAt, EndsAt: endsAt,
			Timezone: firstNonEmpty(r.FormValue("proposedTimezone"), eventTimezone, "UTC"),
			Note:     r.FormValue("proposedNote"), Sequence: sequence, Source: "link",
		})
		if saveErr != nil {
			s.calendarRSVPPage(w, r, http.StatusConflict, "", saveErr.Error())
			return
		}
		s.calendarRSVPPage(w, r, http.StatusOK, "", "Your proposed time was sent to the organizer.")
		return
	}
	response := strings.ToLower(strings.TrimSpace(r.FormValue("response")))
	if response != "accepted" && response != "tentative" && response != "declined" {
		s.calendarRSVPPage(w, r, http.StatusBadRequest, "", "Choose Yes, Maybe or No.")
		return
	}
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE calendar_attendees a
		SET status=$1, responded_at=now(), last_response_sequence=$2,
		    last_response_at=now(), note=''
		FROM calendar_events e
		WHERE a.event_id=e.id AND e.id=$3 AND e.sequence=$2
		  AND lower(a.email)=lower($4)
	`, response, sequence, eventID, claims.Email)
	if err != nil || result.RowsAffected() == 0 {
		s.calendarRSVPPage(w, r, http.StatusInternalServerError, "", "Your response could not be saved. Please try again.")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO calendar_invite_replies
		(event_id, tenant_id, event_uid, attendee_email, sequence, partstat, method, dedup_key, source, applied, detail)
		VALUES ($1,$2,$3,$4,$5,$6,'REPLY',$7,'link',true,$8)
		ON CONFLICT (event_id, dedup_key) DO NOTHING
	`, eventID, tenantID, uid, claims.Email, sequence, strings.ToUpper(response), newID("rsvp"), "Signed RSVP link")
	if organizer, organizerErr := s.userByID(r.Context(), organizerID); organizerErr == nil {
		s.insertAudit(organizer, "calendar.rsvp-received", title,
			fmt.Sprintf("Guest %s responded %s · sequence %d · signed link", claims.Email, response, sequence),
			clientIP(r), sanitizeUserAgent(r.UserAgent()))
	}
	s.publishRealtimeTo(organizerID, "calendar")
	s.calendarRSVPPage(w, r, http.StatusOK, response, "")
}

// calendarRSVPPage loads the event for display and optionally renders the
// result of a just-saved response (selected).
func (s *Server) calendarRSVPPage(w http.ResponseWriter, r *http.Request, code int, selected, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	token := r.PathValue("token")
	claims, tokenErr := s.parseCalendarRSVPToken(token)
	data := calendarRSVPPageData{
		Title: "Calendar invitation", Color: "#0f6cbd", Token: "/calendar/rsvp/" + token,
		Selected: selected,
	}
	if tokenErr != nil {
		data.Title = "Invitation unavailable"
		data.Error = tokenErr.Error()
		w.WriteHeader(code)
		_ = calendarRSVPPageTemplate.Execute(w, data)
		return
	}
	data.Token = "/calendar/rsvp/" + token
	data.Selected = r.URL.Query().Get("response")
	var eventZone string
	var title, description, location, meeting, organizer, organizerName, color, status, attendeeStatus string
	var startsAt, endsAt time.Time
	var allDay bool
	var sequence int
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT e.title, e.description, e.location, e.meeting_url,
		       e.organizer_email::text, e.organizer_name, e.starts_at, e.ends_at, e.all_day,
		       e.timezone, e.status, e.sequence, a.status,
		       COALESCE(e.metadata->>'color','')
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE e.id=$1 AND lower(e.uid)=lower($2) AND lower(a.email)=lower($3)
	`, claims.EventID, claims.UID, claims.Email).Scan(
		&title, &description, &location, &meeting,
		&organizer, &organizerName, &startsAt, &endsAt, &allDay, &eventZone, &status, &sequence,
		&attendeeStatus, &color)
	if err != nil {
		data.Title = "Invitation unavailable"
		data.Error = "This invitation is no longer available."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_ = calendarRSVPPageTemplate.Execute(w, data)
		return
	}
	if calendarHexPattern.MatchString(color) {
		data.Color = color
	}
	event := map[string]any{
		"startsAt": startsAt.UTC().Format(time.RFC3339), "endsAt": endsAt.UTC().Format(time.RFC3339),
		"allDay": allDay, "timezone": eventZone, "title": title,
	}
	data.Title = title
	data.When = calendarInviteWhen(event, "en")
	data.Where = location
	data.Meeting = meeting
	data.Organizer = organizer
	if organizerName != "" {
		data.Organizer = organizerName + " · " + organizer
	}
	data.Description = description
	localStart := startsAt.In(mustLocation(eventZone))
	localEnd := endsAt.In(mustLocation(eventZone))
	data.ProposedDate = localStart.Format("2006-01-02")
	data.ProposedStart = localStart.Format("15:04")
	data.ProposedEnd = localEnd.Format("15:04")
	data.ProposedTimezone = firstNonEmpty(eventZone, "UTC")
	data.DateMonth = localStart.Format("Jan")
	data.DateDay = localStart.Format("2")
	data.TimeZone = firstNonEmpty(eventZone, "UTC")
	data.Updated = sequence > 0
	data.Cancelled = status == "cancelled"
	querySelection := rsvpStatusLabel(data.Selected)
	savedSelection := rsvpStatusLabel(attendeeStatus)
	if querySelection != "" {
		data.Selected = querySelection
		data.SelectedLabel = calendarRSVPSelectedLabel(querySelection)
		if querySelection != savedSelection {
			data.Pending = true
			data.Prompt = "Press the highlighted response below to send it. You can change your choice at any time from this page."
		}
	} else {
		data.Selected = savedSelection
		data.SelectedLabel = calendarRSVPSelectedLabel(savedSelection)
	}
	data.Stale = sequence != claims.Sequence
	data.CanRespond = !data.Cancelled && !data.Stale
	if message != "" {
		data.Notice = message
	} else if selected != "" {
		data.Notice = "Your response was saved. The organizer can see it now."
	}
	w.WriteHeader(code)
	_ = calendarRSVPPageTemplate.Execute(w, data)
}
