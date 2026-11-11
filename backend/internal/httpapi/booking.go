package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var bookingSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

type bookingMember struct {
	ID    string
	Name  string
	Email string
}

type bookingPageRecord struct {
	ID           string
	TenantID     string
	UserID       string
	Slug         string
	Title        string
	Description  string
	Duration     int
	BufferBefore int
	BufferAfter  int
	StartHour    int
	EndHour      int
	Timezone     string
	Weekdays     []int
	RoundRobin   bool
	Active       bool
	Members      []bookingMember
	CreatedAt    time.Time
}

type bookingBusyInterval struct {
	UserID string
	Start  time.Time
	End    time.Time
}

type bookingSlot struct {
	StartAt      time.Time
	EndAt        time.Time
	Member       bookingMember
	MemberBooked int
}

func (s *Server) requireBookingEntitlement(w http.ResponseWriter) bool {
	if s.entitled("booking_pages") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Booking pages require a license with the booking_pages feature")
	return false
}

func (s *Server) bookingPageBySlug(ctx context.Context, slug string) (bookingPageRecord, error) {
	var page bookingPageRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, user_id::text, slug::text, title, description, duration_minutes,
		       buffer_before_minutes, buffer_after_minutes, working_start_hour, working_end_hour,
		       timezone, weekdays, round_robin, active, created_at
		FROM public.booking_pages WHERE slug=$1 AND active
	`, strings.ToLower(strings.TrimSpace(slug))).Scan(
		&page.ID, &page.TenantID, &page.UserID, &page.Slug, &page.Title, &page.Description, &page.Duration,
		&page.BufferBefore, &page.BufferAfter, &page.StartHour, &page.EndHour, &page.Timezone,
		&page.Weekdays, &page.RoundRobin, &page.Active, &page.CreatedAt)
	if err != nil {
		return page, fail(http.StatusNotFound, "BOOKING_NOT_FOUND", "Booking page not found")
	}
	page.Members, _ = s.bookingMembers(ctx, page.ID, page.UserID)
	return page, nil
}

func (s *Server) bookingMembers(ctx context.Context, pageID, fallbackUserID string) ([]bookingMember, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT u.id::text, u.name, u.email::text
		FROM public.booking_page_members m JOIN public.users u ON u.id=m.user_id
		WHERE m.page_id=$1 AND u.status='active' AND u.deleted_at IS NULL
		ORDER BY u.name
	`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []bookingMember{}
	for rows.Next() {
		var member bookingMember
		if rows.Scan(&member.ID, &member.Name, &member.Email) == nil {
			members = append(members, member)
		}
	}
	if len(members) == 0 && fallbackUserID != "" {
		var member bookingMember
		if s.store.DB.QueryRow(ctx, `SELECT id::text, name, email::text FROM public.users WHERE id=$1`, fallbackUserID).Scan(&member.ID, &member.Name, &member.Email) == nil {
			members = append(members, member)
		}
	}
	return members, nil
}

