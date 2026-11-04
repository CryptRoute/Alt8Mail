package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	uploadSessionTTL     = 60 * time.Minute
	maxUploadSessionHour = 24
)

type uploadSessionRecord struct {
	ID        string
	TenantID  string
	UserID    string
	TokenHash string
	Provider  string
	FolderID  string
	ExpiresAt time.Time
	Extended  *time.Time
	Revoked   *time.Time
	Uploads   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type uploadProgressBody struct {
	UploadID string `json:"uploadId"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Progress int    `json:"progress"`
	Status   string `json:"status"`
}

func (s *Server) uploadSessionPublic(row uploadSessionRecord, rawToken string) map[string]any {
	uploads := []any{}
	_ = json.Unmarshal(row.Uploads, &uploads)
	item := map[string]any{
		"id": row.ID, "providerId": row.Provider, "folderId": row.FolderID,
		"expiresAt": row.ExpiresAt.UTC().Format(time.RFC3339),
		"createdAt": row.CreatedAt.UTC().Format(time.RFC3339),
		"uploads":   uploads, "active": row.Revoked == nil && row.ExpiresAt.After(time.Now()),
	}
	if row.Extended != nil {
		item["extendedAt"] = row.Extended.UTC().Format(time.RFC3339)
	}
	if rawToken != "" {
		item["url"] = strings.TrimRight(s.cfg.PublicURL, "/") + "/upload/" + rawToken
	}
	return item
}

func (s *Server) loadUploadSession(ctx context.Context, userID, id string) (uploadSessionRecord, error) {
	var row uploadSessionRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, user_id::text, token_hash, provider_id, COALESCE(folder_id,''),
		       expires_at, extended_at, revoked_at, uploads, created_at, updated_at
		FROM public.file_upload_sessions WHERE id=$1 AND user_id=$2
	`, id, userID).Scan(&row.ID, &row.TenantID, &row.UserID, &row.TokenHash, &row.Provider, &row.FolderID,
		&row.ExpiresAt, &row.Extended, &row.Revoked, &row.Uploads, &row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		return uploadSessionRecord{}, err
	}
	return row, nil
}

func (s *Server) loadUploadSessionByToken(ctx context.Context, token string) (uploadSessionRecord, userRecord, string, string, error) {
	var row uploadSessionRecord
	var owner userRecord
	var tenantName, brandColor string
	err := s.store.DB.QueryRow(ctx, `
		SELECT s.id, s.tenant_id::text, s.user_id::text, s.token_hash, s.provider_id, COALESCE(s.folder_id,''),
		       s.expires_at, s.extended_at, s.revoked_at, s.uploads, s.created_at, s.updated_at,
		       u.name, u.email::text, u.initials, u.color, u.role, u.status,
		       COALESCE(u.password_hash,''), u.preferences, u.security, u.quota_bytes, u.used_bytes, u.enrollment_required,
		       t.name, COALESCE(t.settings->'branding'->>'primaryColor', '#0f6cbd')
		FROM public.file_upload_sessions s
		JOIN public.users u ON u.id=s.user_id
		JOIN public.tenants t ON t.id=s.tenant_id
		WHERE s.token_hash=$1
	`, hashShareToken(token)).Scan(
		&row.ID, &row.TenantID, &row.UserID, &row.TokenHash, &row.Provider, &row.FolderID,
		&row.ExpiresAt, &row.Extended, &row.Revoked, &row.Uploads, &row.CreatedAt, &row.UpdatedAt,
		&owner.Name, &owner.Email, &owner.Initials, &owner.Color, &owner.Role, &owner.Status,
		&owner.PasswordHash, &owner.Preferences, &owner.Security, &owner.QuotaBytes, &owner.UsedBytes, &owner.EnrollmentRequired,
		&tenantName, &brandColor,
	)
	if err != nil {
		return uploadSessionRecord{}, userRecord{}, "", "", err
	}
	owner.ID = row.UserID
	owner.TenantID = row.TenantID
	return row, owner, tenantName, brandColor, nil
}

func (s *Server) createUploadSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		FolderID string `json:"folderId"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	folderID := strings.TrimSpace(body.FolderID)
	if folderID != "" {
		if _, _, err := s.folderPathFor(r.Context(), sess.User.ID, folderID); err != nil {
			writeError(w, http.StatusBadRequest, "FILE_FOLDER", err.Error())
			return
		}
	}
	token := randomToken(32)
	id := newID("uplink")
	expiresAt := time.Now().Add(uploadSessionTTL).UTC()
	if _, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.file_upload_sessions
		  (id, tenant_id, user_id, token_hash, provider_id, folder_id, expires_at)
		VALUES ($1,$2,$3,$4,'a8m',NULLIF($5,''),$6)
	`, id, sess.User.TenantID, sess.User.ID, hashShareToken(token), folderID, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_SESSION", "Could not create the upload session")
		return
	}
	row, err := s.loadUploadSession(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_SESSION", "Could not load the upload session")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.upload_session", id, "Created a mobile upload session")
	writeJSON(w, http.StatusCreated, map[string]any{"session": s.uploadSessionPublic(row, token)})
}

