package control

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/auth"
)

func (s *Server) registerAccessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/access/meta", s.require("access.read", s.handleAccessMeta))
	mux.HandleFunc("GET /control/api/access/users", s.require("access.read", s.handleAccessUsers))
	mux.HandleFunc("POST /control/api/access/users", s.require("admin.manage", s.handleAccessUserCreate))
	mux.HandleFunc("PATCH /control/api/access/users/{id}", s.require("admin.manage", s.handleAccessUserUpdate))
	mux.HandleFunc("DELETE /control/api/access/users/{id}", s.require("admin.manage", s.handleAccessUserDelete))
	mux.HandleFunc("POST /control/api/access/users/{id}/password", s.require("admin.manage", s.handleAccessUserPassword))
	mux.HandleFunc("POST /control/api/access/users/{id}/totp-disable", s.require("admin.manage", s.handleAccessUserTOTPDisable))
	mux.HandleFunc("GET /control/api/access/groups", s.require("access.read", s.handleAccessGroups))
	mux.HandleFunc("POST /control/api/access/groups", s.require("group.manage", s.handleAccessGroupCreate))
	mux.HandleFunc("PATCH /control/api/access/groups/{id}", s.require("group.manage", s.handleAccessGroupUpdate))
	mux.HandleFunc("DELETE /control/api/access/groups/{id}", s.require("group.manage", s.handleAccessGroupDelete))
	mux.HandleFunc("GET /control/api/access/sessions", s.require("sessions.manage", s.handleAllSessions))
	mux.HandleFunc("DELETE /control/api/access/sessions/{id}", s.require("sessions.manage", s.handleAdminSessionRevoke))
	mux.HandleFunc("POST /control/api/access/sessions/revoke-user/{id}", s.require("sessions.manage", s.handleAdminRevokeUserSessions))
	mux.HandleFunc("GET /control/api/audit", s.require("audit.read", s.handleAudit))
	mux.HandleFunc("GET /control/api/audit.csv", s.require("audit.read", s.handleAuditCSV))
}

func (s *Server) handleAccessMeta(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	roles := []map[string]any{}
	for _, role := range []string{"superadmin", "admin", "operator", "readonly"} {
		roles = append(roles, map[string]any{"name": role, "permissions": rolePermissions(role)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roles": roles, "permissions": allPermissions,
		"highRiskPermissions": []string{"admin.manage", "group.manage"},
	})
}

func (s *Server) handleAccessUsers(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT u.id::text,u.email::text,u.name,u.role,u.status,u.totp_enabled,
		       u.session_ttl_seconds,u.failed_attempts,u.locked_until,u.last_login_at,u.created_at,
		       COALESCE((SELECT string_agg(g.name, ', ' ORDER BY g.name) FROM control.group_members gm
		         JOIN control.groups g ON g.id=gm.group_id WHERE gm.user_id=u.id), '')
		FROM control.admin_users u ORDER BY u.created_at
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, email, name, role, status, groups string
		var totpEnabled bool
		var ttl, failed int
		var locked, lastLogin *time.Time
		var created time.Time
		if rows.Scan(&id, &email, &name, &role, &status, &totpEnabled, &ttl, &failed, &locked, &lastLogin, &created, &groups) == nil {
			item := map[string]any{"id": id, "email": email, "name": name, "role": role, "status": status,
				"totpEnabled": totpEnabled, "sessionTtlSeconds": ttl, "failedAttempts": failed,
				"lockedUntil": locked, "groups": groups, "createdAt": created.UTC().Format(time.RFC3339),
				"current": id == sess.User.ID}
			if lastLogin != nil {
				item["lastLoginAt"] = lastLogin.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": items})
}

func validAccessRole(role string) bool {
	return role == "superadmin" || role == "admin" || role == "operator" || role == "readonly"
}

func (s *Server) handleAccessUserCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Email      string   `json:"email"`
		Name       string   `json:"name"`
		Role       string   `json:"role"`
		Password   string   `json:"password"`
		GroupIDs   []string `json:"groupIds"`
		SessionTTL int      `json:"sessionTtlSeconds"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, http.StatusBadRequest, "EMAIL", "Enter a valid email address")
		return
	}
	if !validAccessRole(body.Role) {
		writeError(w, http.StatusBadRequest, "ROLE", "Choose a valid role")
		return
	}
	if body.Role == "superadmin" && sess.User.Role != "superadmin" {
		writeError(w, http.StatusForbidden, "ROLE", "Only a superadmin can create another superadmin")
		return
	}
	rawPassword := strings.TrimSpace(body.Password)
	if rawPassword == "" {
		rawPassword, _ = randomPassword(20)
	}
	if len(rawPassword) < 12 {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Panel password must be at least 12 characters")
		return
	}
	if body.SessionTTL < 900 || body.SessionTTL > 604800 {
		body.SessionTTL = 28800
	}
	hash, err := auth.HashPassword(rawPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PASSWORD", err.Error())
		return
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.admin_users (email,name,password_hash,role,session_ttl_seconds)
		VALUES ($1,$2,$3,$4,$5) RETURNING id::text
	`, body.Email, strings.TrimSpace(body.Name), hash, body.Role, body.SessionTTL).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "ACCOUNT", "That panel account already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	if err := s.replaceUserGroups(r.Context(), id, body.GroupIDs, sess); err != nil {
		_, _ = s.db.Exec(r.Context(), `DELETE FROM control.admin_users WHERE id=$1`, id)
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "access.user_created", body.Email, "Created a panel account", map[string]any{"role": body.Role, "groups": body.GroupIDs})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "temporaryPassword": rawPassword})
}

