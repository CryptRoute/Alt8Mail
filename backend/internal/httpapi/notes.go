package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"
)

const maxStickyNotesPerMailbox = 100

type stickyNoteRow struct {
	ID          string
	Title       string
	Content     string
	Color       string
	Collapsed   bool
	InboxPinned bool
	Position    []byte
	Links       []byte
	ArchivedAt  *time.Time
	UpdatedBy   string
	UpdatedAt   time.Time
	CreatedAt   time.Time
	Revision    int
}

type stickyNoteBoard struct {
	Notes     []map[string]any
	Pinned    map[string]any
	Active    int
	Archived  int
	Shared    bool
	TeamCount int
	Owner     userRecord
}

type stickyNoteBody struct {
	OwnerID   string               `json:"ownerId"`
	Title     *string              `json:"title"`
	Content   *string              `json:"content"`
	Color     *string              `json:"color"`
	Collapsed *bool                `json:"collapsed"`
	PinInbox  *bool                `json:"inboxPinned"`
	Archived  *bool                `json:"archived"`
	Links     *[]stickyNoteLinkRef `json:"links"`
	Revision  *int                 `json:"revision"`
	Position  *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"position"`
}

func stickyNoteTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- [ ] ")
		line = strings.TrimPrefix(line, "- [x] ")
		line = strings.TrimPrefix(line, "- [X] ")
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimSpace(line)
		if line != "" {
			return truncate(line, 90)
		}
	}
	return "Untitled note"
}

func defaultNotePosition(index int) map[string]any {
	// Stagger cards so a fresh desk reads like a small stack rather than a
	// perfectly aligned grid.
	x := 0.66 - float64(index%4)*0.055
	y := 0.10 + float64(index%6)*0.09
	return map[string]any{"x": math.Max(0.04, math.Min(0.92, x)), "y": math.Max(0.04, math.Min(0.88, y))}
}

func notePositionJSON(position map[string]any) []byte {
	if position == nil {
		position = map[string]any{}
	}
	raw, _ := json.Marshal(position)
	return raw
}

func noteArchivedAt(archived bool, current *time.Time) any {
	if !archived {
		return nil
	}
	if current != nil {
		// Editing an archived note must not move it to the top of the archive.
		return *current
	}
	return time.Now().UTC()
}

func (s *Server) notesFeatureEnabled(ctx context.Context, tenantID string) bool {
	return s.tenantPolicyFor(ctx, tenantID).Features.StickyNotes
}

func (s *Server) noteAccess(w http.ResponseWriter, r *http.Request, ownerID string, write bool) (userRecord, bool, bool, bool) {
	sess := s.requireSessionFromContext(r)
	if !s.notesFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Sticky notes are turned off for this organization")
		return userRecord{}, false, false, false
	}
	owner, shared, err := s.collaborationOwner(r.Context(), sess.User, ownerID)
	if err != nil {
		writeFromError(w, err)
		return userRecord{}, false, false, false
	}
	canEdit := owner.ID == sess.User.ID
	if shared && owner.ID != sess.User.ID {
		delegation, ok := s.delegationFor(r.Context(), sess.User.TenantID, owner.ID, sess.User.ID)
		if !ok || !delegation.CanRead || (write && !delegation.CanSend) {
			writeError(w, http.StatusForbidden, "STICKY_NOTE_FORBIDDEN", "You do not have write access to this shared mailbox")
			return userRecord{}, false, false, false
		}
		canEdit = delegation.CanSend
	}
	return owner, true, shared, canEdit
}

func (s *Server) loadStickyNoteBoard(ctx context.Context, owner userRecord, includeArchived bool) (stickyNoteBoard, error) {
	if err := s.migrateLegacyStickyNote(ctx, owner); err != nil {
		return stickyNoteBoard{}, err
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, title, content, color, collapsed, inbox_pinned, position, links,
		       archived_at, updated_by_name, updated_at, created_at, revision
		FROM public.user_sticky_notes
		WHERE owner_user_id=$1
		ORDER BY inbox_pinned DESC, updated_at DESC
	`, owner.ID)
	if err != nil {
		return stickyNoteBoard{}, err
	}
	teamCount := s.stickyNoteTeamCount(ctx, owner)
	board := stickyNoteBoard{Owner: owner, Shared: teamCount > 0, TeamCount: teamCount}
	for rows.Next() {
		var row stickyNoteRow
		if rows.Scan(&row.ID, &row.Title, &row.Content, &row.Color, &row.Collapsed, &row.InboxPinned,
			&row.Position, &row.Links, &row.ArchivedAt, &row.UpdatedBy, &row.UpdatedAt,
			&row.CreatedAt, &row.Revision) != nil {
			continue
		}
		if row.ArchivedAt != nil {
			board.Archived++
		} else {
			board.Active++
		}
		if row.Title == "" {
			row.Title = stickyNoteTitle(row.Content)
		}
		note := stickyNotePublic(row, row.ArchivedAt != nil)
		if row.InboxPinned && row.ArchivedAt == nil {
			board.Pinned = note
		}
		if !includeArchived && row.ArchivedAt != nil {
			continue
		}
		board.Notes = append(board.Notes, note)
	}
	rows.Close()
	if board.Notes == nil {
		board.Notes = []map[string]any{}
	}
	return board, nil
}

