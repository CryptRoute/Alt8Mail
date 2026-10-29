package httpapi

import (
	"net/http"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/auth"
)

func (s *Server) adminSecurityOverview(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	security, methods, passkeys, remaining := s.securityState(r.Context(), sess.User.userRecord())
	var activeSessions int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM admin_sessions WHERE user_id = $1 AND expires_at > now()`, sess.User.ID).Scan(&activeSessions)
	var trusted int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM trusted_devices WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()`, sess.User.ID).Scan(&trusted)
	writeJSON(w, http.StatusOK, map[string]any{
		"security": security, "methods": methods, "passkeys": passkeys,
		"recoveryRemaining": remaining, "adminSessions": activeSessions, "trustedDevices": trusted,
	})
}

func (s *Server) adminBeginTOTP(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not start authenticator setup")
		return
	}
	key := "admin-totp-enroll:" + sess.User.ID
	if err := s.store.Redis.Set(r.Context(), key, secret, 10*time.Minute).Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not store authenticator setup state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret, "issuer": "Meridian Admin", "account": sess.User.Email,
		"otpauth":   auth.TOTPURI(secret, sess.User.Email, "Meridian Admin"),
		"expiresAt": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
	})
}

func (s *Server) adminFinishTOTP(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	key := "admin-totp-enroll:" + sess.User.ID
	secret, err := s.store.Redis.Get(r.Context(), key).Result()
	if err != nil || secret == "" {
		writeError(w, http.StatusGone, "TOTP_EXPIRED", "Authenticator setup expired. Start again.")
		return
	}
	if !auth.ValidateTOTP(secret, body.Code, time.Now(), 1) {
		writeError(w, http.StatusUnauthorized, "TOTP_INVALID", "That authenticator code did not match")
		return
	}
	if err := s.enableTOTP(r.Context(), sess.User.ID, secret); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not save authenticator settings")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM trusted_devices WHERE user_id = $1`, sess.User.ID)
	codes, err := s.replaceRecoveryCodes(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RECOVERY", "Authenticator saved, but recovery codes could not be generated")
		return
	}
	_ = s.store.Redis.Del(r.Context(), key).Err()
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.totp_enabled", "Admin authenticator", "Enabled TOTP for the admin console", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "codes": codes, "remaining": len(codes)})
}

func (s *Server) adminListAdminSessions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, COALESCE(host(ip), ''), user_agent, created_at, last_seen_at, expires_at, active_tenant_id::text
		FROM admin_sessions WHERE user_id = $1 AND expires_at > now()
		ORDER BY (id = $2) DESC, last_seen_at DESC
	`, sess.User.ID, sess.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSIONS", "Could not load admin sessions")
		return
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, ip, userAgent, tenantID string
		var created, lastSeen, expires time.Time
		if rows.Scan(&id, &ip, &userAgent, &created, &lastSeen, &expires, &tenantID) == nil {
			result = append(result, map[string]any{
				"id": id, "current": id == sess.ID, "ip": ip, "device": describeSessionDevice(userAgent),
				"tenantId": tenantID, "createdAt": formatAdminTime(created),
				"lastSeenAt": formatAdminTime(lastSeen), "expiresAt": formatAdminTime(expires),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": result, "count": len(result)})
}

func (s *Server) adminRevokeOtherAdminSessions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tag, err := s.store.DB.Exec(r.Context(), `DELETE FROM admin_sessions WHERE user_id = $1 AND id <> $2`, sess.User.ID, sess.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSIONS", "Could not revoke other admin sessions")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.sessions_revoked", "Admin sessions", "Revoked all other admin console sessions", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": tag.RowsAffected()})
}

func (s *Server) adminDisableTOTP(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Password == "" || !auth.VerifyPassword(sess.User.PasswordHash, body.Password) {
		writeError(w, http.StatusUnauthorized, "BAD_PASSWORD", "Enter your current password")
		return
	}
	secret, err := s.decryptTOTPSecret(sess.User.userRecord())
	if err != nil || secret == "" {
		writeError(w, http.StatusConflict, "TOTP_DISABLED", "No authenticator is enabled")
		return
	}
	if !auth.ValidateTOTP(secret, body.Code, time.Now(), 1) && !s.consumeRecoveryCode(r.Context(), sess.User.ID, body.Code) {
		writeError(w, http.StatusUnauthorized, "TOTP_INVALID", "Enter a valid authenticator or recovery code")
		return
	}
	if err := s.disableTOTP(r.Context(), sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not disable the authenticator")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM recovery_codes WHERE user_id = $1`, sess.User.ID)
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM trusted_devices WHERE user_id = $1`, sess.User.ID)
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_auth_challenges WHERE user_id = $1`, sess.User.ID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.totp_disabled", "Admin authenticator", "Disabled TOTP for the admin console", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