func (s *Server) replaceUserGroups(ctx context.Context, userID string, groupIDs []string, sess *sessionRecord) error {
	for _, groupID := range groupIDs {
		if !validUUID(groupID) {
			return fmt.Errorf("invalid group id")
		}
		var permissions []string
		if err := s.db.QueryRow(ctx, `SELECT permissions FROM control.groups WHERE id=$1`, groupID).Scan(&permissions); err != nil {
			return fmt.Errorf("group not found")
		}
		if sess.User.Role != "superadmin" && (containsString(permissions, "admin.manage") || containsString(permissions, "group.manage")) {
			return fmt.Errorf("only a superadmin can assign a group with admin.manage or group.manage")
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM control.group_members WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, groupID := range groupIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO control.group_members (group_id,user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, groupID, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) handleAccessUserUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Name       *string   `json:"name"`
		Role       *string   `json:"role"`
		Status     *string   `json:"status"`
		GroupIDs   *[]string `json:"groupIds"`
		SessionTTL *int      `json:"sessionTtlSeconds"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var currentRole, currentStatus string
	if err := s.db.QueryRow(r.Context(), `SELECT role,status FROM control.admin_users WHERE id=$1`, id).Scan(&currentRole, &currentStatus); err != nil {
		writeFromError(w, err)
		return
	}
	if body.Role != nil {
		if !validAccessRole(*body.Role) {
			writeError(w, http.StatusBadRequest, "ROLE", "Invalid role")
			return
		}
		if *body.Role == "superadmin" && sess.User.Role != "superadmin" {
			writeError(w, http.StatusForbidden, "ROLE", "Only a superadmin can grant superadmin")
			return
		}
		if currentRole == "superadmin" && *body.Role != "superadmin" {
			var supers int
			_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM control.admin_users WHERE role='superadmin' AND status='active'`).Scan(&supers)
			if supers <= 1 {
				writeError(w, http.StatusConflict, "ROLE", "At least one active superadmin must remain")
				return
			}
		}
		currentRole = *body.Role
	}
	if body.Status != nil {
		if *body.Status != "active" && *body.Status != "disabled" {
			writeError(w, http.StatusBadRequest, "STATUS", "Status must be active or disabled")
			return
		}
		if id == sess.User.ID && *body.Status == "disabled" {
			writeError(w, http.StatusBadRequest, "ACCOUNT", "You cannot disable your own account")
			return
		}
		if currentRole == "superadmin" && *body.Status != "active" {
			var supers int
			_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM control.admin_users WHERE role='superadmin' AND status='active'`).Scan(&supers)
			if supers <= 1 {
				writeError(w, http.StatusConflict, "ACCOUNT", "At least one active superadmin must remain")
				return
			}
		}
		currentStatus = *body.Status
	}
	ttl := 0
	if body.SessionTTL != nil {
		if *body.SessionTTL < 900 || *body.SessionTTL > 604800 {
			writeError(w, http.StatusBadRequest, "TTL", "Session TTL must be between 900 and 604800 seconds")
			return
		}
		ttl = *body.SessionTTL
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.admin_users SET name=COALESCE($2,name), role=$3, status=$4,
		  session_ttl_seconds=CASE WHEN $5 > 0 THEN $5 ELSE session_ttl_seconds END, updated_at=now()
		WHERE id=$1
	`, id, body.Name, currentRole, currentStatus, ttl)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Panel account not found")
		return
	}
	if body.GroupIDs != nil {
		if err := s.replaceUserGroups(r.Context(), id, *body.GroupIDs, sess); err != nil {
			writeError(w, http.StatusForbidden, "GROUP", err.Error())
			return
		}
	}
	if currentStatus == "disabled" {
		_, _ = s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE user_id=$1`, id)
	}
	s.actorAudit(r, sess, "access.user_updated", id, "Updated a panel account", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccessUserDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	if id == sess.User.ID {
		writeError(w, http.StatusBadRequest, "ACCOUNT", "You cannot delete your own panel account")
		return
	}
	var role string
	if err := s.db.QueryRow(r.Context(), `SELECT role FROM control.admin_users WHERE id=$1`, id).Scan(&role); err != nil {
		writeFromError(w, err)
		return
	}
	if role == "superadmin" {
		var supers int
		_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM control.admin_users WHERE role='superadmin' AND status='active'`).Scan(&supers)
		if supers <= 1 {
			writeError(w, http.StatusConflict, "ACCOUNT", "At least one active superadmin must remain")
			return
		}
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.admin_users WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Panel account not found")
		return
	}
	s.actorAudit(r, sess, "access.user_deleted", id, "Deleted a panel account", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccessUserPassword(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	password := strings.TrimSpace(body.Password)
	if password == "" {
		password, _ = randomPassword(20)
	}
	if len(password) < 12 {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Use at least 12 characters")
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PASSWORD", err.Error())
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE control.admin_users SET password_hash=$2, failed_attempts=0, failed_window_start=NULL, locked_until=NULL, updated_at=now() WHERE id=$1`, id, hash)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Panel account not found")
		return
	}
	_, _ = s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE user_id=$1 AND user_id<>$2`, id, sess.User.ID)
	s.actorAudit(r, sess, "access.password_reset", id, "Reset a panel account password", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "temporaryPassword": password})
}