func (s *Server) bookingBusy(ctx context.Context, page bookingPageRecord, from, to time.Time) []bookingBusyInterval {
	memberIDs := []string{}
	for _, member := range page.Members {
		memberIDs = append(memberIDs, member.ID)
	}
	busy := []bookingBusyInterval{}
	if len(memberIDs) > 0 {
		rows, err := s.store.DB.Query(ctx, `
			SELECT user_id::text, starts_at, ends_at FROM public.calendar_events
			WHERE user_id::text = ANY($1::text[]) AND status <> 'cancelled'
			  AND starts_at < $3 AND ends_at > $2
		`, memberIDs, from, to)
		if err == nil {
			for rows.Next() {
				var item bookingBusyInterval
				if rows.Scan(&item.UserID, &item.Start, &item.End) == nil {
					busy = append(busy, item)
				}
			}
			rows.Close()
		}
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT booked_user_id::text, start_at, end_at FROM public.bookings
		WHERE page_id=$1 AND status='confirmed' AND start_at < $3 AND end_at > $2
	`, page.ID, from, to)
	if err == nil {
		for rows.Next() {
			var item bookingBusyInterval
			if rows.Scan(&item.UserID, &item.Start, &item.End) == nil {
				busy = append(busy, item)
			}
		}
		rows.Close()
	}
	return busy
}

func bookingIntervalFree(busy []bookingBusyInterval, userID string, start, end time.Time) bool {
	for _, item := range busy {
		if item.UserID != userID {
			continue
		}
		if item.Start.Before(end) && item.End.After(start) {
			return false
		}
		// Also account for buffers against the adjacent event.
	}
	return true
}

func (s *Server) bookingSlots(ctx context.Context, page bookingPageRecord, days int) []bookingSlot {
	return s.bookingSlotsAt(ctx, page, time.Now(), days)
}

func (s *Server) bookingSlotsAt(ctx context.Context, page bookingPageRecord, now time.Time, days int) []bookingSlot {
	if days < 1 || days > 30 {
		days = 14
	}
	location, err := time.LoadLocation(page.Timezone)
	if err != nil {
		location = time.UTC
	}
	from := now.Add(-24 * time.Hour)
	to := now.Add(time.Duration(days+1) * 24 * time.Hour)
	busy := s.bookingBusy(ctx, page, from, to)
	bookedCount := map[string]int{}
	for _, item := range busy {
		bookedCount[item.UserID]++
	}
	slots := []bookingSlot{}
	for day := 0; day < days; day++ {
		localDay := now.In(location).AddDate(0, 0, day)
		weekday := int(localDay.Weekday())
		allowed := false
		for _, value := range page.Weekdays {
			if value == weekday {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		for hour := page.StartHour; hour < page.EndHour; hour++ {
			for minute := 0; minute < 60; minute += 30 {
				startLocal := time.Date(localDay.Year(), localDay.Month(), localDay.Day(), hour, minute, 0, 0, location)
				endLocal := startLocal.Add(time.Duration(page.Duration) * time.Minute)
				if endLocal.Hour() > page.EndHour || (endLocal.Hour() == page.EndHour && endLocal.Minute() > 0) {
					continue
				}
				if startLocal.Before(now.Add(30 * time.Minute)) {
					continue
				}
				windowStart := startLocal.Add(-time.Duration(page.BufferBefore) * time.Minute)
				windowEnd := endLocal.Add(time.Duration(page.BufferAfter) * time.Minute)
				available := []bookingMember{}
				for _, member := range page.Members {
					if bookingIntervalFree(busy, member.ID, windowStart, windowEnd) {
						available = append(available, member)
					}
				}
				if len(available) == 0 {
					continue
				}
				chosen := available[0]
				if page.RoundRobin {
					chosen = available[0]
					for _, candidate := range available[1:] {
						if bookedCount[candidate.ID] < bookedCount[chosen.ID] {
							chosen = candidate
						}
					}
				}
				slots = append(slots, bookingSlot{StartAt: startLocal.UTC(), EndAt: endLocal.UTC(), Member: chosen, MemberBooked: bookedCount[chosen.ID]})
				if len(slots) >= 240 {
					return slots
				}
			}
		}
	}
	return slots
}

func (s *Server) publicBookingSlots(w http.ResponseWriter, r *http.Request) {
	page, err := s.bookingPageBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	slots := s.bookingSlots(r.Context(), page, 14)
	items := []map[string]any{}
	for _, slot := range slots {
		items = append(items, map[string]any{"startAt": slot.StartAt.Format(time.RFC3339), "endAt": slot.EndAt.Format(time.RFC3339), "label": slot.StartAt.In(mustBookingLocation(page.Timezone)).Format("Mon 2 Jan · 3:04 PM")})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":  map[string]any{"title": page.Title, "description": page.Description, "durationMinutes": page.Duration, "timezone": page.Timezone},
		"slots": items, "count": len(items),
	})
}

func mustBookingLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func (s *Server) publicBookingPage(w http.ResponseWriter, r *http.Request) {
	page, err := s.bookingPageBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	slots := s.bookingSlots(r.Context(), page, 14)
	type slotView struct {
		Start string `json:"startAt"`
		Label string `json:"label"`
	}
	views := []slotView{}
	for _, slot := range slots {
		views = append(views, slotView{Start: slot.StartAt.Format(time.RFC3339), Label: slot.StartAt.In(mustBookingLocation(page.Timezone)).Format("Mon 2 Jan · 3:04 PM")})
	}
	slotsJSON, _ := json.Marshal(views)
	title := html.EscapeString(page.Title)
	description := html.EscapeString(page.Description)
	if strings.TrimSpace(page.Description) == "" {
		description = "Choose a time that works for you."
	}
	timezone := html.EscapeString(page.Timezone)
	slug := html.EscapeString(page.Slug)
	pageHTML := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light">
<title>` + title + ` · Alt8Mail scheduling</title>
<style>
:root{--ink:#142033;--ink-2:#3c4b62;--muted:#6b7a90;--line:#e2e8f0;--line-strong:#cbd5e1;--surface:#fff;--surface-2:#f6f8fb;--accent:#0f6cbd;--accent-soft:#eaf2fb;--navy:#0a2440;--success:#15803d;--success-soft:#e9f8ef;--danger:#b42318;--danger-soft:#fef0ee}
*{box-sizing:border-box}
body{margin:0;background:radial-gradient(900px 460px at 12% -8%,#e8f1fb 0,transparent 60%),#f3f6fa;color:var(--ink);font-family:"Inter","Segoe UI",-apple-system,BlinkMacSystemFont,"Helvetica Neue",Arial,sans-serif;line-height:1.5}
a{color:inherit}
.page{min-height:100vh;display:flex;flex-direction:column}
.topbar{height:64px;display:flex;align-items:center;justify-content:space-between;padding:0 clamp(18px,4vw,48px);background:var(--navy);color:#fff}
.brand{display:inline-flex;align-items:center;gap:9px;font-weight:700;letter-spacing:-.02em;font-size:15px}
.brand img{width:30px;height:30px;border-radius:8px;display:block}
.topbar .tag{font-size:10px;font-weight:750;letter-spacing:.12em;text-transform:uppercase;color:rgba(255,255,255,.62)}
.shell{width:min(1080px,calc(100% - 32px));margin:clamp(20px,4vw,44px) auto;display:grid;grid-template-columns:1.02fr .98fr;gap:22px;align-items:start;flex:1}
.card{background:var(--surface);border:1px solid var(--line);border-radius:18px;box-shadow:0 18px 50px rgba(15,27,51,.08)}
.info{padding:clamp(22px,3vw,32px);position:sticky;top:24px}
.eyebrow{margin:0 0 8px;color:var(--accent);font-size:10px;font-weight:750;letter-spacing:.13em;text-transform:uppercase}
h1{margin:0;font-size:clamp(28px,3.2vw,40px);line-height:1.06;letter-spacing:-.045em}
.lead{margin:14px 0 0;color:var(--ink-2);font-size:14px;line-height:1.65;max-width:48ch}
.meta{display:flex;flex-wrap:wrap;gap:8px;margin:20px 0 0}
.meta span{display:inline-flex;align-items:center;gap:6px;height:30px;padding:0 10px;border:1px solid var(--line);border-radius:999px;background:var(--surface-2);color:var(--ink-2);font-size:11px;font-weight:600}
.meta svg{width:13px;height:13px;stroke:currentColor;fill:none;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round}
.selected{margin:20px 0 0;padding:13px 14px;border:1px solid #bcd8f2;border-radius:12px;background:var(--accent-soft)}
.selected[hidden]{display:none}
.selected span{display:block;font-size:10px;font-weight:750;letter-spacing:.1em;text-transform:uppercase;color:var(--accent)}
.selected strong{display:block;margin-top:3px;font-size:14px}
.host{display:flex;align-items:center;gap:10px;margin-top:22px;padding-top:18px;border-top:1px solid var(--line)}
.host-mark{width:34px;height:34px;flex:none;display:grid;place-items:center;border-radius:50%;background:var(--navy);color:#fff;font-size:11px;font-weight:750}
.host div{display:flex;flex-direction:column;gap:1px}
.host strong{font-size:12px}
.host span{font-size:10.5px;color:var(--muted)}
.steps{display:flex;flex-direction:column;gap:8px;margin-top:18px}
.steps div{display:flex;align-items:center;gap:9px;color:var(--ink-2);font-size:11px}
.steps span{width:20px;height:20px;flex:none;display:grid;place-items:center;border-radius:50%;background:var(--surface-2);border:1px solid var(--line);font-size:9.5px;font-weight:750;color:var(--muted)}
.steps p{margin:0}
.booker{overflow:hidden}
.booker-head{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:20px 22px 14px;border-bottom:1px solid var(--line)}
.booker-head h2{margin:0;font-size:16px;letter-spacing:-.02em}
.booker-head span{font-size:10.5px;color:var(--muted)}
.days{padding:14px 22px 4px;max-height:390px;overflow-y:auto}
.day{margin-bottom:14px}
.day h3{margin:0 0 6px;font-size:10px;font-weight:750;letter-spacing:.08em;text-transform:uppercase;color:var(--muted)}
.slot-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(92px,1fr));gap:6px}
.slot{appearance:none;border:1px solid var(--line);border-radius:9px;background:var(--surface);color:var(--ink-2);padding:9px 6px;font:inherit;font-size:11.5px;font-weight:650;cursor:pointer;transition:border-color .15s,background .15s,color .15s}
.slot:hover{border-color:var(--line-strong);background:var(--surface-2)}
.slot.is-selected{border-color:var(--accent);background:var(--accent-soft);color:var(--accent);box-shadow:0 0 0 1px var(--accent) inset}
.slot:disabled{opacity:.45;cursor:not-allowed}
.booking-form{padding:18px 22px 22px;border-top:1px solid var(--line);background:var(--surface-2)}
.booking-form h3{margin:0 0 10px;font-size:12px;letter-spacing:-.01em}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:9px}
label{display:flex;flex-direction:column;gap:5px;font-size:10.5px;font-weight:650;color:var(--ink-2)}
input,textarea{width:100%;border:1px solid var(--line);border-radius:9px;background:var(--surface);color:var(--ink);font:inherit;font-size:12px;padding:9px 10px;outline:0}
input:focus,textarea:focus{border-color:var(--accent);box-shadow:0 0 0 3px rgba(15,108,189,.14)}
textarea{resize:vertical;min-height:70px}
.primary{width:100%;height:40px;margin-top:11px;border:0;border-radius:10px;background:var(--accent);color:#fff;font:inherit;font-size:12px;font-weight:700;cursor:pointer;transition:background .15s}
.primary:hover{background:#0b5aa0}
.primary:disabled{opacity:.65;cursor:progress}
.status{margin:9px 0 0;font-size:10.5px;color:var(--muted);line-height:1.5}
.status.error{color:var(--danger)}
.success{padding:28px 22px;text-align:center;background:var(--surface)}
.success[hidden]{display:none}
.success-icon{width:46px;height:46px;margin:0 auto 12px;display:grid;place-items:center;border-radius:50%;background:var(--success-soft);color:var(--success);font-size:20px;font-weight:800}
.success h2{margin:0 0 6px;font-size:18px;letter-spacing:-.03em}
.success p{margin:0;color:var(--ink-2);font-size:12px;line-height:1.6}
.success .muted{margin-top:8px;color:var(--muted);font-size:11px}
.foot{padding:18px clamp(18px,4vw,48px) 26px;text-align:center;color:var(--muted);font-size:10.5px}
.foot strong{color:var(--ink-2)}
.empty{padding:22px;border:1px dashed var(--line-strong);border-radius:10px;color:var(--muted);font-size:11.5px;text-align:center}
@media (max-width:820px){.shell{grid-template-columns:1fr}.info{position:static}.days{max-height:none}}
@media (max-width:520px){.grid{grid-template-columns:1fr}.shell{width:calc(100% - 20px)}.topbar{height:56px}.topbar .tag{display:none}}
</style>
</head>
<body>
<div class="page">
  <header class="topbar"><span class="brand"><img src="/icons/icon-512.png" alt="" width="30" height="30">Alt8Mail</span><span class="tag">Scheduling</span></header>
  <main class="shell">
    <section class="card info">
      <p class="eyebrow">Book a time</p>
      <h1>` + title + `</h1>
      <p class="lead">` + description + `</p>
      <div class="meta">
        <span><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>` + strconv.Itoa(page.Duration) + ` minutes</span>
        <span><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 3.8 5.6 3.8 9S14.5 18.5 12 21c-2.5-2.5-3.8-5.6-3.8-9S9.5 5.5 12 3Z"/></svg>` + timezone + `</span>
      </div>
      <div class="selected" id="selectedSummary" hidden><span>Selected time</span><strong id="selectedLabel"></strong></div>
      <div class="host"><span class="host-mark">A8</span><div><strong>` + title + `</strong><span>Hosted with Alt8Mail · times shown in ` + timezone + `</span></div></div>
      <div class="steps"><div><span>1</span><p>Pick a time that works for you.</p></div><div><span>2</span><p>Share your name and email.</p></div><div><span>3</span><p>Receive a calendar invitation.</p></div></div>
    </section>
    <section class="card booker">
      <div class="booker-head"><h2>Choose a time</h2><span id="slotCount"></span></div>
      <div class="days" id="slots"></div>
      <div class="success" id="bookingSuccess" hidden>
        <div class="success-icon">✓</div>
        <h2>You are booked</h2>
        <p id="successCopy"></p>
        <p class="muted">A calendar invitation has been sent to your email.</p>
      </div>
      <form class="booking-form" id="bookingForm">
        <h3>Your details</h3>
        <div class="grid">
          <label>Name<input id="bookerName" autocomplete="name" required placeholder="Your name"></label>
          <label>Email<input id="bookerEmail" type="email" autocomplete="email" required placeholder="you@company.com"></label>
        </div>
        <label style="margin-top:9px">Note <span style="font-weight:500;color:var(--muted)">optional</span><textarea id="bookerNote" rows="3" placeholder="Anything the host should know?"></textarea></label>
        <button class="primary" id="bookingSubmit" type="submit">Confirm booking</button>
        <p class="status" id="bookingStatus">Times are shown in ` + timezone + `.</p>
      </form>
    </section>
  </main>
  <footer class="foot">Secured by <strong>Alt8Mail</strong> · Your details are shared only with the host.</footer>
</div>
<script>
const slots = ` + string(slotsJSON) + `;
const duration = ` + strconv.Itoa(page.Duration) + `;
const timezone = "` + timezone + `";
const slug = "` + slug + `";
let selected = "";
const host = document.getElementById("slots");
const count = document.getElementById("slotCount");
const summary = document.getElementById("selectedSummary");
const summaryLabel = document.getElementById("selectedLabel");
count.textContent = slots.length + (slots.length === 1 ? " time available" : " times available");
function esc(value) { return String(value == null ? "" : value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;"); }
function renderSlots() {
  if (!slots.length) { host.innerHTML = '<div class="empty">No open times in the next two weeks. Please contact the host directly.</div>'; return; }
  const groups = new Map();
  for (const slot of slots) {
    const pieces = String(slot.label || "").split(" · ");
    const day = pieces[0] || "Available";
    const time = pieces[1] || String(slot.label || "");
    if (!groups.has(day)) groups.set(day, []);
    groups.get(day).push({ startAt: slot.startAt, time });
  }
  host.innerHTML = Array.from(groups.entries()).map(([day, items]) => '<section class="day"><h3>' + esc(day) + '</h3><div class="slot-grid">' + items.map((item) => '<button type="button" class="slot" data-slot="' + esc(item.startAt) + '" data-label="' + esc(day + " · " + item.time) + '" aria-pressed="false">' + esc(item.time) + '</button>').join("") + '</div></section>').join("");
}
host.addEventListener("click", (event) => {
  const button = event.target.closest("[data-slot]");
  if (!button) return;
  selected = button.getAttribute("data-slot");
  host.querySelectorAll(".slot").forEach((item) => { const on = item === button; item.classList.toggle("is-selected", on); item.setAttribute("aria-pressed", on ? "true" : "false"); });
  summary.hidden = false;
  summaryLabel.textContent = button.getAttribute("data-label") + " · " + duration + " minutes";
});
document.getElementById("bookingForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const status = document.getElementById("bookingStatus");
  const submit = document.getElementById("bookingSubmit");
  if (!selected) { status.textContent = "Choose a time first."; status.className = "status error"; return; }
  status.textContent = "Confirming your booking…"; status.className = "status";
  submit.disabled = true;
  try {
    const response = await fetch(location.pathname.replace(/\/book\/.*$/, "") + "/api/public/booking/" + encodeURIComponent(slug) + "/book", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ startAt: selected, name: document.getElementById("bookerName").value, email: document.getElementById("bookerEmail").value, note: document.getElementById("bookerNote").value })
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      status.textContent = (data.error && data.error.message) || "That time is no longer available. Choose another.";
      status.className = "status error";
      submit.disabled = false;
      return;
    }
    document.getElementById("bookingForm").hidden = true;
    document.getElementById("slots").querySelectorAll("button").forEach((item) => { item.disabled = true; });
    const success = document.getElementById("bookingSuccess");
    document.getElementById("successCopy").textContent = summaryLabel.textContent;
    success.hidden = false;
  } catch (error) {
    status.textContent = "Cannot reach the scheduling service. Check your connection and try again.";
    status.className = "status error";
    submit.disabled = false;
  }
});
renderSlots();
</script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}

func (s *Server) bookSlot(w http.ResponseWriter, r *http.Request) {
	page, err := s.bookingPageBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		StartAt string `json:"startAt"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Note    string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(body.StartAt))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BOOKING_SLOT", "Choose a valid slot")
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 120)
	email := normalizeEmailValue(body.Email)
	if name == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "BOOKING_CONTACT", "Enter your name and a valid email")
		return
	}
	location := mustBookingLocation(page.Timezone)
	local := start.In(location)
	weekdayOK := false
	for _, weekday := range page.Weekdays {
		if weekday == int(local.Weekday()) {
			weekdayOK = true
			break
		}
	}
	if !weekdayOK || local.Hour() < page.StartHour || local.Hour() >= page.EndHour || local.Minute()%30 != 0 || local.Before(time.Now().Add(30*time.Minute)) {
		writeError(w, http.StatusBadRequest, "BOOKING_SLOT", "That time is not in the booking window")
		return
	}
	end := start.Add(time.Duration(page.Duration) * time.Minute)
	localEnd := end.In(location)
	if localEnd.Hour() > page.EndHour || (localEnd.Hour() == page.EndHour && localEnd.Minute() > 0) {
		writeError(w, http.StatusBadRequest, "BOOKING_SLOT", "That time does not fit the booking window")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not start the booking")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "a8m-booking:"+page.Slug); err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not lock the booking page")
		return
	}
	windowStart := start.Add(-time.Duration(page.BufferBefore) * time.Minute)
	windowEnd := end.Add(time.Duration(page.BufferAfter) * time.Minute)
	var chosen bookingMember
	for _, member := range page.Members {
		var conflict bool
		_ = tx.QueryRow(r.Context(), `
			SELECT EXISTS(
			  SELECT 1 FROM public.calendar_events WHERE user_id=$1 AND status <> 'cancelled'
			    AND starts_at < $3 AND ends_at > $2
			  UNION ALL
			  SELECT 1 FROM public.bookings WHERE booked_user_id=$1 AND status='confirmed'
			    AND start_at < $3 AND end_at > $2
			)
		`, member.ID, windowStart, windowEnd).Scan(&conflict)
		if !conflict {
			if chosen.ID == "" || !page.RoundRobin {
				chosen = member
				if !page.RoundRobin {
					break
				}
			}
		}
	}
	if chosen.ID == "" {
		writeError(w, http.StatusConflict, "BOOKING_TAKEN", "That time was just booked. Choose another slot.")
		return
	}
	bookingID := newID("booking")
	eventID := newID("event")
	_, err = tx.Exec(r.Context(), `
		INSERT INTO public.bookings (id, page_id, tenant_id, booked_user_id, start_at, end_at, booker_name, booker_email, note, calendar_event_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, bookingID, page.ID, page.TenantID, chosen.ID, start, end, name, email, truncate(strings.TrimSpace(body.Note), 1000), eventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not save the booking")
		return
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO public.calendar_events
		  (id, tenant_id, user_id, calendar_id, uid, title, description, starts_at, ends_at, timezone, status, organizer_email)
		VALUES ($1,$2,$3,'default',$4,$5,$6,$7,$8,$9,'confirmed',$10)
	`, eventID, page.TenantID, chosen.ID, bookingID, page.Title+" — "+name,
		"Booked by "+name+" <"+email+">\n"+strings.TrimSpace(body.Note), start, end, page.Timezone, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not create the calendar event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not commit the booking")
		return
	}
	s.insertAudit(userRecord{ID: chosen.ID, TenantID: page.TenantID, Email: chosen.Email, Name: chosen.Name}, "booking.created", bookingID, "Public booking for "+page.Title, "", "")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "bookingId": bookingID, "startAt": start.Format(time.RFC3339), "endAt": end.Format(time.RFC3339)})
}

