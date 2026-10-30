package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/stateinit"
	"github.com/jackc/pgx/v5"
)

type adminUserListFilter struct {
	Query     string
	Status    string
	Role      string
	OrgUnit   string
	AdminOnly bool
	Deleted   bool
	Page      int
	Limit     int
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	filter := adminUserListFilter{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Role: strings.TrimSpace(r.URL.Query().Get("role")), OrgUnit: strings.TrimSpace(r.URL.Query().Get("orgUnit")),
		AdminOnly: r.URL.Query().Get("admin") == "true", Deleted: r.URL.Query().Get("deleted") == "true",
		Page: parseIntDefault(r.URL.Query().Get("page"), 1), Limit: parseIntDefault(r.URL.Query().Get("limit"), 25),
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 25
	}
	where := []string{"u.tenant_id = $1"}
	args := []any{sess.ActiveTenantID}
	position := 2
	if filter.Deleted {
		where = append(where, "u.deleted_at IS NOT NULL")
	} else {
		where = append(where, "u.deleted_at IS NULL")
	}
	switch filter.Status {
	case "active", "suspended", "invited":
		where = append(where, fmt.Sprintf("u.status = $%d", position))
		args = append(args, filter.Status)
		position++
	case "deleted":
		where = append(where, "u.deleted_at IS NOT NULL")
	}
	if filter.Role != "" {
		where = append(where, fmt.Sprintf("u.role = $%d", position))
		args = append(args, filter.Role)
		position++
	}
	if filter.OrgUnit != "" {
		where = append(where, fmt.Sprintf("u.org_unit_id = $%d", position))
		args = append(args, filter.OrgUnit)
		position++
	}
	if filter.AdminOnly {
		where = append(where, `(u.role IN ('owner','admin') OR u.is_system_admin OR EXISTS (
			SELECT 1 FROM admin_role_assignments a WHERE a.user_id = u.id AND a.tenant_id = u.tenant_id
		))`)
	}
	if filter.Query != "" {
		term := "%" + filter.Query + "%"
		where = append(where, fmt.Sprintf(`(u.email::text ILIKE $%d OR u.name ILIKE $%d OR u.title ILIKE $%d OR u.department ILIKE $%d)`, position, position, position, position))
		args = append(args, term)
		position++
	}
	condition := strings.Join(where, " AND ")
	var total int
	if err := s.store.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users u WHERE "+condition, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "USERS", "Could not count users")
		return
	}
	queryArgs := append(append([]any{}, args...), filter.Limit, (filter.Page-1)*filter.Limit)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT u.id::text, u.email::text, u.name, u.initials, u.color, u.role, u.status,
		       COALESCE(u.title, ''), COALESCE(u.department, ''), COALESCE(u.employee_id, ''),
		       COALESCE(u.manager_email::text, ''), COALESCE(u.org_unit_id::text, ''),
		       COALESCE(ou.name, ''), u.quota_bytes,
		       GREATEST(u.used_bytes, COALESCE((
		         SELECT SUM((message->>'sizeBytes')::bigint)
		         FROM jsonb_array_elements(CASE WHEN jsonb_typeof(aps.state->'messages') = 'array' THEN aps.state->'messages' ELSE '[]'::jsonb END) AS message
		       ), 0)) AS used_bytes,
		       u.last_login_at, u.created_at,
		       u.deleted_at, u.is_system_admin, u.force_password_change,
		       COALESCE(mb.provision_status, ''), COALESCE(mb.maildir_path, ''), mb.provisioned_at,
		       (COALESCE(u.security->'twoFactor'->>'totpSecret', '') <> '' OR EXISTS (
		          SELECT 1 FROM webauthn_credentials w WHERE w.user_id = u.id
		       )) AS mfa_enabled,
		       COALESCE((SELECT array_agg(r.name ORDER BY r.name)
		         FROM admin_role_assignments a JOIN admin_roles r ON r.id = a.role_id
		         WHERE a.user_id = u.id AND a.tenant_id = u.tenant_id
		           AND (a.expires_at IS NULL OR a.expires_at > now())), '{}') AS admin_roles,
		       (SELECT count(*) FROM group_members gm WHERE gm.user_id = u.id) AS group_count
		FROM users u
		LEFT JOIN org_units ou ON ou.id = u.org_unit_id
		LEFT JOIN app_states aps ON aps.user_id = u.id
		LEFT JOIN mailboxes mb ON mb.user_id = u.id
		WHERE `+condition+`
		ORDER BY (u.role = 'owner') DESC, u.name
		LIMIT $`+strconv.Itoa(position)+` OFFSET $`+strconv.Itoa(position+1), queryArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USERS", "Could not list users")
		return
	}
	defer rows.Close()
	users := []map[string]any{}
	for rows.Next() {
		var id, email, name, initials, color, role, status, title, department, employeeID, managerEmail, orgUnitID, orgUnitName string
		var quota, used int64
		var lastLogin, created, deletedAt *time.Time
		var isSystem, forceChange, mfa bool
		var mailboxStatus, maildirPath string
		var provisionedAt *time.Time
		var adminRoles []string
		var groupCount int
		if err := rows.Scan(&id, &email, &name, &initials, &color, &role, &status, &title, &department, &employeeID,
			&managerEmail, &orgUnitID, &orgUnitName, &quota, &used, &lastLogin, &created, &deletedAt,
			&isSystem, &forceChange, &mailboxStatus, &maildirPath, &provisionedAt, &mfa, &adminRoles, &groupCount); err != nil {
			continue
		}
		users = append(users, adminUserListItem(id, email, name, initials, color, role, status, title, department, employeeID, managerEmail, orgUnitID, orgUnitName, quota, used, lastLogin, created, deletedAt, isSystem, forceChange, mailboxStatus, maildirPath, provisionedAt, mfa, adminRoles, groupCount))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": total, "page": filter.Page, "limit": filter.Limit})
}

func adminUserListItem(id, email, name, initials, color, role, status, title, department, employeeID, managerEmail, orgUnitID, orgUnitName string, quota, used int64, lastLogin, created, deletedAt *time.Time, isSystem, forceChange bool, mailboxStatus, maildirPath string, provisionedAt *time.Time, mfa bool, adminRoles []string, groupCount int) map[string]any {
	payload := map[string]any{
		"id": id, "email": email, "name": name, "initials": initials, "color": color,
		"role": role, "status": status, "title": title, "department": department,
		"employeeId": employeeID, "managerEmail": managerEmail, "orgUnitId": orgUnitID,
		"orgUnitName": orgUnitName, "quotaBytes": quota, "usedBytes": used,
		"isSystemAdmin": isSystem, "forcePasswordChange": forceChange, "mfaEnabled": mfa,
		"mailboxStatus": mailboxStatus, "maildirPath": maildirPath,
		"adminRoles": adminRoles, "groupCount": groupCount,
	}
	if provisionedAt != nil {
		payload["provisionedAt"] = formatAdminTime(*provisionedAt)
	}
	if lastLogin != nil {
		payload["lastLoginAt"] = formatAdminTime(*lastLogin)
	}
	if created != nil {
		payload["createdAt"] = formatAdminTime(*created)
	}
	if deletedAt != nil {
		payload["deletedAt"] = formatAdminTime(*deletedAt)
	}
	return payload
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	detail, err := s.adminUserDetail(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not load user details")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) adminUserDetail(ctx context.Context, user adminUser) (map[string]any, error) {
	aliases := []map[string]any{}
	rows, err := s.store.DB.Query(ctx, `SELECT id::text, email::text, label, kind, can_send, active, created_at FROM user_aliases WHERE user_id = $1 ORDER BY (kind = 'primary') DESC, email`, user.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, email, label, kind string
		var canSend, active bool
		var created time.Time
		if rows.Scan(&id, &email, &label, &kind, &canSend, &active, &created) == nil {
			aliases = append(aliases, map[string]any{"id": id, "email": email, "label": label, "kind": kind, "canSend": canSend, "active": active, "createdAt": formatAdminTime(created)})
		}
	}
	rows.Close()
	groups := []map[string]any{}
	groupRows, err := s.store.DB.Query(ctx, `
		SELECT g.id::text, g.name, g.email::text, gm.member_role
		FROM group_members gm JOIN groups g ON g.id = gm.group_id
		WHERE gm.user_id = $1 ORDER BY g.name
	`, user.ID)
	if err != nil {
		return nil, err
	}
	for groupRows.Next() {
		var id, name, email, memberRole string
		if groupRows.Scan(&id, &name, &email, &memberRole) == nil {
			groups = append(groups, map[string]any{"id": id, "name": name, "email": email, "memberRole": memberRole})
		}
	}
	groupRows.Close()
	roles := []map[string]any{}
	roleRows, err := s.store.DB.Query(ctx, `
		SELECT r.id::text, r.name, r.description, r.permissions, a.expires_at
		FROM admin_role_assignments a JOIN admin_roles r ON r.id = a.role_id
		WHERE a.user_id = $1 AND a.tenant_id = $2 ORDER BY r.name
	`, user.ID, user.TenantID)
	if err != nil {
		return nil, err
	}
	for roleRows.Next() {
		var id, name, description string
		var permissions []string
		var expires *time.Time
		if roleRows.Scan(&id, &name, &description, &permissions, &expires) == nil {
			entry := map[string]any{"id": id, "name": name, "description": description, "permissions": permissions}
			if expires != nil {
				entry["expiresAt"] = formatAdminTime(*expires)
			}
			roles = append(roles, entry)
		}
	}
	roleRows.Close()
	var sessionCount, passkeyCount, trustedCount int
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1 AND expires_at > now()`, user.ID).Scan(&sessionCount)
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, user.ID).Scan(&passkeyCount)
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM trusted_devices WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()`, user.ID).Scan(&trustedCount)
	security, methods, passkeys, remaining := s.securityState(ctx, user.userRecord())
	payload := adminUserListItem(user.ID, user.Email, user.Name, user.Initials, user.Color, user.Role, user.Status,
		user.Title, user.Department, user.EmployeeID, user.ManagerEmail, user.OrgUnitID, "",
		user.QuotaBytes, user.UsedBytes, user.LastLoginAt, &user.CreatedAt, user.DeletedAt,
		user.IsSystemAdmin, user.ForcePasswordChange, user.MailboxStatus, user.MaildirPath, user.MailboxProvisionedAt,
		len(methods) > 0, []string{}, 0)
	if user.OrgUnitID != "" {
		var orgUnitName string
		if s.store.DB.QueryRow(ctx, `SELECT name FROM org_units WHERE id = $1 AND tenant_id = $2`, user.OrgUnitID, user.TenantID).Scan(&orgUnitName) == nil {
			payload["orgUnitName"] = orgUnitName
		}
	}
	payload["aliases"] = aliases
	payload["groups"] = groups
	payload["adminRoles"] = roles
	payload["security"] = map[string]any{"summary": security, "methods": methods, "passkeys": passkeys, "recoveryRemaining": remaining, "activeSessions": sessionCount, "passkeysCount": passkeyCount, "trustedDevices": trustedCount}
	payload["forcePasswordChange"] = user.ForcePasswordChange
	return map[string]any{"user": payload}, nil
}

// adminUserCreateDomains is a small, focused list for the add-user form. It
// only requires users.create instead of the broader domains.read entitlement.
func (s *Server) adminUserCreateDomains(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT domain::text, is_primary, status, mx_verified, spf_verified, dkim_verified, dmarc_verified
		FROM mail_domains
		WHERE tenant_id = $1 AND status IN ('verified', 'manual')
		ORDER BY is_primary DESC, domain
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOMAINS", "Could not load verified domains")
		return
	}
	defer rows.Close()
	domains := []map[string]any{}
	for rows.Next() {
		var domain, status string
		var primary, mx, spf, dkim, dmarc bool
		if rows.Scan(&domain, &primary, &status, &mx, &spf, &dkim, &dmarc) == nil {
			domains = append(domains, map[string]any{
				"domain": domain, "isPrimary": primary, "status": status,
				"mailReady": mx && spf && dkim && dmarc,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains, "count": len(domains)})
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name         string   `json:"name"`
		Email        string   `json:"email"`
		Role         string   `json:"role"`
		Status       string   `json:"status"`
		QuotaBytes   int64    `json:"quotaBytes"`
		OrgUnitID    string   `json:"orgUnitId"`
		Title        string   `json:"title"`
		Department   string   `json:"department"`
		EmployeeID   string   `json:"employeeId"`
		ManagerEmail string   `json:"managerEmail"`
		Aliases      []string `json:"aliases"`
		Password     string   `json:"password"`
	}
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Name == "" || !strings.Contains(body.Email, "@") {
		writeError(w, http.StatusBadRequest, "USER", "Name and email are required")
		return
	}
	if s.adminAddressInUse(r.Context(), body.Email) {
		writeError(w, http.StatusConflict, "ADDRESS_EXISTS", "That address is already in use")
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	if !validUserRole(body.Role) {
		writeError(w, http.StatusBadRequest, "ROLE", "Invalid role")
		return
	}
	if (body.Role == "owner" || body.Role == "admin") && !sess.can("roles.manage") {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can create an owner or admin")
		return
	}
	if body.Status == "" {
		body.Status = "active"
	}
	if !validUserStatus(body.Status) {
		writeError(w, http.StatusBadRequest, "STATUS", "Invalid status")
		return
	}
	if body.QuotaBytes == 0 {
		body.QuotaBytes = 16 * 1024 * 1024 * 1024
	}
	if body.QuotaBytes < 512*1024*1024 || body.QuotaBytes > 1024*1024*1024*1024 {
		writeError(w, http.StatusBadRequest, "QUOTA", "Quota must be between 512 MB and 1 TB")
		return
	}
	if body.OrgUnitID != "" && !s.adminOrgUnitInTenant(r.Context(), body.OrgUnitID, sess.ActiveTenantID) {
		writeError(w, http.StatusBadRequest, "ORG_UNIT", "Org unit does not belong to this organization")
		return
	}
	if body.ManagerEmail != "" && !strings.Contains(body.ManagerEmail, "@") {
		writeError(w, http.StatusBadRequest, "MANAGER", "Manager email is invalid")
		return
	}
	policy := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	if body.Password != "" {
		if errors := validatePasswordAgainstPolicy(body.Password, policy.Security); len(errors) > 0 {
			writeError(w, http.StatusBadRequest, "PASSWORD_POLICY", strings.Join(errors, "; "))
			return
		}
	}
	temporaryPassword := body.Password
	if temporaryPassword == "" {
		var err error
		temporaryPassword, err = generateTemporaryPassword()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not generate a temporary password")
			return
		}
	}
	webHash, err := auth.HashPassword(temporaryPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not hash the password")
		return
	}
	mailPassword, err := generateTemporaryPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_PASSWORD", "Could not generate a mailbox password")
		return
	}
	mailHash, err := hashMailPassword(mailPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_PASSWORD", "Could not create the Dovecot password hash: "+err.Error())
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not start user provisioning")
		return
	}
	defer tx.Rollback(ctx)
	if err := ensureTenantDomain(ctx, tx, sess.ActiveTenantID, body.Email); err != nil {
		writeError(w, http.StatusBadRequest, "DOMAIN", err.Error())
		return
	}
	created, err := provisionUserTx(ctx, tx, sess.ActiveTenantID, body.Name, body.Email, body.Role, webHash, mailHash, body.QuotaBytes, body.OrgUnitID, body.Title, body.Department, body.EmployeeID, body.ManagerEmail, body.Aliases, body.Status, body.Password == "", s.cfg.MaildirBase)
	if err == nil {
		var expires any
		if policy.Security.PasswordExpiryDays > 0 {
			expires = time.Now().Add(time.Duration(policy.Security.PasswordExpiryDays) * 24 * time.Hour)
		}
		_, _ = tx.Exec(ctx, `UPDATE users SET password_expires_at = $2 WHERE id = $1`, created, expires)
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "USER_EXISTS", "That email or alias is already in use")
		} else {
			writeError(w, http.StatusBadRequest, "USER", err.Error())
		}
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not commit user provisioning")
		return
	}
	stateErr := s.ensureUserState(ctx, body.Email)
	if stateErr != nil {
		s.logger.Warn("new user state provisioning failed", "email", body.Email, "err", stateErr)
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_created", body.Email, "Created user account", map[string]any{"tenantId": sess.ActiveTenantID, "role": body.Role})
	user, _ := s.adminUserByID(ctx, created)
	detail, _ := s.adminUserDetail(ctx, user)
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "user": detail["user"],
		"temporaryPassword": temporaryPassword, "mailPassword": mailPassword,
		"passwordProvided": body.Password != "",
		"stateReady":       stateErr == nil,
		"message":          "Copy the password now. It will not be shown again.",
	})
}

func provisionUserTx(ctx context.Context, tx pgx.Tx, tenantID, name, email, role, webHash, mailHash string, quota int64, orgUnitID, title, department, employeeID, managerEmail string, aliases []string, status string, forcePasswordChange bool, maildirBase string) (string, error) {
	localPart, domain := splitEmail(email)
	if localPart == "" || domain == "" {
		return "", errors.New("invalid email address")
	}
	if status == "" {
		status = "active"
	}
	initials := initialsForName(name)
	color := colorForString(email)
	var orgUnit any
	if orgUnitID != "" {
		orgUnit = orgUnitID
	}
	var manager any
	if strings.TrimSpace(managerEmail) != "" {
		manager = strings.ToLower(strings.TrimSpace(managerEmail))
	}
	var userID string
	err := tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, initials, color, role, status, password_hash,
		                   mail_password_hash, quota_bytes, org_unit_id, title, department, employee_id,
		                   manager_email, force_password_change, onboarding_required)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, true)
		RETURNING id::text
	`, tenantID, email, name, initials, color, role, status, webHash, mailHash, quota, orgUnit, title, department, employeeID, manager, forcePasswordChange).Scan(&userID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_aliases (tenant_id, user_id, email, label, kind, can_send, active)
		VALUES ($1, $2, $3, $4, 'primary', true, true)
	`, tenantID, userID, email, name); err != nil {
		return "", err
	}
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias == "" || alias == email {
			continue
		}
		aliasLocal, aliasDomain := splitEmail(alias)
		if aliasLocal == "" || aliasDomain == "" {
			return "", fmt.Errorf("alias %q is invalid", alias)
		}
		var domainAllowed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mail_domains WHERE tenant_id = $1 AND domain = $2 AND status IN ('verified','manual'))`, tenantID, aliasDomain).Scan(&domainAllowed); err != nil {
			return "", err
		}
		if !domainAllowed {
			return "", fmt.Errorf("alias domain %s is not verified for this organization", aliasDomain)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_aliases (tenant_id, user_id, email, label, kind, can_send, active) VALUES ($1, $2, $3, $4, 'alias', true, true)`, tenantID, userID, alias, name); err != nil {
			return "", err
		}
	}
	maildir := fmt.Sprintf("%s/%s/%s/Maildir", strings.TrimRight(maildirBase, "/"), domain, localPart)
	if _, err := tx.Exec(ctx, `
		INSERT INTO mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, tenantID, userID, email, domain, localPart, maildir, status == "active"); err != nil {
		return "", err
	}
	return userID, nil
}

