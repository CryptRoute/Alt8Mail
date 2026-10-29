package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (s *Server) adminListOrgUnits(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT o.id::text, COALESCE(o.parent_id::text, ''), o.name, o.description, o.path,
		       o.created_at, (SELECT count(*) FROM users u WHERE u.org_unit_id = o.id AND u.deleted_at IS NULL)
		FROM org_units o WHERE o.tenant_id = $1 ORDER BY o.path, o.name
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ORG_UNITS", "Could not load org units")
		return
	}
	defer rows.Close()
	units := []map[string]any{}
	for rows.Next() {
		var id, parentID, name, description, path string
		var created time.Time
		var users int
		if rows.Scan(&id, &parentID, &name, &description, &path, &created, &users) == nil {
			units = append(units, map[string]any{"id": id, "parentId": parentID, "name": name, "description": description, "path": path, "users": users, "createdAt": formatAdminTime(created)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgUnits": units, "count": len(units)})
}

func (s *Server) adminCreateOrgUnit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name        string `json:"name"`
		ParentID    string `json:"parentId"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len([]rune(body.Name)) > 80 {
		writeError(w, http.StatusBadRequest, "NAME", "Org unit name is required and must be under 80 characters")
		return
	}
	path := "/" + slugifyAdmin(body.Name)
	if body.ParentID != "" {
		parent, err := s.adminOrgUnit(r.Context(), body.ParentID, sess.ActiveTenantID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PARENT", "Parent org unit does not exist")
			return
		}
		if strings.Count(parent["path"].(string), "/") >= 6 {
			writeError(w, http.StatusBadRequest, "DEPTH", "Organization unit nesting is limited to six levels")
			return
		}
		path = strings.TrimRight(parent["path"].(string), "/") + "/" + slugifyAdmin(body.Name)
	}
	var id string
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO org_units (tenant_id, parent_id, name, description, path)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5) RETURNING id::text
	`, sess.ActiveTenantID, body.ParentID, body.Name, truncate(strings.TrimSpace(body.Description), 400), path).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "ORG_UNIT_EXISTS", "An org unit with that name already exists at this level")
		} else {
			writeError(w, http.StatusInternalServerError, "ORG_UNIT", "Could not create org unit")
		}
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.org_unit_created", body.Name, "Created org unit", map[string]any{"orgUnitId": id, "path": path})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "orgUnit": map[string]any{"id": id, "parentId": body.ParentID, "name": body.Name, "description": body.Description, "path": path, "users": 0}})
}

func (s *Server) adminUpdateOrgUnit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	unit, err := s.adminOrgUnit(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORG_UNIT_NOT_FOUND", "Org unit not found")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		ParentID    *string `json:"parentId"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	name := unit["name"].(string)
	parentID := unit["parentId"].(string)
	description := unit["description"].(string)
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		name = truncate(strings.TrimSpace(*body.Name), 80)
	}
	if body.Description != nil {
		description = truncate(strings.TrimSpace(*body.Description), 400)
	}
	if body.ParentID != nil {
		parentID = *body.ParentID
		if parentID == unit["id"].(string) {
			writeError(w, http.StatusBadRequest, "CYCLE", "An org unit cannot be its own parent")
			return
		}
		if parentID != "" {
			parent, err := s.adminOrgUnit(r.Context(), parentID, sess.ActiveTenantID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "PARENT", "Parent org unit does not exist")
				return
			}
			if strings.HasPrefix(parent["path"].(string)+"/", unit["path"].(string)+"/") {
				writeError(w, http.StatusBadRequest, "CYCLE", "An org unit cannot be moved inside its own descendant")
				return
			}
		}
	}
	path := "/" + slugifyAdmin(name)
	if parentID != "" {
		parent, _ := s.adminOrgUnit(r.Context(), parentID, sess.ActiveTenantID)
		path = strings.TrimRight(parent["path"].(string), "/") + "/" + slugifyAdmin(name)
	}
	oldPath := unit["path"].(string)
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ORG_UNIT", "Could not update org unit")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `UPDATE org_units SET parent_id = NULLIF($2, '')::uuid, name = $3, description = $4, path = $5, updated_at = now() WHERE id = $1`, unit["id"], parentID, name, description, path); err != nil {
		writeError(w, http.StatusInternalServerError, "ORG_UNIT", "Could not update org unit")
		return
	}
	// Keep descendant paths consistent after a rename or move.
	_, _ = tx.Exec(r.Context(), `
		UPDATE org_units SET path = $2 || substring(path FROM length($1) + 1), updated_at = now()
		WHERE tenant_id = $3 AND path LIKE $1 || '/%'
	`, oldPath, path, sess.ActiveTenantID)
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ORG_UNIT", "Could not commit org unit update")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.org_unit_updated", name, "Updated org unit", map[string]any{"orgUnitId": unit["id"], "path": path})
	updated, _ := s.adminOrgUnit(r.Context(), unit["id"].(string), sess.ActiveTenantID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "orgUnit": updated})
}

