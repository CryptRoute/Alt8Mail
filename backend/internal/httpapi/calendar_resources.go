package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// loadCalendarResources returns the rooms and resources the user has added to
// their own account. The tenant-wide seed list used by early releases is not
// inherited by new accounts.
func (s *Server) loadCalendarResources(ctx context.Context, tenantID, userID string) ([]calendarResourcePublic, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT r.id, r.name, r.email::text, r.kind, r.location, r.capacity, r.requires_approval,
		       COALESCE(a.email::text, '')
		FROM calendar_resources r
		LEFT JOIN users a ON a.id=r.approver_user_id
		WHERE r.tenant_id=$1 AND r.created_by=$2 AND r.active=true
		ORDER BY r.kind, r.name
	`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []calendarResourcePublic{}
	for rows.Next() {
		var item calendarResourcePublic
		if rows.Scan(&item.ID, &item.Name, &item.Email, &item.Kind, &item.Location, &item.Capacity,
			&item.RequiresApproval, &item.ApproverEmail) == nil {
			out = append(out, item)
		}
	}
	return out, rows.Err()
}

func (s *Server) getCalendarResources(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	resources, err := s.loadCalendarResources(r.Context(), sess.User.TenantID, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load rooms and resources")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": resources, "count": len(resources)})
}

func (s *Server) createCalendarResource(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Name             string `json:"name"`
		Email            string `json:"email"`
		Kind             string `json:"kind"`
		Location         string `json:"location"`
		Capacity         int    `json:"capacity"`
		RequiresApproval bool   `json:"requiresApproval"`
		ApproverEmail    string `json:"approverEmail"`
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
	var existingOwner string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT COALESCE(created_by::text, '') FROM calendar_resources
		WHERE tenant_id=$1 AND email=$2
	`, sess.User.TenantID, email).Scan(&existingOwner); err == nil && existingOwner != "" && existingOwner != sess.User.ID {
		writeError(w, http.StatusConflict, "RESOURCE_EMAIL", "That address already belongs to another room or resource")
		return
	}
	if body.Capacity < 0 || body.Capacity > 10000 {
		writeError(w, http.StatusBadRequest, "RESOURCE_CAPACITY", "Capacity must be between 0 and 10000")
		return
	}
	approverID := ""
	approverEmail := ""
	if body.RequiresApproval {
		approverEmail = normalizeEmailValue(body.ApproverEmail)
		if approverEmail == "" {
			approverEmail = sess.User.Email
		}
		approver, approverErr := s.workspaceUserByEmail(r.Context(), sess.User.TenantID, approverEmail)
		if approverErr != nil {
			writeError(w, http.StatusBadRequest, "RESOURCE_APPROVER", "Choose an active approver in this organization")
			return
		}
		approverID = approver.ID
		approverEmail = approver.Email
	}
	id := newID("room")
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO calendar_resources (id, tenant_id, name, email, kind, location, capacity, created_by,
		                                requires_approval, approver_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid)
		ON CONFLICT (tenant_id, email) DO UPDATE SET name=EXCLUDED.name, kind=EXCLUDED.kind,
		    location=EXCLUDED.location, capacity=EXCLUDED.capacity, active=true,
		    requires_approval=EXCLUDED.requires_approval, approver_user_id=EXCLUDED.approver_user_id,
		    updated_at=now()
		RETURNING id
	`, id, sess.User.TenantID, name, email, kind, truncate(strings.TrimSpace(body.Location), 160),
		body.Capacity, sess.User.ID, body.RequiresApproval, approverID).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not save the room or resource")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.resource.saved", name, "Room/resource saved")
	resources, _ := s.loadCalendarResources(r.Context(), sess.User.TenantID, sess.User.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"resource": map[string]any{
		"id": id, "name": name, "email": email, "kind": kind,
		"location": body.Location, "capacity": body.Capacity,
		"requiresApproval": body.RequiresApproval, "approverEmail": approverEmail,
	}, "resources": resources})
}
