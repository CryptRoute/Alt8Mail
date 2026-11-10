package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) fileAccessRecord(ctx context.Context, user userRecord, fileID string) (map[string]any, string, error) {
	file, err := s.loadFile(ctx, user, fileID)
	if err != nil {
		return nil, "", fail(http.StatusNotFound, "FILE_NOT_FOUND", "File not found")
	}
	access := stateString(file, "access")
	if access == "" {
		access = "owner"
	}
	return file, access, nil
}

func fileCanEdit(access string) bool {
	return access == "owner" || access == "edit"
}

func (s *Server) listFileVersions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, _, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id")); err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT v.version, v.name, v.mime, v.size_bytes, v.checksum, COALESCE(u.name,''), v.created_at
		FROM public.file_versions v
		LEFT JOIN public.users u ON u.id=v.created_by
		WHERE v.file_id=$1 ORDER BY v.version DESC
	`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_VERSIONS", "Could not load versions")
		return
	}
	defer rows.Close()
	versions := []map[string]any{}
	for rows.Next() {
		var version int
		var name, mime, checksum, author string
		var size int64
		var created time.Time
		if rows.Scan(&version, &name, &mime, &size, &checksum, &author, &created) == nil {
			versions = append(versions, map[string]any{
				"version": version, "name": name, "mime": mime, "sizeBytes": size,
				"checksum": checksum, "author": author, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "count": len(versions)})
}

func (s *Server) snapshotFileVersion(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	file, access, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !fileCanEdit(access) {
		writeError(w, http.StatusForbidden, "FILE_FORBIDDEN", "You cannot version this file")
		return
	}
	id := stateString(file, "id")
	content, err := s.loadFileContent(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "FILE_NOT_FOUND", "File not found")
		return
	}
	var version int
	if err := s.store.DB.QueryRow(r.Context(), `SELECT version FROM public.files WHERE id=$1 AND user_id=$2`, id, sess.User.ID).Scan(&version); err != nil {
		writeError(w, http.StatusNotFound, "FILE_NOT_FOUND", "File not found")
		return
	}
	var existing bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM public.file_versions WHERE file_id=$1 AND version=$2)`, id, version).Scan(&existing)
	if existing {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version, "replayed": true})
		return
	}
	data, err := s.readFileBytes(r.Context(), content, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "FILE_VERSION", "The file content could not be read for versioning")
		return
	}
	sealed, _, err := s.sealTenantBlob(r.Context(), sess.User.TenantID, data)
	if err != nil {
		writeError(w, http.StatusLocked, "FILE_VERSION", "Unlock the organization's encryption key before versioning files")
		return
	}
	storageKey := newID("fileverblob")
	if err := os.MkdirAll(s.cfg.UploadDir, 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_VERSION", "Could not prepare version storage")
		return
	}
	if err := os.WriteFile(filepath.Join(s.cfg.UploadDir, storageKey), sealed, 0o640); err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_VERSION", "Could not store the version copy")
		return
	}
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	result, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.file_versions (id, file_id, tenant_id, version, name, mime, size_bytes, storage_key, attachment_id, checksum, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULL,$9,$10)
		ON CONFLICT (file_id, version) DO NOTHING
	`, newID("filever"), id, sess.User.TenantID, version, content.Name, content.MIME, len(data), storageKey, checksum, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		_ = os.Remove(filepath.Join(s.cfg.UploadDir, storageKey))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "FILE_VERSION", "Could not snapshot the file")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version, "replayed": true})
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.version_saved", content.Name, "Saved immutable byte copy of version "+fmt.Sprint(version))
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "version": version})
}

func (s *Server) restoreFileVersion(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	file, access, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if access != "owner" {
		writeError(w, http.StatusForbidden, "FILE_FORBIDDEN", "Only the file owner can restore a version")
		return
	}
	version, err := parseInt64Path(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "FILE_VERSION", "Choose a valid version")
		return
	}
	id := stateString(file, "id")
	content, err := s.loadFileContent(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "FILE_NOT_FOUND", "File not found")
		return
	}
	var clientEncrypted bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT client_encrypted FROM public.files WHERE id=$1 AND user_id=$2`, id, sess.User.ID).Scan(&clientEncrypted)
	var name, mime, checksum, versionKey string
	var size int64
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, mime, size_bytes, storage_key, checksum
		FROM public.file_versions WHERE file_id=$1 AND version=$2
	`, id, version).Scan(&name, &mime, &size, &versionKey, &checksum); err != nil {
		writeError(w, http.StatusNotFound, "FILE_VERSION", "That version is not available")
		return
	}
	if versionKey == "" || strings.ContainsAny(versionKey, `/\`) || versionKey == "." || versionKey == ".." {
		writeError(w, http.StatusNotFound, "FILE_VERSION", "That version copy is no longer available")
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, versionKey))
	if err != nil {
		writeError(w, http.StatusNotFound, "FILE_VERSION", "That version copy is no longer available")
		return
	}
	data, err := s.openTenantBlob(raw)
	if err != nil {
		writeError(w, http.StatusLocked, "ENCRYPTION_LOCKED", "Unlock the organization's encryption key before restoring this version")
		return
	}
	var storageKey, externalID, externalURL string
	encryptionVersion := 0
	switch content.ProviderID {
	case "a8m":
		stored := data
		if !clientEncrypted {
			sealed, keyVersion, sealErr := s.sealTenantBlob(r.Context(), sess.User.TenantID, data)
			if sealErr != nil {
				writeError(w, http.StatusLocked, "ENCRYPTION_LOCKED", "Unlock the organization's encryption key before restoring this version")
				return
			}
			stored, encryptionVersion = sealed, keyVersion
		}
		storageKey = newID("fileblob")
		if err := os.MkdirAll(s.cfg.UploadDir, 0o750); err != nil {
			writeError(w, http.StatusInternalServerError, "FILE_VERSION", "Could not prepare file storage")
			return
		}
		if err := os.WriteFile(filepath.Join(s.cfg.UploadDir, storageKey), stored, 0o640); err != nil {
			writeError(w, http.StatusInternalServerError, "FILE_VERSION", "Could not restore the file bytes")
			return
		}
	case "s3":
		stored := data
		if !clientEncrypted {
			sealed, keyVersion, sealErr := s.sealTenantBlob(r.Context(), sess.User.TenantID, data)
			if sealErr != nil {
				writeError(w, http.StatusLocked, "ENCRYPTION_LOCKED", "Unlock the organization's encryption key before restoring this version")
				return
			}
			stored, encryptionVersion = sealed, keyVersion
		}
		storageKey = fmt.Sprintf("tenants/%s/users/%s/files/%s/%s", sess.User.TenantID, sess.User.ID, id, safeFileName(name))
		if err := s.s3PutObject(r.Context(), sess.User.TenantID, storageKey, mime, stored); err != nil {
			writeError(w, http.StatusBadGateway, "FILE_VERSION", "Could not restore the version to S3")
			return
		}
		externalID = storageKey
	case "oauth":
		if !clientEncrypted {
			if keyVersion, _, _, keyErr := s.activeTenantKey(r.Context(), sess.User.TenantID); keyErr == nil && keyVersion > 0 {
				writeError(w, http.StatusConflict, "FILE_VERSION", "Connected storage cannot restore BYOK-protected versions; restore to Cabinet storage instead")
				return
			}
		}
		remoteID, remoteURL, uploadErr := s.uploadToOAuthProvider(r.Context(), sess, content.ProviderID, name, mime, data)
		if uploadErr != nil {
			writeError(w, http.StatusBadGateway, "FILE_VERSION", uploadErr.Error())
			return
		}
		externalID, externalURL = remoteID, remoteURL
	default:
		writeError(w, http.StatusBadRequest, "FILE_VERSION", "This storage provider cannot restore file versions")
		return
	}
	sum := sha256.Sum256(data)
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.files SET name=$2, mime=$3, size_bytes=$4, storage_key=$5, external_id=$6,
		  external_url=$7, attachment_id=NULL, checksum=$8, encryption_key_version=$9,
		  client_encrypted=$10, version=version+1, updated_at=now()
		WHERE id=$1 AND user_id=$11
	`, id, name, mime, size, storageKey, externalID, externalURL, hex.EncodeToString(sum[:]), encryptionVersion, clientEncrypted, sess.User.ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusInternalServerError, "FILE_VERSION", "Could not restore the file version")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.version_restored", name, "Restored version "+r.PathValue("version"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listFileComments(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, _, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id")); err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT c.id, c.author_user_id::text, COALESCE(u.name,''), COALESCE(u.email::text,''), c.body,
		       c.resolved_at, c.created_at
		FROM public.file_comments c LEFT JOIN public.users u ON u.id=c.author_user_id
		WHERE c.file_id=$1 ORDER BY c.created_at
	`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_COMMENTS", "Could not load comments")
		return
	}
	defer rows.Close()
	comments := []map[string]any{}
	for rows.Next() {
		var id, authorID, name, email, body string
		var resolved *time.Time
		var created time.Time
		if rows.Scan(&id, &authorID, &name, &email, &body, &resolved, &created) == nil {
			item := map[string]any{"id": id, "authorId": authorID, "authorName": name, "authorEmail": email, "body": body, "createdAt": created.UTC().Format(time.RFC3339)}
			if resolved != nil {
				item["resolvedAt"] = resolved.UTC().Format(time.RFC3339)
			}
			comments = append(comments, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": comments, "count": len(comments)})
}

func (s *Server) addFileComment(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, _, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id")); err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	text := truncate(strings.TrimSpace(body.Body), 2000)
	if text == "" {
		writeError(w, http.StatusBadRequest, "FILE_COMMENT", "Write a comment of 1–2000 characters")
		return
	}
	id := newID("fcomment")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.file_comments (id, file_id, tenant_id, author_user_id, body)
		VALUES ($1,$2,$3,$4,$5)
	`, id, r.PathValue("id"), sess.User.TenantID, sess.User.ID, text)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_COMMENT", "Could not add the comment")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.comment_added", r.PathValue("id"), "Added a file comment")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "comment": map[string]any{
		"id": id, "authorId": sess.User.ID, "authorName": sess.User.Name, "authorEmail": sess.User.Email,
		"body": text, "createdAt": time.Now().UTC().Format(time.RFC3339),
	}})
}