func (s *Server) adminDeleteOrgUnit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	unit, err := s.adminOrgUnit(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORG_UNIT_NOT_FOUND", "Org unit not found")
		return
	}
	var childCount, userCount int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM org_units WHERE parent_id = $1`, unit["id"]).Scan(&childCount)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE org_unit_id = $1 AND deleted_at IS NULL`, unit["id"]).Scan(&userCount)
	if childCount > 0 || userCount > 0 {
		writeError(w, http.StatusConflict, "ORG_UNIT_IN_USE", "Move child units and users before deleting this org unit")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM org_units WHERE id = $1 AND tenant_id = $2`, unit["id"], sess.ActiveTenantID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.org_unit_deleted", unit["name"].(string), "Deleted org unit", map[string]any{"orgUnitId": unit["id"]})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminOrgUnit(ctx context.Context, id, tenantID string) (map[string]any, error) {
	var unitID, parentID, name, description, path string
	var created time.Time
	var users int
	err := s.store.DB.QueryRow(ctx, `
		SELECT o.id::text, COALESCE(o.parent_id::text, ''), o.name, o.description, o.path, o.created_at,
		       (SELECT count(*) FROM users u WHERE u.org_unit_id = o.id AND u.deleted_at IS NULL)
		FROM org_units o WHERE o.id = $1 AND o.tenant_id = $2
	`, id, tenantID).Scan(&unitID, &parentID, &name, &description, &path, &created, &users)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": unitID, "parentId": parentID, "name": name, "description": description, "path": path, "users": users, "createdAt": formatAdminTime(created)}, nil
}

func (s *Server) adminListGroups(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT g.id::text, g.name, g.email::text, g.description, g.group_type, g.status,
		       g.created_at, (SELECT count(*) FROM group_members gm WHERE gm.group_id = g.id) AS members
		FROM groups g
		WHERE g.tenant_id = $1 AND ($2 = '' OR g.name ILIKE $3 OR g.email::text ILIKE $3)
		ORDER BY g.name
	`, sess.ActiveTenantID, query, "%"+query+"%")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GROUPS", "Could not load groups")
		return
	}
	defer rows.Close()
	groups := []map[string]any{}
	for rows.Next() {
		var id, name, email, description, groupType, status string
		var created time.Time
		var members int
		if rows.Scan(&id, &name, &email, &description, &groupType, &status, &created, &members) == nil {
			groups = append(groups, map[string]any{"id": id, "name": name, "email": email, "description": description, "type": groupType, "status": status, "members": members, "createdAt": formatAdminTime(created)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "count": len(groups)})
}

func (s *Server) adminCreateGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		Description string `json:"description"`
		Type        string `json:"type"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Name == "" || !strings.Contains(body.Email, "@") {
		writeError(w, http.StatusBadRequest, "GROUP", "Group name and email are required")
		return
	}
	if body.Type == "" {
		body.Type = "distribution"
	}
	if body.Type != "distribution" && body.Type != "security" && body.Type != "shared" {
		writeError(w, http.StatusBadRequest, "TYPE", "Invalid group type")
		return
	}
	_, groupDomain := splitEmail(body.Email)
	if !s.adminDomainVerified(r.Context(), sess.ActiveTenantID, groupDomain) {
		writeError(w, http.StatusBadRequest, "GROUP", "Group email must use a verified organization domain")
		return
	}
	if s.adminAddressInUse(r.Context(), body.Email) {
		writeError(w, http.StatusConflict, "ADDRESS_EXISTS", "That address is already in use")
		return
	}
	var id string
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO groups (tenant_id, name, email, description, group_type, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text
	`, sess.ActiveTenantID, truncate(body.Name, 100), body.Email, truncate(strings.TrimSpace(body.Description), 500), body.Type, sess.User.ID).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "GROUP_EXISTS", "That group email is already in use")
		} else {
			writeError(w, http.StatusInternalServerError, "GROUP", "Could not create group")
		}
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.group_created", body.Email, "Created group", map[string]any{"groupId": id})
	group, _ := s.adminGroupDetail(r.Context(), id, sess.ActiveTenantID)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "group": group})
}

