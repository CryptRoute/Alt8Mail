package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const maxAttachmentBytes = 8 * 1024 * 1024

type fileCreateBody struct {
	ProviderID   string  `json:"providerId"`
	AttachmentID string  `json:"attachmentId"`
	Name         string  `json:"name"`
	SizeBytes    float64 `json:"sizeBytes"`
	MIME         string  `json:"mime"`
	Path         string  `json:"path"`
	Description  string  `json:"description"`
}

type filePatchBody struct {
	Starred     *bool   `json:"starred"`
	Description *string `json:"description"`
}

type fileShareBody struct {
	Access         string  `json:"access"`
	ExpiresDays    float64 `json:"expiresDays"`
	RecipientEmail string  `json:"recipientEmail"`
}

type attachmentUploadBody struct {
	Name       string `json:"name"`
	MIME       string `json:"mime"`
	DataBase64 string `json:"dataBase64"`
}

type fileRecord struct {
	ID, ProviderID, ExternalID, Name, MIME, Path, Description, StorageKey, AttachmentID, ExternalURL string
	Metadata                                                                                         []byte
	SizeBytes                                                                                        int64
	Starred, Locked, Previewable                                                                     bool
	Version                                                                                          int
	Checksum                                                                                         string
	CreatedAt, UpdatedAt                                                                             time.Time
}

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	providers, err := s.publicStorageProviders(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE", "Could not load storage providers")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

func (s *Server) storageAction(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	providerID := r.PathValue("id")
	action := r.PathValue("action")
	provider, ok := s.storageProvider(providerID)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Storage provider not found")
		return
	}
	switch action {
	case "connect":
		if provider.Kind == "oauth" && !provider.Configured {
			writeError(w, http.StatusServiceUnavailable, "OAUTH_NOT_CONFIGURED", provider.Name+" OAuth is not configured on this server")
			return
		}
		if provider.Kind == "s3" && !provider.Configured {
			writeError(w, http.StatusServiceUnavailable, "S3_NOT_CONFIGURED", "S3/MinIO is not configured on this server")
			return
		}
		if provider.Kind == "oauth" {
			authorizeURL, err := s.beginStorageOAuth(r.Context(), sess, providerID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "OAUTH", err.Error())
				return
			}
			providers, _ := s.publicStorageProviders(r.Context(), sess.User)
			writeJSON(w, http.StatusOK, map[string]any{"provider": storageProviderPayload(providers, providerID), "authorizationUrl": authorizeURL})
			return
		}
		accountEmail := sess.User.Email
		providerKind := "internal"
		if provider.Kind == "s3" {
			accountEmail = s.cfg.S3Bucket
			providerKind = "s3"
			if err := s.checkS3(r.Context()); err != nil {
				writeError(w, http.StatusBadGateway, "S3_UNAVAILABLE", "The S3 bucket is not reachable: "+err.Error())
				return
			}
		}
		_, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO storage_accounts (id, tenant_id, user_id, provider_id, provider_kind, account_email, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'connected')
			ON CONFLICT (user_id, provider_id) DO UPDATE
			SET status = 'connected', account_email = EXCLUDED.account_email, updated_at = now()
		`, newID("storage"), sess.User.TenantID, sess.User.ID, providerID, providerKind, accountEmail)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "STORAGE", "Could not connect the storage provider")
			return
		}
		if provider.Kind == "s3" {
			if err := s.syncS3Provider(r.Context(), sess); err != nil {
				s.logger.Warn("initial S3 sync failed", "err", err)
			}
		}
		s.recordWorkspaceAudit(r.Context(), sess, "storage.connected", provider.Name, "Connected as "+accountEmail)
	case "disconnect":
		if provider.Kind == "internal" {
			writeError(w, http.StatusBadRequest, "INTERNAL_STORAGE", "Meridian Drive is managed by the organization and cannot be disconnected")
			return
		}
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM files WHERE user_id=$1 AND provider_id=$2`, sess.User.ID, providerID)
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM storage_accounts WHERE user_id=$1 AND provider_id=$2`, sess.User.ID, providerID)
		s.recordWorkspaceAudit(r.Context(), sess, "storage.disconnected", provider.Name, "Storage provider disconnected")
	case "sync":
		if err := s.syncStorageProvider(r.Context(), sess, providerID); err != nil {
			writeError(w, http.StatusBadGateway, "STORAGE_SYNC", err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "ACTION", "Unknown storage action")
		return
	}
	providers, _ := s.publicStorageProviders(r.Context(), sess.User)
	files, _ := s.loadFiles(r.Context(), sess.User, providerID)
	writeJSON(w, http.StatusOK, map[string]any{"provider": storageProviderPayload(providers, providerID), "files": files})
}

func storageProviderPayload(providers []map[string]any, id string) map[string]any {
	for _, provider := range providers {
		if stateString(provider, "id") == id {
			return provider
		}
	}
	return map[string]any{"id": id}
}

func (s *Server) getFiles(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	files, err := s.loadFiles(r.Context(), sess.User, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILES", "Could not load files")
		return
	}
	providers, _ := s.publicStorageProviders(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{"count": len(files), "files": files, "providers": providers})
}

func (s *Server) loadFiles(ctx context.Context, user userRecord, providerID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT f.id, f.provider_id, f.external_id, f.name, f.mime, f.path, f.description,
		       f.size_bytes, f.storage_key, COALESCE(f.attachment_id,''), f.external_url,
		       f.starred, f.locked, f.version, f.previewable, f.checksum, f.metadata,
		       f.created_at, f.updated_at,
		       COALESCE((SELECT jsonb_agg(
		         CASE WHEN s2.recipient_email <> '' THEN s2.recipient_email::text ELSE 'External link' END
		         ORDER BY s2.created_at DESC)
		         FROM file_shares s2 WHERE s2.file_id = f.id AND s2.revoked_at IS NULL AND s2.expires_at > now()), '[]'::jsonb)
		FROM files f
		WHERE f.user_id = $1 AND ($2 = '' OR f.provider_id = $2)
		ORDER BY f.updated_at DESC
	`, user.ID, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []map[string]any{}
	for rows.Next() {
		var file fileRecord
		var sharedRaw []byte
		if err := rows.Scan(&file.ID, &file.ProviderID, &file.ExternalID, &file.Name, &file.MIME,
			&file.Path, &file.Description, &file.SizeBytes, &file.StorageKey, &file.AttachmentID,
			&file.ExternalURL, &file.Starred, &file.Locked, &file.Version, &file.Previewable,
			&file.Checksum, &file.Metadata, &file.CreatedAt, &file.UpdatedAt, &sharedRaw); err != nil {
			continue
		}
		files = append(files, s.publicFile(file, user, sharedRaw))
	}
	return files, rows.Err()
}