func (s *Server) resolveFileComment(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_, access, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !fileCanEdit(access) {
		writeError(w, http.StatusForbidden, "FILE_FORBIDDEN", "You cannot resolve comments on this file")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.file_comments SET resolved_at=now(), resolved_by=$3
		WHERE id=$2 AND file_id=$1 AND resolved_at IS NULL
	`, r.PathValue("id"), r.PathValue("commentId"), sess.User.ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "FILE_COMMENT", "Comment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listFileApprovals(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, _, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id")); err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT a.id, COALESCE(req.name,''), COALESCE(app.name,''), a.approver_user_id::text, a.status, a.note, a.created_at, a.decided_at
		FROM public.file_approvals a
		LEFT JOIN public.users req ON req.id=a.requested_by
		LEFT JOIN public.users app ON app.id=a.approver_user_id
		WHERE a.file_id=$1 ORDER BY a.created_at DESC
	`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_APPROVALS", "Could not load approvals")
		return
	}
	defer rows.Close()
	approvals := []map[string]any{}
	for rows.Next() {
		var id, requester, approver, approverID, status, note string
		var created time.Time
		var decided *time.Time
		if rows.Scan(&id, &requester, &approver, &approverID, &status, &note, &created, &decided) == nil {
			item := map[string]any{
				"id": id, "requestedBy": requester, "approver": approver, "approverId": approverID,
				"status": status, "note": note, "createdAt": created.UTC().Format(time.RFC3339),
				"self": approverID == sess.User.ID,
			}
			if decided != nil {
				item["decidedAt"] = decided.UTC().Format(time.RFC3339)
			}
			approvals = append(approvals, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals, "count": len(approvals)})
}

func (s *Server) requestFileApproval(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_, access, err := s.fileAccessRecord(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !fileCanEdit(access) {
		writeError(w, http.StatusForbidden, "FILE_FORBIDDEN", "You cannot request approval for this file")
		return
	}
	var body struct {
		ApproverEmail string `json:"approverEmail"`
		Note          string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	approver, err := s.workspaceUserByEmail(r.Context(), sess.User.TenantID, normalizeEmailValue(body.ApproverEmail))
	if err != nil || approver.ID == sess.User.ID {
		writeError(w, http.StatusBadRequest, "FILE_APPROVER", "Choose another active person in this organization")
		return
	}
	id := newID("approval")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.file_approvals (id, file_id, tenant_id, requested_by, approver_user_id, note)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, id, r.PathValue("id"), sess.User.TenantID, sess.User.ID, approver.ID, truncate(strings.TrimSpace(body.Note), 500))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_APPROVAL", "Could not request approval")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_notifications (id, tenant_id, user_id, kind, title, body, detail)
		VALUES ($1,$2,$3,'file_approval','File approval requested',$4,$5)
	`, newID("notification"), sess.User.TenantID, approver.ID, r.PathValue("id"), "Approval requested by "+sess.User.Name)
	s.recordWorkspaceAudit(r.Context(), sess, "file.approval_requested", r.PathValue("id"), "Requested approval from "+approver.Email)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) decideFileApproval(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "approved" && status != "changes_requested" && status != "rejected" {
		writeError(w, http.StatusBadRequest, "FILE_APPROVAL", "Status must be approved, changes_requested or rejected")
		return
	}
	var approverID, fileOwnerID string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT a.approver_user_id::text, f.user_id::text
		FROM public.file_approvals a JOIN public.files f ON f.id=a.file_id
		WHERE a.id=$1 AND a.file_id=$2 AND a.tenant_id=$3
	`, r.PathValue("approvalId"), r.PathValue("id"), sess.User.TenantID).Scan(&approverID, &fileOwnerID); err != nil {
		writeError(w, http.StatusNotFound, "FILE_APPROVAL", "Approval request not found")
		return
	}
	if approverID != sess.User.ID && fileOwnerID != sess.User.ID {
		writeError(w, http.StatusForbidden, "FILE_FORBIDDEN", "Only the approver or file owner can decide this request")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.file_approvals SET status=$3, note=$4, decided_at=now()
		WHERE id=$1 AND file_id=$2 AND status='pending'
	`, r.PathValue("approvalId"), r.PathValue("id"), status, truncate(strings.TrimSpace(body.Note), 500))
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "FILE_APPROVAL", "This approval has already been decided")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.approval_"+status, r.PathValue("id"), "File approval decision")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) registerFileCollaborationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/files/{id}/versions", s.requireUser(s.listFileVersions))
	mux.HandleFunc("POST /api/files/{id}/versions", s.requireUser(s.snapshotFileVersion))
	mux.HandleFunc("POST /api/files/{id}/versions/{version}/restore", s.requireUser(s.restoreFileVersion))
	mux.HandleFunc("GET /api/files/{id}/comments", s.requireUser(s.listFileComments))
	mux.HandleFunc("POST /api/files/{id}/comments", s.requireUser(s.addFileComment))
	mux.HandleFunc("POST /api/files/{id}/comments/{commentId}/resolve", s.requireUser(s.resolveFileComment))
	mux.HandleFunc("GET /api/files/{id}/approvals", s.requireUser(s.listFileApprovals))
	mux.HandleFunc("POST /api/files/{id}/approvals", s.requireUser(s.requestFileApproval))
	mux.HandleFunc("POST /api/files/{id}/approvals/{approvalId}/decide", s.requireUser(s.decideFileApproval))
}

func (s *Server) vipSenderList(ctx context.Context, userID string) []string {
	rows, err := s.store.DB.Query(ctx, `SELECT email::text FROM public.vip_senders WHERE user_id=$1 ORDER BY email`, userID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	senders := []string{}
	for rows.Next() {
		var email string
		if rows.Scan(&email) == nil {
			senders = append(senders, email)
		}
	}
	return senders
}

func (s *Server) threadMuteList(ctx context.Context, userID string) []string {
	rows, err := s.store.DB.Query(ctx, `SELECT thread_id FROM public.mail_thread_mutes WHERE user_id=$1 ORDER BY muted_at DESC`, userID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	threads := []string{}
	for rows.Next() {
		var threadID string
		if rows.Scan(&threadID) == nil {
			threads = append(threads, threadID)
		}
	}
	return threads
}

func (s *Server) listVIPSenders(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `SELECT email::text FROM public.vip_senders WHERE user_id=$1 ORDER BY email`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "VIP", "Could not load VIP senders")
		return
	}
	defer rows.Close()
	senders := []string{}
	for rows.Next() {
		var email string
		if rows.Scan(&email) == nil {
			senders = append(senders, email)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"senders": senders})
}

func (s *Server) addVIPSender(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Email string `json:"email"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	email := normalizeEmailValue(body.Email)
	if !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "VIP", "Enter a valid sender email")
		return
	}
	_, err := s.store.DB.Exec(r.Context(), `INSERT INTO public.vip_senders (tenant_id, user_id, email) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, sess.User.TenantID, sess.User.ID, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "VIP", "Could not add the VIP sender")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) removeVIPSender(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	email := normalizeEmailValue(r.PathValue("email"))
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM public.vip_senders WHERE user_id=$1 AND email=$2`, sess.User.ID, email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listThreadMutes(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `SELECT thread_id FROM public.mail_thread_mutes WHERE user_id=$1 ORDER BY muted_at DESC`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MUTE", "Could not load muted conversations")
		return
	}
	defer rows.Close()
	threads := []string{}
	for rows.Next() {
		var threadID string
		if rows.Scan(&threadID) == nil {
			threads = append(threads, threadID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func (s *Server) muteThread(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	threadID := strings.TrimSpace(r.PathValue("id"))
	if threadID == "" || !s.threadBelongsTo(r.Context(), sess.User.ID, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	_, err := s.store.DB.Exec(r.Context(), `INSERT INTO public.mail_thread_mutes (user_id, thread_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, sess.User.ID, threadID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MUTE", "Could not mute the conversation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) unmuteThread(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM public.mail_thread_mutes WHERE user_id=$1 AND thread_id=$2`, sess.User.ID, strings.TrimSpace(r.PathValue("id")))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) registerSenderControlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/vip-senders", s.requireUser(s.listVIPSenders))
	mux.HandleFunc("POST /api/vip-senders", s.requireUser(s.addVIPSender))
	mux.HandleFunc("DELETE /api/vip-senders/{email}", s.requireUser(s.removeVIPSender))
	mux.HandleFunc("GET /api/thread-mutes", s.requireUser(s.listThreadMutes))
	mux.HandleFunc("POST /api/threads/{id}/mute", s.requireUser(s.muteThread))
	mux.HandleFunc("DELETE /api/threads/{id}/mute", s.requireUser(s.unmuteThread))
}