func stickyNotePublic(row stickyNoteRow, archived bool) map[string]any {
	position := map[string]any{}
	_ = json.Unmarshal(row.Position, &position)
	links := normalizeStickyNoteLinks(row.Links)
	return map[string]any{
		"id": row.ID, "title": row.Title, "content": row.Content, "color": row.Color,
		"collapsed": row.Collapsed, "inboxPinned": row.InboxPinned, "archived": archived,
		"position": position, "links": links, "updatedBy": row.UpdatedBy,
		"updatedAt": row.UpdatedAt.UTC().Format(time.RFC3339),
		"createdAt": row.CreatedAt.UTC().Format(time.RFC3339), "revision": row.Revision,
	}
}

func (s *Server) noteBoardResponse(ctx context.Context, owner userRecord, includeArchived bool) (map[string]any, error) {
	board, err := s.loadStickyNoteBoard(ctx, owner, includeArchived)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"notes": board.Notes, "pinned": board.Pinned, "activeCount": board.Active,
		"archivedCount": board.Archived, "shared": board.Shared, "team": board.Shared,
		"teamCount": board.TeamCount,
		"owner": map[string]any{
			"id": owner.ID, "name": owner.Name, "email": owner.Email,
			"initials": owner.Initials, "color": owner.Color,
		},
	}
	return out, nil
}