// booking page management
type bookingPageBody struct {
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Duration     int    `json:"durationMinutes"`
	BufferBefore int    `json:"bufferBeforeMinutes"`
	BufferAfter  int    `json:"bufferAfterMinutes"`
	StartHour    int    `json:"workingStartHour"`
	EndHour      int    `json:"workingEndHour"`
	Timezone     string `json:"timezone"`
	Weekdays     []int  `json:"weekdays"`
	RoundRobin   bool   `json:"roundRobin"`
	Active       *bool  `json:"active"`
}

func sanitizeBookingBody(body bookingPageBody) (bookingPageBody, error) {
	body.Slug = strings.ToLower(strings.TrimSpace(body.Slug))
	body.Title = truncate(strings.TrimSpace(body.Title), 120)
	if body.Slug == "" && body.Title != "" {
		body.Slug = strings.ToLower(strings.NewReplacer(" ", "-", "_", "-").Replace(body.Title))
	}
	if !bookingSlugPattern.MatchString(body.Slug) {
		return body, fmt.Errorf("slug must be 3-64 lowercase letters, numbers or hyphens")
	}
	if body.Title == "" {
		return body, fmt.Errorf("title is required")
	}
	if body.Duration == 0 {
		body.Duration = 30
	}
	if body.Duration < 5 || body.Duration > 240 {
		return body, fmt.Errorf("duration must be between 5 and 240 minutes")
	}
	if body.BufferBefore < 0 || body.BufferBefore > 120 || body.BufferAfter < 0 || body.BufferAfter > 120 {
		return body, fmt.Errorf("buffers must be between 0 and 120 minutes")
	}
	if body.StartHour < 0 || body.StartHour > 23 || body.EndHour < 1 || body.EndHour > 24 || body.EndHour <= body.StartHour {
		return body, fmt.Errorf("working hours are invalid")
	}
	if _, err := time.LoadLocation(body.Timezone); err != nil {
		return body, fmt.Errorf("timezone must be an IANA zone")
	}
	if len(body.Weekdays) == 0 {
		body.Weekdays = []int{1, 2, 3, 4, 5}
	}
	seen := map[int]bool{}
	weekdays := []int{}
	for _, day := range body.Weekdays {
		if day < 0 || day > 6 || seen[day] {
			return body, fmt.Errorf("weekdays must be unique values 0-6")
		}
		seen[day] = true
		weekdays = append(weekdays, day)
	}
	body.Weekdays = weekdays
	return body, nil
}

