package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const maxFileFolderDepth = 8

type fileFolderRecord struct {
	ID                string
	ParentID          string
	Name              string
	Color             string
	Path              string
	FileCount         int64
	ClientEncrypted   bool
	EncryptionKeyHint string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (folder fileFolderRecord) public() map[string]any {
	return map[string]any{
		"id": folder.ID, "parentId": folder.ParentID, "name": folder.Name,
		"color": folder.Color, "path": folder.Path, "count": folder.FileCount,
		"clientEncrypted": folder.ClientEncrypted, "encryptionKeyHint": folder.EncryptionKeyHint,
		"createdAt": folder.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": folder.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) loadFileFolders(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT f.id, COALESCE(f.parent_id, ''), f.name, f.color, f.created_at, f.updated_at,
		       f.client_encrypted, f.encryption_key_hint,
		       (SELECT count(*) FROM files x WHERE x.user_id=f.user_id AND x.folder_id=f.id
		          AND x.provider_id='a8m')
		FROM file_folders f WHERE f.user_id=$1
		ORDER BY lower(f.name)
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []fileFolderRecord{}
	for rows.Next() {
		var folder fileFolderRecord
		if rows.Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.Color,
			&folder.CreatedAt, &folder.UpdatedAt, &folder.ClientEncrypted, &folder.EncryptionKeyHint, &folder.FileCount) == nil {
			records = append(records, folder)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byID := map[string]fileFolderRecord{}
	for _, folder := range records {
		byID[folder.ID] = folder
	}
	out := make([]map[string]any, 0, len(records))
	for _, folder := range records {
		names := []string{folder.Name}
		cursor := folder.ParentID
		seen := map[string]bool{folder.ID: true}
		for depth := 0; cursor != "" && depth < maxFileFolderDepth+2 && !seen[cursor]; depth++ {
			parent, ok := byID[cursor]
			if !ok {
				break
			}
			seen[cursor] = true
			names = append([]string{parent.Name}, names...)
			cursor = parent.ParentID
		}
		folder.Path = strings.Join(names, " / ")
		out = append(out, folder.public())
	}
	return out, nil
}

func (s *Server) getFileFolders(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	folders, err := s.loadFileFolders(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDERS", "Could not load Cabinet folders")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders, "count": len(folders)})
}

type fileFolderBody struct {
	Name              *string `json:"name"`
	ParentID          *string `json:"parentId"`
	Color             *string `json:"color"`
	ClientEncrypted   *bool   `json:"clientEncrypted"`
	EncryptionKeyHint *string `json:"encryptionKeyHint"`
}

func cleanFolderName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	name = strings.ReplaceAll(name, "/", "-")
	if name == "" || len([]rune(name)) > 80 {
		return "", false
	}
	return name, true
}

func cleanFolderColor(value string) (string, bool) {
	color := strings.TrimSpace(value)
	if color == "" {
		return "", true
	}
	if !mailLabelColorPattern.MatchString(color) {
		return "", false
	}
	return strings.ToLower(color), true
}

// folderChainDepth validates a parent chain and returns the folder path.
func (s *Server) folderPathFor(ctx context.Context, userID, folderID string) (string, int, error) {
	if folderID == "" {
		return "", 0, nil
	}
	names := []string{}
	cursor := folderID
	seen := map[string]bool{}
	for depth := 0; depth < maxFileFolderDepth+2; depth++ {
		if cursor == "" {
			return strings.Join(names, " / "), len(names), nil
		}
		if seen[cursor] {
			return "", 0, fmt.Errorf("folder hierarchy contains a cycle")
		}
		seen[cursor] = true
		var name, parent string
		if err := s.store.DB.QueryRow(ctx, `
			SELECT name, COALESCE(parent_id, '') FROM file_folders WHERE id=$1 AND user_id=$2
		`, cursor, userID).Scan(&name, &parent); err != nil {
			return "", 0, fmt.Errorf("folder not found")
		}
		names = append([]string{name}, names...)
		cursor = parent
	}
	return "", 0, fmt.Errorf("folder hierarchy is too deep")
}

func (s *Server) createFileFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body fileFolderBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if body.Name == nil {
		writeError(w, http.StatusBadRequest, "FOLDER_NAME", "Give the folder a name")
		return
	}
	name, ok := cleanFolderName(*body.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "FOLDER_NAME", "Use a folder name of 1–80 characters")
		return
	}
	color, ok := cleanFolderColor(valueOrEmpty(body.Color))
	if !ok {
		writeError(w, http.StatusBadRequest, "FOLDER_COLOR", "Choose a six-digit hex colour")
		return
	}
	parentID := strings.TrimSpace(valueOrEmpty(body.ParentID))
	_, depth, err := s.folderPathFor(r.Context(), sess.User.ID, parentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FOLDER_PARENT", err.Error())
		return
	}
	if depth >= maxFileFolderDepth {
		writeError(w, http.StatusBadRequest, "FOLDER_DEPTH", "Cabinet folders can be nested up to eight levels")
		return
	}
	var duplicate int
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FROM file_folders
		WHERE user_id=$1 AND COALESCE(parent_id, '')=$2 AND lower(name)=lower($3)
	`, sess.User.ID, parentID, name).Scan(&duplicate)
	if duplicate > 0 {
		writeError(w, http.StatusConflict, "FOLDER_EXISTS", "A folder with that name already exists here")
		return
	}
	id := newID("folder")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO file_folders (id, tenant_id, user_id, parent_id, name, color, client_encrypted, encryption_key_hint)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)
	`, id, sess.User.TenantID, sess.User.ID, parentID, name, color, body.ClientEncrypted != nil && *body.ClientEncrypted, truncate(strings.TrimSpace(valueOrEmpty(body.EncryptionKeyHint)), 120))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not create the folder")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.folder_created", name, "Created a Cabinet folder")
	s.writeFolders(w, r)
}