func provisionAdminUser(ctx context.Context, tx pgx.Tx, tenantID, name, email, role, passwordHash string, forceChange bool, maildirBase string) (map[string]any, error) {
	mailPassword, err := generateTemporaryPassword()
	if err != nil {
		return nil, err
	}
	mailHash, err := hashMailPassword(mailPassword)
	if err != nil {
		return nil, err
	}
	userID, err := provisionUserTx(ctx, tx, tenantID, name, email, role, passwordHash, mailHash, 16*1024*1024*1024, "", "", "", "", "", nil, "active", forceChange, maildirBase)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": userID, "email": strings.ToLower(email), "name": name, "role": role, "mailPassword": mailPassword}, nil
}

func (s *Server) ensureUserState(ctx context.Context, email string) error {
	state, err := stateinit.Build(ctx, s.store.DB, email)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var userID, tenantID string
	if err := s.store.DB.QueryRow(ctx, `SELECT id::text, tenant_id::text FROM users WHERE email = $1`, strings.ToLower(email)).Scan(&userID, &tenantID); err != nil {
		return err
	}
	_, err = s.store.DB.Exec(ctx, `
		INSERT INTO app_states (user_id, tenant_id, state, updated_at)
		VALUES ($1, $2, $3::jsonb, now())
		ON CONFLICT (user_id) DO NOTHING
	`, userID, tenantID, raw)
	return err
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if user.DeletedAt != nil {
		writeError(w, http.StatusConflict, "USER_CLOSED", "Restore this closed account before editing it")
		return
	}
	if !s.adminCanManageTarget(sess, user) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can change a privileged account")
		return
	}
	var body struct {
		Name                *string `json:"name"`
		Role                *string `json:"role"`
		Status              *string `json:"status"`
		QuotaBytes          *int64  `json:"quotaBytes"`
		OrgUnitID           *string `json:"orgUnitId"`
		Title               *string `json:"title"`
		Department          *string `json:"department"`
		EmployeeID          *string `json:"employeeId"`
		ManagerEmail        *string `json:"managerEmail"`
		ForcePasswordChange *bool   `json:"forcePasswordChange"`
	}
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if user.ID == sess.User.ID && body.Status != nil && *body.Status != "active" {
		writeError(w, http.StatusBadRequest, "SELF", "You cannot suspend your own admin account")
		return
	}
	if user.ID == sess.User.ID && body.Role != nil && *body.Role != user.Role {
		writeError(w, http.StatusBadRequest, "SELF", "You cannot change your own role")
		return
	}
	if user.IsSystemAdmin && !sess.User.IsSystemAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only a system administrator can edit a system administrator")
		return
	}
	if body.Role != nil && !validUserRole(*body.Role) {
		writeError(w, http.StatusBadRequest, "ROLE", "Invalid role")
		return
	}
	if body.Role != nil && (*body.Role == "owner" || *body.Role == "admin" || user.Role == "owner" || user.Role == "admin") && !sess.can("roles.manage") {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can change a privileged role")
		return
	}
	if body.Status != nil && !validUserStatus(*body.Status) && *body.Status != "deleted" {
		writeError(w, http.StatusBadRequest, "STATUS", "Invalid status")
		return
	}
	if (body.Role != nil && *body.Role != "owner" && user.Role == "owner") || (body.Status != nil && *body.Status != "active" && user.Role == "owner") {
		if protected, err := s.adminLastOwner(r.Context(), user); err != nil || protected {
			writeError(w, http.StatusConflict, "LAST_OWNER", "This is the last owner of the organization; assign another owner first")
			return
		}
	}
	if body.QuotaBytes != nil && (*body.QuotaBytes < 512*1024*1024 || *body.QuotaBytes > 1024*1024*1024*1024) {
		writeError(w, http.StatusBadRequest, "QUOTA", "Quota must be between 512 MB and 1 TB")
		return
	}
	if body.OrgUnitID != nil && *body.OrgUnitID != "" && !s.adminOrgUnitInTenant(r.Context(), *body.OrgUnitID, sess.ActiveTenantID) {
		writeError(w, http.StatusBadRequest, "ORG_UNIT", "Org unit does not belong to this organization")
		return
	}
	fields := map[string]any{}
	newName := ""
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		newName = truncate(strings.TrimSpace(*body.Name), 120)
		fields["name"] = newName
		fields["initials"] = initialsForName(newName)
	}
	if body.Role != nil {
		fields["role"] = *body.Role
	}
	if body.Status != nil {
		status := *body.Status
		if status == "deleted" {
			writeError(w, http.StatusBadRequest, "STATUS", "Use the delete endpoint to close an account")
			return
		}
		fields["status"] = status
	}
	if body.QuotaBytes != nil {
		fields["quota_bytes"] = *body.QuotaBytes
	}
	if body.OrgUnitID != nil {
		if *body.OrgUnitID == "" {
			fields["org_unit_id"] = nil
		} else {
			fields["org_unit_id"] = *body.OrgUnitID
		}
	}
	if body.Title != nil {
		fields["title"] = truncate(strings.TrimSpace(*body.Title), 120)
	}
	if body.Department != nil {
		fields["department"] = truncate(strings.TrimSpace(*body.Department), 120)
	}
	if body.EmployeeID != nil {
		fields["employee_id"] = truncate(strings.TrimSpace(*body.EmployeeID), 60)
	}
	if body.ManagerEmail != nil {
		manager := strings.ToLower(strings.TrimSpace(*body.ManagerEmail))
		if manager != "" && !strings.Contains(manager, "@") {
			writeError(w, http.StatusBadRequest, "MANAGER", "Manager email is invalid")
			return
		}
		if manager == "" {
			fields["manager_email"] = nil
		} else {
			fields["manager_email"] = manager
		}
	}
	if body.ForcePasswordChange != nil {
		fields["force_password_change"] = *body.ForcePasswordChange
	}
	if len(fields) == 0 {
		writeError(w, http.StatusBadRequest, "NO_CHANGES", "No changes were supplied")
		return
	}
	setParts := []string{"updated_at = now()"}
	args := []any{user.ID}
	position := 2
	for key, value := range fields {
		setParts = append(setParts, fmt.Sprintf("%s = $%d", key, position))
		args = append(args, value)
		position++
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not update user")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE users SET "+strings.Join(setParts, ", ")+" WHERE id = $1", args...); err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not update user")
		return
	}
	if newName != "" {
		_, _ = tx.Exec(ctx, `
			UPDATE user_aliases SET label = $2
			WHERE user_id = $1 AND (
				kind = 'primary'
				OR (kind = 'alias' AND (trim(label) = '' OR lower(label) IN ('primary', 'alias') OR label = $3))
			)
		`, user.ID, newName, user.Name)
		_, _ = tx.Exec(ctx, `
			UPDATE mail_identities SET display_name = $2, updated_at = now()
			WHERE user_id = $1 AND email = $3 AND (trim(display_name) = '' OR display_name = $4)
		`, user.ID, newName, user.Email, user.Name)
	}
	if body.Status != nil {
		active := *body.Status == "active"
		_, _ = tx.Exec(ctx, `UPDATE mailboxes SET active = $2 WHERE user_id = $1`, user.ID, active)
		if !active {
			_, _ = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, user.ID)
			_, _ = tx.Exec(ctx, `DELETE FROM auth_challenges WHERE user_id = $1`, user.ID)
			_, _ = tx.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id = $1`, user.ID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not commit user update")
		return
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_updated", user.Email, "Updated user account", map[string]any{"userId": user.ID, "fields": sortedMapKeys(fields)})
	updated, _ := s.adminUserByID(ctx, user.ID)
	detail, _ := s.adminUserDetail(ctx, updated)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": detail["user"]})
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if user.ID == sess.User.ID {
		writeError(w, http.StatusBadRequest, "SELF", "You cannot delete your own admin account")
		return
	}
	if user.IsSystemAdmin && !sess.User.IsSystemAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only a system administrator can delete a system administrator")
		return
	}
	if !s.adminCanManageTarget(sess, user) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can close a privileged account")
		return
	}
	if user.Role == "owner" {
		if protected, _ := s.adminLastOwner(r.Context(), user); protected {
			writeError(w, http.StatusConflict, "LAST_OWNER", "This is the last owner of the organization; assign another owner first")
			return
		}
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not delete user")
		return
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `UPDATE users SET deleted_at = now(), status = 'suspended', force_password_change = true, updated_at = now() WHERE id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `UPDATE mailboxes SET active = false WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM auth_challenges WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM trusted_devices WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM admin_auth_challenges WHERE user_id = $1`, user.ID)
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not commit user deletion")
		return
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_deleted", user.Email, "Closed user account (soft delete)", map[string]any{"userId": user.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
}