func (s *Server) listBookingPages(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, slug::text, title, description, duration_minutes, buffer_before_minutes, buffer_after_minutes,
		       working_start_hour, working_end_hour, timezone, weekdays, round_robin, active, created_at
		FROM public.booking_pages WHERE user_id=$1 ORDER BY created_at DESC
	`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not load booking pages")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var page bookingPageRecord
		if rows.Scan(&page.ID, &page.Slug, &page.Title, &page.Description, &page.Duration, &page.BufferBefore,
			&page.BufferAfter, &page.StartHour, &page.EndHour, &page.Timezone, &page.Weekdays,
			&page.RoundRobin, &page.Active, &page.CreatedAt) == nil {
			items = append(items, bookingPageMap(page))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": items, "count": len(items)})
}

func bookingPageMap(page bookingPageRecord) map[string]any {
	return map[string]any{
		"id": page.ID, "slug": page.Slug, "url": "/book/" + page.Slug, "title": page.Title,
		"description": page.Description, "durationMinutes": page.Duration,
		"bufferBeforeMinutes": page.BufferBefore, "bufferAfterMinutes": page.BufferAfter,
		"workingStartHour": page.StartHour, "workingEndHour": page.EndHour,
		"timezone": page.Timezone, "weekdays": page.Weekdays, "roundRobin": page.RoundRobin,
		"active": page.Active,
	}
}

func (s *Server) createBookingPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body bookingPageBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	body, err := sanitizeBookingBody(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BOOKING", err.Error())
		return
	}
	id := newID("booking")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.booking_pages
		  (id, tenant_id, user_id, slug, title, description, duration_minutes, buffer_before_minutes,
		   buffer_after_minutes, working_start_hour, working_end_hour, timezone, weekdays, round_robin)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, id, sess.User.TenantID, sess.User.ID, body.Slug, body.Title, body.Description, body.Duration,
		body.BufferBefore, body.BufferAfter, body.StartHour, body.EndHour, body.Timezone, body.Weekdays, body.RoundRobin)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "BOOKING_EXISTS", "That booking slug is already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not create the booking page")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `INSERT INTO public.booking_page_members (page_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, sess.User.ID)
	s.audit(r, sess.User, "booking.page_created", id, "Created booking page "+body.Title)
	page, _ := s.bookingPageBySlug(r.Context(), body.Slug)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "page": bookingPageMap(page)})
}