func (s *Server) adminGetGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	group, err := s.adminGroupDetail(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "GROUP_NOT_FOUND", "Group not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group})
}

func (s *Server) adminUpdateGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	group, err := s.adminGroupDetail(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "GROUP_NOT_FOUND", "Group not found")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Email       *string `json:"email"`
		Description *string `json:"description"`
		Type        *string `json:"type"`
		Status      *string `json:"status"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	name := group["name"].(string)
	email := group["email"].(string)
	description := group["description"].(string)
	groupType := group["type"].(string)
	status := group["status"].(string)
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		name = truncate(strings.TrimSpace(*body.Name), 100)
	}
	if body.Email != nil {
		email = strings.ToLower(strings.TrimSpace(*body.Email))
		_, groupDomain := splitEmail(email)
		if !s.adminDomainVerified(r.Context(), sess.ActiveTenantID, groupDomain) {
			writeError(w, http.StatusBadRequest, "EMAIL", "Group email must use a verified organization domain")
			return
		}
		if email != group["email"].(string) && s.adminAddressInUse(r.Context(), email) {
			writeError(w, http.StatusConflict, "ADDRESS_EXISTS", "That address is already in use")
			return
		}
	}
	if body.Description != nil {
		description = truncate(strings.TrimSpace(*body.Description), 500)
	}
	if body.Type != nil {
		if *body.Type != "distribution" && *body.Type != "security" && *body.Type != "shared" {
			writeError(w, http.StatusBadRequest, "TYPE", "Invalid group type")
			return
		}
		groupType = *body.Type
	}
	if body.Status != nil {
		if *body.Status != "active" && *body.Status != "archived" {
			writeError(w, http.StatusBadRequest, "STATUS", "Invalid group status")
			return
		}
		status = *body.Status
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE groups SET name = $2, email = $3, description = $4, group_type = $5, status = $6, updated_at = now()
		WHERE id = $1 AND tenant_id = $7
	`, group["id"], name, email, description, groupType, status, sess.ActiveTenantID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "GROUP", "Could not update group; email may already be in use")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.group_updated", email, "Updated group", map[string]any{"groupId": group["id"]})
	updated, _ := s.adminGroupDetail(r.Context(), group["id"].(string), sess.ActiveTenantID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": updated})
}