func (s *Server) handleAccessUserTOTPDisable(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `UPDATE control.admin_users SET totp_secret='',totp_pending_secret='',totp_enabled=false,updated_at=now() WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Panel account not found")
		return
	}
	s.actorAudit(r, sess, "access.totp_disabled", id, "Disabled TOTP for a panel account", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccessGroups(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT g.id::text,g.name,g.description,g.permissions,g.created_at,
		       COALESCE((SELECT string_agg(u.email::text, ', ' ORDER BY u.email) FROM control.group_members gm
		         JOIN control.admin_users u ON u.id=gm.user_id WHERE gm.group_id=g.id), '')
		FROM control.groups g ORDER BY lower(g.name)
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, description, members string
		var permissions []string
		var created time.Time
		if rows.Scan(&id, &name, &description, &permissions, &created, &members) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "description": description,
				"permissions": permissions, "members": members, "createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": items, "allPermissions": allPermissions})
}

func validateGroupPermissions(permissions []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if permission == "" || seen[permission] {
			continue
		}
		if !validPermission(permission) {
			return nil, fmt.Errorf("unknown permission %q", permission)
		}
		seen[permission] = true
		out = append(out, permission)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) checkHighRiskGroup(permissions []string, sess *sessionRecord) error {
	if sess.User.Role == "superadmin" {
		return nil
	}
	if containsString(permissions, "admin.manage") || containsString(permissions, "group.manage") {
		return fmt.Errorf("only a superadmin can grant admin.manage or group.manage")
	}
	return nil
}

func (s *Server) handleAccessGroupCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	permissions, err := validateGroupPermissions(body.Permissions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PERMISSIONS", err.Error())
		return
	}
	if err := s.checkHighRiskGroup(permissions, sess); err != nil {
		writeError(w, http.StatusForbidden, "PERMISSIONS", err.Error())
		return
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.groups (name,description,permissions,created_by)
		VALUES ($1,$2,$3,$4) RETURNING id::text
	`, strings.TrimSpace(body.Name), truncateText(strings.TrimSpace(body.Description), 1000), permissions, sess.User.ID).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "GROUP", "A group with that name already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "access.group_created", body.Name, "Created a panel permission group", map[string]any{"permissions": permissions})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleAccessGroupUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		Permissions *[]string `json:"permissions"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var permissions []string
	if body.Permissions != nil {
		var err error
		permissions, err = validateGroupPermissions(*body.Permissions)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PERMISSIONS", err.Error())
			return
		}
		if err := s.checkHighRiskGroup(permissions, sess); err != nil {
			writeError(w, http.StatusForbidden, "PERMISSIONS", err.Error())
			return
		}
	}
	var permissionsArg any
	if body.Permissions != nil {
		permissionsArg = permissions
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.groups SET name=COALESCE($2,name),description=COALESCE($3,description),
		  permissions=COALESCE($4,permissions),updated_at=now() WHERE id=$1
	`, id, body.Name, body.Description, permissionsArg)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Group not found")
		return
	}
	s.actorAudit(r, sess, "access.group_updated", id, "Updated a panel permission group", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccessGroupDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.groups WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Group not found")
		return
	}
	s.actorAudit(r, sess, "access.group_deleted", id, "Deleted a panel permission group", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAllSessions(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT s.id,s.ip::text,s.user_agent,s.created_at,s.last_seen_at,s.expires_at,
		       u.email::text,u.name,u.role
		FROM control.sessions s JOIN control.admin_users u ON u.id=s.user_id
		WHERE s.expires_at > now() ORDER BY s.last_seen_at DESC
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, ip, ua, email, name, role string
		var created, lastSeen, expires time.Time
		if rows.Scan(&id, &ip, &ua, &created, &lastSeen, &expires, &email, &name, &role) == nil {
			items = append(items, map[string]any{"id": id, "ip": ip, "userAgent": ua,
				"email": email, "name": name, "role": role, "current": id == sess.ID,
				"createdAt": created.UTC().Format(time.RFC3339), "lastSeenAt": lastSeen.UTC().Format(time.RFC3339),
				"expiresAt": expires.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) handleAdminSessionRevoke(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Session not found")
		return
	}
	s.actorAudit(r, sess, "access.session_revoked", id, "Revoked a panel session", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminRevokeUserSessions(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	userID := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE user_id=$1 AND id<>$2`, userID, sess.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "access.user_sessions_revoked", userID, "Revoked all sessions for a panel account", map[string]any{"revoked": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": tag.RowsAffected()})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	limit := queryInt(r, "limit", 100, 1, 1000)
	page := queryInt(r, "page", 1, 1, 100000)
	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	offset := (page - 1) * limit
	rows, err := s.db.Query(r.Context(), `
		SELECT id,actor_email::text,action,target,detail,ip::text,user_agent,metadata,created_at
		FROM control.audit_log
		WHERE ($1='' OR actor_email::text ILIKE '%'||$1||'%' OR target ILIKE '%'||$1||'%')
		  AND ($2='' OR action ILIKE '%'||$2||'%')
		  AND ($3='' OR created_at >= $3::timestamptz) AND ($4='' OR created_at <= $4::timestamptz)
		ORDER BY created_at DESC LIMIT $5 OFFSET $6
	`, actor, action, from, to, limit, offset)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var actorEmail, act, target, detail, ip, ua string
		var metadata []byte
		var created time.Time
		if rows.Scan(&id, &actorEmail, &act, &target, &detail, &ip, &ua, &metadata, &created) == nil {
			var meta any
			_ = json.Unmarshal(metadata, &meta)
			items = append(items, map[string]any{"id": id, "actor": actorEmail, "action": act,
				"target": target, "detail": detail, "ip": ip, "userAgent": ua, "metadata": meta,
				"createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	var total int64
	_ = s.db.QueryRow(r.Context(), `
		SELECT count(*) FROM control.audit_log
		WHERE ($1='' OR actor_email::text ILIKE '%'||$1||'%' OR target ILIKE '%'||$1||'%')
		  AND ($2='' OR action ILIKE '%'||$2||'%')
		  AND ($3='' OR created_at >= $3::timestamptz) AND ($4='' OR created_at <= $4::timestamptz)
	`, actor, action, from, to).Scan(&total)
	writeJSON(w, http.StatusOK, map[string]any{"entries": items, "total": total, "page": page, "limit": limit})
}

func (s *Server) handleAuditCSV(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id,actor_email::text,action,target,detail,ip::text,user_agent,metadata,created_at
		FROM control.audit_log ORDER BY created_at DESC LIMIT 10000
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"alt8mail-control-audit.csv\"")
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"id", "actor", "action", "target", "detail", "ip", "user_agent", "metadata", "created_at"})
	for rows.Next() {
		var id int64
		var actor, action, target, detail, ip, ua string
		var metadata []byte
		var created time.Time
		if rows.Scan(&id, &actor, &action, &target, &detail, &ip, &ua, &metadata, &created) == nil {
			_ = writer.Write([]string{strconv.FormatInt(id, 10), actor, action, target, detail, ip, ua, string(metadata), created.UTC().Format(time.RFC3339)})
		}
	}
	writer.Flush()
}