func (s *Server) addBookingMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	page, err := s.bookingPageForOwner(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	member, err := s.workspaceUserByEmail(r.Context(), page.TenantID, normalizeEmailValue(body.Email))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BOOKING_MEMBER", "Choose an active person in this organization")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `INSERT INTO public.booking_page_members (page_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, page.ID, member.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) bookingPageForOwner(ctx context.Context, user userRecord, id string) (bookingPageRecord, error) {
	var page bookingPageRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, user_id::text, slug::text, title, description, duration_minutes,
		       buffer_before_minutes, buffer_after_minutes, working_start_hour, working_end_hour,
		       timezone, weekdays, round_robin, active, created_at
		FROM public.booking_pages WHERE id=$1 AND user_id=$2
	`, id, user.ID).Scan(
		&page.ID, &page.TenantID, &page.UserID, &page.Slug, &page.Title, &page.Description, &page.Duration,
		&page.BufferBefore, &page.BufferAfter, &page.StartHour, &page.EndHour, &page.Timezone,
		&page.Weekdays, &page.RoundRobin, &page.Active, &page.CreatedAt)
	if err != nil {
		return page, fail(http.StatusNotFound, "BOOKING_NOT_FOUND", "Booking page not found")
	}
	page.Members, _ = s.bookingMembers(ctx, page.ID, page.UserID)
	return page, nil
}

func (s *Server) updateBookingPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	page, err := s.bookingPageForOwner(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body bookingPageBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	body, err = sanitizeBookingBody(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BOOKING", err.Error())
		return
	}
	active := page.Active
	if body.Active != nil {
		active = *body.Active
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.booking_pages
		SET slug=$3, title=$4, description=$5, duration_minutes=$6, buffer_before_minutes=$7,
		    buffer_after_minutes=$8, working_start_hour=$9, working_end_hour=$10, timezone=$11,
		    weekdays=$12, round_robin=$13, active=$14, updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, page.ID, sess.User.ID, body.Slug, body.Title, body.Description, body.Duration,
		body.BufferBefore, body.BufferAfter, body.StartHour, body.EndHour, body.Timezone,
		body.Weekdays, body.RoundRobin, active)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "BOOKING_EXISTS", "That booking slug is already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not update the booking page")
		return
	}
	s.audit(r, sess.User, "booking.page_updated", page.ID, "Updated booking page "+body.Title)
	updated, _ := s.bookingPageForOwner(r.Context(), sess.User, page.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "page": bookingPageMap(updated)})
}

func (s *Server) deleteBookingPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	page, err := s.bookingPageForOwner(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.booking_pages SET active=false, updated_at=now() WHERE id=$1 AND user_id=$2
	`, page.ID, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not disable the booking page")
		return
	}
	s.audit(r, sess.User, "booking.page_disabled", page.ID, "Disabled booking page "+page.Title)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": false})
}

