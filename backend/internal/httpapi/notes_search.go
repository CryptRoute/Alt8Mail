package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) noteMailboxes(ctx context.Context, user userRecord) []map[string]any {
	result := []map[string]any{{
		"id": user.ID, "name": user.Name, "email": user.Email,
		"initials": user.Initials, "color": user.Color,
		"canEdit": true, "self": true,
	}}
	seen := map[string]bool{user.ID: true}
	for _, delegation := range s.delegationsFor(ctx, user.TenantID, user.ID) {
		if seen[delegation.OwnerID] {
			continue
		}
		owner, err := s.userByID(ctx, delegation.OwnerID)
		if err != nil || owner.TenantID != user.TenantID {
			continue
		}
		seen[delegation.OwnerID] = true
		result = append(result, map[string]any{
			"id": owner.ID, "name": owner.Name, "email": owner.Email,
			"initials": owner.Initials, "color": owner.Color,
			"canEdit": delegation.CanSend, "self": false,
		})
	}
	return result
}

func (s *Server) searchStickyNotes(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.notesFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Sticky notes are turned off for this organization")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > 200 {
		writeError(w, http.StatusBadRequest, "QUERY_TOO_LONG", "Note searches are limited to 200 characters")
		return
	}
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope != "all" {
		scope = "mailbox"
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 100 {
		limit = 100
	}
	var archived *bool
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("archived"))) {
	case "1", "true", "archived":
		value := true
		archived = &value
	case "0", "false", "active":
		value := false
		archived = &value
	}

	owners := []userRecord{}
	canEdit := map[string]bool{}
	if scope == "all" {
		owners = append(owners, sess.User)
		canEdit[sess.User.ID] = true
		for _, delegation := range s.delegationsFor(r.Context(), sess.User.TenantID, sess.User.ID) {
			if _, exists := canEdit[delegation.OwnerID]; exists {
				continue
			}
			owner, err := s.userByID(r.Context(), delegation.OwnerID)
			if err != nil || owner.TenantID != sess.User.TenantID {
				continue
			}
			owners = append(owners, owner)
			canEdit[owner.ID] = delegation.CanSend
		}
	} else {
		owner, ok, _, writable := s.noteAccess(w, r, r.URL.Query().Get("ownerId"), false)
		if !ok {
			return
		}
		owners = append(owners, owner)
		canEdit[owner.ID] = writable
	}

	args := []any{}
	placeholders := make([]string, 0, len(owners))
	ownerByID := map[string]userRecord{}
	for _, owner := range owners {
		ownerByID[owner.ID] = owner
		args = append(args, owner.ID)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	where := []string{fmt.Sprintf("n.owner_user_id IN (%s)", strings.Join(placeholders, ","))}
	if archived != nil {
		args = append(args, *archived)
		where = append(where, fmt.Sprintf("(n.archived_at IS NOT NULL) = $%d", len(args)))
	}
	if query != "" {
		args = append(args, query)
		tsIndex := len(args)
		args = append(args, likePattern(query))
		likeIndex := len(args)
		where = append(where, fmt.Sprintf(
			`(n.search_vector @@ websearch_to_tsquery('simple', $%d) OR n.title ILIKE $%d ESCAPE '\' OR n.content ILIKE $%d ESCAPE '\' OR n.links::text ILIKE $%d ESCAPE '\')`,
			tsIndex, likeIndex, likeIndex, likeIndex))
	}
	args = append(args, limit)
	querySQL := `
		SELECT n.id, n.title, n.content, n.color, n.collapsed, n.inbox_pinned, n.position, n.links,
		       n.archived_at, n.updated_by_name, n.updated_at, n.created_at, n.revision,
		       n.owner_user_id::text,
		       (SELECT count(*) FROM public.mailbox_delegates d
		        WHERE d.tenant_id=n.tenant_id AND d.owner_user_id=n.owner_user_id) AS team_count
		FROM public.user_sticky_notes n
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY n.inbox_pinned DESC, n.updated_at DESC
		LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.store.DB.Query(r.Context(), querySQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE_SEARCH", "Could not search notes")
		return
	}
	defer rows.Close()
	notes := []any{}
	for rows.Next() {
		var row stickyNoteRow
		var ownerID string
		var teamCount int
		if rows.Scan(&row.ID, &row.Title, &row.Content, &row.Color, &row.Collapsed, &row.InboxPinned,
			&row.Position, &row.Links, &row.ArchivedAt, &row.UpdatedBy, &row.UpdatedAt,
			&row.CreatedAt, &row.Revision, &ownerID, &teamCount) != nil {
			continue
		}
		owner, ok := ownerByID[ownerID]
		if !ok {
			continue
		}
		if row.Title == "" {
			row.Title = stickyNoteTitle(row.Content)
		}
		note := stickyNotePublic(row, row.ArchivedAt != nil)
		note["owner"] = map[string]any{
			"id": owner.ID, "name": owner.Name, "email": owner.Email,
			"initials": owner.Initials, "color": owner.Color,
		}
		note["team"] = teamCount > 0
		note["teamCount"] = teamCount
		note["canEdit"] = canEdit[ownerID]
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE_SEARCH", "Could not search notes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": query, "scope": scope, "count": len(notes),
		"notes": notes, "mailboxes": s.noteMailboxes(r.Context(), sess.User),
	})
}

func (s *Server) getStickyNoteAttachCandidates(w http.ResponseWriter, r *http.Request) {
	owner, ok, _, _ := s.noteAccess(w, r, r.URL.Query().Get("ownerId"), false)
	if !ok {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > 100 {
		writeError(w, http.StatusBadRequest, "QUERY_TOO_LONG", "Attachment searches are limited to 100 characters")
		return
	}
	like := likePattern(query)
	items := []any{}
	switch kind {
	case "thread":
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT thread_id, subject, from_name, from_email, sent_at FROM (
				SELECT DISTINCT ON (thread_id) thread_id,
				       COALESCE(NULLIF(subject,''), '(no subject)') AS subject,
				       COALESCE(NULLIF(from_name,''), from_email::text) AS from_name,
				       from_email::text AS from_email, sent_at
				FROM public.mail_messages
				WHERE user_id=$1 AND ($2='' OR subject ILIKE $3 ESCAPE '\' OR from_name ILIKE $3 ESCAPE '\' OR from_email::text ILIKE $3 ESCAPE '\')
				ORDER BY thread_id, sent_at DESC
			) t ORDER BY sent_at DESC LIMIT 40
		`, owner.ID, query, like)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "STICKY_ATTACH", "Could not load conversations")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, subject, fromName, fromEmail string
			var sentAt time.Time
			if rows.Scan(&id, &subject, &fromName, &fromEmail, &sentAt) != nil {
				continue
			}
			items = append(items, map[string]any{
				"type": "thread", "id": id, "threadId": id,
				"subject": truncate(subject, 240), "fromName": truncate(fromName, 120),
				"fromEmail": truncate(fromEmail, 320), "date": sentAt.UTC().Format(time.RFC3339),
			})
		}
	case "event":
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT e.id, e.title, e.starts_at, e.ends_at, e.all_day, e.location, e.status, c.name
			FROM public.calendar_events e
			JOIN public.calendars c ON c.id=e.calendar_id AND c.user_id=e.user_id
			WHERE (e.user_id=$1
			  OR EXISTS (SELECT 1 FROM public.calendar_shares sh
			             WHERE sh.calendar_id=e.calendar_id AND sh.owner_user_id=e.user_id AND sh.grantee_user_id=$1)
			  OR EXISTS (SELECT 1 FROM public.calendar_attendees a
			             WHERE a.event_id=e.id AND lower(a.email)=lower($2)))
			  AND ($3='' OR e.title ILIKE $4 ESCAPE '\' OR e.location ILIKE $4 ESCAPE '\')
			ORDER BY e.starts_at DESC LIMIT 40
		`, owner.ID, owner.Email, query, like)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "STICKY_ATTACH", "Could not load events")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, title, location, status, calendarName string
			var startsAt, endsAt time.Time
			var allDay bool
			if rows.Scan(&id, &title, &startsAt, &endsAt, &allDay, &location, &status, &calendarName) != nil {
				continue
			}
			items = append(items, map[string]any{
				"type": "event", "id": id, "title": truncate(title, 240),
				"startAt": startsAt.UTC().Format(time.RFC3339), "endAt": endsAt.UTC().Format(time.RFC3339),
				"allDay": allDay, "location": truncate(location, 200),
				"calendarName": truncate(calendarName, 120), "status": status,
				"date": startsAt.UTC().Format("2006-01-02"),
			})
		}
	case "file":
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT id, name, mime, size_bytes, COALESCE(folder_id,''), updated_at
			FROM public.files
			WHERE user_id=$1 AND ($2='' OR name ILIKE $3 ESCAPE '\')
			ORDER BY updated_at DESC LIMIT 40
		`, owner.ID, query, like)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "STICKY_ATTACH", "Could not load files")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, mime, folderID string
			var sizeBytes int64
			var updatedAt time.Time
			if rows.Scan(&id, &name, &mime, &sizeBytes, &folderID, &updatedAt) != nil {
				continue
			}
			items = append(items, map[string]any{
				"type": "file", "id": id, "name": truncate(name, 240), "mime": mime,
				"sizeBytes": sizeBytes, "size": formatBytes(sizeBytes), "folderId": folderID,
				"date": updatedAt.UTC().Format(time.RFC3339),
			})
		}
	default:
		writeError(w, http.StatusBadRequest, "STICKY_LINK_TYPE", "Choose conversations, events or files")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": kind, "items": items})
}