func (s *Server) publicFile(file fileRecord, user userRecord, sharedRaw []byte) map[string]any {
	shared := []any{}
	_ = jsonUnmarshal(sharedRaw, &shared)
	owner := map[string]any{"name": user.Name, "email": user.Email, "initials": user.Initials, "color": user.Color}
	contentURL := "/api/files/" + file.ID + "/content"
	return map[string]any{
		"id": file.ID, "name": file.Name, "type": attachmentBadgeType(file.MIME, file.Name),
		"mime": file.MIME, "size": formatBytes(file.SizeBytes), "sizeBytes": file.SizeBytes,
		"providerId": file.ProviderID, "path": firstNonEmpty(file.Path, "Files"),
		"owner": owner, "modifiedAt": file.UpdatedAt.UTC().Format(time.RFC3339),
		"createdAt": file.CreatedAt.UTC().Format(time.RFC3339), "starred": file.Starred,
		"sharedWith": shared, "locked": file.Locked, "version": fmt.Sprint(file.Version),
		"previewable": file.Previewable, "contentUrl": contentURL, "description": file.Description,
		"externalId": file.ExternalID, "externalUrl": file.ExternalURL,
	}
}

func (s *Server) createFile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body fileCreateBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	providerID := body.ProviderID
	if providerID == "" {
		providerID = "meridian"
	}
	provider, ok := s.storageProvider(providerID)
	if !ok {
		writeError(w, http.StatusBadRequest, "FILE_PROVIDER", "Choose a valid storage provider")
		return
	}
	connected, err := s.storageConnected(r.Context(), sess.User.ID, providerID)
	if err != nil || !connected {
		writeError(w, http.StatusConflict, "PROVIDER_OFFLINE", provider.Name+" is not connected")
		return
	}
	if body.AttachmentID == "" {
		writeError(w, http.StatusBadRequest, "FILE_CONTENT", "Upload a file attachment before adding it to storage")
		return
	}
	var attachmentName, attachmentMIME, storedName string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, mime, stored_path FROM attachments WHERE id=$1 AND user_id=$2
	`, body.AttachmentID, sess.User.ID).Scan(&attachmentName, &attachmentMIME, &storedName); err != nil {
		writeError(w, http.StatusNotFound, "ATTACHMENT_NOT_FOUND", "Attachment not found")
		return
	}
	if storedName == "" || strings.ContainsAny(storedName, `/\`) || storedName == "." || storedName == ".." {
		writeError(w, http.StatusNotFound, "ATTACHMENT_NOT_FOUND", "Attachment content is no longer available")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, storedName))
	if err != nil {
		writeError(w, http.StatusNotFound, "ATTACHMENT_NOT_FOUND", "Attachment content is no longer available")
		return
	}
	name := safeFileName(attachmentName)
	if name == "" {
		name = "Untitled"
	}
	mime := attachmentMIME
	description := body.Description
	if description == "" {
		description = "Uploaded from the webmail compose window."
	}
	path := body.Path
	if path == "" {
		if provider.Kind == "internal" {
			path = "Uploads"
		} else {
			path = provider.Name
		}
	}
	id := newID("file")
	storageKey := storedName
	externalID := ""
	externalURL := ""
	mime, _ = detectUploadMIME(data, mime, name)
	switch provider.Kind {
	case "s3":
		key := fmt.Sprintf("tenants/%s/users/%s/files/%s/%s", sess.User.TenantID, sess.User.ID, id, name)
		if err := s.s3PutObject(r.Context(), key, mime, data); err != nil {
			writeError(w, http.StatusBadGateway, "S3_UPLOAD", "Could not upload the file to S3: "+err.Error())
			return
		}
		storageKey = key
		// Keep the object key as the provider identity so a later bucket sync
		// updates this row instead of creating a duplicate.
		externalID = key
	case "oauth":
		remoteID, remoteURL, err := s.uploadToOAuthProvider(r.Context(), sess, providerID, name, mime, data)
		if err != nil {
			writeError(w, http.StatusBadGateway, "PROVIDER_UPLOAD", err.Error())
			return
		}
		externalID = remoteID
		externalURL = remoteURL
		storageKey = ""
	}
	previewable := (mime == "application/pdf" || strings.HasPrefix(mime, "image/")) && mime != "image/svg+xml"
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path, description,
		                   size_bytes, storage_key, attachment_id, external_url, previewable, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`, id, sess.User.TenantID, sess.User.ID, providerID, externalID, name, mime, truncate(path, 160),
		truncate(description, 500), len(data), storageKey, body.AttachmentID, externalURL, previewable, `{}`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE", "Could not save the file record")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.created", name, "Added to "+provider.Name+" · "+formatBytes(int64(len(data))))
	file, _ := s.loadFile(r.Context(), sess.User, id)
	writeJSON(w, http.StatusCreated, map[string]any{"file": file})
}

