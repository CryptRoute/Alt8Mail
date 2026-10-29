package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/meridian-mail/meridian/backend/internal/auth"
)

func (s *Server) adminTenantStats(ctx context.Context, tenantID string) (map[string]any, error) {
	stats := map[string]any{}
	var totalUsers, activeUsers, suspendedUsers, invitedUsers, admins, mfaUsers, groups, orgUnits, domains, pendingDomains, mailboxes int
	var storageUsed, storageQuota int64
	err := s.store.DB.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE deleted_at IS NULL),
			COUNT(*) FILTER (WHERE deleted_at IS NULL AND status = 'active'),
			COUNT(*) FILTER (WHERE deleted_at IS NULL AND status = 'suspended'),
			COUNT(*) FILTER (WHERE deleted_at IS NULL AND status = 'invited'),
			COUNT(*) FILTER (WHERE deleted_at IS NULL AND status = 'active' AND is_system_admin),
			COUNT(*) FILTER (WHERE deleted_at IS NULL AND status = 'active' AND COALESCE(security->'twoFactor'->>'totpSecret', '') <> ''),
			COALESCE(SUM(quota_bytes) FILTER (WHERE deleted_at IS NULL), 0),
			COALESCE(SUM(used_bytes) FILTER (WHERE deleted_at IS NULL), 0)
		FROM users WHERE tenant_id = $1
	`, tenantID).Scan(&totalUsers, &activeUsers, &suspendedUsers, &invitedUsers, &admins, &mfaUsers, &storageQuota, &storageUsed)
	if err != nil {
		return nil, err
	}
	_ = s.store.DB.QueryRow(ctx, `SELECT COUNT(*) FROM groups WHERE tenant_id = $1 AND status = 'active'`, tenantID).Scan(&groups)
	_ = s.store.DB.QueryRow(ctx, `SELECT COUNT(*) FROM org_units WHERE tenant_id = $1`, tenantID).Scan(&orgUnits)
	_ = s.store.DB.QueryRow(ctx, `SELECT COUNT(*) FROM mail_domains WHERE tenant_id = $1`, tenantID).Scan(&domains)
	_ = s.store.DB.QueryRow(ctx, `SELECT COUNT(*) FROM mail_domains WHERE tenant_id = $1 AND status NOT IN ('verified', 'manual')`, tenantID).Scan(&pendingDomains)
	_ = s.store.DB.QueryRow(ctx, `SELECT COUNT(*) FROM mailboxes WHERE tenant_id = $1 AND active = true`, tenantID).Scan(&mailboxes)
	var adminRoleUsers, logins24h int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT COUNT(DISTINCT u.id) FROM users u
		WHERE u.tenant_id = $1 AND u.deleted_at IS NULL AND (
			u.role IN ('owner','admin') OR EXISTS (
				SELECT 1 FROM admin_role_assignments a WHERE a.user_id = u.id AND a.tenant_id = u.tenant_id
			)
		)
	`, tenantID).Scan(&adminRoleUsers)
	_ = s.store.DB.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE tenant_id = $1 AND last_login_at > now() - interval '24 hours'`, tenantID).Scan(&logins24h)
	var stateUsed int64
	_ = s.store.DB.QueryRow(ctx, `
		SELECT COALESCE(SUM((message->>'sizeBytes')::bigint), 0)
		FROM app_states s
		JOIN users u ON u.id = s.user_id
		CROSS JOIN LATERAL jsonb_array_elements(
			CASE WHEN jsonb_typeof(s.state->'messages') = 'array' THEN s.state->'messages' ELSE '[]'::jsonb END
		) AS message
		WHERE u.tenant_id = $1 AND u.deleted_at IS NULL
	`, tenantID).Scan(&stateUsed)
	if stateUsed > storageUsed {
		storageUsed = stateUsed
	}
	mfaCoverage := 0
	if activeUsers > 0 {
		mfaCoverage = mfaUsers * 100 / activeUsers
	}
	recent, _ := s.adminAuditEntries(ctx, tenantID, "", "", 6, 0)
	stats["users"] = map[string]any{"total": totalUsers, "active": activeUsers, "suspended": suspendedUsers, "invited": invitedUsers, "admins": adminRoleUsers, "mfa": mfaUsers}
	stats["groups"] = groups
	stats["orgUnits"] = orgUnits
	stats["domains"] = map[string]any{"total": domains, "pending": pendingDomains}
	stats["mailboxes"] = mailboxes
	stats["storage"] = map[string]any{"usedBytes": storageUsed, "quotaBytes": storageQuota}
	stats["logins24h"] = logins24h
	stats["posture"] = map[string]any{
		"mfaCoverage": mfaCoverage,
		"mfaUsers":    mfaUsers,
		"mfaTarget":   activeUsers,
		"domainHealth": func() int {
			if domains == 0 {
				return 0
			}
			return (domains - pendingDomains) * 100 / domains
		}(),
		"adminAccounts": adminRoleUsers,
	}
	stats["recentAudit"] = recent
	return stats, nil
}

func (s *Server) adminListTenants(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT t.id::text, t.slug, t.name, t.status, t.plan, t.region, t.created_at,
		       (SELECT count(*) FROM users u WHERE u.tenant_id = t.id AND u.deleted_at IS NULL) AS users,
		       (SELECT count(*) FROM users u WHERE u.tenant_id = t.id AND u.deleted_at IS NULL AND u.status = 'active') AS active_users,
		       (SELECT count(*) FROM mail_domains d WHERE d.tenant_id = t.id) AS domains,
		       (SELECT COALESCE(SUM(u.used_bytes), 0) FROM users u WHERE u.tenant_id = t.id AND u.deleted_at IS NULL) AS used_bytes,
		       (SELECT COALESCE(SUM(u.quota_bytes), 0) FROM users u WHERE u.tenant_id = t.id AND u.deleted_at IS NULL) AS quota_bytes
		FROM tenants t
		ORDER BY t.name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TENANTS", "Could not list organizations")
		return
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, slug, name, status, plan, region string
		var created time.Time
		var users, activeUsers, domains int
		var used, quota int64
		if err := rows.Scan(&id, &slug, &name, &status, &plan, &region, &created, &users, &activeUsers, &domains, &used, &quota); err != nil {
			continue
		}
		result = append(result, map[string]any{
			"id": id, "slug": slug, "name": name, "status": status, "plan": plan, "region": region,
			"createdAt": created.UTC().Format(time.RFC3339), "users": users, "activeUsers": activeUsers,
			"domains": domains, "storage": map[string]any{"usedBytes": used, "quotaBytes": quota},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": result, "count": len(result), "activeTenantId": sess.ActiveTenantID})
}

func (s *Server) adminCreateTenant(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		Plan          string `json:"plan"`
		Region        string `json:"region"`
		PrimaryDomain string `json:"primaryDomain"`
		OwnerName     string `json:"ownerName"`
		OwnerEmail    string `json:"ownerEmail"`
	}
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Slug = strings.ToLower(strings.TrimSpace(body.Slug))
	body.PrimaryDomain = strings.ToLower(strings.TrimSpace(body.PrimaryDomain))
	body.OwnerEmail = strings.ToLower(strings.TrimSpace(body.OwnerEmail))
	if body.Name == "" || len([]rune(body.Name)) > 120 {
		writeError(w, http.StatusBadRequest, "NAME", "Organization name is required")
		return
	}
	if !validSlug(body.Slug) {
		writeError(w, http.StatusBadRequest, "SLUG", "Slug must be 2-32 lowercase letters, numbers or hyphens")
		return
	}
	if !validMailDomain(body.PrimaryDomain) {
		writeError(w, http.StatusBadRequest, "DOMAIN", "Enter a valid primary mail domain")
		return
	}
	if body.Plan == "" {
		body.Plan = "business"
	}
	if body.Region == "" {
		body.Region = "us-east-1"
	}
	if !validPlan(body.Plan) || !validRegion(body.Region) {
		writeError(w, http.StatusBadRequest, "PLAN", "Choose a valid plan and region")
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TENANT", "Could not start tenant provisioning")
		return
	}
	defer tx.Rollback(ctx)
	var tenantID string
	err = tx.QueryRow(ctx, `
		INSERT INTO tenants (slug, name, plan, region, billing_email)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		RETURNING id::text
	`, body.Slug, body.Name, body.Plan, body.Region, body.OwnerEmail).Scan(&tenantID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "SLUG_TAKEN", "That organization slug is already in use")
		} else {
			writeError(w, http.StatusInternalServerError, "TENANT", "Could not create organization")
		}
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mail_domains (tenant_id, domain, is_primary, dkim_selector, status, verification_token) VALUES ($1, $2, true, 'meridian', 'pending', $3)`, tenantID, body.PrimaryDomain, randomToken(18)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "DOMAIN_TAKEN", "That domain is already assigned to another organization")
		} else {
			writeError(w, http.StatusInternalServerError, "DOMAIN", "Could not add the primary domain")
		}
		return
	}
	if err := seedAdminRoles(ctx, tx, tenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "TENANT", "Could not seed admin roles")
		return
	}
	var ownerResult map[string]any
	if body.OwnerEmail != "" {
		if !strings.HasSuffix(body.OwnerEmail, "@"+body.PrimaryDomain) {
			writeError(w, http.StatusBadRequest, "OWNER_EMAIL", "Owner email must use the primary domain")
			return
		}
		if body.OwnerName == "" {
			body.OwnerName = strings.Split(body.OwnerEmail, "@")[0]
		}
		password, err := generateTemporaryPassword()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "OWNER", "Could not generate an owner password")
			return
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "OWNER", "Could not hash the owner password")
			return
		}
		ownerResult, err = provisionAdminUser(ctx, tx, tenantID, body.OwnerName, body.OwnerEmail, "owner", hash, true, s.cfg.MaildirBase)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "OWNER", "Could not create the owner account: "+err.Error())
			return
		}
		ownerResult["temporaryPassword"] = password
		if ownerID, ok := ownerResult["id"].(string); ok && ownerID != "" {
			_, _ = tx.Exec(ctx, `UPDATE users SET password_expires_at = NULL WHERE id = $1`, ownerID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "TENANT", "Could not commit organization")
		return
	}
	if ownerEmail, ok := ownerResult["email"].(string); ok && ownerEmail != "" {
		if err := s.ensureUserState(ctx, ownerEmail); err != nil {
			s.logger.Warn("tenant owner state provisioning failed", "email", ownerEmail, "err", err)
		}
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.tenant_created", body.Name, "Created organization", map[string]any{"tenantId": tenantID, "domain": body.PrimaryDomain})
	tenant, _ := s.adminTenantByID(r.Context(), tenantID)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "tenant": adminTenantPayload(tenant), "owner": ownerResult})
}

