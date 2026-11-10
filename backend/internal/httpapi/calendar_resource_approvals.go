package httpapi

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) listCalendarResourceApprovals(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT er.event_id, er.resource_id, er.name, COALESCE(e.title,''), e.starts_at, e.ends_at,
		       COALESCE(u.name,''), COALESCE(u.email::text,'')
		FROM public.calendar_event_resources er
		JOIN public.calendar_resources cr ON cr.id=er.resource_id
		JOIN public.calendar_events e ON e.id=er.event_id
		LEFT JOIN public.users u ON u.id=e.user_id
		WHERE er.status='pending' AND cr.tenant_id=$1
		  AND (cr.approver_user_id=$2 OR cr.created_by=$2)
		ORDER BY e.starts_at ASC LIMIT 200
	`, sess.User.TenantID, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RESOURCE_APPROVALS", "Could not load resource approvals")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var eventID, resourceID, resourceName, title, organizerName, organizerEmail string
		var starts, ends time.Time
		if rows.Scan(&eventID, &resourceID, &resourceName, &title, &starts, &ends, &organizerName, &organizerEmail) == nil {
			items = append(items, map[string]any{
				"eventId": eventID, "resourceId": resourceID, "resourceName": resourceName,
				"title": title, "startsAt": starts.UTC().Format(time.RFC3339),
				"endsAt": ends.UTC().Format(time.RFC3339), "status": "pending",
				"organizerName": organizerName, "organizerEmail": organizerEmail,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": items, "count": len(items)})
}

func (s *Server) decideCalendarResourceApproval(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	decision := strings.ToLower(strings.TrimSpace(body.Decision))
	if decision != "approve" && decision != "deny" {
		writeError(w, http.StatusBadRequest, "RESOURCE_APPROVAL", "Decision must be approve or deny")
		return
	}
	eventID := strings.TrimSpace(r.PathValue("eventId"))
	resourceID := strings.TrimSpace(r.PathValue("resourceId"))
	var resourceName, eventTitle, organizerID string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT er.name, COALESCE(e.title,''), e.user_id::text
		FROM public.calendar_event_resources er
		JOIN public.calendar_resources cr ON cr.id=er.resource_id
		JOIN public.calendar_events e ON e.id=er.event_id
		WHERE er.event_id=$1 AND er.resource_id=$2 AND er.status='pending'
		  AND cr.tenant_id=$3 AND (cr.approver_user_id=$4 OR cr.created_by=$4)
	`, eventID, resourceID, sess.User.TenantID, sess.User.ID).Scan(&resourceName, &eventTitle, &organizerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "RESOURCE_APPROVAL", "Pending resource approval not found")
		return
	}
	status := "accepted"
	if decision == "deny" {
		status = "declined"
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.calendar_event_resources SET status=$3
		WHERE event_id=$1 AND resource_id=$2 AND status='pending'
	`, eventID, resourceID, status); err != nil {
		writeError(w, http.StatusInternalServerError, "RESOURCE_APPROVAL", "Could not save the resource decision")
		return
	}
	if organizerID != "" {
		_, _ = s.store.DB.Exec(r.Context(), `
			INSERT INTO public.mail_notifications (id, tenant_id, user_id, kind, title, body, detail)
			VALUES ($1,$2,$3,'resource_approval_result',$4,$5,$6)
		`, newID("notification"), sess.User.TenantID, organizerID,
			truncate("Room request "+status+": "+resourceName, 240), truncate(eventTitle, 500),
			truncate(strings.TrimSpace(body.Note), 500))
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.resource_approval", resourceName,
		"Resource request "+status+" for "+eventTitle)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func (s *Server) registerCalendarResourceApprovalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/calendar/resources/approvals", s.requireUser(s.listCalendarResourceApprovals))
	mux.HandleFunc("POST /api/calendar/resources/approvals/{eventId}/{resourceId}", s.requireUser(s.decideCalendarResourceApproval))
}
