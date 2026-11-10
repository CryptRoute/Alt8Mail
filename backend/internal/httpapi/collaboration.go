package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) collaborationOwner(ctx context.Context, user userRecord, ownerID string) (userRecord, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || ownerID == user.ID {
		return user, true, nil
	}
	if _, allowed := s.delegationFor(ctx, user.TenantID, ownerID, user.ID); !allowed {
		return userRecord{}, false, fail(http.StatusForbidden, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox")
	}
	owner, err := s.userByID(ctx, ownerID)
	if err != nil || owner.TenantID != user.TenantID {
		return userRecord{}, false, fail(http.StatusNotFound, "THREAD_OWNER", "The shared mailbox is no longer available")
	}
	return owner, true, nil
}

func (s *Server) threadBelongsTo(ctx context.Context, ownerID, threadID string) bool {
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.mail_messages WHERE user_id=$1 AND thread_id=$2)
	`, ownerID, threadID).Scan(&exists)
	return exists
}

func (s *Server) loadThreadMeta(ctx context.Context, ownerID, threadID string) map[string]any {
	row := map[string]any{
		"ownerId": ownerID, "threadId": threadID, "status": "new", "assignedTo": nil, "closed": false,
	}
	var status, assignedID, assignedName, assignedEmail string
	var closedAt, updatedAt *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT m.status, COALESCE(m.assigned_user_id::text,''), COALESCE(a.name,''), COALESCE(a.email::text,''),
		       m.closed_at, m.updated_at
		FROM public.mail_thread_meta m LEFT JOIN public.users a ON a.id=m.assigned_user_id
		WHERE m.owner_user_id=$1 AND m.thread_id=$2
	`, ownerID, threadID).Scan(&status, &assignedID, &assignedName, &assignedEmail, &closedAt, &updatedAt)
	if err == nil {
		row["status"] = status
		if assignedID != "" {
			row["assignedTo"] = map[string]any{"id": assignedID, "name": assignedName, "email": assignedEmail}
		}
		row["closed"] = closedAt != nil
		if closedAt != nil {
			row["closedAt"] = closedAt.UTC().Format(time.RFC3339)
		}
		if updatedAt != nil {
			row["updatedAt"] = updatedAt.UTC().Format(time.RFC3339)
		}
	}
	return row
}

