package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type encryptionMigrationIdentity struct {
	ID          string `json:"id"`
	UserID      string `json:"userId"`
	UserEmail   string `json:"userEmail"`
	Email       string `json:"email"`
	Protocol    string `json:"protocol"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Exported    bool   `json:"exported"`
}

type encryptionMigrationReport struct {
	FromMode             string                        `json:"fromMode"`
	ToMode               string                        `json:"toMode"`
	Protocol             string                        `json:"protocol"`
	Users                int                           `json:"users"`
	PrivateKeyCount      int                           `json:"privateKeyCount"`
	ExportedCount        int                           `json:"exportedCount"`
	PrivateKeyIdentities []encryptionMigrationIdentity `json:"privateKeyIdentities"`
	EncryptedMessages    int64                         `json:"encryptedMessages"`
	ActiveLegalHolds     int                           `json:"activeLegalHolds"`
	RetentionDays        int                           `json:"retentionDays"`
	DelegatedDecrypt     bool                          `json:"delegatedDecrypt"`
	CanMigrate           bool                          `json:"canMigrate"`
	Blockers             []string                      `json:"blockers"`
	Warnings             []string                      `json:"warnings"`
}

type encryptionExportableIdentity struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Protocol    string     `json:"protocol"`
	Fingerprint string     `json:"fingerprint"`
	Status      string     `json:"status"`
	Subject     string     `json:"subject,omitempty"`
	Issuer      string     `json:"issuer,omitempty"`
	NotAfter    *time.Time `json:"notAfter,omitempty"`
	ExportedAt  *time.Time `json:"exportedAt,omitempty"`
}

func (s *Server) listExportableCryptoIdentities(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if policy.Mode != "server_managed" {
		writeError(w, http.StatusConflict, "ENCRYPTION_MODE", "Private-key export is available only while the organization uses server-managed encryption")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT i.id, i.email::text, i.protocol, i.fingerprint, i.status,
		       i.subject, i.issuer, i.not_after, e.exported_at
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		LEFT JOIN user_crypto_key_exports e
		       ON e.user_id=i.user_id AND e.protocol=i.protocol AND e.fingerprint=i.fingerprint
		WHERE i.user_id=$1 AND i.tenant_id=$2 AND i.status IN ('active','retired')
		ORDER BY CASE i.status WHEN 'active' THEN 0 ELSE 1 END, i.created_at DESC
	`, sess.User.ID, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_EXPORT", "Could not list exportable keys")
		return
	}
	defer rows.Close()
	items := []encryptionExportableIdentity{}
	for rows.Next() {
		var item encryptionExportableIdentity
		if rows.Scan(&item.ID, &item.Email, &item.Protocol, &item.Fingerprint, &item.Status,
			&item.Subject, &item.Issuer, &item.NotAfter, &item.ExportedAt) == nil {
			items = append(items, item)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"identities": items})
}