func (s *Server) adminRestoreUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if user.DeletedAt == nil {
		writeError(w, http.StatusBadRequest, "NOT_DELETED", "That account is not closed")
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not restore user")
		return
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `UPDATE users SET deleted_at = NULL, status = 'active', updated_at = now() WHERE id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `UPDATE mailboxes SET active = true WHERE user_id = $1`, user.ID)
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "USER", "Could not commit user restore")
		return
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_restored", user.Email, "Restored user account", map[string]any{"userId": user.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": true})
}

func (s *Server) adminResetUserPassword(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if user.IsSystemAdmin && !sess.User.IsSystemAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only a system administrator can reset a system administrator")
		return
	}
	if !s.adminCanManageTarget(sess, user) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can reset a privileged account")
		return
	}
	password, err := generateTemporaryPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not generate a temporary password")
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not hash the password")
		return
	}
	mailPassword, err := generateTemporaryPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_PASSWORD", "Could not generate a mailbox password")
		return
	}
	mailHash, err := hashMailPassword(mailPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_PASSWORD", "Could not create the Dovecot password hash")
		return
	}
	policy := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	var expires any
	if policy.Security.PasswordExpiryDays > 0 {
		expires = time.Now().Add(time.Duration(policy.Security.PasswordExpiryDays) * 24 * time.Hour)
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not reset password")
		return
	}
	defer tx.Rollback(ctx)
	if policy.Security.PasswordHistory > 0 {
		_, _ = tx.Exec(ctx, `INSERT INTO password_history (user_id, password_hash) SELECT $1, $2 WHERE $2 <> ''`, user.ID, user.PasswordHash)
		_, _ = tx.Exec(ctx, `
			DELETE FROM password_history WHERE user_id = $1 AND id NOT IN (
				SELECT id FROM password_history WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2
			)
		`, user.ID, policy.Security.PasswordHistory)
	} else {
		_, _ = tx.Exec(ctx, `DELETE FROM password_history WHERE user_id = $1`, user.ID)
	}
	_, _ = tx.Exec(ctx, `UPDATE users SET password_hash = $2, mail_password_hash = $3, password_changed_at = now(), password_expires_at = $4, force_password_change = true, updated_at = now() WHERE id = $1`, user.ID, hash, mailHash, expires)
	_, _ = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM auth_challenges WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM trusted_devices WHERE user_id = $1`, user.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id = $1`, user.ID)
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not commit password reset")
		return
	}
	s.clearDAVAuthCache(user.Email)
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_password_reset", user.Email, "Issued a temporary password and revoked sessions", map[string]any{"userId": user.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "temporaryPassword": password, "mailPassword": mailPassword, "message": "Copy these credentials now. They will not be shown again."})
}

func (s *Server) adminResetUserMFA(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if user.IsSystemAdmin && !sess.User.IsSystemAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only a system administrator can reset a system administrator")
		return
	}
	if !s.adminCanManageTarget(sess, user) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can reset a privileged account")
		return
	}
	ctx := r.Context()
	if _, err := s.store.DB.Exec(ctx, `
		UPDATE users SET security = security - 'twoFactor' - 'totpSecret', updated_at = now() WHERE id = $1
	`, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "MFA", "Could not reset authentication factors")
		return
	}
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM trusted_devices WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM auth_challenges WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM admin_auth_challenges WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id = $1`, user.ID)
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_mfa_reset", user.Email, "Reset all multi-factor credentials", map[string]any{"userId": user.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "MFA factors and sessions were removed. The user can enroll again at next sign-in if policy requires it."})
}