func (s *Server) adminUpdateTenant(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	id := r.PathValue("id")
	tenant, err := s.adminTenantByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "TENANT_NOT_FOUND", "Organization not found")
		return
	}
	var body struct {
		Name         *string `json:"name"`
		Plan         *string `json:"plan"`
		Region       *string `json:"region"`
		Status       *string `json:"status"`
		BillingEmail *string `json:"billingEmail"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	name, plan, region, status := tenant.Name, tenant.Plan, tenant.Region, tenant.Status
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		name = truncate(strings.TrimSpace(*body.Name), 120)
	}
	if body.Plan != nil {
		if !validPlan(*body.Plan) {
			writeError(w, http.StatusBadRequest, "PLAN", "Invalid plan")
			return
		}
		plan = *body.Plan
	}
	if body.Region != nil {
		if !validRegion(*body.Region) {
			writeError(w, http.StatusBadRequest, "REGION", "Invalid region")
			return
		}
		region = *body.Region
	}
	if body.Status != nil {
		if *body.Status != "active" && *body.Status != "suspended" {
			writeError(w, http.StatusBadRequest, "STATUS", "Status must be active or suspended")
			return
		}
		status = *body.Status
	}
	var billing any
	if body.BillingEmail != nil && strings.TrimSpace(*body.BillingEmail) != "" {
		billing = strings.ToLower(strings.TrimSpace(*body.BillingEmail))
		if !strings.Contains(billing.(string), "@") {
			writeError(w, http.StatusBadRequest, "BILLING_EMAIL", "Billing email is invalid")
			return
		}
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TENANT", "Could not update organization")
		return
	}
	defer tx.Rollback(r.Context())
	var suspendedAt any
	if status == "suspended" {
		suspendedAt = time.Now()
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE tenants SET name = $2, plan = $3, region = $4, status = $5,
		       billing_email = COALESCE($6, billing_email), suspended_at = $7, updated_at = now()
		WHERE id = $1
	`, id, name, plan, region, status, billing, suspendedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "TENANT", "Could not update organization")
		return
	}
	if status == "suspended" {
		_, _ = tx.Exec(r.Context(), `DELETE FROM sessions WHERE tenant_id = $1`, id)
		_, _ = tx.Exec(r.Context(), `DELETE FROM admin_sessions WHERE tenant_id = $1 AND user_id <> $2`, id, sess.User.ID)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "TENANT", "Could not commit organization update")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.tenant_updated", name, "Updated organization lifecycle or plan", map[string]any{"tenantId": id, "status": status})
	updated, _ := s.adminTenantByID(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tenant": adminTenantPayload(updated)})
}

