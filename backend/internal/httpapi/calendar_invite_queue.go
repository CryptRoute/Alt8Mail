package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// queueCalendarInvites holds invitation delivery for the user's undo-send
// window. The worker only sends a job when its SEQUENCE still matches the
// event, so a rapid second edit supersedes the first queued invitation.
func (s *Server) queueCalendarInvites(ctx context.Context, organizer userRecord, eventID, method string) error {
	event, err := s.loadCalendarEvent(ctx, organizer, eventID)
	if err != nil {
		return err
	}
	organizer = s.calendarOrganizerUser(ctx, organizer, event)
	recipients := 0
	for _, raw := range stateSlice(event, "attendeeList") {
		entry, ok := raw.(map[string]any)
		if ok && !stateBool(entry, "organizer") && stateString(entry, "email") != "" {
			recipients++
		}
	}
	if recipients == 0 || strings.EqualFold(method, "CANCEL") {
		if strings.EqualFold(method, "CANCEL") {
			go s.sendCalendarInvitePayload(context.Background(), organizer, event, "CANCEL")
		}
		return nil
	}
	hold := 8 * time.Second
	if state, stateErr := s.loadState(ctx, organizer.ID); stateErr == nil {
		preferences := stateMap(stateMap(state, "user"), "preferences")
		if seconds := stateInt(preferences, "undoSendSeconds"); seconds >= 5 && seconds <= 10 {
			hold = time.Duration(seconds) * time.Second
		}
	}
	_, err = s.store.DB.Exec(ctx, `
		INSERT INTO calendar_invite_jobs
		(id, tenant_id, event_id, organizer_user_id, method, sequence, due_at, status)
		VALUES ($1,$2,$3,$4,'REQUEST',$5,$6,'queued')
	`, newID("caljob"), organizer.TenantID, eventID, organizer.ID, stateInt(event, "sequence"), time.Now().Add(hold))
	return err
}

func (s *Server) undoCalendarInvites(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	event, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	access := stateString(event, "access")
	if access != "owner" && access != "edit" {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only the organizer or a calendar editor can cancel invitations")
		return
	}
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE calendar_invite_jobs
		SET status='cancelled'
		WHERE event_id=$1 AND status='queued'
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not cancel the queued invitations")
		return
	}
	cancelled := result.RowsAffected()
	if cancelled > 0 {
		s.recordWorkspaceAudit(r.Context(), sess, "calendar.invites.undone", stateString(event, "title"),
			"Invitations cancelled inside the undo-send window")
		s.publishRealtime("calendar")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": cancelled})
}

func (s *Server) calendarInviteWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchDueCalendarInvites(ctx)
		}
	}
}

func (s *Server) dispatchDueCalendarInvites(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, event_id, organizer_user_id::text, method, sequence
		FROM calendar_invite_jobs
		WHERE status='queued' AND due_at <= now()
		ORDER BY due_at
		LIMIT 50
	`)
	if err != nil {
		return
	}
	type job struct {
		id, eventID, organizerID, method string
		sequence                         int
	}
	jobs := []job{}
	for rows.Next() {
		var item job
		if rows.Scan(&item.id, &item.eventID, &item.organizerID, &item.method, &item.sequence) == nil {
			jobs = append(jobs, item)
		}
	}
	rows.Close()
	for _, item := range jobs {
		var claimed string
		if err := s.store.DB.QueryRow(ctx, `
			UPDATE calendar_invite_jobs SET status='sending', attempts=attempts+1
			WHERE id=$1 AND status='queued'
			RETURNING id
		`, item.id).Scan(&claimed); err != nil {
			continue
		}
		organizer, err := s.userByID(ctx, item.organizerID)
		if err != nil {
			_, _ = s.store.DB.Exec(ctx, `UPDATE calendar_invite_jobs SET status='skipped', last_error='organizer unavailable' WHERE id=$1`, item.id)
			continue
		}
		event, err := s.loadCalendarEvent(ctx, organizer, item.eventID)
		if err != nil || int(stateInt(event, "sequence")) != item.sequence {
			_, _ = s.store.DB.Exec(ctx, `UPDATE calendar_invite_jobs SET status='skipped', last_error='event changed or removed' WHERE id=$1`, item.id)
			continue
		}
		s.sendCalendarInvitePayload(ctx, organizer, event, item.method)
		_, _ = s.store.DB.Exec(ctx, `UPDATE calendar_invite_jobs SET status='sent', sent_at=now() WHERE id=$1`, item.id)
		s.publishRealtimeTo(organizer.ID, "calendar")
	}
}