func (s *Server) loadThreadMetaForOwners(ctx context.Context, ownerIDs []string) []map[string]any {
	if len(ownerIDs) == 0 {
		return []map[string]any{}
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT m.owner_user_id::text, m.thread_id, m.status,
		       COALESCE(m.assigned_user_id::text,''), COALESCE(a.name,''), COALESCE(a.email::text,''),
		       m.closed_at, m.updated_at
		FROM public.mail_thread_meta m LEFT JOIN public.users a ON a.id=m.assigned_user_id
		WHERE m.owner_user_id::text = ANY($1::text[])
		ORDER BY m.updated_at DESC
	`, ownerIDs)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var ownerID, threadID, status, assignedID, assignedName, assignedEmail string
		var closedAt, updatedAt *time.Time
		if rows.Scan(&ownerID, &threadID, &status, &assignedID, &assignedName, &assignedEmail, &closedAt, &updatedAt) != nil {
			continue
		}
		item := map[string]any{
			"ownerId": ownerID, "threadId": threadID, "status": status, "closed": closedAt != nil,
			"assignedTo": nil,
		}
		if assignedID != "" {
			item["assignedTo"] = map[string]any{"id": assignedID, "name": assignedName, "email": assignedEmail}
		}
		if closedAt != nil {
			item["closedAt"] = closedAt.UTC().Format(time.RFC3339)
		}
		if updatedAt != nil {
			item["updatedAt"] = updatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) collaborationThreadMeta(ctx context.Context, user userRecord) []map[string]any {
	owners := []string{user.ID}
	for _, delegation := range s.delegationsFor(ctx, user.TenantID, user.ID) {
		owners = append(owners, delegation.OwnerID)
	}
	return s.loadThreadMetaForOwners(ctx, owners)
}

func (s *Server) registerCollaborationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/thread-meta", s.requireUser(s.getThreadMeta))
	mux.HandleFunc("PATCH /api/threads/{id}/meta", s.requireUser(s.patchThreadMeta))
	mux.HandleFunc("GET /api/thread-notes", s.requireUser(s.getThreadNotes))
	mux.HandleFunc("POST /api/threads/{id}/notes", s.requireUser(s.createThreadNote))
	mux.HandleFunc("DELETE /api/threads/{id}/notes/{noteId}", s.requireUser(s.deleteThreadNote))
	mux.HandleFunc("GET /api/notifications", s.requireUser(s.listNotifications))
	mux.HandleFunc("POST /api/notifications/{id}/read", s.requireUser(s.readNotification))
	mux.HandleFunc("POST /api/notifications/read-all", s.requireUser(s.readAllNotifications))
}

func (s *Server) getThreadMeta(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, r.URL.Query().Get("ownerId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("threadId"))
	if threadID != "" {
		writeJSON(w, http.StatusOK, map[string]any{"meta": s.loadThreadMeta(r.Context(), owner.ID, threadID)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": s.loadThreadMetaForOwners(r.Context(), []string{owner.ID})})
}

func (s *Server) patchThreadMeta(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		OwnerID    string  `json:"ownerId"`
		Status     *string `json:"status"`
		AssignedTo *string `json:"assignedTo"`
		Closed     *bool   `json:"closed"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if body.Status == nil && body.AssignedTo == nil && body.Closed == nil {
		writeError(w, http.StatusBadRequest, "THREAD_META", "Choose a status, assignee or close state to update")
		return
	}
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, body.OwnerID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("id"))
	if threadID == "" || !s.threadBelongsTo(r.Context(), owner.ID, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	current := s.loadThreadMeta(r.Context(), owner.ID, threadID)
	currentStatus := firstNonEmpty(stateString(current, "status"), "new")
	nextStatus := currentStatus
	var statusValue any
	if body.Status != nil {
		next := strings.ToLower(strings.TrimSpace(*body.Status))
		if next != "new" && next != "in_progress" && next != "done" {
			writeError(w, http.StatusBadRequest, "THREAD_STATUS", "Status must be new, in_progress or done")
			return
		}
		nextStatus, statusValue = next, next
	}
	var assignedID, assignedBy, assignedAt any
	assignedName := ""
	currentAssigneeID := ""
	if currentAssigned, ok := current["assignedTo"].(map[string]any); ok {
		currentAssigneeID = stateString(currentAssigned, "id")
	}
	notifyAssigneeID, notifyAssigneeName := "", ""
	assignedProvided := body.AssignedTo != nil
	if assignedProvided {
		email := normalizeEmailValue(*body.AssignedTo)
		if email != "" {
			if team, isTeam := s.teamMailboxForOwner(r.Context(), owner.TenantID, owner.ID); isTeam {
				var member bool
				_ = s.store.DB.QueryRow(r.Context(), `
					SELECT EXISTS(
						SELECT 1 FROM public.group_members gm
						JOIN public.users u ON u.id = gm.user_id
						WHERE gm.group_id=$1 AND lower(u.email::text)=$2
						  AND u.status='active' AND u.deleted_at IS NULL
					)
				`, team.GroupID, email).Scan(&member)
				if !member {
					writeError(w, http.StatusBadRequest, "THREAD_ASSIGNEE", "Choose an active agent in this team inbox")
					return
				}
			}
			if err := s.store.DB.QueryRow(r.Context(), `
				SELECT id::text, name FROM public.users
				WHERE tenant_id=$1 AND lower(email::text)=lower($2) AND status='active' AND deleted_at IS NULL
			`, owner.TenantID, email).Scan(&assignedID, &assignedName); err != nil {
				writeError(w, http.StatusBadRequest, "THREAD_ASSIGNEE", "Choose an active person in this organization")
				return
			}
			assignedBy, assignedAt = sess.User.ID, time.Now().UTC()
			if targetID := stateStringOf(assignedID); targetID != sess.User.ID && targetID != currentAssigneeID {
				notifyAssigneeID, notifyAssigneeName = targetID, assignedName
			}
		}
	}
	var closedAt, closedBy any
	closedProvided := body.Closed != nil
	if closedProvided && *body.Closed {
		closedAt, closedBy = time.Now().UTC(), sess.User.ID
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_thread_meta
		  (owner_user_id, thread_id, status, assigned_user_id, assigned_by, assigned_at, closed_at, closed_by, updated_at)
		VALUES ($1,$2,COALESCE($3,'new'),$4,$5,$6,$7,$8,now())
		ON CONFLICT (owner_user_id, thread_id) DO UPDATE SET
		  status=COALESCE($3, public.mail_thread_meta.status),
		  assigned_user_id=CASE WHEN $9 THEN $4 ELSE public.mail_thread_meta.assigned_user_id END,
		  assigned_by=CASE WHEN $9 THEN $5 ELSE public.mail_thread_meta.assigned_by END,
		  assigned_at=CASE WHEN $9 THEN $6 ELSE public.mail_thread_meta.assigned_at END,
		  closed_at=CASE WHEN $10 THEN $7 ELSE public.mail_thread_meta.closed_at END,
		  closed_by=CASE WHEN $10 THEN $8 ELSE public.mail_thread_meta.closed_by END,
		  updated_at=now()
	`, owner.ID, threadID, statusValue, assignedID, assignedBy, assignedAt, closedAt, closedBy, assignedProvided, closedProvided)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if notifyAssigneeID != "" {
		s.createAssignmentNotification(r.Context(), sess.User, owner, threadID, notifyAssigneeID, notifyAssigneeName, nextStatus)
	}
	changes := []string{}
	if body.Status != nil {
		changes = append(changes, "status "+nextStatus)
	}
	if assignedProvided {
		changes = append(changes, "assignee "+firstNonEmpty(assignedName, "unassigned"))
	}
	if closedProvided {
		if *body.Closed {
			changes = append(changes, "closed")
		} else {
			changes = append(changes, "reopened")
		}
	}
	s.audit(r, sess.User, "thread.meta_updated", threadID, "Updated handling: "+strings.Join(changes, ", "))
	if body.Status != nil && nextStatus != "new" {
		s.markTeamThreadResponded(r.Context(), owner.TenantID, owner.ID, threadID, sess.User.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "meta": s.loadThreadMeta(r.Context(), owner.ID, threadID)})
}

func stateStringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func normalizeEmailValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (s *Server) threadSubject(ctx context.Context, ownerID, threadID string) (subject, snippet, messageID string) {
	_ = s.store.DB.QueryRow(ctx, `
		SELECT subject, snippet, id FROM public.mail_messages
		WHERE user_id=$1 AND thread_id=$2 ORDER BY sent_at DESC LIMIT 1
	`, ownerID, threadID).Scan(&subject, &snippet, &messageID)
	return
}

func (s *Server) createAssignmentNotification(ctx context.Context, actor userRecord, owner userRecord, threadID, targetID, targetName, status string) {
	subject, snippet, messageID := s.threadSubject(ctx, owner.ID, threadID)
	title := firstNonEmpty(actor.Name, actor.Email) + " assigned you a conversation"
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.mail_notifications
		  (id, tenant_id, user_id, kind, title, body, detail, actor_user_id, actor_name, actor_email,
		   thread_id, message_id, owner_user_id)
		VALUES ($1,$2,$3,'assignment',$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, newID("notification"), owner.TenantID, targetID, title, firstNonEmpty(subject, "(no subject)"),
		firstNonEmpty(snippet, "Status: "+status), actor.ID, actor.Name, actor.Email, threadID, messageID, owner.ID)
}

func (s *Server) getThreadNotes(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, r.URL.Query().Get("ownerId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("threadId"))
	if threadID == "" {
		writeError(w, http.StatusBadRequest, "THREAD", "A conversation is required")
		return
	}
	notes := s.loadThreadNotes(r.Context(), owner.ID, threadID)
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

func (s *Server) loadThreadNotes(ctx context.Context, ownerID, threadID string) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, author_user_id::text, author_name, author_email::text, body, created_at
		FROM public.mail_thread_notes
		WHERE owner_user_id=$1 AND thread_id=$2 AND deleted_at IS NULL
		ORDER BY created_at ASC
	`, ownerID, threadID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, authorID, authorName, authorEmail, body string
		var created time.Time
		if rows.Scan(&id, &authorID, &authorName, &authorEmail, &body, &created) == nil {
			out = append(out, map[string]any{
				"id": id, "authorId": authorID, "authorName": authorName, "authorEmail": authorEmail,
				"body": body, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	return out
}

func (s *Server) createThreadNote(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		OwnerID string `json:"ownerId"`
		Body    string `json:"body"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, body.OwnerID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("id"))
	if !s.threadBelongsTo(r.Context(), owner.ID, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" || len([]rune(text)) > 2000 {
		writeError(w, http.StatusBadRequest, "THREAD_NOTE", "Write a note of 1–2000 characters")
		return
	}
	noteID := newID("note")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_thread_notes
		  (id, owner_user_id, thread_id, author_user_id, author_name, author_email, body)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, noteID, owner.ID, threadID, sess.User.ID, sess.User.Name, sess.User.Email, text)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.audit(r, sess.User, "thread.note_added", threadID, "Added an internal team note")
	subject, _, _ := s.threadSubject(r.Context(), owner.ID, threadID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": map[string]any{
		"id": noteID, "authorId": sess.User.ID, "authorName": sess.User.Name,
		"authorEmail": sess.User.Email, "body": text, "createdAt": time.Now().UTC().Format(time.RFC3339),
		"subject": subject,
	}})
}

func (s *Server) deleteThreadNote(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		OwnerID string `json:"ownerId"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&body)
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, body.OwnerID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("id"))
	noteID := strings.TrimSpace(r.PathValue("noteId"))
	var authorID string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT author_user_id::text FROM public.mail_thread_notes
		WHERE id=$1 AND owner_user_id=$2 AND thread_id=$3 AND deleted_at IS NULL
	`, noteID, owner.ID, threadID).Scan(&authorID)
	if err != nil {
		writeError(w, http.StatusNotFound, "THREAD_NOTE_NOT_FOUND", "Team note not found")
		return
	}
	if authorID != sess.User.ID && owner.ID != sess.User.ID {
		writeError(w, http.StatusForbidden, "THREAD_NOTE_FORBIDDEN", "Only the author or mailbox owner can remove this note")
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.mail_thread_notes SET deleted_at=now(), deleted_by=$2 WHERE id=$1
	`, noteID, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.audit(r, sess.User, "thread.note_deleted", threadID, "Removed an internal team note")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{"notifications": s.loadMailNotifications(r.Context(), sess.User.ID, 100)})
}

func (s *Server) loadMailNotifications(ctx context.Context, userID string, limit int) []map[string]any {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, kind, title, body, detail, actor_name, COALESCE(actor_email::text,''),
		       thread_id, message_id, COALESCE(owner_user_id::text,''), read_at, created_at
		FROM public.mail_notifications
		WHERE user_id=$1
		ORDER BY created_at DESC LIMIT $2
	`, userID, limit)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, kind, title, body, detail, actorName, actorEmail, threadID, messageID, ownerID string
		var readAt *time.Time
		var created time.Time
		if rows.Scan(&id, &kind, &title, &body, &detail, &actorName, &actorEmail,
			&threadID, &messageID, &ownerID, &readAt, &created) == nil {
			item := map[string]any{
				"id": id, "kind": kind, "title": title, "body": body, "detail": detail,
				"actorName": actorName, "actorEmail": actorEmail, "threadId": threadID,
				"messageId": messageID, "ownerId": ownerID, "createdAt": created.UTC().Format(time.RFC3339),
			}
			if readAt != nil {
				item["readAt"] = readAt.UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.mail_notifications SET read_at=now()
		WHERE id=$1 AND user_id=$2
	`, strings.TrimSpace(r.PathValue("id")), sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.mail_notifications SET read_at=now()
		WHERE user_id=$1 AND read_at IS NULL
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func sanitizeMentionInput(values []any) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		email := ""
		switch value := raw.(type) {
		case string:
			email = value
		case map[string]any:
			email = stateString(value, "email")
		}
		email = normalizeEmailValue(email)
		if email == "" || !looksLikeEmail(email) || seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, email)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func (s *Server) createMentionNotifications(ctx context.Context, sender userRecord, item map[string]any, sentID string) {
	emails := []string{}
	seen := map[string]bool{}
	for _, raw := range stateSlice(item, "mentions") {
		email := ""
		switch value := raw.(type) {
		case string:
			email = value
		case map[string]any:
			email = stateString(value, "email")
		}
		email = normalizeEmailValue(email)
		if email == "" || email == normalizeEmailValue(sender.Email) || seen[email] || !looksLikeEmail(email) {
			continue
		}
		seen[email] = true
		emails = append(emails, email)
	}
	if len(emails) == 0 {
		return
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id::text, email::text, name FROM public.users
		WHERE tenant_id=$1 AND status='active' AND deleted_at IS NULL
		  AND lower(email::text) = ANY($2::text[])
	`, sender.TenantID, emails)
	if err != nil {
		return
	}
	type target struct{ id, email, name string }
	targets := []target{}
	for rows.Next() {
		var item target
		if rows.Scan(&item.id, &item.email, &item.name) == nil {
			targets = append(targets, item)
		}
	}
	rows.Close()
	if len(targets) == 0 {
		return
	}
	threadID := strings.TrimSpace(stateString(item, "threadId"))
	subject := stateString(item, "subject")
	snippet := ""
	if sentID != "" {
		_ = s.store.DB.QueryRow(ctx, `
			SELECT thread_id, subject, snippet FROM public.mail_messages WHERE id=$1 AND user_id=$2
		`, sentID, sender.ID).Scan(&threadID, &subject, &snippet)
	}
	actorName := firstNonEmpty(sender.Name, sender.Email)
	for _, target := range targets {
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_notifications
			  (id, tenant_id, user_id, kind, title, body, detail, actor_user_id, actor_name, actor_email,
			   thread_id, message_id, owner_user_id)
			VALUES ($1,$2,$3,'mention',$4,$5,$6,$7,$8,$9,$10,$11,$12)
		`, newID("notification"), sender.TenantID, target.id,
			actorName+" mentioned you", firstNonEmpty(subject, "(no subject)"),
			firstNonEmpty(snippet, "Open the conversation to see the context."),
			sender.ID, actorName, sender.Email, threadID, sentID, sender.ID)
	}
}
