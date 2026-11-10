package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// calendarFreeBusy exposes privacy-preserving busy intervals for internal
// colleagues. Event titles, descriptions, locations and attendees are never
// returned through this endpoint.
func (s *Server) calendarFreeBusy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	start, end := time.Now(), time.Now().Add(24*time.Hour)
	if raw := strings.TrimSpace(r.URL.Query().Get("start")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			start = parsed
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("end")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			end = parsed
		}
	}
	if end.Before(start) || end.Sub(start) > 31*24*time.Hour {
		writeError(w, http.StatusBadRequest, "FREE_BUSY", "Choose a range of up to 31 days")
		return
	}
	emails := []string{}
	seen := map[string]bool{}
	for _, raw := range strings.Split(r.URL.Query().Get("emails"), ",") {
		email := normalizeEmailValue(raw)
		if email == "" || seen[email] || len(emails) >= 20 {
			continue
		}
		seen[email] = true
		emails = append(emails, email)
	}
	results := []map[string]any{}
	for _, email := range emails {
		user, err := s.workspaceUserByEmail(r.Context(), sess.User.TenantID, email)
		if err != nil {
			continue
		}
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT starts_at, ends_at FROM public.calendar_events
			WHERE user_id=$1 AND status <> 'cancelled' AND starts_at < $3 AND ends_at > $2
			ORDER BY starts_at
		`, user.ID, start, end)
		busy := []map[string]any{}
		if err == nil {
			for rows.Next() {
				var from, to time.Time
				if rows.Scan(&from, &to) == nil {
					busy = append(busy, map[string]any{"start": from.UTC().Format(time.RFC3339), "end": to.UTC().Format(time.RFC3339)})
				}
			}
			rows.Close()
		}
		results = append(results, map[string]any{"email": user.Email, "busy": busy})
	}
	if feeds := externalFeedQuery(r.URL); len(feeds) > 0 {
		for _, feed := range s.externalFreeBusy(r.Context(), feeds, start, end) {
			item := map[string]any{"email": "external:" + feed.Label, "external": true, "label": feed.Label, "busy": feed.Busy}
			if feed.Error != "" {
				item["error"] = feed.Error
			}
			results = append(results, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"freeBusy": results, "start": start.UTC().Format(time.RFC3339), "end": end.UTC().Format(time.RFC3339)})
}

func (s *Server) calendarResourceUtilization(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	days := 30
	if value := strings.TrimSpace(r.URL.Query().Get("days")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			days = parsed
		}
	}
	if days < 1 || days > 365 {
		days = 30
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT cr.id, cr.name, count(er.event_id), COALESCE(sum(EXTRACT(EPOCH FROM (e.ends_at - e.starts_at)) / 60), 0)
		FROM public.calendar_resources cr
		LEFT JOIN public.calendar_event_resources er ON er.resource_id=cr.id
		LEFT JOIN public.calendar_events e ON e.id=er.event_id AND e.status <> 'cancelled'
		  AND e.starts_at > now() - ($2 * interval '1 day')
		WHERE cr.tenant_id=$1 AND cr.created_by=$3 AND cr.active
		GROUP BY cr.id, cr.name ORDER BY cr.name
	`, sess.User.TenantID, days, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RESOURCE_UTILIZATION", "Could not load resource utilization")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name string
		var bookings int
		var minutes float64
		if rows.Scan(&id, &name, &bookings, &minutes) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "bookings": bookings, "bookedMinutes": int(minutes)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": items, "days": days})
}

func (s *Server) registerCalendarAvailabilityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/calendar/free-busy", s.requireUser(s.calendarFreeBusy))
	mux.HandleFunc("GET /api/calendar/resources/utilization", s.requireUser(s.calendarResourceUtilization))
}
