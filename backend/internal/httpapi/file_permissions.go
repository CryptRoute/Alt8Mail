package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type fileAccess struct {
	FileID   string
	OwnerID  string
	TenantID string
	Access   string
}

func (access fileAccess) CanEdit() bool {
	return access.Access == "owner" || access.Access == "edit"
}

func (access fileAccess) IsOwner() bool {
	return access.Access == "owner"
}

// fileAccessForUser resolves the caller's relationship to a Cabinet file:
// owner, explicit/group edit, explicit/group view, or no access.
func (s *Server) fileAccessForUser(ctx context.Context, user userRecord, fileID string) (fileAccess, error) {
	var access fileAccess
	err := s.store.DB.QueryRow(ctx, `
		SELECT f.id, f.user_id::text, f.tenant_id::text,
		       CASE
		         WHEN f.user_id = $2 THEN 'owner'
		         WHEN perm.can_edit THEN 'edit'
		         WHEN perm.can_view THEN 'view'
		         ELSE ''
		       END
		FROM files f
		LEFT JOIN LATERAL (
			SELECT bool_or(p.access = 'edit') AS can_edit,
			       bool_or(p.access = 'view') AS can_view
			FROM file_permissions p
			WHERE p.file_id = f.id AND (
				(p.grantee_type = 'user' AND p.grantee_id = $2) OR
				(p.grantee_type = 'group' AND p.grantee_id IN (
					SELECT gm.group_id FROM group_members gm
					JOIN groups g ON g.id = gm.group_id AND g.status = 'active' AND g.tenant_id = f.tenant_id
					WHERE gm.user_id = $2
				))
			)
		) perm ON true
		WHERE f.id = $1 AND f.tenant_id = $3
	`, fileID, user.ID, user.TenantID).Scan(&access.FileID, &access.OwnerID, &access.TenantID, &access.Access)
	if err != nil || access.Access == "" {
		return fileAccess{}, fmt.Errorf("file not found")
	}
	return access, nil
}

type filePermissionRecord struct {
	ID          string
	GranteeType string
	GranteeID   string
	Name        string
	Email       string
	Initials    string
	Color       string
	Access      string
	CreatedAt   time.Time
}

func (s *Server) filePermissionRows(ctx context.Context, fileID string) ([]filePermissionRecord, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT p.id, p.grantee_type, p.grantee_id::text, p.access, p.created_at,
		       CASE WHEN p.grantee_type = 'user' THEN COALESCE(u.name, '') ELSE COALESCE(g.name, '') END,
		       CASE WHEN p.grantee_type = 'user' THEN COALESCE(u.email::text, '') ELSE '' END,
		       CASE WHEN p.grantee_type = 'user' THEN COALESCE(u.initials, '') ELSE '' END,
		       CASE WHEN p.grantee_type = 'user' THEN COALESCE(u.color, '') ELSE '' END
		FROM file_permissions p
		LEFT JOIN users u ON p.grantee_type = 'user' AND u.id = p.grantee_id
		LEFT JOIN groups g ON p.grantee_type = 'group' AND g.id = p.grantee_id
		WHERE p.file_id = $1
		ORDER BY p.grantee_type, lower(COALESCE(u.name, g.name, ''))
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	permissions := []filePermissionRecord{}
	for rows.Next() {
		var permission filePermissionRecord
		if rows.Scan(&permission.ID, &permission.GranteeType, &permission.GranteeID, &permission.Access,
			&permission.CreatedAt, &permission.Name, &permission.Email, &permission.Initials, &permission.Color) == nil {
			permissions = append(permissions, permission)
		}
	}
	return permissions, rows.Err()
}

