package httpapi

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"
)

var stickyNoteColors = map[string]bool{"yellow": true, "blue": true, "pink": true, "green": true}

func defaultStickyNote() map[string]any {
	return map[string]any{
		"content": "", "color": "yellow", "collapsed": false, "enabled": true,
		"links": []any{}, "position": map[string]any{"x": 0.72, "y": 0.12},
		"updatedBy": "", "updatedAt": "",
	}
}

func normalizeStickyNote(raw any) map[string]any {
	note := defaultStickyNote()
	stored, _ := raw.(map[string]any)
	if stored == nil {
		return note
	}
	if value, ok := stored["content"].(string); ok {
		note["content"] = truncate(value, 2000)
	}
	if value, ok := stored["color"].(string); ok && stickyNoteColors[value] {
		note["color"] = value
	}
	if value, ok := stored["collapsed"].(bool); ok {
		note["collapsed"] = value
	}
	if value, ok := stored["enabled"].(bool); ok {
		note["enabled"] = value
	}
	if value, ok := stored["links"].([]any); ok {
		note["links"] = value
	}
	if value, ok := stored["position"].(map[string]any); ok {
		x, xOK := numericStateValue(value["x"])
		y, yOK := numericStateValue(value["y"])
		if xOK && yOK {
			note["position"] = map[string]any{"x": clampStickyPosition(x), "y": clampStickyPosition(y)}
		}
	}
	if value, ok := stored["updatedBy"].(string); ok {
		note["updatedBy"] = truncate(value, 120)
	}
	if value, ok := stored["updatedAt"].(string); ok {
		note["updatedAt"] = truncate(value, 40)
	}
	return note
}

func numericStateValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func clampStickyPosition(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (s *Server) stickyNoteFor(ctx context.Context, owner userRecord) map[string]any {
	state, ok := s.loadStateFor(ctx, owner.ID)
	if !ok {
		return normalizeStickyNote(nil)
	}
	prefs := statePreferences(state, preferencesFor(owner))
	return normalizeStickyNote(prefs["stickyNote"])
}

func (s *Server) stickyNoteTeamCount(ctx context.Context, owner userRecord) int {
	var count int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM public.mailbox_delegates WHERE tenant_id=$1 AND owner_user_id=$2
	`, owner.TenantID, owner.ID).Scan(&count)
	return count
}

func (s *Server) stickyNoteShared(ctx context.Context, owner userRecord) bool {
	return s.stickyNoteTeamCount(ctx, owner) > 0
}

func (s *Server) stickyNoteResponse(ctx context.Context, owner userRecord, note map[string]any) map[string]any {
	return map[string]any{
		"note": note, "shared": s.stickyNoteShared(ctx, owner),
		"owner": map[string]any{"id": owner.ID, "name": owner.Name, "email": owner.Email},
	}
}

func (s *Server) registerStickyNoteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sticky-note", s.requireUser(s.getStickyNote))
	mux.HandleFunc("PUT /api/sticky-note", s.requireUser(s.putStickyNote))
	mux.HandleFunc("GET /api/notes", s.requireUser(s.getStickyNotes))
	mux.HandleFunc("GET /api/notes/search", s.requireUser(s.searchStickyNotes))
	mux.HandleFunc("GET /api/notes/attach", s.requireUser(s.getStickyNoteAttachCandidates))
	mux.HandleFunc("POST /api/notes", s.requireUser(s.createStickyNote))
	mux.HandleFunc("PATCH /api/notes/{id}", s.requireUser(s.patchStickyNote))
}

func (s *Server) getStickyNote(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.tenantPolicyFor(r.Context(), sess.User.TenantID).Features.StickyNotes {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Sticky notes are turned off for this organization")
		return
	}
	owner, shared, err := s.collaborationOwner(r.Context(), sess.User, r.URL.Query().Get("ownerId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if shared && owner.ID != sess.User.ID {
		delegation, ok := s.delegationFor(r.Context(), sess.User.TenantID, owner.ID, sess.User.ID)
		if !ok || !delegation.CanRead {
			writeError(w, http.StatusForbidden, "STICKY_NOTE_FORBIDDEN", "You do not have access to this shared mailbox")
			return
		}
	}
	writeJSON(w, http.StatusOK, s.stickyNoteResponse(r.Context(), owner, s.stickyNoteFor(r.Context(), owner)))
}

func (s *Server) putStickyNote(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.tenantPolicyFor(r.Context(), sess.User.TenantID).Features.StickyNotes {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Sticky notes are turned off for this organization")
		return
	}
	var body struct {
		OwnerID   string    `json:"ownerId"`
		Content   *string   `json:"content"`
		Color     *string   `json:"color"`
		Collapsed *bool     `json:"collapsed"`
		Enabled   *bool     `json:"enabled"`
		Links     *[]string `json:"links"`
		Position  *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"position"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	owner, shared, err := s.collaborationOwner(r.Context(), sess.User, body.OwnerID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if shared && owner.ID != sess.User.ID {
		delegation, ok := s.delegationFor(r.Context(), sess.User.TenantID, owner.ID, sess.User.ID)
		if !ok || !delegation.CanRead {
			writeError(w, http.StatusForbidden, "STICKY_NOTE_FORBIDDEN", "You do not have access to this shared mailbox")
			return
		}
	}
	note := s.stickyNoteFor(r.Context(), owner)
	if body.Content != nil {
		content := strings.TrimRight(*body.Content, " \t\r\n")
		if len([]rune(content)) > 2000 {
			writeError(w, http.StatusBadRequest, "STICKY_NOTE", "Keep the sticky note under 2,000 characters")
			return
		}
		note["content"] = content
	}
	if body.Color != nil {
		color := strings.ToLower(strings.TrimSpace(*body.Color))
		if !stickyNoteColors[color] {
			writeError(w, http.StatusBadRequest, "STICKY_NOTE_COLOR", "Choose a valid sticky-note color")
			return
		}
		note["color"] = color
	}
	if body.Collapsed != nil {
		note["collapsed"] = *body.Collapsed
	}
	if body.Enabled != nil {
		note["enabled"] = *body.Enabled
	}
	if body.Position != nil {
		note["position"] = map[string]any{"x": clampStickyPosition(body.Position.X), "y": clampStickyPosition(body.Position.Y)}
	}
	if body.Links != nil {
		links, err := s.buildStickyNoteLinks(r.Context(), owner, threadLinkRefs(*body.Links))
		if err != nil {
			writeFromError(w, err)
			return
		}
		note["links"] = links
	}
	note["updatedBy"] = sess.User.Name
	note["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	err = s.withState(owner.ID, owner.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		userMap := stateMap(state, "user")
		if len(userMap) == 0 {
			userMap = map[string]any{}
			state["user"] = userMap
		}
		prefs := stateMap(userMap, "preferences")
		prefs["stickyNote"] = note
		userMap["preferences"] = prefs
		appendStateAudit(state, "sticky_note.updated", "Sticky note", sess.User.Name,
			"Updated the mailbox sticky note", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	target := firstNonEmpty(owner.Email, "Sticky note")
	detail := "Updated the sticky note"
	if owner.ID != sess.User.ID {
		detail = "Updated the shared sticky note for " + owner.Email
	}
	s.insertAudit(sess.User, "sticky_note.updated", target, detail, clientIP(r), sanitizeUserAgent(r.UserAgent()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note, "shared": s.stickyNoteShared(r.Context(), owner)})
}