func seedAdminRoles(ctx context.Context, tx pgx.Tx, tenantID string) error {
	roles := []struct {
		name, description string
		permissions       []string
	}{
		{"User administrator", "Create and manage users, groups and directory details.", []string{"users.read", "users.create", "users.update", "users.delete", "users.reset_password", "users.security", "users.aliases", "groups.read", "groups.manage", "orgunits.read", "audit.read"}},
		{"Security administrator", "Manage authentication policy, user security and sessions.", []string{"users.read", "users.security", "users.reset_password", "sessions.manage", "policies.read", "policies.manage", "roles.read", "audit.read"}},
		{"Helpdesk", "Reset passwords and unlock accounts without policy changes.", []string{"users.read", "users.reset_password", "users.security", "sessions.manage", "audit.read"}},
		{"Auditor", "Read-only access to users, policy, domains and audit.", []string{"tenant.read", "users.read", "groups.read", "orgunits.read", "roles.read", "policies.read", "domains.read", "audit.read"}},
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_roles (tenant_id, name, description, permissions, is_system)
			SELECT $1, $2, $3, $4, true
			WHERE NOT EXISTS (SELECT 1 FROM admin_roles WHERE tenant_id = $1 AND lower(name) = lower($2))
		`, tenantID, role.name, role.description, role.permissions); err != nil {
			return err
		}
	}
	return nil
}

func validSlug(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			continue
		}
		if char == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

func validMailDomain(value string) bool {
	if len(value) < 4 || len(value) > 253 || !strings.Contains(value, ".") || strings.ContainsAny(value, " @/\\") {
		return false
	}
	return true
}

func validPlan(value string) bool {
	switch value {
	case "starter", "business", "enterprise", "education":
		return true
	}
	return false
}

func validRegion(value string) bool {
	switch value {
	case "us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-1":
		return true
	}
	return false
}

func (s *Server) adminListAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	total, entries := s.adminAuditPage(r.Context(), sess.ActiveTenantID, action, query, limit, (page-1)*limit)
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total, "page": page, "limit": limit})
}

func (s *Server) adminAuditEntries(ctx context.Context, tenantID, action, query string, limit, offset int) ([]map[string]any, error) {
	_, entries := s.adminAuditPage(ctx, tenantID, action, query, limit, offset)
	return entries, nil
}

func (s *Server) adminAuditPage(ctx context.Context, tenantID, action, query string, limit, offset int) (int, []map[string]any) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	position := 2
	if action != "" {
		where = append(where, fmt.Sprintf("action = $%d", position))
		args = append(args, action)
		position++
	}
	if query != "" {
		where = append(where, fmt.Sprintf("(actor_email ILIKE $%d OR target ILIKE $%d OR detail ILIKE $%d OR action ILIKE $%d)", position, position, position, position))
		args = append(args, "%"+query+"%")
		position++
	}
	condition := strings.Join(where, " AND ")
	var total int
	_ = s.store.DB.QueryRow(ctx, "SELECT COUNT(*) FROM admin_audit_log WHERE "+condition, args...).Scan(&total)
	args = append(args, limit, offset)
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, COALESCE(actor_email::text, ''), action, target, detail,
		       COALESCE(host(ip), ''), user_agent, metadata, created_at
		FROM admin_audit_log
		WHERE `+condition+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(position)+` OFFSET $`+strconv.Itoa(position+1), args...)
	if err != nil {
		return total, []map[string]any{}
	}
	defer rows.Close()
	entries := []map[string]any{}
	for rows.Next() {
		var id int64
		var actor, actionValue, target, detail, ip, userAgent string
		var metadata json.RawMessage
		var created time.Time
		if err := rows.Scan(&id, &actor, &actionValue, &target, &detail, &ip, &userAgent, &metadata, &created); err != nil {
			continue
		}
		entries = append(entries, map[string]any{
			"id": id, "actor": actor, "action": actionValue, "target": target, "detail": detail,
			"ip": ip, "userAgent": userAgent, "metadata": jsonRaw(metadata),
			"createdAt": created.UTC().Format(time.RFC3339),
		})
	}
	return total, entries
}