func (s *Server) patchFile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body filePatchBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	updates := []string{}
	args := []any{id, sess.User.ID}
	if body.Starred != nil {
		updates = append(updates, fmt.Sprintf("starred=$%d", len(args)+1))
		args = append(args, *body.Starred)
	}
	if body.Description != nil {
		updates = append(updates, fmt.Sprintf("description=$%d", len(args)+1))
		args = append(args, truncate(*body.Description, 500))
	}
	if len(updates) == 0 {
		file, _ := s.loadFile(r.Context(), sess.User, id)
		writeJSON(w, http.StatusOK, map[string]any{"file": file})
		return
	}
	updates = append(updates, "updated_at=now()")
	result, err := s.store.DB.Exec(r.Context(), `UPDATE files SET `+strings.Join(updates, ", ")+` WHERE id=$1 AND user_id=$2`, args...)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.updated", id, "File metadata updated")
	file, _ := s.loadFile(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"file": file})
}

func (s *Server) shareFile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body fileShareBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	access := "view"
	if body.Access == "edit" {
		access = "edit"
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Sharing
	if !policy.ExternalSharing {
		writeError(w, http.StatusForbidden, "SHARING_DISABLED", "External file sharing is disabled by your organization")
		return
	}
	if policy.LinkDefault == "disabled" {
		writeError(w, http.StatusForbidden, "LINK_SHARING_DISABLED", "Link sharing is disabled by your organization")
		return
	}
	if access == "edit" && policy.LinkDefault != "edit" {
		writeError(w, http.StatusForbidden, "SHARING_ACCESS_LIMIT", "Your organization permits view-only external links")
		return
	}
	recipient := strings.ToLower(strings.TrimSpace(body.RecipientEmail))
	allowedDomain := ""
	if len(policy.AllowedDomains) > 0 {
		if recipient == "" || !calendarEmailPattern.MatchString(recipient) {
			writeError(w, http.StatusBadRequest, "SHARING_RECIPIENT", "A recipient email is required for domain-restricted sharing")
			return
		}
		_, domain := splitEmail(recipient)
		allowed := false
		for _, candidate := range policy.AllowedDomains {
			if strings.EqualFold(strings.TrimSpace(candidate), domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "SHARING_DOMAIN_RESTRICTED", "That recipient domain is not approved for external sharing")
			return
		}
		allowedDomain = domain
	} else if recipient != "" && !calendarEmailPattern.MatchString(recipient) {
		writeError(w, http.StatusBadRequest, "SHARING_RECIPIENT", "Recipient email is invalid")
		return
	}
	expiresDays := int(body.ExpiresDays)
	if expiresDays < 1 {
		expiresDays = 7
	}
	if expiresDays > 90 {
		expiresDays = 90
	}
	token := randomToken(32)
	expiresAt := time.Now().Add(time.Duration(expiresDays) * 24 * time.Hour).UTC()
	result, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO file_shares (id, file_id, tenant_id, created_by, token_hash, access, expires_at, recipient_email, allowed_domain)
		SELECT $1, f.id, f.tenant_id, f.user_id, $3, $4, $5, $6, $7
		FROM files f WHERE f.id=$2 AND f.user_id=$8
	`, newID("share"), id, hashShareToken(token), access, expiresAt, recipient, allowedDomain, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.shared", id, fmt.Sprintf("External %s link · expires in %d days", access, expiresDays))
	sharesCount, _ := s.fileShareCount(r.Context(), id)
	share := map[string]any{
		"url":    strings.TrimRight(s.cfg.PublicURL, "/") + "/files/s/" + token,
		"access": access, "expiresAt": expiresAt.Format(time.RFC3339), "expiresDays": expiresDays,
		"recipientEmail": recipient, "sharedWith": sharesCount,
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": share})
}

func hashShareToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) fileShareCount(ctx context.Context, fileID string) (int, error) {
	var count int
	err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM file_shares WHERE file_id=$1 AND revoked_at IS NULL AND expires_at > now()`, fileID).Scan(&count)
	return count, err
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if file, err := s.loadFileContent(r.Context(), sess.User.ID, id); err == nil && file.ProviderID == "s3" && file.StorageKey != "" {
		if err := s.s3DeleteObject(r.Context(), file.StorageKey); err != nil {
			s.logger.Warn("S3 object delete failed", "err", err)
			// Keep the row so the user can retry. Deleting it now would make the
			// next bucket sync resurrect the object as a new file record.
			writeError(w, http.StatusBadGateway, "FILE_DELETE", "The storage provider could not delete the object; try again")
			return
		}
	}
	result, err := s.store.DB.Exec(r.Context(), `DELETE FROM files WHERE id=$1 AND user_id=$2`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.deleted", id, "File removed from the workspace")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) loadFile(ctx context.Context, user userRecord, id string) (map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT f.id, f.provider_id, f.external_id, f.name, f.mime, f.path, f.description,
		       f.size_bytes, f.storage_key, COALESCE(f.attachment_id,''), f.external_url,
		       f.starred, f.locked, f.version, f.previewable, f.checksum, f.metadata,
		       f.created_at, f.updated_at, '[]'::jsonb
		FROM files f WHERE f.id=$1 AND f.user_id=$2
	`, id, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("file not found")
	}
	var file fileRecord
	var sharedRaw []byte
	if err := rows.Scan(&file.ID, &file.ProviderID, &file.ExternalID, &file.Name, &file.MIME, &file.Path,
		&file.Description, &file.SizeBytes, &file.StorageKey, &file.AttachmentID, &file.ExternalURL,
		&file.Starred, &file.Locked, &file.Version, &file.Previewable, &file.Checksum, &file.Metadata,
		&file.CreatedAt, &file.UpdatedAt, &sharedRaw); err != nil {
		return nil, err
	}
	return s.publicFile(file, user, sharedRaw), nil
}

func (s *Server) getFileContent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	file, err := s.loadFileContent(r.Context(), sess.User.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	s.serveFileContent(w, r, file, file.OwnerID)
}

type contentFile struct {
	fileRecord
	OwnerID string
}

func (s *Server) loadFileContent(ctx context.Context, userID, id string) (contentFile, error) {
	var file contentFile
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, provider_id, external_id, name, mime, path, description, size_bytes, storage_key,
		       COALESCE(attachment_id,''), external_url, starred, locked, version, previewable,
		       checksum, metadata, created_at, updated_at, user_id::text
		FROM files WHERE id=$1 AND user_id=$2
	`, id, userID).Scan(&file.ID, &file.ProviderID, &file.ExternalID, &file.Name, &file.MIME,
		&file.Path, &file.Description, &file.SizeBytes, &file.StorageKey, &file.AttachmentID,
		&file.ExternalURL, &file.Starred, &file.Locked, &file.Version, &file.Previewable,
		&file.Checksum, &file.Metadata, &file.CreatedAt, &file.UpdatedAt, &file.OwnerID)
	if err != nil {
		return contentFile{}, err
	}
	return file, nil
}

