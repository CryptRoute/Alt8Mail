package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sharedDraftMaxPayload = 1 << 20

type sharedDraftRecord struct {
	ID             string
	TenantID       string
	TeamID         string
	ThreadID       string
	Subject        string
	Payload        json.RawMessage
	Version        int64
	CreatedBy      string
	UpdatedBy      string
	UpdatedByName  string
	UpdatedByMail  string
	LeaseOwner     string
	LeaseOwnerName string
	LeaseToken     string
	LeaseExpires   *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CommentCount   int
}

func (s *Server) requireSharedDraftEntitlement(w http.ResponseWriter) bool {
	if s.entitled("shared_drafts") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Shared drafts require a license with the shared_drafts feature")
	return false
}

func sharedDraftPayload(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), "", nil
	}
	if len(raw) > sharedDraftMaxPayload {
		return nil, "", errors.New("draft payload is too large")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, "", errors.New("draft payload must be a JSON object")
	}
	compacted, err := json.Marshal(object)
	if err != nil {
		return nil, "", errors.New("draft payload could not be encoded")
	}
	subject := ""
	if value, ok := object["subject"].(string); ok {
		subject = truncate(strings.TrimSpace(value), 250)
	}
	return compacted, subject, nil
}

func (s *Server) sharedDraftRecord(ctx context.Context, tenantID, draftID string) (sharedDraftRecord, error) {
	var draft sharedDraftRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT d.id, d.tenant_id::text, d.team_mailbox_id::text, d.thread_id, d.subject, d.payload,
		       d.version, COALESCE(d.created_by::text,''), COALESCE(d.updated_by::text,''),
		       COALESCE(u.name,''), COALESCE(u.email::text,''), COALESCE(d.lease_owner::text,''),
		       COALESCE(lu.name,''), d.lease_token, d.lease_expires_at, d.created_at, d.updated_at,
		       (SELECT count(*) FROM public.shared_draft_comments c
		        WHERE c.shared_draft_id=d.id AND c.resolved_at IS NULL)
		FROM public.shared_drafts d
		LEFT JOIN public.users u ON u.id=d.updated_by
		LEFT JOIN public.users lu ON lu.id=d.lease_owner
		WHERE d.id=$1 AND d.tenant_id=$2 AND d.deleted_at IS NULL
	`, strings.TrimSpace(draftID), tenantID).Scan(
		&draft.ID, &draft.TenantID, &draft.TeamID, &draft.ThreadID, &draft.Subject, &draft.Payload,
		&draft.Version, &draft.CreatedBy, &draft.UpdatedBy, &draft.UpdatedByName, &draft.UpdatedByMail,
		&draft.LeaseOwner, &draft.LeaseOwnerName, &draft.LeaseToken, &draft.LeaseExpires,
		&draft.CreatedAt, &draft.UpdatedAt, &draft.CommentCount)
	return draft, err
}

func (s *Server) sharedDraftForUser(ctx context.Context, user userRecord, draftID string) (sharedDraftRecord, teamMailboxRecord, error) {
	draft, err := s.sharedDraftRecord(ctx, user.TenantID, draftID)
	if err != nil {
		return draft, teamMailboxRecord{}, fail(http.StatusNotFound, "DRAFT_NOT_FOUND", "Shared draft not found")
	}
	team, err := s.teamMailboxForUser(ctx, user, draft.TeamID)
	if err != nil {
		return draft, team, fail(http.StatusNotFound, "DRAFT_NOT_FOUND", "Shared draft not found")
	}
	return draft, team, nil
}

func sharedDraftPayloadJSON(draft sharedDraftRecord, userID string) map[string]any {
	lease := map[string]any{"active": false, "self": false, "ownerId": "", "ownerName": "", "expiresAt": nil}
	if draft.LeaseOwner != "" && draft.LeaseExpires != nil && draft.LeaseExpires.After(time.Now()) {
		lease = map[string]any{
			"active": true, "self": draft.LeaseOwner == userID,
			"ownerId": draft.LeaseOwner, "ownerName": draft.LeaseOwnerName,
			"expiresAt": draft.LeaseExpires.UTC().Format(time.RFC3339),
		}
	}
	return map[string]any{
		"id": draft.ID, "teamId": draft.TeamID, "threadId": draft.ThreadID,
		"subject": draft.Subject, "version": draft.Version, "payload": draft.Payload,
		"updatedBy": map[string]any{"id": draft.UpdatedBy, "name": draft.UpdatedByName, "email": draft.UpdatedByMail},
		"lease":     lease, "unresolvedComments": draft.CommentCount,
		"updatedAt": draft.UpdatedAt.UTC().Format(time.RFC3339),
		"createdAt": draft.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) listSharedDrafts(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.URL.Query().Get("teamId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT d.id, d.thread_id, d.subject, d.version, COALESCE(u.name,''), COALESCE(u.email::text,''),
		       COALESCE(d.lease_owner::text,''), d.lease_expires_at, d.updated_at,
		       (SELECT count(*) FROM public.shared_draft_comments c
		        WHERE c.shared_draft_id=d.id AND c.resolved_at IS NULL)
		FROM public.shared_drafts d
		LEFT JOIN public.users u ON u.id=d.updated_by
		WHERE d.team_mailbox_id=$1 AND d.tenant_id=$2 AND d.deleted_at IS NULL
		ORDER BY d.updated_at DESC
		LIMIT 200
	`, team.ID, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SHARED_DRAFTS", "Could not load shared drafts")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, threadID, subject, updatedName, updatedMail, leaseOwner string
		var version int64
		var leaseExpires *time.Time
		var updated time.Time
		var comments int
		if rows.Scan(&id, &threadID, &subject, &version, &updatedName, &updatedMail, &leaseOwner, &leaseExpires, &updated, &comments) != nil {
			continue
		}
		lease := map[string]any{"active": false, "self": false}
		if leaseOwner != "" && leaseExpires != nil && leaseExpires.After(time.Now()) {
			lease = map[string]any{"active": true, "self": leaseOwner == sess.User.ID, "ownerId": leaseOwner, "expiresAt": leaseExpires.UTC().Format(time.RFC3339)}
		}
		items = append(items, map[string]any{
			"id": id, "threadId": threadID, "subject": subject, "version": version,
			"updatedBy": map[string]any{"name": updatedName, "email": updatedMail},
			"updatedAt": updated.UTC().Format(time.RFC3339), "lease": lease,
			"unresolvedComments": comments,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": items, "count": len(items), "teamInbox": teamPayload(team)})
}

func (s *Server) createSharedDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body struct {
		TeamID   string          `json:"teamId"`
		ThreadID string          `json:"threadId"`
		Payload  json.RawMessage `json:"payload"`
	}
	if !s.decodeBody(w, r, &body, sharedDraftMaxPayload+16*1024) {
		return
	}
	team, err := s.teamMailboxForUser(r.Context(), sess.User, body.TeamID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	payload, subject, err := sharedDraftPayload(body.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DRAFT_PAYLOAD", err.Error())
		return
	}
	id := newID("sdraft")
	token := newID("lease")
	expires := time.Now().Add(5 * time.Minute)
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.shared_drafts
		  (id, tenant_id, team_mailbox_id, thread_id, subject, payload, version,
		   created_by, updated_by, lease_owner, lease_token, lease_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,1,$7,$7,$7,$8,$9)
	`, id, sess.User.TenantID, team.ID, truncate(strings.TrimSpace(body.ThreadID), 120), subject,
		string(payload), sess.User.ID, token, expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DRAFT_CREATE", "Could not create the shared draft")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.shared_draft_versions (shared_draft_id, version, payload, author_user_id)
		VALUES ($1,1,$2::jsonb,$3) ON CONFLICT (shared_draft_id, version) DO NOTHING
	`, id, string(payload), sess.User.ID)
	draft, _ := s.sharedDraftRecord(r.Context(), sess.User.TenantID, id)
	s.audit(r, sess.User, "shared_draft.created", id, "Created a shared draft in "+team.Name)
	response := sharedDraftPayloadJSON(draft, sess.User.ID)
	response["writerToken"] = token
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "draft": response})
}