func (s *Server) listBookingPageBookings(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	page, err := s.bookingPageForOwner(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT b.id, b.start_at, b.end_at, b.booker_name, b.booker_email::text, b.note,
		       b.status, COALESCE(b.calendar_event_id,''), b.created_at,
		       COALESCE(u.name,''), COALESCE(u.email::text,'')
		FROM public.bookings b
		LEFT JOIN public.users u ON u.id=b.booked_user_id
		WHERE b.page_id=$1 AND b.tenant_id=$2
		ORDER BY b.start_at DESC LIMIT 200
	`, page.ID, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not load bookings")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, email, note, status, eventID, memberName, memberEmail string
		var starts, ends, created time.Time
		if rows.Scan(&id, &starts, &ends, &name, &email, &note, &status, &eventID, &created,
			&memberName, &memberEmail) == nil {
			items = append(items, map[string]any{
				"id": id, "startAt": starts.UTC().Format(time.RFC3339), "endAt": ends.UTC().Format(time.RFC3339),
				"bookerName": name, "bookerEmail": email, "note": note, "status": status,
				"calendarEventId": eventID, "createdAt": created.UTC().Format(time.RFC3339),
				"memberName": memberName, "memberEmail": memberEmail,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": items, "count": len(items), "page": bookingPageMap(page)})
}

func (s *Server) cancelBooking(w http.ResponseWriter, r *http.Request) {
	if !s.requireBookingEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	page, err := s.bookingPageForOwner(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	bookingID := strings.TrimSpace(r.PathValue("bookingId"))
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not cancel the booking")
		return
	}
	defer tx.Rollback(r.Context())
	var eventID string
	var status string
	err = tx.QueryRow(r.Context(), `
		UPDATE public.bookings SET status='cancelled', updated_at=now()
		WHERE id=$1 AND page_id=$2 AND tenant_id=$3 AND status='confirmed'
		RETURNING COALESCE(calendar_event_id,''), status
	`, bookingID, page.ID, sess.User.TenantID).Scan(&eventID, &status)
	if err != nil {
		writeError(w, http.StatusNotFound, "BOOKING_NOT_FOUND", "Confirmed booking not found")
		return
	}
	if eventID != "" {
		_, _ = tx.Exec(r.Context(), `UPDATE public.calendar_events SET status='cancelled', updated_at=now() WHERE id=$1`, eventID)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "BOOKING", "Could not cancel the booking")
		return
	}
	s.audit(r, sess.User, "booking.cancelled", bookingID, "Cancelled booking on page "+page.Title)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "cancelled"})
}

func (s *Server) registerBookingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/booking-pages", s.requireUser(featureGate(s, "booking_pages", s.listBookingPages)))
	mux.HandleFunc("POST /api/booking-pages", s.requireUser(featureGate(s, "booking_pages", s.createBookingPage)))
	mux.HandleFunc("PUT /api/booking-pages/{id}", s.requireUser(featureGate(s, "booking_pages", s.updateBookingPage)))
	mux.HandleFunc("DELETE /api/booking-pages/{id}", s.requireUser(featureGate(s, "booking_pages", s.deleteBookingPage)))
	mux.HandleFunc("GET /api/booking-pages/{id}/bookings", s.requireUser(featureGate(s, "booking_pages", s.listBookingPageBookings)))
	mux.HandleFunc("POST /api/booking-pages/{id}/bookings/{bookingId}/cancel", s.requireUser(featureGate(s, "booking_pages", s.cancelBooking)))
	mux.HandleFunc("POST /api/booking-pages/{id}/members", s.requireUser(featureGate(s, "booking_pages", s.addBookingMember)))
	mux.HandleFunc("GET /api/public/booking/{slug}/slots", s.publicBookingSlots)
	mux.HandleFunc("POST /api/public/booking/{slug}/book", s.bookSlot)
	mux.HandleFunc("GET /book/{slug}", s.publicBookingPage)
}