func (s *Server) adminRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if !s.adminCanManageTarget(sess, user) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can revoke a privileged account")
		return
	}
	ctx := r.Context()
	var count int
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID).Scan(&count)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM auth_challenges WHERE user_id = $1`, user.ID)
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id = $1`, user.ID)
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_sessions_revoked", user.Email, "Revoked all user sessions", map[string]any{"userId": user.ID, "count": count})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": count})
}

func (s *Server) adminUserSessions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, COALESCE(host(ip), ''), user_agent, created_at, last_seen_at, expires_at
		FROM sessions WHERE user_id = $1 AND expires_at > now() ORDER BY last_seen_at DESC
	`, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSIONS", "Could not load sessions")
		return
	}
	defer rows.Close()
	sessions := []map[string]any{}
	for rows.Next() {
		var id, ip, userAgent string
		var created, lastSeen, expires time.Time
		if rows.Scan(&id, &ip, &userAgent, &created, &lastSeen, &expires) == nil {
			sessions = append(sessions, map[string]any{
				"id": id, "ip": ip, "device": describeSessionDevice(userAgent),
				"createdAt": formatAdminTime(created), "lastSeenAt": formatAdminTime(lastSeen), "expiresAt": formatAdminTime(expires),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "count": len(sessions)})
}

func (s *Server) adminAddUserAlias(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	var body struct {
		Email   string `json:"email"`
		Label   string `json:"label"`
		CanSend *bool  `json:"canSend"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || !strings.Contains(body.Email, "@") {
		writeError(w, http.StatusBadRequest, "ALIAS", "Enter a valid alias email")
		return
	}
	if !strings.HasSuffix(body.Email, "@"+strings.Split(user.Email, "@")[1]) {
		writeError(w, http.StatusBadRequest, "ALIAS", "Alias must use one of this organization's domains")
		return
	}
	if s.adminAddressInUse(r.Context(), body.Email) {
		writeError(w, http.StatusConflict, "ADDRESS_EXISTS", "That address is already in use")
		return
	}
	canSend := true
	if body.CanSend != nil {
		canSend = *body.CanSend
	}
	label := truncate(strings.TrimSpace(body.Label), 80)
	if label == "" {
		label = "Alias"
	}
	var id string
	err = s.store.DB.QueryRow(r.Context(), `
		INSERT INTO user_aliases (tenant_id, user_id, email, label, kind, can_send, active)
		VALUES ($1, $2, $3, $4, 'alias', $5, true) RETURNING id::text
	`, sess.ActiveTenantID, user.ID, body.Email, label, canSend).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "ALIAS_EXISTS", "That address is already in use")
		} else {
			writeError(w, http.StatusInternalServerError, "ALIAS", "Could not add alias")
		}
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.alias_added", user.Email, "Added alias "+body.Email, map[string]any{"userId": user.ID, "alias": body.Email})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "alias": map[string]any{"id": id, "email": body.Email, "label": label, "kind": "alias", "canSend": canSend, "active": true}})
}

