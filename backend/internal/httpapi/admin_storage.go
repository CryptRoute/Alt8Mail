package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type adminStoragePolicyBody struct {
	Domain            string `json:"domain"`
	Email             string `json:"email"`
	Mode              string `json:"mode"`
	EmailQuotaBytes   int64  `json:"emailQuotaBytes"`
	StorageQuotaBytes int64  `json:"storageQuotaBytes"`
}

func cleanStorageQuotaValue(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value < 100*1024*1024 {
		return 100 * 1024 * 1024
	}
	if value > 100*1024*1024*1024*1024 {
		return 100 * 1024 * 1024 * 1024 * 1024
	}
	return value
}

func storagePolicyEntry(scope, domain, userID, email, name, mode string, emailQuota, storageQuota int64, updatedAt time.Time) map[string]any {
	entry := map[string]any{
		"scope": scope, "mode": mode, "emailQuotaBytes": emailQuota,
		"storageQuotaBytes": storageQuota, "email": email, "name": name,
		"userId": userID, "domain": domain,
		"updatedAt": updatedAt.UTC().Format(time.RFC3339),
	}
	return entry
}

func (s *Server) adminStoragePolicies(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tenantID := sess.ActiveTenantID
	var mode string
	var emailQuota, storageQuota int64
	var updatedAt time.Time
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT mode, email_quota_bytes, storage_quota_bytes, updated_at
		FROM storage_policy_defaults WHERE tenant_id=$1
	`, tenantID).Scan(&mode, &emailQuota, &storageQuota, &updatedAt)
	if err != nil {
		mode, emailQuota, storageQuota, updatedAt = "combined", 0, 0, time.Now()
	}
	domainRows, _ := s.store.DB.Query(r.Context(), `
		SELECT name::text FROM domains WHERE tenant_id=$1 ORDER BY name
	`, tenantID)
	availableDomains := []string{}
	if domainRows != nil {
		for domainRows.Next() {
			var domain string
			if domainRows.Scan(&domain) == nil {
				availableDomains = append(availableDomains, domain)
			}
		}
		domainRows.Close()
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT p.scope, p.domain::text, COALESCE(p.user_id::text,''), COALESCE(u.email::text,''),
		       COALESCE(u.name,''), p.mode, p.email_quota_bytes, p.storage_quota_bytes, p.updated_at
		FROM storage_policies p
		LEFT JOIN users u ON u.id=p.user_id
		WHERE p.tenant_id=$1
		ORDER BY p.scope, lower(p.domain), lower(u.email)
	`, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_POLICY", "Could not load storage policies")
		return
	}
	defer rows.Close()
	domains := []map[string]any{}
	users := []map[string]any{}
	for rows.Next() {
		var scope, domain, userID, email, name, policyMode string
		var emailBytes, storageBytes int64
		var updated time.Time
		if rows.Scan(&scope, &domain, &userID, &email, &name, &policyMode, &emailBytes, &storageBytes, &updated) != nil {
			continue
		}
		entry := storagePolicyEntry(scope, domain, userID, email, name, policyMode, emailBytes, storageBytes, updated)
		if scope == "domain" {
			domains = append(domains, entry)
		} else {
			users = append(users, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":  storagePolicyEntry("tenant", "", "", "", "", mode, emailQuota, storageQuota, updatedAt),
		"domains": domains, "users": users, "availableDomains": availableDomains,
		"defaultMode": mode,
	})
}