func (s *Server) updateFileFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body fileFolderBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	var currentName, currentParent, currentColor string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, COALESCE(parent_id, ''), color FROM file_folders WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID).Scan(&currentName, &currentParent, &currentColor); err != nil {
		writeError(w, http.StatusNotFound, "FOLDER_NOT_FOUND", "Folder not found")
		return
	}
	name := currentName
	if body.Name != nil {
		value, ok := cleanFolderName(*body.Name)
		if !ok {
			writeError(w, http.StatusBadRequest, "FOLDER_NAME", "Use a folder name of 1–80 characters")
			return
		}
		name = value
	}
	color := currentColor
	if body.Color != nil {
		value, ok := cleanFolderColor(*body.Color)
		if !ok {
			writeError(w, http.StatusBadRequest, "FOLDER_COLOR", "Choose a six-digit hex colour")
			return
		}
		color = value
	}
	parentID := currentParent
	if body.ParentID != nil {
		parentID = strings.TrimSpace(*body.ParentID)
	}
	if parentID == id {
		writeError(w, http.StatusBadRequest, "FOLDER_PARENT", "A folder cannot be moved inside itself")
		return
	}
	// Reject moving a folder into one of its own descendants.
	cursor := parentID
	for depth := 0; cursor != "" && depth < maxFileFolderDepth+2; depth++ {
		if cursor == id {
			writeError(w, http.StatusBadRequest, "FOLDER_CYCLE", "A folder cannot be moved inside one of its subfolders")
			return
		}
		var parent string
		if err := s.store.DB.QueryRow(r.Context(), `SELECT COALESCE(parent_id,'') FROM file_folders WHERE id=$1 AND user_id=$2`, cursor, sess.User.ID).Scan(&parent); err != nil {
			writeError(w, http.StatusBadRequest, "FOLDER_PARENT", "The destination folder does not exist")
			return
		}
		cursor = parent
	}
	_, depth, err := s.folderPathFor(r.Context(), sess.User.ID, parentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FOLDER_PARENT", err.Error())
		return
	}
	if depth >= maxFileFolderDepth {
		writeError(w, http.StatusBadRequest, "FOLDER_DEPTH", "Cabinet folders can be nested up to eight levels")
		return
	}
	var duplicate int
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FROM file_folders
		WHERE user_id=$1 AND COALESCE(parent_id,'')=$2 AND lower(name)=lower($3) AND id<>$4
	`, sess.User.ID, parentID, name, id).Scan(&duplicate)
	if duplicate > 0 {
		writeError(w, http.StatusConflict, "FOLDER_EXISTS", "A folder with that name already exists here")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE file_folders SET name=$3, color=$4, parent_id=NULLIF($5,''), updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID, name, color, parentID); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not update the folder")
		return
	}
	s.refreshFolderPaths(r.Context(), sess.User.ID)
	s.recordWorkspaceAudit(r.Context(), sess, "file.folder_updated", name, "Updated a Cabinet folder")
	s.writeFolders(w, r)
}

func (s *Server) deleteFileFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var parentID, name string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT COALESCE(parent_id,''), name FROM file_folders WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID).Scan(&parentID, &name); err != nil {
		writeError(w, http.StatusNotFound, "FOLDER_NOT_FOUND", "Folder not found")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not delete the folder")
		return
	}
	defer tx.Rollback(r.Context())
	// Keep the contents: files and subfolders move up one level.
	if _, err := tx.Exec(r.Context(), `UPDATE files SET folder_id=NULLIF($3,'') WHERE user_id=$1 AND folder_id=$2`, sess.User.ID, id, parentID); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not move the folder contents")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE file_folders SET parent_id=NULLIF($3,''), updated_at=now() WHERE user_id=$1 AND parent_id=$2`, sess.User.ID, id, parentID); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not move subfolders")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM file_folders WHERE id=$1 AND user_id=$2`, id, sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not delete the folder")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER", "Could not commit the folder deletion")
		return
	}
	s.refreshFolderPaths(r.Context(), sess.User.ID)
	s.recordWorkspaceAudit(r.Context(), sess, "file.folder_deleted", name, "Deleted a Cabinet folder; contents moved up one level")
	s.writeFolders(w, r)
}