func (s *Server) exportCryptoIdentity(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if policy.Mode != "server_managed" {
		writeError(w, http.StatusConflict, "ENCRYPTION_MODE", "Private-key export is available only while the organization uses server-managed encryption")
		return
	}
	var body struct {
		Password       string `json:"password"`
		ExportPassword string `json:"exportPassword"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Password == "" || !auth.VerifyPassword(sess.User.PasswordHash, body.Password) {
		writeError(w, http.StatusForbidden, "REAUTH_REQUIRED", "Enter your current sign-in password to export a private key")
		return
	}
	identityID := r.PathValue("id")
	var protocol, email, fingerprint, certPEM, chainPEM, wrapped string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT i.protocol, i.email::text, i.fingerprint, i.public_key, i.cert_chain, k.wrapped_key
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		WHERE i.id=$1 AND i.user_id=$2 AND i.tenant_id=$3 AND i.status IN ('active','retired')
	`, identityID, sess.User.ID, sess.User.TenantID).Scan(&protocol, &email, &fingerprint, &certPEM, &chainPEM, &wrapped)
	if err != nil {
		writeError(w, http.StatusNotFound, "ENCRYPTION_EXPORT", "That private key is not available for export")
		return
	}
	vault, err := s.encryptionVault()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_EXPORT", "The key vault is not configured")
		return
	}
	privateKey, err := vault.open(sess.User.TenantID, sess.User.ID, identityID, wrapped)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_EXPORT", "The private key could not be opened")
		return
	}
	response := map[string]any{
		"id": identityID, "email": email, "protocol": protocol, "fingerprint": fingerprint,
	}
	switch protocol {
	case "openpgp":
		response["privateKey"] = privateKey
		response["publicKey"] = certPEM
	case "smime":
		if len(strings.TrimSpace(body.ExportPassword)) < 8 {
			writeError(w, http.StatusBadRequest, "EXPORT_PASSPHRASE", "Choose an export passphrase of at least 8 characters for the PKCS#12 bundle")
			return
		}
		identity, err := mailcrypto.ParseSMIMEIdentity([]byte(certPEM), []byte(privateKey), []byte(chainPEM), nil)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "ENCRYPTION_EXPORT", "The S/MIME identity could not be prepared for export")
			return
		}
		bundle, err := mailcrypto.EncodePKCS12Identity(identity, body.ExportPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ENCRYPTION_EXPORT", "The PKCS#12 bundle could not be created")
			return
		}
		response["pkcs12Base64"] = base64.StdEncoding.EncodeToString(bundle)
	default:
		writeError(w, http.StatusBadRequest, "ENCRYPTION_EXPORT", "Unsupported key protocol")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO user_crypto_key_exports (user_id, tenant_id, protocol, fingerprint, exported_at)
		VALUES ($1,$2,$3,$4,now())
		ON CONFLICT (user_id, protocol, fingerprint) DO UPDATE SET exported_at=now()
	`, sess.User.ID, sess.User.TenantID, protocol, fingerprint); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_EXPORT", "The export could not be recorded")
		return
	}
	s.audit(r, sess.User, "encryption.private_key_exported", email,
		"Exported a server-managed "+strings.ToUpper(protocol)+" private key · "+fingerprint)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) encryptionMigrationReport(ctx context.Context, tenantID string, policy adminTenantPolicy) (encryptionMigrationReport, error) {
	report := encryptionMigrationReport{
		FromMode:             policy.Encryption.Mode,
		ToMode:               "client_side",
		Protocol:             policy.Encryption.Protocol,
		PrivateKeyIdentities: []encryptionMigrationIdentity{},
		Blockers:             []string{}, Warnings: []string{},
	}
	if err := s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM users WHERE tenant_id=$1 AND deleted_at IS NULL
	`, tenantID).Scan(&report.Users); err != nil {
		return report, err
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT i.id, i.user_id, u.email::text, i.email::text, i.protocol, i.fingerprint, i.status,
		       EXISTS(
		         SELECT 1 FROM user_crypto_key_exports e
		         WHERE e.user_id=i.user_id AND e.protocol=i.protocol AND e.fingerprint=i.fingerprint
		       ) AS exported
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		JOIN users u ON u.id=i.user_id
		WHERE i.tenant_id=$1 AND i.status IN ('active','retired') AND u.deleted_at IS NULL
		ORDER BY u.email, i.protocol, i.created_at
	`, tenantID)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var item encryptionMigrationIdentity
		if rows.Scan(&item.ID, &item.UserID, &item.UserEmail, &item.Email, &item.Protocol,
			&item.Fingerprint, &item.Status, &item.Exported) != nil {
			continue
		}
		report.PrivateKeyIdentities = append(report.PrivateKeyIdentities, item)
		report.PrivateKeyCount++
		if item.Exported {
			report.ExportedCount++
		}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	if err := s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM mail_messages WHERE tenant_id=$1 AND crypto_encrypted=true
	`, tenantID).Scan(&report.EncryptedMessages); err != nil {
		return report, err
	}
	if err := s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM mail_legal_holds WHERE tenant_id=$1 AND status='active'
	`, tenantID).Scan(&report.ActiveLegalHolds); err != nil && !strings.Contains(strings.ToLower(err.Error()), "does not exist") {
		return report, err
	}
	report.RetentionDays = policy.Mail.RetentionDays
	report.DelegatedDecrypt = policy.Encryption.AllowDelegatedDecrypt
	if report.PrivateKeyCount > report.ExportedCount {
		report.Blockers = append(report.Blockers,
			"Every server-held private key must be exported before the switch. Ask affected users to export each key from Security settings.")
	}
	if report.PrivateKeyCount == 0 {
		report.Warnings = append(report.Warnings, "No server-held private keys exist; the switch removes no key material.")
	}
	if report.EncryptedMessages > 0 {
		report.Warnings = append(report.Warnings,
			"Existing encrypted messages stay readable only with the exported keys. The server will no longer decrypt them after the switch.")
	}
	if report.ActiveLegalHolds > 0 {
		report.Warnings = append(report.Warnings,
			"Active legal holds exist. Review them before removing server decryption because eDiscovery may no longer open historic encrypted mail.")
	}
	if report.RetentionDays > 0 {
		report.Warnings = append(report.Warnings,
			"Message retention is enabled. Retention jobs cannot inspect encrypted bodies after the switch.")
	}
	report.CanMigrate = len(report.Blockers) == 0
	return report, nil
}

func writeMigrationError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "details": details,
	}})
}

func (s *Server) adminEncryptionMigrationReport(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var settings json.RawMessage
	if err := s.store.DB.QueryRow(r.Context(), `SELECT settings FROM tenants WHERE id=$1`, sess.ActiveTenantID).Scan(&settings); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION_PREFLIGHT", "Could not read the organization policy")
		return
	}
	policy := tenantPolicyFromSettings(settings)
	report, err := s.encryptionMigrationReport(r.Context(), sess.ActiveTenantID, policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION_PREFLIGHT", "Could not build the migration report")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

func (s *Server) adminEncryptionMigration(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Password             string `json:"password"`
		Confirm              string `json:"confirm"`
		AcknowledgeLegalHold bool   `json:"acknowledgeLegalHold"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Password == "" || !auth.VerifyPassword(sess.User.PasswordHash, body.Password) {
		writeError(w, http.StatusForbidden, "REAUTH_REQUIRED", "Enter your current administrator password to run the migration")
		return
	}
	if strings.TrimSpace(strings.ToLower(body.Confirm)) != "migrate to client-side" {
		writeError(w, http.StatusBadRequest, "MIGRATION_CONFIRM", `Type "migrate to client-side" exactly to confirm`)
		return
	}
	var settingsSnapshot json.RawMessage
	if err := s.store.DB.QueryRow(r.Context(), `SELECT settings FROM tenants WHERE id=$1`, sess.ActiveTenantID).Scan(&settingsSnapshot); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not read the organization policy")
		return
	}
	policy := tenantPolicyFromSettings(settingsSnapshot)
	if policy.Encryption.Mode != "server_managed" {
		writeError(w, http.StatusConflict, "MIGRATION_MODE", "The organization is not using server-managed encryption")
		return
	}
	report, err := s.encryptionMigrationReport(r.Context(), sess.ActiveTenantID, policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION_PREFLIGHT", "Could not build the migration report")
		return
	}
	if len(report.Blockers) > 0 {
		writeMigrationError(w, http.StatusConflict, "MIGRATION_EXPORTS_REQUIRED", report.Blockers[0], map[string]any{"report": report})
		return
	}
	if report.ActiveLegalHolds > 0 && !body.AcknowledgeLegalHold {
		writeMigrationError(w, http.StatusConflict, "MIGRATION_LEGAL_HOLD",
			"Active legal holds must be acknowledged before server decryption is removed",
			map[string]any{"report": report})
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not start the migration")
		return
	}
	defer tx.Rollback(r.Context())
	var rawSettings json.RawMessage
	if err := tx.QueryRow(r.Context(), `SELECT settings FROM tenants WHERE id=$1 FOR UPDATE`, sess.ActiveTenantID).Scan(&rawSettings); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not lock the organization policy")
		return
	}
	// Recheck under the tenant lock: a key generated after the preflight must
	// not be deleted without its own export.
	var unexported int
	if err := tx.QueryRow(r.Context(), `
		SELECT count(*)
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		JOIN users u ON u.id=i.user_id AND u.deleted_at IS NULL
		WHERE i.tenant_id=$1 AND i.status IN ('active','retired')
		  AND NOT EXISTS (
		    SELECT 1 FROM user_crypto_key_exports e
		    WHERE e.user_id=i.user_id AND e.protocol=i.protocol AND e.fingerprint=i.fingerprint
		  )
	`, sess.ActiveTenantID).Scan(&unexported); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not recheck key exports")
		return
	}
	if unexported > 0 {
		writeMigrationError(w, http.StatusConflict, "MIGRATION_EXPORTS_REQUIRED",
			"A private key was created after the preflight and has not been exported",
			map[string]any{"unexported": unexported})
		return
	}
	deleted, err := tx.Exec(r.Context(), `
		DELETE FROM user_crypto_private_keys k
		USING user_crypto_identities i
		WHERE k.identity_id=i.id AND i.tenant_id=$1
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not remove the server-held private keys")
		return
	}
	var raced int
	if err := tx.QueryRow(r.Context(), `
		SELECT count(*)
		FROM user_crypto_private_keys k
		JOIN user_crypto_identities i ON i.id=k.identity_id
		JOIN users u ON u.id=i.user_id AND u.deleted_at IS NULL
		WHERE i.tenant_id=$1
	`, sess.ActiveTenantID).Scan(&raced); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not verify the private-key removal")
		return
	}
	if raced > 0 {
		writeMigrationError(w, http.StatusConflict, "MIGRATION_EXPORTS_REQUIRED",
			"A private key was created while the migration was running and has not been exported",
			map[string]any{"unexported": raced})
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='retired', updated_at=now()
		WHERE tenant_id=$1 AND status='active' AND source <> 'client'
	`, sess.ActiveTenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not retire the server-managed identities")
		return
	}
	settings := map[string]any{}
	_ = json.Unmarshal(rawSettings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}
	encryption, _ := settings["encryption"].(map[string]any)
	if encryption == nil {
		encryption = map[string]any{}
	}
	encryption["mode"] = "client_side"
	encryption["allowDelegatedDecrypt"] = false
	settings["encryption"] = encryption
	updatedSettings, _ := json.Marshal(settings)
	if _, err := tx.Exec(r.Context(), `UPDATE tenants SET settings=$2, updated_at=now() WHERE id=$1`, sess.ActiveTenantID, updatedSettings); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not switch the encryption mode")
		return
	}
	deletedCount := deleted.RowsAffected()
	reportBytes, _ := json.Marshal(report)
	migrationID := newID("encmig")
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO tenant_encryption_migrations
			(id, tenant_id, from_mode, to_mode, status, report, private_keys_deleted, actor_user_id, completed_at)
		VALUES ($1,$2,'server_managed','client_side','completed',$3,$4,$5,now())
	`, migrationID, sess.ActiveTenantID, reportBytes, deletedCount, sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not record the migration")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "MIGRATION", "Could not commit the migration")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.encryption_migrated", "Organization encryption",
		"Switched from server-managed to client-side encryption and removed server-held private keys",
		map[string]any{"tenantId": sess.ActiveTenantID, "deletedPrivateKeys": deletedCount, "migrationId": migrationID})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "migrationId": migrationID, "deletedPrivateKeys": deletedCount, "report": report,
	})
}