func (s *Server) adminPutStorageTenantPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body adminStoragePolicyBody
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	mode := cleanStorageMode(body.Mode)
	emailQuota := cleanStorageQuotaValue(body.EmailQuotaBytes)
	storageQuota := cleanStorageQuotaValue(body.StorageQuotaBytes)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO storage_policy_defaults (tenant_id, mode, email_quota_bytes, storage_quota_bytes, updated_by, updated_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (tenant_id) DO UPDATE
		SET mode=EXCLUDED.mode, email_quota_bytes=EXCLUDED.email_quota_bytes,
		    storage_quota_bytes=EXCLUDED.storage_quota_bytes, updated_by=EXCLUDED.updated_by, updated_at=now()
	`, sess.ActiveTenantID, mode, emailQuota, storageQuota, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_POLICY", "Could not save the organization storage policy")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "storage.policy_updated", "Organization default",
		"Storage mode "+mode, map[string]any{"emailQuotaBytes": emailQuota, "storageQuotaBytes": storageQuota})
	s.applyMailboxAllowance(r.Context(), sess.ActiveTenantID, "tenant", "", "", emailQuota)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminPutStorageDomainPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body adminStoragePolicyBody
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	if domain == "" {
		writeError(w, http.StatusBadRequest, "STORAGE_POLICY_DOMAIN", "Choose a domain")
		return
	}
	var exists bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM domains WHERE tenant_id=$1 AND lower(name::text)=lower($2))`, sess.ActiveTenantID, domain).Scan(&exists)
	if !exists {
		writeError(w, http.StatusNotFound, "STORAGE_POLICY_DOMAIN", "That domain does not belong to this organization")
		return
	}
	mode := cleanStorageMode(body.Mode)
	emailQuota := cleanStorageQuotaValue(body.EmailQuotaBytes)
	storageQuota := cleanStorageQuotaValue(body.StorageQuotaBytes)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO storage_policies (id, tenant_id, scope, domain, mode, email_quota_bytes, storage_quota_bytes, updated_by, updated_at)
		VALUES ($1,$2,'domain',$3,$4,$5,$6,$7,now())
		ON CONFLICT (tenant_id, domain) WHERE scope='domain'
		DO UPDATE SET mode=EXCLUDED.mode, email_quota_bytes=EXCLUDED.email_quota_bytes,
		              storage_quota_bytes=EXCLUDED.storage_quota_bytes, updated_by=EXCLUDED.updated_by, updated_at=now()
	`, newID("storagepolicy"), sess.ActiveTenantID, domain, mode, emailQuota, storageQuota, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_POLICY", "Could not save the domain storage policy")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "storage.domain_policy_updated", domain, "Storage mode "+mode, nil)
	s.applyMailboxAllowance(r.Context(), sess.ActiveTenantID, "domain", domain, "", emailQuota)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminDeleteStorageDomainPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	result, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM storage_policies WHERE tenant_id=$1 AND scope='domain' AND domain=$2
	`, sess.ActiveTenantID, domain)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "STORAGE_POLICY", "Domain override not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "storage.domain_policy_removed", domain, "Removed the domain storage override", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminPutStorageUserPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body adminStoragePolicyBody
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, "STORAGE_POLICY_USER", "Enter a user email address")
		return
	}
	var userID, name string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT id::text, name FROM users
		WHERE tenant_id=$1 AND lower(email)=lower($2) AND deleted_at IS NULL
	`, sess.ActiveTenantID, email).Scan(&userID, &name); err != nil {
		writeError(w, http.StatusNotFound, "STORAGE_POLICY_USER", "No user in this organization has that address")
		return
	}
	mode := cleanStorageMode(body.Mode)
	emailQuota := cleanStorageQuotaValue(body.EmailQuotaBytes)
	storageQuota := cleanStorageQuotaValue(body.StorageQuotaBytes)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO storage_policies (id, tenant_id, scope, user_id, mode, email_quota_bytes, storage_quota_bytes, updated_by, updated_at)
		VALUES ($1,$2,'user',$3,$4,$5,$6,$7,now())
		ON CONFLICT (tenant_id, user_id) WHERE scope='user'
		DO UPDATE SET mode=EXCLUDED.mode, email_quota_bytes=EXCLUDED.email_quota_bytes,
		              storage_quota_bytes=EXCLUDED.storage_quota_bytes, updated_by=EXCLUDED.updated_by, updated_at=now()
	`, newID("storagepolicy"), sess.ActiveTenantID, userID, mode, emailQuota, storageQuota, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_POLICY", "Could not save the user storage policy")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "storage.user_policy_updated", email, "Storage mode "+mode, nil)
	s.applyMailboxAllowance(r.Context(), sess.ActiveTenantID, "user", "", userID, emailQuota)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminDeleteStorageUserPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	result, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM storage_policies WHERE tenant_id=$1 AND scope='user' AND user_id=$2
	`, sess.ActiveTenantID, r.PathValue("id"))
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "STORAGE_POLICY", "User override not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "storage.user_policy_removed", r.PathValue("id"), "Removed a user storage override", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// applyMailboxAllowance mirrors an explicit mail allowance into users.quota_bytes,
// which Dovecot enforces for IMAP, POP3 and submission. A zero value means the
// administrator wants each user's existing mailbox quota left alone. Removing a
// policy does not silently rewrite quotas again; it leaves the last explicit
// allowance in place so nobody gains unlimited storage by accident.
func (s *Server) applyMailboxAllowance(ctx context.Context, tenantID, scope, domain, userID string, emailQuota int64) {
	if emailQuota <= 0 {
		return
	}
	switch scope {
	case "user":
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE users SET quota_bytes=$2, updated_at=now()
			WHERE id=$1 AND tenant_id=$3 AND deleted_at IS NULL
		`, userID, emailQuota, tenantID)
	case "domain":
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE users u SET quota_bytes=$3, updated_at=now()
			WHERE u.tenant_id=$1 AND u.deleted_at IS NULL
			  AND split_part(lower(u.email::text), '@', 2) = lower($2)
			  AND NOT EXISTS (
			    SELECT 1 FROM storage_policies p
			    WHERE p.tenant_id=$1 AND p.scope='user' AND p.user_id=u.id
			  )
		`, tenantID, domain, emailQuota)
	default:
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE users u SET quota_bytes=$2, updated_at=now()
			WHERE u.tenant_id=$1 AND u.deleted_at IS NULL
			  AND NOT EXISTS (
			    SELECT 1 FROM storage_policies p
			    WHERE p.tenant_id=$1 AND p.scope='user' AND p.user_id=u.id
			  )
			  AND NOT EXISTS (
			    SELECT 1 FROM storage_policies p
			    WHERE p.tenant_id=$1 AND p.scope='domain'
			      AND p.domain = split_part(lower(u.email::text), '@', 2)
			  )
		`, tenantID, emailQuota)
	}
}