func (s *Server) adminUpdateUserAlias(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	aliasID := r.PathValue("aliasId")
	var kind, currentLabel string
	var currentCanSend, currentActive bool
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT kind, label, can_send, active FROM user_aliases WHERE id=$1 AND user_id=$2 AND tenant_id=$3
	`, aliasID, user.ID, sess.ActiveTenantID).Scan(&kind, &currentLabel, &currentCanSend, &currentActive); err != nil {
		writeError(w, http.StatusNotFound, "ALIAS_NOT_FOUND", "Alias not found")
		return
	}
	if kind == "primary" {
		writeError(w, http.StatusBadRequest, "PRIMARY_ALIAS", "The primary address cannot be changed")
		return
	}
	var body struct {
		Label   *string `json:"label"`
		CanSend *bool   `json:"canSend"`
		Active  *bool   `json:"active"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	label, canSend, active := currentLabel, currentCanSend, currentActive
	if body.Label != nil {
		label = truncate(strings.TrimSpace(*body.Label), 80)
		if label == "" {
			label = "Alias"
		}
	}
	if body.CanSend != nil {
		canSend = *body.CanSend
	}
	if body.Active != nil {
		active = *body.Active
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE user_aliases SET label=$2, can_send=$3, active=$4 WHERE id=$1 AND user_id=$5
	`, aliasID, label, canSend, active, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "ALIAS", "Could not update the alias")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.alias_updated", user.Email, "Updated alias "+label, map[string]any{"userId": user.ID, "aliasId": aliasID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alias": map[string]any{"id": aliasID, "label": label, "canSend": canSend, "active": active}})
}

func (s *Server) adminDeleteUserAlias(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	aliasID := r.PathValue("aliasId")
	var kind string
	_ = s.store.DB.QueryRow(r.Context(), `SELECT kind FROM user_aliases WHERE id = $1 AND user_id = $2 AND tenant_id = $3`, aliasID, user.ID, sess.ActiveTenantID).Scan(&kind)
	if kind == "" {
		writeError(w, http.StatusNotFound, "ALIAS_NOT_FOUND", "Alias not found")
		return
	}
	if kind == "primary" {
		writeError(w, http.StatusBadRequest, "PRIMARY_ALIAS", "The primary address cannot be removed")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM user_aliases WHERE id = $1 AND user_id = $2`, aliasID, user.ID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.alias_removed", user.Email, "Removed an alias", map[string]any{"userId": user.ID, "aliasId": aliasID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminBulkUsers(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Action != "activate" && body.Action != "suspend" && body.Action != "delete" {
		writeError(w, http.StatusBadRequest, "ACTION", "Bulk action must be activate, suspend or delete")
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > 100 {
		writeError(w, http.StatusBadRequest, "IDS", "Select between 1 and 100 users")
		return
	}
	if len(body.IDs) > 0 && (body.Action == "suspend" || body.Action == "delete") {
		// Reject if any selected owner is the last owner.
		for _, id := range body.IDs {
			user, err := s.adminUserByID(r.Context(), id)
			if err != nil || user.TenantID != sess.ActiveTenantID || user.Role != "owner" || user.DeletedAt != nil {
				continue
			}
			if protected, _ := s.adminLastOwner(r.Context(), user); protected {
				writeError(w, http.StatusConflict, "LAST_OWNER", "One selected user is the last owner of the organization")
				return
			}
		}
	}
	ctx := r.Context()
	affected := 0
	for _, id := range body.IDs {
		user, err := s.adminUserByID(ctx, id)
		if err != nil || user.TenantID != sess.ActiveTenantID || user.ID == sess.User.ID {
			continue
		}
		if !s.adminCanManageTarget(sess, user) {
			continue
		}
		switch body.Action {
		case "activate":
			if user.DeletedAt != nil {
				continue
			}
			_, _ = s.store.DB.Exec(ctx, `UPDATE users SET status='active', updated_at=now() WHERE id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `UPDATE mailboxes SET active=true WHERE user_id=$1`, user.ID)
		case "suspend":
			_, _ = s.store.DB.Exec(ctx, `UPDATE users SET status='suspended', updated_at=now() WHERE id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `UPDATE mailboxes SET active=false WHERE user_id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `DELETE FROM auth_challenges WHERE user_id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id=$1`, user.ID)
		case "delete":
			if user.IsSystemAdmin {
				continue
			}
			_, _ = s.store.DB.Exec(ctx, `UPDATE users SET deleted_at=now(), status='suspended', updated_at=now() WHERE id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `UPDATE mailboxes SET active=false WHERE user_id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, user.ID)
			_, _ = s.store.DB.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id=$1`, user.ID)
		}
		affected++
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.users_bulk", fmt.Sprintf("%d users", affected), "Bulk action: "+body.Action, map[string]any{"action": body.Action, "count": affected})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected, "action": body.Action})
}

func (s *Server) adminLastOwner(ctx context.Context, user adminUser) (bool, error) {
	var count int
	err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE tenant_id = $1 AND role = 'owner' AND status = 'active' AND deleted_at IS NULL`, user.TenantID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count <= 1, nil
}

func (s *Server) adminOrgUnitInTenant(ctx context.Context, orgUnitID, tenantID string) bool {
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM org_units WHERE id = $1 AND tenant_id = $2)`, orgUnitID, tenantID).Scan(&exists)
	return exists
}

func (s *Server) adminAddressInUse(ctx context.Context, email string) bool {
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM users WHERE lower(email::text) = lower($1))
		    OR EXISTS(SELECT 1 FROM user_aliases WHERE lower(email::text) = lower($1))
		    OR EXISTS(SELECT 1 FROM groups WHERE lower(email::text) = lower($1))
	`, email).Scan(&exists)
	return exists
}

func (s *Server) adminDomainVerified(ctx context.Context, tenantID, domain string) bool {
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM mail_domains WHERE tenant_id = $1 AND lower(domain::text) = lower($2) AND status IN ('verified','manual'))
	`, tenantID, domain).Scan(&exists)
	return exists
}