func (s *Server) getSharedDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": sharedDraftPayloadJSON(draft, sess.User.ID), "comments": s.loadSharedDraftComments(r.Context(), draft.ID)})
}

func (s *Server) loadSharedDraftComments(ctx context.Context, draftID string) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		SELECT c.id, c.author_user_id::text, u.name, u.email::text, c.body, c.anchor,
		       c.resolved_at, c.created_at
		FROM public.shared_draft_comments c
		LEFT JOIN public.users u ON u.id=c.author_user_id
		WHERE c.shared_draft_id=$1
		ORDER BY c.created_at ASC
	`, draftID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	comments := []map[string]any{}
	for rows.Next() {
		var id, authorID, name, email, body string
		var anchor []byte
		var resolved *time.Time
		var created time.Time
		if rows.Scan(&id, &authorID, &name, &email, &body, &anchor, &resolved, &created) != nil {
			continue
		}
		item := map[string]any{
			"id": id, "authorId": authorID, "authorName": name, "authorEmail": email,
			"body": body, "anchor": json.RawMessage(anchor),
			"createdAt": created.UTC().Format(time.RFC3339),
		}
		if resolved != nil {
			item["resolvedAt"] = resolved.UTC().Format(time.RFC3339)
		}
		comments = append(comments, item)
	}
	return comments
}

func (s *Server) acquireSharedDraftLease(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		TTLSeconds int `json:"ttlSeconds"`
	}
	_ = s.decodeBody(w, r, &body, 8*1024)
	ttl := body.TTLSeconds
	if ttl == 0 {
		ttl = 300
	}
	if ttl < 30 || ttl > 900 {
		writeError(w, http.StatusBadRequest, "DRAFT_LEASE", "Lease TTL must be 30–900 seconds")
		return
	}
	if draft.LeaseOwner != "" && draft.LeaseOwner != sess.User.ID && draft.LeaseExpires != nil && draft.LeaseExpires.After(time.Now()) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]any{"code": "DRAFT_LEASED", "message": draft.LeaseOwnerName + " is editing this draft"},
			"lease": map[string]any{"ownerId": draft.LeaseOwner, "ownerName": draft.LeaseOwnerName, "expiresAt": draft.LeaseExpires.UTC().Format(time.RFC3339)},
		})
		return
	}
	token := newID("lease")
	expires := time.Now().Add(time.Duration(ttl) * time.Second)
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.shared_drafts SET lease_owner=$2, lease_token=$3, lease_expires_at=$4, updated_at=now()
		WHERE id=$1 AND deleted_at IS NULL
	`, draft.ID, sess.User.ID, token, expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DRAFT_LEASE", "Could not acquire the draft lease")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "writerToken": token, "expiresAt": expires.UTC().Format(time.RFC3339), "version": draft.Version})
}

