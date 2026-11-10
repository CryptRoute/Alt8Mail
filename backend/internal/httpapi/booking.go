package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
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
	pageHTML := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` + title + `</title><style>body{font-family:system-ui,sans-serif;background:#f4f7fb;color:#172230;margin:0;padding:32px}main{max-width:680px;margin:0 auto;background:#fff;border:1px solid #dbe3ee;border-radius:16px;padding:28px}h1{font-size:24px;margin:0 0 8px}p{color:#4b5b70;line-height:1.5}.slots{display:grid;grid-template-columns:repeat(auto-fill,minmax(190px,1fr));gap:8px;margin:20px 0}.slot{padding:11px 12px;border:1px solid #dbe3ee;border-radius:10px;background:#f8fafc;cursor:pointer;text-align:left;font:inherit}.slot.is-selected{border-color:#0f6cbd;background:#e8f2fc}form{display:grid;gap:10px;margin-top:18px}label{display:grid;gap:4px;font-weight:650}input,textarea{padding:9px 10px;border:1px solid #dbe3ee;border-radius:8px;font:inherit}button.primary{padding:11px 16px;border:0;border-radius:9px;background:#0f6cbd;color:#fff;font-weight:700;cursor:pointer}.note{font-size:12px;color:#667085}.error{color:#b42318}.ok{color:#087443}</style></head><body><main><h1>` + title + `</h1><p>` + description + `</p><div class="slots" id="slots"></div><form id="bookingForm"><label>Name<input id="bookerName" required></label><label>Email<input id="bookerEmail" type="email" required></label><label>Note<textarea id="bookerNote" rows="3"></textarea></label><button class="primary" type="submit">Confirm booking</button><p class="note" id="bookingStatus">Times are shown in ` + html.EscapeString(page.Timezone) + `.</p></form></main><script>
const slots = ` + string(slotsJSON) + `;
let selected = "";
const host = document.getElementById("slots");
host.innerHTML = slots.length ? slots.map((slot) => '<button type="button" class="slot" data-slot="' + slot.startAt + '">' + slot.label + '</button>').join("") : '<p class="note">No open times in the next two weeks.</p>';
host.addEventListener("click", (event) => { const button = event.target.closest("[data-slot]"); if (!button) return; selected = button.dataset.slot; host.querySelectorAll(".slot").forEach((item) => item.classList.toggle("is-selected", item === button)); });
document.getElementById("bookingForm").addEventListener("submit", async (event) => { event.preventDefault(); const status = document.getElementById("bookingStatus"); if (!selected) { status.textContent = "Choose a time first."; status.className = "error"; return; } status.textContent = "Booking…"; status.className = "note"; const response = await fetch(location.pathname.replace(/\/book\/.*$/, "") + "/api/public/booking/` + html.EscapeString(page.Slug) + `/book", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ startAt: selected, name: document.getElementById("bookerName").value, email: document.getElementById("bookerEmail").value, note: document.getElementById("bookerNote").value }) }); const data = await response.json(); if (!response.ok) { status.textContent = data.error?.message || "That time is no longer available."; status.className = "error"; return; } status.textContent = "Booked. A calendar invitation has been created."; status.className = "ok"; document.getElementById("bookingForm").querySelectorAll("input,textarea,button").forEach((item) => { item.disabled = true; }); });
</script></body></html>`
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
	var slug string
	if err := s.store.DB.QueryRow(ctx, `SELECT slug::text FROM public.booking_pages WHERE id=$1 AND user_id=$2`, id, user.ID).Scan(&slug); err != nil {
		return bookingPageRecord{}, fail(http.StatusNotFound, "BOOKING_NOT_FOUND", "Booking page not found")
	}
	return s.bookingPageBySlug(ctx, slug)
}

func (s *Server) registerBookingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/booking-pages", s.requireUser(s.listBookingPages))
	mux.HandleFunc("POST /api/booking-pages", s.requireUser(s.createBookingPage))
	mux.HandleFunc("POST /api/booking-pages/{id}/members", s.requireUser(s.addBookingMember))
	mux.HandleFunc("GET /api/public/booking/{slug}/slots", s.publicBookingSlots)
	mux.HandleFunc("POST /api/public/booking/{slug}/book", s.bookSlot)
	mux.HandleFunc("GET /book/{slug}", s.publicBookingPage)
}