func filePermissionPayload(permission filePermissionRecord) map[string]any {
	return map[string]any{
		"id": permission.ID, "granteeType": permission.GranteeType, "granteeId": permission.GranteeID,
		"name": permission.Name, "email": permission.Email, "initials": permission.Initials,
		"color": permission.Color, "access": permission.Access,
		"createdAt": permission.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) listFilePermissions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	access, err := s.fileAccessForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if !access.IsOwner() {
		writeError(w, http.StatusForbidden, "FILE_OWNER", "Only the file owner can manage internal access")
		return
	}
	permissions, err := s.filePermissionRows(r.Context(), access.FileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_PERMISSIONS", "Could not load file permissions")
		return
	}
	out := make([]map[string]any, 0, len(permissions))
	for _, permission := range permissions {
		out = append(out, filePermissionPayload(permission))
	}
	var fileLocked bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT locked FROM files WHERE id=$1`, access.FileID).Scan(&fileLocked)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{
		"permissions": out, "internalSharing": policy.Office.InternalSharing, "fileLocked": fileLocked,
	})
}

func (s *Server) createFilePermission(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	access, err := s.fileAccessForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if !access.IsOwner() {
		writeError(w, http.StatusForbidden, "FILE_OWNER", "Only the file owner can grant internal access")
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	if !policy.Office.InternalSharing {
		writeError(w, http.StatusForbidden, "INTERNAL_SHARING", "Internal file sharing is disabled by your organization")
		return
	}
	var body struct {
		Email   string `json:"email"`
		GroupID string `json:"groupId"`
		Access  string `json:"access"`
	}
	if !s.decodeBody(w, r, &body, 32<<10) {
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	body.GroupID = strings.TrimSpace(body.GroupID)
	body.Access = strings.ToLower(strings.TrimSpace(body.Access))
	if body.Access != "view" && body.Access != "edit" {
		writeError(w, http.StatusBadRequest, "FILE_ACCESS", "Choose view or edit access")
		return
	}
	if (body.Email == "") == (body.GroupID == "") {
		writeError(w, http.StatusBadRequest, "FILE_GRANTEE", "Choose a person or a group")
		return
	}
	granteeType := "user"
	granteeID := ""
	name, email := "", ""
	if body.GroupID != "" {
		granteeType = "group"
		if err := s.store.DB.QueryRow(r.Context(), `
			SELECT id::text, name FROM groups
			WHERE id=$1 AND tenant_id=$2 AND status='active'
		`, body.GroupID, sess.User.TenantID).Scan(&granteeID, &name); err != nil {
			writeError(w, http.StatusNotFound, "FILE_GROUP", "Group not found in this organization")
			return
		}
	} else {
		if err := s.store.DB.QueryRow(r.Context(), `
			SELECT id::text, name, email::text FROM users
			WHERE tenant_id=$1 AND lower(email::text)=lower($2)
			  AND status='active' AND deleted_at IS NULL
		`, sess.User.TenantID, body.Email).Scan(&granteeID, &name, &email); err != nil {
			writeError(w, http.StatusNotFound, "FILE_PERSON", "Person not found in this organization")
			return
		}
		if granteeID == sess.User.ID {
			writeError(w, http.StatusBadRequest, "FILE_SELF", "You already own this file")
			return
		}
	}
	permissionID := newID("fperm")
	err = s.store.DB.QueryRow(r.Context(), `
		INSERT INTO file_permissions (id, file_id, tenant_id, grantee_type, grantee_id, access, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (file_id, grantee_type, grantee_id)
		DO UPDATE SET access=EXCLUDED.access, created_by=EXCLUDED.created_by, created_at=now()
		RETURNING id
	`, permissionID, access.FileID, sess.User.TenantID, granteeType, granteeID, body.Access, sess.User.ID).Scan(&permissionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_PERMISSIONS", "Could not grant internal access")
		return
	}
	var fileName string
	_ = s.store.DB.QueryRow(r.Context(), `SELECT name FROM files WHERE id=$1`, access.FileID).Scan(&fileName)
	s.recordWorkspaceAudit(r.Context(), sess, "file.permission_granted", fileName,
		fmt.Sprintf("%s access for %s", body.Access, firstNonEmpty(name, email)))
	permission := filePermissionRecord{
		ID: permissionID, GranteeType: granteeType, GranteeID: granteeID,
		Name: name, Email: email, Access: body.Access, CreatedAt: time.Now().UTC(),
	}
	writeJSON(w, http.StatusCreated, map[string]any{"permission": filePermissionPayload(permission)})
}

func (s *Server) deleteFilePermission(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	access, err := s.fileAccessForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if !access.IsOwner() {
		writeError(w, http.StatusForbidden, "FILE_OWNER", "Only the file owner can manage internal access")
		return
	}
	result, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM file_permissions WHERE id=$1 AND file_id=$2 AND tenant_id=$3
	`, r.PathValue("permissionId"), access.FileID, sess.User.TenantID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "FILE_PERMISSION", "Permission not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "file.permission_revoked", access.FileID, "Internal file access revoked")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listShareGroups(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT g.id::text, g.name, g.description,
		       (SELECT count(*) FROM group_members gm WHERE gm.group_id=g.id) AS members
		FROM groups g
		WHERE g.tenant_id=$1 AND g.status='active'
		ORDER BY lower(g.name)
		LIMIT 500
	`, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_GROUPS", "Could not load groups")
		return
	}
	defer rows.Close()
	groups := []map[string]any{}
	for rows.Next() {
		var id, name, description string
		var members int
		if rows.Scan(&id, &name, &description, &members) == nil {
			groups = append(groups, map[string]any{"id": id, "name": name, "description": description, "members": members})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "count": len(groups)})
}
