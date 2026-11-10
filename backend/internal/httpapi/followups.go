package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type followUpRecord struct {
	ID          string
	UserID      string
	OwnerID     string
	ThreadID    string
	Kind        string
	DueAt       time.Time
	Note        string
	Status      string
	CreatedAt   time.Time
	ReplyID     string
	CompletedAt *time.Time
}

func (s *Server) followUpViews(ctx context.Context, userID string) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		SELECT f.id, f.thread_id, f.owner_user_id::text, f.kind, f.due_at, f.note, f.status,
		       COALESCE(m.subject,'')
		FROM public.follow_ups f
		LEFT JOIN LATERAL (
			SELECT subject FROM public.mail_messages
			WHERE user_id=f.owner_user_id AND thread_id=f.thread_id
			ORDER BY sent_at DESC LIMIT 1
		) m ON true
		WHERE f.user_id=$1 AND f.status IN ('pending','notified')
		ORDER BY f.due_at
	`, userID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, threadID, ownerID, kind, note, status, subject string
		var due time.Time
		if rows.Scan(&id, &threadID, &ownerID, &kind, &due, &note, &status, &subject) == nil {
			items = append(items, map[string]any{
				"id": id, "threadId": threadID, "ownerId": ownerID, "kind": kind,
				"dueAt": due.UTC().Format(time.RFC3339), "note": note, "status": status,
				"subject": subject,
			})
		}
	}
	return items
}

func (s *Server) listFollowUps(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{"followUps": s.followUpViews(r.Context(), sess.User.ID)})
}

func (s *Server) createFollowUp(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		OwnerID   string `json:"ownerId"`
		ThreadID  string `json:"threadId"`
		Kind      string `json:"kind"`
		Days      int    `json:"days"`
		DueAt     string `json:"dueAt"`
		Note      string `json:"note"`
		MessageID string `json:"messageId"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, body.OwnerID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(body.ThreadID)
	if threadID == "" || !s.threadBelongsTo(r.Context(), owner.ID, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind == "" {
		kind = "awaiting_reply"
	}
	if kind != "awaiting_reply" && kind != "remind_me" {
		writeError(w, http.StatusBadRequest, "FOLLOWUP_KIND", "Follow-up kind must be awaiting_reply or remind_me")
		return
	}
	due := time.Now().Add(3 * 24 * time.Hour)
	if body.Days != 0 {
		if body.Days < 1 || body.Days > 90 {
			writeError(w, http.StatusBadRequest, "FOLLOWUP_DAYS", "Follow-up must be between 1 and 90 days")
			return
		}
		due = time.Now().Add(time.Duration(body.Days) * 24 * time.Hour)
	}
	if raw := strings.TrimSpace(body.DueAt); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil || !parsed.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "FOLLOWUP_DUE", "dueAt must be a future RFC3339 timestamp")
			return
		}
		due = parsed
	}
	var existing string
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT id FROM public.follow_ups
		WHERE user_id=$1 AND thread_id=$2 AND kind=$3 AND status IN ('pending','notified')
		LIMIT 1
	`, sess.User.ID, threadID, kind).Scan(&existing)
	if existing != "" {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE public.follow_ups SET due_at=$2, note=$3, status='pending', notified_at=NULL, updated_at=now()
			WHERE id=$1
		`, existing, due, truncate(strings.TrimSpace(body.Note), 500))
		s.audit(r, sess.User, "followup.updated", threadID, "Updated a follow-up")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": existing, "duplicate": true})
		return
	}
	id := newID("followup")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.follow_ups (id, tenant_id, user_id, owner_user_id, thread_id, message_id, kind, due_at, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, id, sess.User.TenantID, sess.User.ID, owner.ID, threadID, truncate(strings.TrimSpace(body.MessageID), 120), kind, due, truncate(strings.TrimSpace(body.Note), 500))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLLOWUP", "Could not create the follow-up")
		return
	}
	s.audit(r, sess.User, "followup.created", threadID, "Created a "+kind+" follow-up")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) cancelFollowUp(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.follow_ups SET status='cancelled', updated_at=now()
		WHERE id=$1 AND user_id=$2 AND status IN ('pending','notified')
	`, r.PathValue("id"), sess.User.ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "FOLLOWUP_NOT_FOUND", "Follow-up not found")
		return
	}
	s.audit(r, sess.User, "followup.cancelled", r.PathValue("id"), "Cancelled a follow-up")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) followupWorker(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runFollowUpChecks(ctx)
		}
	}
}

func (s *Server) runFollowUpChecks(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT f.id, f.user_id::text, f.owner_user_id::text, f.thread_id, f.kind, f.due_at,
		       f.note, f.status, f.created_at, owner.email::text
		FROM public.follow_ups f
		JOIN public.users owner ON owner.id=f.owner_user_id
		WHERE f.status IN ('pending','notified')
		ORDER BY f.due_at LIMIT 500
	`)
	if err != nil {
		return
	}
	items := []struct {
		id, userID, ownerID, threadID, kind, note, status, ownerEmail string
		due, created                                                  time.Time
	}{}
	for rows.Next() {
		var item struct {
			id, userID, ownerID, threadID, kind, note, status, ownerEmail string
			due, created                                                  time.Time
		}
		if rows.Scan(&item.id, &item.userID, &item.ownerID, &item.threadID, &item.kind, &item.due,
			&item.note, &item.status, &item.created, &item.ownerEmail) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	for _, item := range items {
		if item.kind == "awaiting_reply" {
			var replyID string
			_ = s.store.DB.QueryRow(ctx, `
				SELECT id FROM public.mail_messages
				WHERE user_id=$1 AND thread_id=$2 AND folder='inbox'
				  AND sent_at > $3 AND lower(from_email::text) <> lower($4)
				ORDER BY sent_at ASC LIMIT 1
			`, item.ownerID, item.threadID, item.created, item.ownerEmail).Scan(&replyID)
			if replyID != "" {
				_, _ = s.store.DB.Exec(ctx, `
					UPDATE public.follow_ups SET status='completed', completed_at=now(), reply_message_id=$2, updated_at=now()
					WHERE id=$1
				`, item.id, replyID)
				continue
			}
		}
		if item.status != "pending" || item.due.After(time.Now()) {
			continue
		}
		var claimed bool
		_ = s.store.DB.QueryRow(ctx, `
			UPDATE public.follow_ups SET status='notified', notified_at=now(), updated_at=now()
			WHERE id=$1 AND status='pending' RETURNING true
		`, item.id).Scan(&claimed)
		if !claimed {
			continue
		}
		subject := ""
		_ = s.store.DB.QueryRow(ctx, `
			SELECT subject FROM public.mail_messages WHERE user_id=$1 AND thread_id=$2 ORDER BY sent_at DESC LIMIT 1
		`, item.ownerID, item.threadID).Scan(&subject)
		title := "Follow up on this conversation"
		if item.kind == "remind_me" {
			title = "Remember to reply"
		}
		body := firstNonEmpty(subject, "(no subject)")
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_notifications
			  (id, tenant_id, user_id, kind, title, body, detail, thread_id, owner_user_id)
			VALUES ($1,(SELECT tenant_id FROM public.users WHERE id=$2),$2,'followup',$3,$4,$5,$6,$7)
		`, newID("notification"), item.userID, title, body, "Follow-up due", item.threadID, item.ownerID)
	}
}

func (s *Server) registerFollowUpRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/followups", s.requireUser(s.listFollowUps))
	mux.HandleFunc("POST /api/followups", s.requireUser(s.createFollowUp))
	mux.HandleFunc("DELETE /api/followups/{id}", s.requireUser(s.cancelFollowUp))
}