func (s *Server) writeFolders(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	folders, err := s.loadFileFolders(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDERS", "Could not reload Cabinet folders")
		return
	}
	files, _ := s.loadFiles(r.Context(), sess.User, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "folders": folders, "files": files})
}

// refreshFolderPaths keeps files.path in sync with their folder location so
// search, details and connected clients see a stable human-readable path.
func (s *Server) refreshFolderPaths(ctx context.Context, userID string) {
	folders, err := s.loadFileFolders(ctx, userID)
	if err != nil {
		return
	}
	paths := map[string]string{}
	for _, folder := range folders {
		paths[stateString(folder, "id")] = stateString(folder, "path")
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, COALESCE(folder_id,''), provider_id FROM files
		WHERE user_id=$1 AND (provider_id='a8m' OR folder_id IS NOT NULL)
	`, userID)
	if err != nil {
		return
	}
	defer rows.Close()
	type update struct{ id, path string }
	updates := []update{}
	for rows.Next() {
		var id, folderID, providerID string
		if rows.Scan(&id, &folderID, &providerID) != nil {
			continue
		}
		if providerID != "a8m" && folderID == "" {
			// Connected-drive paths belong to the provider; folder moves must not
			// overwrite them with the internal Cabinet label.
			continue
		}
		path := paths[folderID]
		if path == "" {
			path = "Files"
		}
		updates = append(updates, update{id: id, path: path})
	}
	for _, item := range updates {
		_, _ = s.store.DB.Exec(ctx, `UPDATE files SET path=$2 WHERE id=$1 AND user_id=$3`, item.id, truncate(item.path, 160), userID)
	}
}