func (s *Server) serveFileContent(w http.ResponseWriter, r *http.Request, file contentFile, ownerID string) {
	inline := r.URL.Query().Get("download") != "1"
	disposition := "attachment"
	if inline && file.Previewable {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", file.MIME)
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+url.PathEscape(file.Name))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if file.MIME == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	}
	switch file.ProviderID {
	case "meridian":
		path := file.StorageKey
		if path == "" {
			path = file.AttachmentID
		}
		if path == "" || strings.ContainsAny(path, `/\`) || path == "." || path == ".." {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "File content is no longer available")
			return
		}
		handle, err := os.Open(filepath.Join(s.cfg.UploadDir, path))
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "File content is no longer available")
			return
		}
		defer handle.Close()
		if stat, err := handle.Stat(); err == nil {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, handle)
	case "s3":
		data, err := s.s3GetObject(r.Context(), file.StorageKey)
		if err != nil {
			writeError(w, http.StatusBadGateway, "STORAGE_CONTENT", "Could not read the S3 object")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	default:
		data, mime, err := s.downloadFromOAuthProvider(r.Context(), ownerID, file.ProviderID, file.ExternalID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "STORAGE_CONTENT", err.Error())
			return
		}
		if mime != "" {
			w.Header().Set("Content-Type", mime)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func (s *Server) storageConnected(ctx context.Context, userID, providerID string) (bool, error) {
	if providerID == "meridian" {
		return true, nil
	}
	var connected bool
	err := s.store.DB.QueryRow(ctx, `SELECT status='connected' FROM storage_accounts WHERE user_id=$1 AND provider_id=$2`, userID, providerID).Scan(&connected)
	if err != nil {
		return false, nil
	}
	return connected, nil
}

func jsonUnmarshal(data []byte, target any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, target)
}

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body attachmentUploadBody
	if !s.decodeBody(w, r, &body, 13*1024*1024) {
		return
	}
	raw := body.DataBase64
	if comma := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && comma >= 0 {
		raw = raw[comma+1:]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "ATTACHMENT_INVALID", "Choose a file to upload")
		return
	}
	if len(data) > maxAttachmentBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "ATTACHMENT_TOO_LARGE", "Attachments are limited to 8 MB")
		return
	}
	if err := s.scanUpload(r.Context(), data); err != nil {
		if errors.Is(err, errUploadInfected) {
			writeError(w, http.StatusUnprocessableEntity, "ATTACHMENT_INFECTED", "The uploaded file failed the antivirus scan")
		} else {
			s.logger.Warn("attachment antivirus scan failed", "err", err)
			writeError(w, http.StatusServiceUnavailable, "ATTACHMENT_SCAN", "The file could not be scanned for malware")
		}
		return
	}
	name := safeFileName(body.Name)
	if name == "" {
		name = "attachment"
	}
	mime, previewable := detectUploadMIME(data, body.MIME, name)
	id := newID("att")
	record := map[string]any{
		"id": id, "name": name, "type": attachmentBadgeType(mime, name), "mime": mime,
		"size": formatBytes(int64(len(data))), "sizeBytes": len(data), "storedName": id,
		"previewable": previewable && mime != "text/plain", "uploadedAt": nowISO(),
	}
	uploadDir := s.cfg.UploadDir
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		s.logger.Error("attachment dir", "err", err)
		writeError(w, http.StatusInternalServerError, "ATTACHMENT_STORE", "Could not store the attachment")
		return
	}
	target := filepath.Join(uploadDir, id)
	if err := os.WriteFile(target, data, 0o640); err != nil {
		s.logger.Error("attachment write", "err", err)
		writeError(w, http.StatusInternalServerError, "ATTACHMENT_STORE", "Could not store the attachment")
		return
	}
	sum := sha256.Sum256(data)
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO attachments (id, tenant_id, user_id, name, mime, size_bytes, sha256, stored_path, previewable)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, id, sess.User.TenantID, sess.User.ID, name, mime, len(data), hex.EncodeToString(sum[:]), id, stateBool(record, "previewable"))
	if err != nil {
		_ = os.Remove(target)
		writeError(w, http.StatusInternalServerError, "ATTACHMENT_STORE", "Could not save the attachment")
		return
	}
	record["owner"] = personFor(nil, sess.User.Email, sess.User.Name)
	s.auditRequest(r, sess, "attachment.uploaded", name,
		fmt.Sprintf("%s · %s", formatBytes(int64(len(data))), mime))
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": publicAttachment(record)})
}

func (s *Server) attachmentContent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var name, mime, storedName string
	var sizeBytes int64
	var previewable bool
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, mime, size_bytes, stored_path, previewable
		FROM attachments WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID).Scan(&name, &mime, &sizeBytes, &storedName, &previewable)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Attachment not found")
		return
	}
	if storedName == "" || strings.ContainsAny(storedName, `/\`) || storedName == "." || storedName == ".." {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Attachment content is no longer available")
		return
	}
	file, err := os.Open(filepath.Join(s.cfg.UploadDir, storedName))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Attachment content is no longer available")
		return
	}
	defer file.Close()
	if mime == "" {
		mime = "application/octet-stream"
	}
	disposition := "attachment"
	if previewable {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", sizeBytes))
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if mime == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func detectUploadMIME(data []byte, declared, name string) (string, bool) {
	if len(data) >= 5 && string(data[:5]) == "%PDF-" {
		return "application/pdf", true
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png", true
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg", true
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif", true
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp", true
	}
	allowed := map[string]bool{"application/pdf": true, "image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true, "text/plain": true}
	if allowed[declared] {
		return declared, declared != "text/plain"
	}
	byExtension := mimeForExtension(name)
	if allowed[byExtension] {
		return byExtension, byExtension != "text/plain"
	}
	if utf8.Valid(data) && len(data) < 1024*1024 {
		return "text/plain", false
	}
	return "application/octet-stream", false
}