func (s *Server) releaseSharedDraftLease(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE public.shared_drafts SET lease_owner=NULL, lease_token='', lease_expires_at=NULL, updated_at=now()
		WHERE id=$1 AND lease_owner=$2 AND deleted_at IS NULL
	`, draft.ID, sess.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var (
	errSharedDraftLease    = errors.New("shared draft lease is held by another editor")
	errSharedDraftConflict = errors.New("shared draft version conflict")
)

type sharedDraftUpdateResult struct {
	Draft     sharedDraftRecord
	Conflicts bool
}

func (s *Server) updateSharedDraft(ctx context.Context, user userRecord, draftID string, baseVersion int64, writerToken string, payload json.RawMessage) (sharedDraftRecord, error) {
	compacted, subject, err := sharedDraftPayload(payload)
	if err != nil {
		return sharedDraftRecord{}, err
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return sharedDraftRecord{}, err
	}
	defer tx.Rollback(ctx)
	var version int64
	var currentPayload []byte
	var leaseOwner, leaseToken string
	var leaseExpires *time.Time
	err = tx.QueryRow(ctx, `
		SELECT version, payload, COALESCE(lease_owner::text,''), lease_token, lease_expires_at
		FROM public.shared_drafts
		WHERE id=$1 AND tenant_id=$2 AND deleted_at IS NULL
		FOR UPDATE
	`, draftID, user.TenantID).Scan(&version, &currentPayload, &leaseOwner, &leaseToken, &leaseExpires)
	if err != nil {
		return sharedDraftRecord{}, fail(http.StatusNotFound, "DRAFT_NOT_FOUND", "Shared draft not found")
	}
	now := time.Now()
	if leaseOwner != user.ID || writerToken == "" || leaseToken != writerToken || leaseExpires == nil || !leaseExpires.After(now) {
		return sharedDraftRecord{}, errSharedDraftLease
	}
	if baseVersion != version {
		return sharedDraftRecord{}, errSharedDraftConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.shared_draft_versions (shared_draft_id, version, payload, author_user_id)
		VALUES ($1,$2,$3::jsonb,$4) ON CONFLICT (shared_draft_id, version) DO NOTHING
	`, draftID, version, string(currentPayload), user.ID); err != nil {
		return sharedDraftRecord{}, err
	}
	nextVersion := version + 1
	if _, err := tx.Exec(ctx, `
		UPDATE public.shared_drafts SET payload=$2::jsonb, subject=$3, version=$4,
		  updated_by=$5, updated_at=now(), lease_expires_at=now()+interval '5 minutes'
		WHERE id=$1
	`, draftID, string(compacted), subject, nextVersion, user.ID); err != nil {
		return sharedDraftRecord{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.shared_draft_versions (shared_draft_id, version, payload, author_user_id)
		VALUES ($1,$2,$3::jsonb,$4) ON CONFLICT (shared_draft_id, version) DO NOTHING
	`, draftID, nextVersion, string(compacted), user.ID); err != nil {
		return sharedDraftRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sharedDraftRecord{}, err
	}
	return s.sharedDraftRecord(ctx, user.TenantID, draftID)
}

func (s *Server) writeSharedDraftUpdateError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errSharedDraftLease) {
		writeError(w, http.StatusConflict, "DRAFT_LEASED", "The edit lease has expired or is held by another agent")
		return
	}
	if errors.Is(err, errSharedDraftConflict) {
		session := s.requireSessionFromContext(r)
		draft, _ := s.sharedDraftRecord(r.Context(), session.User.TenantID, r.PathValue("id"))
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code": "DRAFT_CONFLICT", "message": "Another agent saved a newer version",
				"details": map[string]any{
					"version": draft.Version, "payload": draft.Payload,
					"updatedByName": draft.UpdatedByName, "updatedAt": draft.UpdatedAt.UTC().Format(time.RFC3339),
				},
			},
			"draft": sharedDraftPayloadJSON(draft, session.User.ID),
		})
		return
	}
	writeError(w, http.StatusBadRequest, "DRAFT_SAVE", err.Error())
}

func (s *Server) updateSharedDraftHandler(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		BaseVersion int64           `json:"baseVersion"`
		WriterToken string          `json:"writerToken"`
		Payload     json.RawMessage `json:"payload"`
	}
	if !s.decodeBody(w, r, &body, sharedDraftMaxPayload+16*1024) {
		return
	}
	updated, err := s.updateSharedDraft(r.Context(), sess.User, draft.ID, body.BaseVersion, body.WriterToken, body.Payload)
	if err != nil {
		s.writeSharedDraftUpdateError(w, r, err)
		return
	}
	s.audit(r, sess.User, "shared_draft.updated", draft.ID, "Updated shared draft version "+strconv.FormatInt(updated.Version, 10))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "draft": sharedDraftPayloadJSON(updated, sess.User.ID)})
}

func stateStringOfAny(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func (s *Server) deleteSharedDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if draft.CreatedBy != sess.User.ID && draft.LeaseOwner != sess.User.ID {
		writeError(w, http.StatusForbidden, "DRAFT_FORBIDDEN", "Only the creator or current editor can delete this draft")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE public.shared_drafts SET deleted_at=now(), lease_owner=NULL, lease_token='', lease_expires_at=NULL
		WHERE id=$1
	`, draft.ID)
	s.audit(r, sess.User, "shared_draft.deleted", draft.ID, "Deleted a shared draft")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) sharedDraftVersions(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT v.version, v.payload, COALESCE(u.name,''), COALESCE(u.email::text,''), v.created_at
		FROM public.shared_draft_versions v
		LEFT JOIN public.users u ON u.id=v.author_user_id
		WHERE v.shared_draft_id=$1
		ORDER BY v.version DESC
	`, draft.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DRAFT_VERSIONS", "Could not load the version history")
		return
	}
	defer rows.Close()
	versions := []map[string]any{}
	for rows.Next() {
		var version int64
		var payload []byte
		var name, email string
		var created time.Time
		if rows.Scan(&version, &payload, &name, &email, &created) == nil {
			versions = append(versions, map[string]any{
				"version": version, "payload": json.RawMessage(payload),
				"authorName": name, "authorEmail": email, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "currentVersion": draft.Version})
}

func (s *Server) restoreSharedDraftVersion(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	version, err := parseInt64Path(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "DRAFT_VERSION", "Choose a version to restore")
		return
	}
	var body struct {
		BaseVersion int64  `json:"baseVersion"`
		WriterToken string `json:"writerToken"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	var payload []byte
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT payload FROM public.shared_draft_versions WHERE shared_draft_id=$1 AND version=$2
	`, draft.ID, version).Scan(&payload); err != nil {
		writeError(w, http.StatusNotFound, "DRAFT_VERSION", "That draft version is not available")
		return
	}
	updated, err := s.updateSharedDraft(r.Context(), sess.User, draft.ID, body.BaseVersion, body.WriterToken, payload)
	if err != nil {
		s.writeSharedDraftUpdateError(w, r, err)
		return
	}
	s.audit(r, sess.User, "shared_draft.restored", draft.ID, "Restored shared draft version "+strconv.FormatInt(version, 10))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "draft": sharedDraftPayloadJSON(updated, sess.User.ID)})
}

func parseInt64Path(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 1 {
		return 0, errors.New("invalid version")
	}
	return value, nil
}

func (s *Server) addSharedDraftComment(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		Body   string          `json:"body"`
		Anchor json.RawMessage `json:"anchor"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	text := truncate(strings.TrimSpace(body.Body), 1000)
	if text == "" {
		writeError(w, http.StatusBadRequest, "DRAFT_COMMENT", "Write a comment of 1–1000 characters")
		return
	}
	anchor := json.RawMessage(`{}`)
	if len(body.Anchor) > 0 && len(body.Anchor) <= 8*1024 {
		var probe map[string]any
		if json.Unmarshal(body.Anchor, &probe) == nil {
			anchor = body.Anchor
		}
	}
	id := newID("comment")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.shared_draft_comments (id, shared_draft_id, author_user_id, body, anchor)
		VALUES ($1,$2,$3,$4,$5::jsonb)
	`, id, draft.ID, sess.User.ID, text, string(anchor))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DRAFT_COMMENT", "Could not add the comment")
		return
	}
	s.audit(r, sess.User, "shared_draft.comment_added", draft.ID, "Commented on a shared draft")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "comment": map[string]any{
		"id": id, "authorId": sess.User.ID, "authorName": sess.User.Name,
		"authorEmail": sess.User.Email, "body": text, "anchor": anchor,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}})
}

func (s *Server) resolveSharedDraftComment(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedDraftEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	draft, _, err := s.sharedDraftForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.shared_draft_comments SET resolved_at=now(), resolved_by=$3
		WHERE id=$1 AND shared_draft_id=$2 AND resolved_at IS NULL
	`, strings.TrimSpace(r.PathValue("commentId")), draft.ID, sess.User.ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "DRAFT_COMMENT", "Comment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) registerSharedDraftRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/shared-drafts", s.requireUser(s.listSharedDrafts))
	mux.HandleFunc("POST /api/shared-drafts", s.requireUser(s.createSharedDraft))
	mux.HandleFunc("GET /api/shared-drafts/{id}", s.requireUser(s.getSharedDraft))
	mux.HandleFunc("PATCH /api/shared-drafts/{id}", s.requireUser(s.updateSharedDraftHandler))
	mux.HandleFunc("DELETE /api/shared-drafts/{id}", s.requireUser(s.deleteSharedDraft))
	mux.HandleFunc("POST /api/shared-drafts/{id}/lease", s.requireUser(s.acquireSharedDraftLease))
	mux.HandleFunc("DELETE /api/shared-drafts/{id}/lease", s.requireUser(s.releaseSharedDraftLease))
	mux.HandleFunc("GET /api/shared-drafts/{id}/versions", s.requireUser(s.sharedDraftVersions))
	mux.HandleFunc("POST /api/shared-drafts/{id}/versions/{version}/restore", s.requireUser(s.restoreSharedDraftVersion))
	mux.HandleFunc("POST /api/shared-drafts/{id}/comments", s.requireUser(s.addSharedDraftComment))
	mux.HandleFunc("POST /api/shared-drafts/{id}/comments/{commentId}/resolve", s.requireUser(s.resolveSharedDraftComment))
}