func (s *Server) adminExportAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	_, entries := s.adminAuditPage(r.Context(), sess.ActiveTenantID, strings.TrimSpace(r.URL.Query().Get("action")), strings.TrimSpace(r.URL.Query().Get("q")), 5000, 0)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="meridian-admin-audit.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"created_at", "actor", "action", "target", "detail", "ip", "user_agent"})
	for _, entry := range entries {
		_ = writer.Write([]string{
			fmt.Sprint(entry["createdAt"]), fmt.Sprint(entry["actor"]), fmt.Sprint(entry["action"]),
			fmt.Sprint(entry["target"]), fmt.Sprint(entry["detail"]), fmt.Sprint(entry["ip"]), fmt.Sprint(entry["userAgent"]),
		})
	}
	writer.Flush()
}

func parseIntDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s *Server) adminListSystemAdmins(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT u.id::text, u.email::text, u.name, u.initials, u.color, u.status, u.last_login_at
		FROM users u WHERE u.is_system_admin = true AND u.deleted_at IS NULL ORDER BY u.name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADMINS", "Could not load system administrators")
		return
	}
	defer rows.Close()
	admins := []map[string]any{}
	for rows.Next() {
		var id, email, name, initials, color, status string
		var lastLogin *time.Time
		if rows.Scan(&id, &email, &name, &initials, &color, &status, &lastLogin) == nil {
			entry := map[string]any{"id": id, "email": email, "name": name, "initials": initials, "color": color, "status": status}
			if lastLogin != nil {
				entry["lastLoginAt"] = formatAdminTime(*lastLogin)
			}
			admins = append(admins, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": admins, "count": len(admins), "currentUserId": sess.User.ID})
}

func (s *Server) adminGrantSystemAdmin(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, "EMAIL", "Email is required")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE users SET is_system_admin = true, updated_at = now()
		WHERE email = $1 AND status = 'active' AND deleted_at IS NULL
	`, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADMIN", "Could not grant system administrator")
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "No active user has that email")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.system_admin_granted", email, "Granted platform-wide administrator access", map[string]any{"email": email})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminRevokeSystemAdmin(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	id := r.PathValue("id")
	if id == sess.User.ID {
		writeError(w, http.StatusBadRequest, "SELF", "You cannot revoke your own system administrator access")
		return
	}
	var count int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE is_system_admin = true AND deleted_at IS NULL`).Scan(&count)
	if count <= 1 {
		writeError(w, http.StatusConflict, "LAST_ADMIN", "At least one system administrator must remain")
		return
	}
	var email string
	err := s.store.DB.QueryRow(r.Context(), `SELECT email::text FROM users WHERE id = $1 AND is_system_admin = true`, id).Scan(&email)
	if err != nil {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "System administrator not found")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE users SET is_system_admin = false, updated_at = now() WHERE id = $1`, id)
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_sessions WHERE user_id = $1`, id)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.system_admin_revoked", email, "Revoked platform-wide administrator access", map[string]any{"userId": id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