func (s *Server) adminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	group, err := s.adminGroupDetail(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "GROUP_NOT_FOUND", "Group not found")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM groups WHERE id = $1 AND tenant_id = $2`, group["id"], sess.ActiveTenantID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.group_deleted", group["email"].(string), "Deleted group", map[string]any{"groupId": group["id"]})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminAddGroupMember(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	group, err := s.adminGroupDetail(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "GROUP_NOT_FOUND", "Group not found")
		return
	}
	var body struct {
		UserID     string `json:"userId"`
		MemberRole string `json:"memberRole"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	user, err := s.adminUserByID(r.Context(), body.UserID)
	if err != nil || user.TenantID != sess.ActiveTenantID || user.DeletedAt != nil {
		writeError(w, http.StatusBadRequest, "USER", "User does not belong to this organization")
		return
	}
	if body.MemberRole == "" {
		body.MemberRole = "member"
	}
	if body.MemberRole != "member" && body.MemberRole != "owner" {
		writeError(w, http.StatusBadRequest, "MEMBER_ROLE", "Member role must be member or owner")
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO group_members (group_id, user_id, member_role, added_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (group_id, user_id) DO UPDATE SET member_role = EXCLUDED.member_role
	`, group["id"], user.ID, body.MemberRole, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GROUP_MEMBER", "Could not add group member")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.group_member_added", group["email"].(string), "Added "+user.Email+" to group", map[string]any{"groupId": group["id"], "userId": user.ID})
	updated, _ := s.adminGroupDetail(r.Context(), group["id"].(string), sess.ActiveTenantID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": updated})
}

func (s *Server) adminRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	group, err := s.adminGroupDetail(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "GROUP_NOT_FOUND", "Group not found")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, group["id"], r.PathValue("userId"))
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.group_member_removed", group["email"].(string), "Removed a group member", map[string]any{"groupId": group["id"], "userId": r.PathValue("userId")})
	updated, _ := s.adminGroupDetail(r.Context(), group["id"].(string), sess.ActiveTenantID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": updated})
}

func (s *Server) adminGroupDetail(ctx context.Context, id, tenantID string) (map[string]any, error) {
	var groupID, name, email, description, groupType, status string
	var created time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT id::text, name, email::text, description, group_type, status, created_at
		FROM groups WHERE id = $1 AND tenant_id = $2
	`, id, tenantID).Scan(&groupID, &name, &email, &description, &groupType, &status, &created)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT u.id::text, u.email::text, u.name, u.initials, u.color, gm.member_role
		FROM group_members gm JOIN users u ON u.id = gm.user_id
		WHERE gm.group_id = $1 AND u.deleted_at IS NULL ORDER BY u.name
	`, groupID)
	if err != nil {
		return nil, err
	}
	members := []map[string]any{}
	for rows.Next() {
		var userID, userEmail, userName, initials, color, memberRole string
		if rows.Scan(&userID, &userEmail, &userName, &initials, &color, &memberRole) == nil {
			members = append(members, map[string]any{"id": userID, "email": userEmail, "name": userName, "initials": initials, "color": color, "memberRole": memberRole})
		}
	}
	rows.Close()
	return map[string]any{"id": groupID, "name": name, "email": email, "description": description, "type": groupType, "status": status, "createdAt": formatAdminTime(created), "members": members, "memberCount": len(members)}, nil
}

func (s *Server) adminListRoles(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT r.id::text, r.name, r.description, r.permissions, r.is_system, r.created_at,
		       (SELECT count(*) FROM admin_role_assignments a WHERE a.role_id = r.id AND (a.expires_at IS NULL OR a.expires_at > now())) AS assignments
		FROM admin_roles r WHERE r.tenant_id = $1 ORDER BY r.is_system DESC, r.name
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ROLES", "Could not load roles")
		return
	}
	defer rows.Close()
	roles := []map[string]any{}
	for rows.Next() {
		var id, name, description string
		var permissions []string
		var isSystem bool
		var created time.Time
		var assignments int
		if rows.Scan(&id, &name, &description, &permissions, &isSystem, &created, &assignments) == nil {
			roles = append(roles, map[string]any{"id": id, "name": name, "description": description, "permissions": permissions, "isSystem": isSystem, "assignments": assignments, "createdAt": formatAdminTime(created)})
		}
	}
	implicit := []map[string]any{
		{"id": "implicit-owner", "name": "Owner", "description": "Organization owner with full tenant administration rights.", "permissions": tenantAdminPermissionKeys, "isSystem": true, "assignments": 0, "implicit": true},
		{"id": "implicit-admin", "name": "Admin", "description": "Organization administrator with full tenant administration rights.", "permissions": tenantAdminPermissionKeys, "isSystem": true, "assignments": 0, "implicit": true},
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": append(implicit, roles...), "catalog": adminPermissionCatalog, "count": len(roles) + 2})
}

func (s *Server) adminCreateRole(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len([]rune(body.Name)) > 80 {
		writeError(w, http.StatusBadRequest, "NAME", "Role name is required")
		return
	}
	permissions := normalizeAdminPermissions(body.Permissions, false)
	if len(permissions) == 0 {
		writeError(w, http.StatusBadRequest, "PERMISSIONS", "Choose at least one permission")
		return
	}
	var id string
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO admin_roles (tenant_id, name, description, permissions, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text
	`, sess.ActiveTenantID, body.Name, truncate(strings.TrimSpace(body.Description), 300), permissions, sess.User.ID).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "ROLE_EXISTS", "A role with that name already exists")
		} else {
			writeError(w, http.StatusInternalServerError, "ROLE", "Could not create role")
		}
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.role_created", body.Name, "Created custom admin role", map[string]any{"roleId": id, "permissions": permissions})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "role": map[string]any{"id": id, "name": body.Name, "description": body.Description, "permissions": permissions, "isSystem": false, "assignments": 0}})
}

func (s *Server) adminUpdateRole(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var id, name, description string
	var isSystem bool
	var current []string
	err := s.store.DB.QueryRow(r.Context(), `SELECT id::text, name, description, permissions, is_system FROM admin_roles WHERE id = $1 AND tenant_id = $2`, r.PathValue("id"), sess.ActiveTenantID).Scan(&id, &name, &description, &current, &isSystem)
	if err != nil {
		writeError(w, http.StatusNotFound, "ROLE_NOT_FOUND", "Role not found")
		return
	}
	if isSystem {
		writeError(w, http.StatusBadRequest, "SYSTEM_ROLE", "Built-in roles cannot be edited; create a custom role instead")
		return
	}
	var body struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		Permissions *[]string `json:"permissions"`
	}
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		name = truncate(strings.TrimSpace(*body.Name), 80)
	}
	if body.Description != nil {
		description = truncate(strings.TrimSpace(*body.Description), 300)
	}
	permissions := current
	if body.Permissions != nil {
		permissions = normalizeAdminPermissions(*body.Permissions, false)
		if len(permissions) == 0 {
			writeError(w, http.StatusBadRequest, "PERMISSIONS", "Choose at least one permission")
			return
		}
	}
	_, err = s.store.DB.Exec(r.Context(), `UPDATE admin_roles SET name = $2, description = $3, permissions = $4, updated_at = now() WHERE id = $1`, id, name, description, permissions)
	if err != nil {
		writeError(w, http.StatusConflict, "ROLE", "Could not update role; name may already be in use")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.role_updated", name, "Updated custom admin role", map[string]any{"roleId": id, "permissions": permissions})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "role": map[string]any{"id": id, "name": name, "description": description, "permissions": permissions, "isSystem": false}})
}

