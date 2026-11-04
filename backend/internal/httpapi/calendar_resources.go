package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) ensureDefaultCalendarResources(ctx context.Context, tenantID string) error {
	var count int
	if err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM calendar_resources WHERE tenant_id=$1`, tenantID).Scan(&count); err != nil || count > 0 {
		return err
	}
	var domain string
	_ = s.store.DB.QueryRow(ctx, `
		SELECT domain::text FROM mail_domains WHERE tenant_id=$1
		ORDER BY is_primary DESC, domain LIMIT 1
	`, tenantID).Scan(&domain)
	domain = firstNonEmpty(strings.TrimSpace(domain), "alt8mail.local")
	defaults := []struct {
		name, local, kind, location string
		capacity                    int
	}{
		{"Conference Room A", "room-a", "room", "Head office", 8},
		{"Board Room", "board-room", "room", "Executive floor", 16},
		{"Portable projector", "projector", "equipment", "IT cupboard", 0},
	}
	for _, item := range defaults {
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO calendar_resources (id, tenant_id, name, email, kind, location, capacity)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id, email) DO NOTHING
		`, newID("room"), tenantID, item.name, item.local+"@"+domain, item.kind, item.location, item.capacity)
	}
	return nil
}

func (s *Server) loadCalendarResources(ctx context.Context, tenantID string) ([]calendarResourcePublic, error) {
	if err := s.ensureDefaultCalendarResources(ctx, tenantID); err != nil {
		return nil, err
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, name, email::text, kind, location, capacity
		FROM calendar_resources WHERE tenant_id=$1 AND active=true
		ORDER BY kind, name
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []calendarResourcePublic{}
	for rows.Next() {
		var item calendarResourcePublic
		if rows.Scan(&item.ID, &item.Name, &item.Email, &item.Kind, &item.Location, &item.Capacity) == nil {
			out = append(out, item)
		}
	}
	return out, rows.Err()
}

func (s *Server) getCalendarResources(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	resources, err := s.loadCalendarResources(r.Context(), sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load rooms and resources")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": resources, "count": len(resources)})
}

func (s *Server) createCalendarResource(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Kind     string `json:"kind"`
		Location string `json:"location"`
		Capacity int    `json:"capacity"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len([]rune(name)) > 120 {
		writeError(w, http.StatusBadRequest, "RESOURCE_NAME", "Give the room or resource a name")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind != "room" && kind != "equipment" && kind != "other" {
		writeError(w, http.StatusBadRequest, "RESOURCE_KIND", "Choose room, equipment or other")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		email = fmt.Sprintf("%s-%s@resources.local", kind, newID("r"))
	}
	if !calendarEmailPattern.MatchString(email) {
		writeError(w, http.StatusBadRequest, "RESOURCE_EMAIL", "Enter a valid resource address")
		return
	}
	if body.Capacity < 0 || body.Capacity > 10000 {
		writeError(w, http.StatusBadRequest, "RESOURCE_CAPACITY", "Capacity must be between 0 and 10000")
		return
	}
	id := newID("room")
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO calendar_resources (id, tenant_id, name, email, kind, location, capacity, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, email) DO UPDATE SET name=EXCLUDED.name, kind=EXCLUDED.kind,
		    location=EXCLUDED.location, capacity=EXCLUDED.capacity, active=true, updated_at=now()
		RETURNING id
	`, id, sess.User.TenantID, name, email, kind, truncate(strings.TrimSpace(body.Location), 160), body.Capacity, sess.User.ID).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not save the room or resource")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.resource.saved", name, "Room/resource saved")
	resources, _ := s.loadCalendarResources(r.Context(), sess.User.TenantID)
	writeJSON(w, http.StatusCreated, map[string]any{"resource": map[string]any{
		"id": id, "name": name, "email": email, "kind": kind,
		"location": body.Location, "capacity": body.Capacity,
	}, "resources": resources})
}