func (s *Server) getUploadSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	row, err := s.loadUploadSession(r.Context(), sess.User.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "UPLOAD_SESSION", "Upload session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s.uploadSessionPublic(row, "")})
}

func (s *Server) extendUploadSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Hours int `json:"hours"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	if body.Hours < 1 {
		body.Hours = 1
	}
	if body.Hours > maxUploadSessionHour {
		body.Hours = maxUploadSessionHour
	}
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.file_upload_sessions
		SET expires_at = GREATEST(expires_at, now()) + make_interval(hours => $3),
		    extended_at = now(), updated_at = now()
		WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL
	`, r.PathValue("id"), sess.User.ID, body.Hours)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "UPLOAD_SESSION", "Upload session not found")
		return
	}
	row, err := s.loadUploadSession(r.Context(), sess.User.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_SESSION", "Could not load the upload session")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.upload_session_extended", row.ID, fmt.Sprintf("Extended mobile upload session by %d hour(s)", body.Hours))
	writeJSON(w, http.StatusOK, map[string]any{"session": s.uploadSessionPublic(row, "")})
}

func (s *Server) deleteUploadSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.file_upload_sessions SET revoked_at=now(), updated_at=now()
		WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL
	`, r.PathValue("id"), sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "UPLOAD_SESSION", "Upload session not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.upload_session_revoked", r.PathValue("id"), "Revoked a mobile upload session")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) updateUploadProgress(ctx context.Context, token, uploadID, name string, size int64, progress int, status string) error {
	row, _, _, _, err := s.loadUploadSessionByToken(ctx, token)
	if err != nil || row.Revoked != nil || !row.ExpiresAt.After(time.Now()) {
		return errors.New("upload session is no longer active")
	}
	if uploadID == "" {
		uploadID = newID("upfile")
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	switch status {
	case "uploading", "done", "error":
	default:
		status = "uploading"
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var expiresAt time.Time
	var revoked *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT uploads, expires_at, revoked_at FROM public.file_upload_sessions
		WHERE token_hash=$1 FOR UPDATE
	`, hashShareToken(token)).Scan(&raw, &expiresAt, &revoked); err != nil {
		return err
	}
	if revoked != nil || !expiresAt.After(time.Now()) {
		return errors.New("upload session is no longer active")
	}
	uploads := []map[string]any{}
	_ = json.Unmarshal(raw, &uploads)
	found := false
	for _, item := range uploads {
		if fmt.Sprint(item["id"]) == uploadID {
			item["name"] = truncate(name, 240)
			item["sizeBytes"] = size
			item["progress"] = progress
			item["status"] = status
			item["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
			found = true
			break
		}
	}
	if !found {
		uploads = append([]map[string]any{{
			"id": uploadID, "name": truncate(name, 240), "sizeBytes": size,
			"progress": progress, "status": status, "startedAt": time.Now().UTC().Format(time.RFC3339),
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		}}, uploads...)
	}
	if len(uploads) > 50 {
		uploads = uploads[:50]
	}
	raw, _ = json.Marshal(uploads)
	_, err = tx.Exec(ctx, `UPDATE public.file_upload_sessions SET uploads=$2, updated_at=now() WHERE token_hash=$1`, hashShareToken(token), raw)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) publicUploadProgress(w http.ResponseWriter, r *http.Request) {
	var body uploadProgressBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if err := s.updateUploadProgress(r.Context(), r.PathValue("token"), body.UploadID, body.Name, body.Size, body.Progress, body.Status); err != nil {
		writeError(w, http.StatusGone, "UPLOAD_SESSION", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uploadId": body.UploadID})
}

func (s *Server) saveInternalUpload(ctx context.Context, owner userRecord, name, mimeHint string, data []byte, folderID, description string) (map[string]any, error) {
	if len(data) == 0 {
		return nil, errors.New("the uploaded file is empty")
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("files are limited to %s", formatBytes(maxAttachmentBytes))
	}
	if err := s.scanUpload(ctx, data); err != nil {
		return nil, err
	}
	name = safeFileName(name)
	if name == "" {
		name = "Untitled"
	}
	mimeType, previewable := detectUploadMIME(data, mimeHint, name)
	folderPath, _, err := s.folderPathFor(ctx, owner.ID, folderID)
	if err != nil {
		return nil, err
	}
	path := folderPath
	if path == "" {
		path = "Files"
	}
	if err := s.enforceStorageQuota(ctx, owner, int64(len(data))); err != nil {
		return nil, err
	}
	attachmentID := newID("att")
	uploadDir := s.cfg.UploadDir
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(uploadDir, attachmentID), data, 0o640); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if _, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.attachments (id, tenant_id, user_id, name, mime, size_bytes, sha256, stored_path, previewable)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, attachmentID, owner.TenantID, owner.ID, name, mimeType, len(data), hex.EncodeToString(sum[:]), attachmentID, previewable); err != nil {
		_ = os.Remove(filepath.Join(uploadDir, attachmentID))
		return nil, err
	}
	fileID := newID("file")
	if _, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.files
		  (id, tenant_id, user_id, provider_id, name, mime, path, folder_id, description,
		   size_bytes, storage_key, attachment_id, previewable, metadata)
		VALUES ($1,$2,$3,'a8m',$4,$5,$6,NULLIF($7,''),$8,$9,$10,$10,$11,'{}')
	`, fileID, owner.TenantID, owner.ID, name, mimeType, truncate(path, 160), folderID,
		truncate(description, 500), len(data), attachmentID, previewable || strings.HasPrefix(mimeType, "image/")); err != nil {
		return nil, err
	}
	return s.loadFile(ctx, owner, fileID)
}

func (s *Server) publicUpload(w http.ResponseWriter, r *http.Request) {
	row, owner, _, _, err := s.loadUploadSessionByToken(r.Context(), r.PathValue("token"))
	if err != nil || row.Revoked != nil || !row.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusGone, "UPLOAD_SESSION", "This upload session has expired")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1024*1024)
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		writeError(w, http.StatusBadRequest, "UPLOAD_INVALID", "The uploaded file is too large or invalid")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "UPLOAD_INVALID", "Choose a file to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxAttachmentBytes {
		writeError(w, http.StatusBadRequest, "UPLOAD_INVALID", "The uploaded file is too large or empty")
		return
	}
	uploadID := strings.TrimSpace(r.FormValue("uploadId"))
	name := safeFileName(header.Filename)
	if name == "" {
		name = "Mobile upload"
	}
	mimeType, _ := detectUploadMIME(data, header.Header.Get("Content-Type"), name)
	fileRecord, err := s.saveInternalUpload(r.Context(), owner, name, mimeType, data, row.FolderID, "Uploaded from a mobile device")
	if err != nil {
		if errors.Is(err, errUploadInfected) {
			writeError(w, http.StatusUnprocessableEntity, "UPLOAD_INFECTED", "The uploaded file failed the antivirus scan")
		} else {
			writeError(w, http.StatusBadRequest, "UPLOAD_FAILED", err.Error())
		}
		return
	}
	_ = s.updateUploadProgress(r.Context(), r.PathValue("token"), uploadID, name, int64(len(data)), 100, "done")
	sess := &sessionRecord{User: owner}
	s.recordWorkspaceAudit(r.Context(), sess, "file.mobile_upload", name, fmt.Sprintf("%s · mobile upload session", formatBytes(int64(len(data)))))
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "uploadId": uploadID, "file": fileRecord})
}

func (s *Server) importFileFromURL(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		URL      string `json:"url"`
		FolderID string `json:"folderId"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	rawURL := strings.TrimSpace(body.URL)
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		writeError(w, http.StatusBadRequest, "IMPORT_URL", "Enter a valid http or https URL")
		return
	}
	data, name, mimeType, err := fetchRemoteUpload(r.Context(), parsed)
	if err != nil {
		writeError(w, http.StatusBadGateway, "IMPORT_URL", err.Error())
		return
	}
	fileRecord, err := s.saveInternalUpload(r.Context(), sess.User, name, mimeType, data, strings.TrimSpace(body.FolderID), "Imported from URL")
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMPORT_URL", err.Error())
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.imported_url", name, "Imported from "+truncate(rawURL, 300))
	writeJSON(w, http.StatusCreated, map[string]any{"file": fileRecord})
}

func safeRemoteIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified()
}

func fetchRemoteUpload(ctx context.Context, source *url.URL) ([]byte, string, string, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if safeRemoteIP(ip.IP) {
					return dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				}
			}
			return nil, errors.New("that URL points to a private or unavailable address")
		},
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("redirected to an unsupported URL")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "Alt8Mail/1.0 Cabinet importer")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", errors.New("could not fetch that URL")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("the URL returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil || len(data) == 0 {
		return nil, "", "", errors.New("the URL returned an empty file")
	}
	if len(data) > maxAttachmentBytes {
		return nil, "", "", fmt.Errorf("files are limited to %s", formatBytes(maxAttachmentBytes))
	}
	name := safeFileName(filepath.Base(resp.Request.URL.Path))
	if name == "" {
		name = "download"
	}
	contentType := resp.Header.Get("Content-Type")
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = parsed
	}
	return data, name, contentType, nil
}
