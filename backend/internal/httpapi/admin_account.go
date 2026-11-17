package httpapi

import (
	"net/http"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
)

func (s *Server) adminChangePassword(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.CurrentPassword == "" || !auth.VerifyPassword(sess.User.PasswordHash, body.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "BAD_PASSWORD", "Your current password is incorrect")
		return
	}
	if body.NewPassword == "" || body.NewPassword == body.CurrentPassword {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Choose a new password that is different from the current one")
		return
	}
	policy := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	if problems := validatePasswordAgainstPolicy(body.NewPassword, policy.Security); len(problems) > 0 {
		writeError(w, http.StatusBadRequest, "PASSWORD_POLICY", strings.Join(problems, "; "))
		return
	}
	user := sess.User.userRecord()
	if s.passwordReused(r.Context(), user, body.NewPassword, policy.Security.PasswordHistory) {
		writeError(w, http.StatusBadRequest, "PASSWORD_HISTORY", "That password was used recently. Choose a different one.")
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not protect the new password")
		return
	}
	var mailHash any
	if s.mailAccountConfigured(sess.User.Email) {
		hashed, err := hashMailPassword(body.NewPassword)
		if err != nil {
			writeError(w, http.StatusBadGateway, "MAIL_PASSWORD", "Could not synchronize the mailbox password")
			return
		}
		mailHash = hashed
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not change the password")
		return
	}
	defer tx.Rollback(r.Context())
	if policy.Security.PasswordHistory > 0 && sess.User.PasswordHash != "" {
		if _, err := tx.Exec(r.Context(), `INSERT INTO password_history (user_id, password_hash) VALUES ($1,$2)`, sess.User.ID, sess.User.PasswordHash); err != nil {
			writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not record password history")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE users SET password_hash=$2, mail_password_hash=COALESCE($3, mail_password_hash),
		       password_changed_at=now(), force_password_change=false, updated_at=now()
		WHERE id=$1
	`, sess.User.ID, hash, mailHash); err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not save the new password")
		return
	}
	if policy.Security.PasswordHistory > 0 {
		_, _ = tx.Exec(r.Context(), `
			DELETE FROM password_history WHERE user_id=$1 AND id NOT IN (
				SELECT id FROM password_history WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2
			)
		`, sess.User.ID, policy.Security.PasswordHistory)
	} else {
		_, _ = tx.Exec(r.Context(), `DELETE FROM password_history WHERE user_id=$1`, sess.User.ID)
	}
	// Keep this admin session, revoke every other console session and all
	// webmail sessions so a stolen cookie cannot outlive the password change.
	var revoked int64
	if tag, err := tx.Exec(r.Context(), `DELETE FROM admin_sessions WHERE user_id=$1 AND id<>$2`, sess.User.ID, sess.ID); err == nil {
		revoked = tag.RowsAffected()
	}
	_, _ = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, sess.User.ID)
	_, _ = tx.Exec(r.Context(), `DELETE FROM trusted_devices WHERE user_id=$1`, sess.User.ID)
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not commit the password change")
		return
	}
	s.clearDAVAuthCache(sess.User.Email)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.password_changed", sess.User.Email,
		"Changed the account password from the admin console", map[string]any{"revokedAdminSessions": revoked})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revokedAdminSessions": revoked})
}