func (s *Server) adminDeleteRole(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var id, name string
	var isSystem bool
	err := s.store.DB.QueryRow(r.Context(), `SELECT id::text, name, is_system FROM admin_roles WHERE id = $1 AND tenant_id = $2`, r.PathValue("id"), sess.ActiveTenantID).Scan(&id, &name, &isSystem)
	if err != nil {
		writeError(w, http.StatusNotFound, "ROLE_NOT_FOUND", "Role not found")
		return
	}
	if isSystem {
		writeError(w, http.StatusBadRequest, "SYSTEM_ROLE", "Built-in roles cannot be deleted")
		return
	}
	var assignments int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM admin_role_assignments WHERE role_id = $1 AND (expires_at IS NULL OR expires_at > now())`, id).Scan(&assignments)
	if assignments > 0 {
		writeError(w, http.StatusConflict, "ROLE_IN_USE", "Remove this role from all users before deleting it")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_roles WHERE id = $1 AND tenant_id = $2`, id, sess.ActiveTenantID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.role_deleted", name, "Deleted custom admin role", map[string]any{"roleId": id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminAssignRole(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var roleID, roleName string
	var isSystem bool
	err := s.store.DB.QueryRow(r.Context(), `SELECT id::text, name, is_system FROM admin_roles WHERE id = $1 AND tenant_id = $2`, r.PathValue("id"), sess.ActiveTenantID).Scan(&roleID, &roleName, &isSystem)
	if err != nil {
		writeError(w, http.StatusNotFound, "ROLE_NOT_FOUND", "Role not found")
		return
	}
	var body struct {
		UserID    string `json:"userId"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	user, err := s.adminUserByID(r.Context(), body.UserID)
	if err != nil || user.TenantID != sess.ActiveTenantID || user.DeletedAt != nil {
		writeError(w, http.StatusBadRequest, "USER", "User does not belong to this organization")
		return
	}
	var expires any
	if body.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, body.ExpiresAt)
		if err != nil || parsed.Before(time.Now()) {
			writeError(w, http.StatusBadRequest, "EXPIRES", "Expiry must be a valid future time")
			return
		}
		expires = parsed
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO admin_role_assignments (user_id, role_id, tenant_id, assigned_by, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, role_id) DO UPDATE SET expires_at = EXCLUDED.expires_at, assigned_by = EXCLUDED.assigned_by
	`, user.ID, roleID, sess.ActiveTenantID, sess.User.ID, expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ROLE", "Could not assign role")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.role_assigned", user.Email, "Assigned role "+roleName, map[string]any{"roleId": roleID, "userId": user.ID, "isSystem": isSystem})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminUnassignRole(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	roleID := r.PathValue("id")
	userID := r.PathValue("userId")
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_role_assignments WHERE role_id = $1 AND user_id = $2 AND tenant_id = $3`, roleID, userID, sess.ActiveTenantID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.role_unassigned", userID, "Removed admin role assignment", map[string]any{"roleId": roleID, "userId": userID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func normalizeAdminPermissions(input []string, allowSystem bool) []string {
	allowed := adminPermissionKeys()
	seen := map[string]bool{}
	result := []string{}
	for _, key := range input {
		key = strings.TrimSpace(key)
		if !allowed[key] || seen[key] {
			continue
		}
		if !allowSystem && strings.HasPrefix(key, "system.") {
			continue
		}
		seen[key] = true
		result = append(result, key)
	}
	return result
}

func (s *Server) adminActiveTenantDomain(ctx context.Context, tenantID string) string {
	var domain string
	_ = s.store.DB.QueryRow(ctx, `SELECT domain::text FROM mail_domains WHERE tenant_id = $1 ORDER BY is_primary DESC, created_at LIMIT 1`, tenantID).Scan(&domain)
	return domain
}

func slugifyAdmin(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	lastDash := false
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			out.WriteRune(char)
			lastDash = false
		} else if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "unit"
	}
	return result
}
