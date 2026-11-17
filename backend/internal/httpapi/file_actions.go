package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type fileCopyBody struct {
	Name     string `json:"name"`
	FolderID string `json:"folderId"`
}

func (s *Server) copyFile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	source, err := s.loadFileContent(r.Context(), sess.User.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	var body fileCopyBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	folderID := strings.TrimSpace(body.FolderID)
	if body.FolderID == "" {
		folderID = source.FolderID
	}
	folderPath, _, err := s.folderPathFor(r.Context(), sess.User.ID, folderID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FILE_FOLDER", err.Error())
		return
	}
	if source.SizeBytes > maxAttachmentBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "FILE_COPY_SIZE", "Files larger than 8 MB cannot be copied inside Alt8Mail Drive")
		return
	}
	data, err := s.readFileBytes(r.Context(), source, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "FILE_READ", err.Error())
		return
	}
	name := safeFileName(body.Name)
	if name == "" {
		base := source.Name
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		name = safeFileName(stem + " copy" + ext)
	}
	if name == "" {
		name = "Copy"
	}
	path := folderPath
	if path == "" {
		path = "Files"
	}
	id := newID("file")
	storageKey := ""
	externalID := ""
	externalURL := ""
	switch source.ProviderID {
	case "a8m":
		if err := s.enforceStorageQuota(r.Context(), sess.User, int64(len(data))); err != nil {
			writeError(w, http.StatusInsufficientStorage, "STORAGE_QUOTA", err.Error())
			return
		}
		storageKey = newID("att")
		target := filepath.Join(s.cfg.UploadDir, storageKey)
		if err := os.WriteFile(target, data, 0o640); err != nil {
			writeError(w, http.StatusInternalServerError, "FILE_COPY", "Could not copy the file")
			return
		}
	case "s3":
		storageKey = fmt.Sprintf("tenants/%s/users/%s/files/%s/%s", sess.User.TenantID, sess.User.ID, id, name)
		if err := s.s3PutObject(r.Context(), sess.User.TenantID, storageKey, source.MIME, data); err != nil {
			writeError(w, http.StatusBadGateway, "FILE_COPY", "Could not copy the S3 object")
			return
		}
		externalID = storageKey
	default:
		remoteID, remoteURL, err := s.uploadToOAuthProvider(r.Context(), sess, source.ProviderID, name, source.MIME, data)
		if err != nil {
			writeError(w, http.StatusBadGateway, "FILE_COPY", err.Error())
			return
		}
		externalID = remoteID
		externalURL = remoteURL
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path, folder_id,
		                   description, size_bytes, storage_key, external_url, previewable, checksum, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12,$13,$14,$15,'{}'::jsonb)
	`, id, sess.User.TenantID, sess.User.ID, source.ProviderID, externalID, name, source.MIME,
		truncate(path, 160), folderID, truncate(source.Description, 500), len(data), storageKey,
		externalURL, source.Previewable, source.Checksum)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_COPY", "Could not save the copied file")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.copied", name, "Copied "+source.Name)
	file, _ := s.loadFile(r.Context(), sess.User, id)
	writeJSON(w, http.StatusCreated, map[string]any{"file": file})
}

func (s *Server) readFileBytes(ctx context.Context, file contentFile, tenantID string) ([]byte, error) {
	switch file.ProviderID {
	case "a8m":
		path := file.StorageKey
		if path == "" {
			path = file.AttachmentID
		}
		if path == "" || strings.ContainsAny(path, `/\`) || path == "." || path == ".." {
			return nil, fmt.Errorf("file content is no longer available")
		}
		data, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, path))
		if err != nil {
			return nil, fmt.Errorf("file content is no longer available")
		}
		return s.openCabinetBlob(ctx, file.ID, data)
	case "s3":
		data, err := s.s3GetObject(ctx, tenantID, file.StorageKey)
		if err != nil {
			return nil, err
		}
		return s.openCabinetBlob(ctx, file.ID, data)
	default:
		data, _, err := s.downloadFromOAuthProvider(ctx, file.OwnerID, file.ProviderID, file.ExternalID)
		return data, err
	}
}

func (s *Server) listFileShares(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	fileID := r.PathValue("id")
	var exists bool
	if err := s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM files WHERE id=$1 AND user_id=$2)`, fileID, sess.User.ID).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, access, recipient_email::text, allowed_domain::text, expires_at, revoked_at,
		       access_count, created_at
		FROM file_shares WHERE file_id=$1 ORDER BY created_at DESC
	`, fileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SHARES", "Could not load share permissions")
		return
	}
	defer rows.Close()
	shares := []map[string]any{}
	for rows.Next() {
		var id, access, recipient, domain string
		var expiresAt, createdAt time.Time
		var revokedAt *time.Time
		var accessCount int
		if rows.Scan(&id, &access, &recipient, &domain, &expiresAt, &revokedAt, &accessCount, &createdAt) != nil {
			continue
		}
		entry := map[string]any{
			"id": id, "access": access, "recipient": recipient, "domain": domain,
			"expiresAt": expiresAt.UTC().Format(time.RFC3339),
			"createdAt": createdAt.UTC().Format(time.RFC3339), "accessCount": accessCount,
			"active": revokedAt == nil && expiresAt.After(time.Now()),
		}
		if revokedAt != nil {
			entry["revokedAt"] = revokedAt.UTC().Format(time.RFC3339)
		}
		shares = append(shares, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares, "count": len(shares)})
}

func (s *Server) revokeFileShare(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE file_shares s SET revoked_at=now()
		FROM files f
		WHERE s.id=$1 AND s.file_id=f.id AND f.user_id=$2 AND s.revoked_at IS NULL
	`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "SHARE_NOT_FOUND", "Share link not found or already revoked")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.share_revoked", id, "Revoked an external file link")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