func (s *Server) migrateLegacyStickyNote(ctx context.Context, owner userRecord) error {
	var count int
	if err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM public.user_sticky_notes WHERE owner_user_id=$1`, owner.ID).Scan(&count); err != nil || count > 0 {
		return err
	}
	state, ok := s.loadStateFor(ctx, owner.ID)
	if !ok {
		return nil
	}
	prefs := statePreferences(state, preferencesFor(owner))
	legacy, _ := prefs["stickyNote"].(map[string]any)
	if legacy == nil {
		return nil
	}
	content := truncate(strings.TrimRight(stateString(legacy, "content"), " \t\r\n"), 2000)
	linksRaw, _ := legacy["links"].([]any)
	if len(linksRaw) > 12 {
		linksRaw = linksRaw[:12]
	}
	if content == "" && len(linksRaw) == 0 {
		return nil
	}
	color := stateString(legacy, "color")
	if !stickyNoteColors[color] {
		color = "yellow"
	}
	enabled := true
	if value, ok := legacy["enabled"].(bool); ok {
		enabled = value
	}
	collapsed := stateBool(legacy, "collapsed")
	position := map[string]any{"x": 0.72, "y": 0.12}
	if raw, ok := legacy["position"].(map[string]any); ok {
		x, xOK := numericStateValue(raw["x"])
		y, yOK := numericStateValue(raw["y"])
		if xOK && yOK {
			position = map[string]any{"x": clampStickyPosition(x), "y": clampStickyPosition(y)}
		}
	}
	linksJSON, _ := json.Marshal(linksRaw)
	positionJSON, _ := json.Marshal(position)
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "notes-migrate:"+owner.ID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.user_sticky_notes WHERE owner_user_id=$1`, owner.ID).Scan(&count); err != nil || count > 0 {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO public.user_sticky_notes
		(id, tenant_id, owner_user_id, created_by, title, content, color, collapsed,
		 inbox_pinned, position, links, archived_at, updated_by_id, updated_by_name, revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULL,$3,$12,0)
	`, newID("note"), owner.TenantID, owner.ID, owner.ID, stickyNoteTitle(content), content,
		color, collapsed, enabled, positionJSON, linksJSON, owner.Name)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) getStickyNotes(w http.ResponseWriter, r *http.Request) {
	owner, ok, _, canEdit := s.noteAccess(w, r, r.URL.Query().Get("ownerId"), false)
	if !ok {
		return
	}
	includeArchived := r.URL.Query().Get("archived") == "1"
	response, err := s.noteBoardResponse(r.Context(), owner, includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not load sticky notes")
		return
	}
	response["canEdit"] = canEdit
	response["mailboxes"] = s.noteMailboxes(r.Context(), s.requireSessionFromContext(r).User)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) createStickyNote(w http.ResponseWriter, r *http.Request) {
	owner, ok, _, _ := s.noteAccess(w, r, r.URL.Query().Get("ownerId"), true)
	if !ok {
		return
	}
	var body stickyNoteBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	sess := s.requireSessionFromContext(r)
	content := ""
	if body.Content != nil {
		content = strings.TrimRight(*body.Content, " \t\r\n")
	}
	if len([]rune(content)) > 2000 {
		writeError(w, http.StatusBadRequest, "STICKY_NOTE", "Keep each sticky note under 2,000 characters")
		return
	}
	color := "yellow"
	if body.Color != nil {
		color = strings.ToLower(strings.TrimSpace(*body.Color))
	}
	if !stickyNoteColors[color] {
		writeError(w, http.StatusBadRequest, "STICKY_NOTE_COLOR", "Choose a valid sticky-note color")
		return
	}
	title := ""
	if body.Title != nil {
		title = truncate(strings.TrimSpace(*body.Title), 90)
	}
	if title == "" {
		title = stickyNoteTitle(content)
	}
	linksJSON := []byte("[]")
	if body.Links != nil {
		built, err := s.buildStickyNoteLinks(r.Context(), owner, *body.Links)
		if err != nil {
			writeFromError(w, err)
			return
		}
		linksJSON, _ = json.Marshal(built)
	}
	id := newID("note")
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not create the note")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "notes-pin:"+owner.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not pin the note")
		return
	}
	// Count and default-pin decisions happen under the per-mailbox lock so two
	// simultaneous tabs cannot both claim the Inbox slot or exceed the cap.
	var count int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM public.user_sticky_notes WHERE owner_user_id=$1`, owner.ID).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not create the note")
		return
	}
	if count >= maxStickyNotesPerMailbox {
		writeError(w, http.StatusBadRequest, "STICKY_NOTE_LIMIT", "You can keep up to 100 notes in this mailbox")
		return
	}
	pinned := false
	if body.PinInbox != nil {
		pinned = *body.PinInbox
	} else {
		var pinnedCount int
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*) FROM public.user_sticky_notes
			WHERE owner_user_id=$1 AND inbox_pinned=true AND archived_at IS NULL
		`, owner.ID).Scan(&pinnedCount); err != nil {
			writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not create the note")
			return
		}
		pinned = pinnedCount == 0
	}
	position := defaultNotePosition(count)
	if body.Position != nil {
		position = map[string]any{"x": clampStickyPosition(body.Position.X), "y": clampStickyPosition(body.Position.Y)}
	}
	if pinned {
		if _, err := tx.Exec(r.Context(), `
			UPDATE public.user_sticky_notes SET inbox_pinned=false, updated_at=now()
			WHERE owner_user_id=$1 AND inbox_pinned=true AND archived_at IS NULL
		`, owner.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not pin the note")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO public.user_sticky_notes
		(id, tenant_id, owner_user_id, created_by, title, content, color, collapsed,
		 inbox_pinned, position, links, updated_by_id, updated_by_name, revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,false,$8,$9,$11,$4,$10,0)
	`, id, owner.TenantID, owner.ID, sess.User.ID, title, content, color, pinned,
		notePositionJSON(position), sess.User.Name, linksJSON); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not create the note")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not commit the note")
		return
	}
	s.insertAudit(sess.User, "sticky_note.created", title, "Created a sticky note", clientIP(r), sanitizeUserAgent(r.UserAgent()))
	response, err := s.noteBoardResponse(r.Context(), owner, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Note saved but the desk could not be refreshed")
		return
	}
	response["created"] = id
	response["canEdit"] = true
	response["mailboxes"] = s.noteMailboxes(r.Context(), sess.User)
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) patchStickyNote(w http.ResponseWriter, r *http.Request) {
	owner, ok, _, _ := s.noteAccess(w, r, r.URL.Query().Get("ownerId"), true)
	if !ok {
		return
	}
	noteID := r.PathValue("id")
	var body stickyNoteBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	sess := s.requireSessionFromContext(r)
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not update the note")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "notes-pin:"+owner.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not pin the note")
		return
	}
	// Read the note under the per-mailbox lock so two tabs cannot overwrite
	// each other's pin, archive or edit decisions.
	var row stickyNoteRow
	var archived *time.Time
	err = tx.QueryRow(r.Context(), `
		SELECT id, title, content, color, collapsed, inbox_pinned, position, links,
		       archived_at, updated_by_name, updated_at, created_at, revision
		FROM public.user_sticky_notes WHERE id=$1 AND owner_user_id=$2
	`, noteID, owner.ID).Scan(&row.ID, &row.Title, &row.Content, &row.Color, &row.Collapsed,
		&row.InboxPinned, &row.Position, &row.Links, &archived, &row.UpdatedBy,
		&row.UpdatedAt, &row.CreatedAt, &row.Revision)
	if err != nil {
		writeError(w, http.StatusNotFound, "STICKY_NOTE_NOT_FOUND", "Sticky note not found")
		return
	}
	if body.Content != nil {
		row.Content = strings.TrimRight(*body.Content, " \t\r\n")
		if len([]rune(row.Content)) > 2000 {
			writeError(w, http.StatusBadRequest, "STICKY_NOTE", "Keep each sticky note under 2,000 characters")
			return
		}
		row.Title = stickyNoteTitle(row.Content)
	}
	if body.Title != nil {
		row.Title = truncate(strings.TrimSpace(*body.Title), 90)
	}
	if body.Color != nil {
		color := strings.ToLower(strings.TrimSpace(*body.Color))
		if !stickyNoteColors[color] {
			writeError(w, http.StatusBadRequest, "STICKY_NOTE_COLOR", "Choose a valid sticky-note color")
			return
		}
		row.Color = color
	}
	if body.Collapsed != nil {
		row.Collapsed = *body.Collapsed
	}
	pinned := row.InboxPinned
	if body.PinInbox != nil {
		pinned = *body.PinInbox
	}
	archivedFlag := archived != nil
	if body.Archived != nil {
		archivedFlag = *body.Archived
	}
	if archivedFlag {
		if body.PinInbox != nil && *body.PinInbox {
			writeError(w, http.StatusBadRequest, "STICKY_NOTE_STATE", "Restore the note before pinning it")
			return
		}
		pinned = false
	}
	position := row.Position
	if body.Position != nil {
		position = notePositionJSON(map[string]any{
			"x": clampStickyPosition(body.Position.X), "y": clampStickyPosition(body.Position.Y),
		})
	}
	links := row.Links
	if body.Links != nil {
		built, err := s.buildStickyNoteLinks(r.Context(), owner, *body.Links)
		if err != nil {
			writeFromError(w, err)
			return
		}
		links, _ = json.Marshal(built)
	}
	if pinned {
		if _, err := tx.Exec(r.Context(), `
			UPDATE public.user_sticky_notes SET inbox_pinned=false, updated_at=now()
			WHERE owner_user_id=$1 AND inbox_pinned=true AND id<>$2 AND archived_at IS NULL
		`, owner.ID, noteID); err != nil {
			writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not pin the note")
			return
		}
	}
	archivedAtValue := noteArchivedAt(archivedFlag, archived)
	if _, err := tx.Exec(r.Context(), `
		UPDATE public.user_sticky_notes
		SET title=$3, content=$4, color=$5, collapsed=$6, inbox_pinned=$7,
		    position=$8, links=$9, archived_at=$10, updated_by_id=$11,
		    updated_by_name=$12, revision=revision+1, updated_at=now()
		WHERE id=$1 AND owner_user_id=$2
	`, noteID, owner.ID, row.Title, row.Content, row.Color, row.Collapsed, pinned,
		position, links, archivedAtValue, sess.User.ID, sess.User.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not update the note")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Could not commit the note")
		return
	}
	action := "sticky_note.updated"
	detail := "Updated a sticky note"
	if body.Archived != nil && *body.Archived {
		action, detail = "sticky_note.archived", "Archived a sticky note"
	} else if body.Archived != nil && !*body.Archived {
		action, detail = "sticky_note.restored", "Restored a sticky note"
	} else if pinned && !row.InboxPinned {
		action, detail = "sticky_note.pinned", "Pinned a sticky note to Inbox"
	}
	s.insertAudit(sess.User, action, row.Title, detail, clientIP(r), sanitizeUserAgent(r.UserAgent()))
	response, err := s.noteBoardResponse(r.Context(), owner, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STICKY_NOTE", "Note saved but the desk could not be refreshed")
		return
	}
	response["updated"] = noteID
	response["canEdit"] = true
	response["mailboxes"] = s.noteMailboxes(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, response)
}