func ensureTenantDomain(ctx context.Context, tx pgx.Tx, tenantID, email string) error {
	_, domain := splitEmail(email)
	if domain == "" {
		return errors.New("invalid email address")
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM mail_domains WHERE tenant_id = $1 AND domain = $2 AND status IN ('verified','manual'))
	`, tenantID, domain).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("domain %s is not a verified domain for this organization", domain)
	}
	return nil
}

func validUserRole(value string) bool {
	switch value {
	case "owner", "admin", "member", "readonly":
		return true
	}
	return false
}

func validUserStatus(value string) bool {
	switch value {
	case "active", "suspended", "invited":
		return true
	}
	return false
}

func validatePasswordAgainstPolicy(password string, policy adminSecurityPolicy) []string {
	errors := []string{}
	if len([]rune(password)) < policy.PasswordMinLength {
		errors = append(errors, fmt.Sprintf("Password must be at least %d characters", policy.PasswordMinLength))
	}
	hasUpper, hasLower, hasNumber, hasSymbol := false, false, false, false
	for _, char := range password {
		switch {
		case char >= 'A' && char <= 'Z':
			hasUpper = true
		case char >= 'a' && char <= 'z':
			hasLower = true
		case char >= '0' && char <= '9':
			hasNumber = true
		default:
			hasSymbol = true
		}
	}
	if policy.PasswordRequiresUpper && !hasUpper {
		errors = append(errors, "Password must contain an uppercase letter")
	}
	if policy.PasswordRequiresLower && !hasLower {
		errors = append(errors, "Password must contain a lowercase letter")
	}
	if policy.PasswordRequiresNumber && !hasNumber {
		errors = append(errors, "Password must contain a number")
	}
	if policy.PasswordRequiresSymbol && !hasSymbol {
		errors = append(errors, "Password must contain a symbol")
	}
	return errors
}

func generateTemporaryPassword() (string, error) {
	upper := "ABCDEFGHJKLMNPQRSTUVWXYZ"
	lower := "abcdefghijkmnopqrstuvwxyz"
	digits := "23456789"
	symbols := "!@#$%^&*"
	all := upper + lower + digits + symbols
	out := make([]byte, 20)
	for index := 0; index < 4; index++ {
		set := []string{upper, lower, digits, symbols}[index]
		value, err := secureRandomByte(len(set))
		if err != nil {
			return "", err
		}
		out[index] = set[value]
	}
	for index := 4; index < len(out); index++ {
		value, err := secureRandomByte(len(all))
		if err != nil {
			return "", err
		}
		out[index] = all[value]
	}
	for index := len(out) - 1; index > 0; index-- {
		value, err := secureRandomByte(index + 1)
		if err != nil {
			return "", err
		}
		out[index], out[value] = out[value], out[index]
	}
	return string(out), nil
}

func secureRandomByte(limit int) (int, error) {
	if limit <= 0 || limit > 256 {
		return 0, errors.New("invalid random range")
	}
	buffer := make([]byte, 1)
	threshold := 256 - (256 % limit)
	for {
		if _, err := rand.Read(buffer); err != nil {
			return 0, err
		}
		if int(buffer[0]) < threshold {
			return int(buffer[0]) % limit, nil
		}
	}
}

func splitEmail(email string) (string, string) {
	email = strings.ToLower(strings.TrimSpace(email))
	index := strings.LastIndex(email, "@")
	if index <= 0 || index == len(email)-1 {
		return "", ""
	}
	return email[:index], email[index+1:]
}

func initialsForName(name string) string {
	initials := ""
	for _, part := range strings.Fields(name) {
		runes := []rune(part)
		if len(runes) == 0 {
			continue
		}
		initials += strings.ToUpper(string(runes[0]))
		if len([]rune(initials)) >= 2 {
			break
		}
	}
	if initials == "" {
		return "?"
	}
	return initials
}

func colorForString(value string) string {
	palette := []string{"#34557a", "#3d6b62", "#7a5a3a", "#7a4a5c", "#5b5f8a", "#2f6b8a", "#6a5b3d", "#4f6b4f"}
	hash := 0
	for _, char := range value {
		hash = (hash*31 + int(char)) & 0x7fffffff
	}
	return palette[hash%len(palette)]
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) adminCanManageTarget(sess *adminSessionRecord, target adminUser) bool {
	if sess.User.IsSystemAdmin || sess.can("roles.manage") {
		return true
	}
	if target.ID == sess.User.ID {
		return true
	}
	return target.Role != "owner" && target.Role != "admin" && !target.IsSystemAdmin
}
