package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
)

const (
	sessionCookieName    = "alt8mail_control_sid"
	csrfCookieName       = "alt8mail_control_csrf"
	sessionIdleRecheck   = 60 * time.Second
	challengeTTL         = 5 * time.Minute
	maxChallengeAttempts = 5
	loginWindow          = 15 * time.Minute
	maxLoginFailures     = 5
)

func (s *Server) loadAdminByEmail(ctx context.Context, email string) (*adminUser, error) {
	return s.scanAdmin(ctx, `
		SELECT id::text, email::text, name, COALESCE(password_hash,''), role, status,
		       COALESCE(totp_secret,''), COALESCE(totp_pending_secret,''), totp_enabled,
		       session_ttl_seconds, failed_attempts, failed_window_start, locked_until,
		       last_login_at, created_at
		FROM control.admin_users WHERE email = $1
	`, strings.ToLower(strings.TrimSpace(email)))
}

func (s *Server) loadAdminByID(ctx context.Context, id string) (*adminUser, error) {
	return s.scanAdmin(ctx, `
		SELECT id::text, email::text, name, COALESCE(password_hash,''), role, status,
		       COALESCE(totp_secret,''), COALESCE(totp_pending_secret,''), totp_enabled,
		       session_ttl_seconds, failed_attempts, failed_window_start, locked_until,
		       last_login_at, created_at
		FROM control.admin_users WHERE id = $1
	`, id)
}

func (s *Server) scanAdmin(ctx context.Context, query string, arg any) (*adminUser, error) {
	var user adminUser
	err := s.db.QueryRow(ctx, query, arg).Scan(
		&user.ID, &user.Email, &user.Name, &user.PasswordHash, &user.Role, &user.Status,
		&user.TOTPSecret, &user.TOTPPending, &user.TOTPEnabled, &user.SessionTTL,
		&user.FailedAttempts, &user.FailedWindow, &user.LockedUntil,
		&user.LastLoginAt, &user.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Server) hydratePermissions(ctx context.Context, user *adminUser) error {
	effective := append([]string{}, rolePermissions(user.Role)...)
	rows, err := s.db.Query(ctx, `
		SELECT g.name, g.permissions
		FROM control.group_members gm
		JOIN control.groups g ON g.id = gm.group_id
		WHERE gm.user_id = $1 ORDER BY lower(g.name)
	`, user.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var permissions []string
		if err := rows.Scan(&name, &permissions); err != nil {
			return err
		}
		user.Groups = append(user.Groups, name)
		effective = append(effective, permissions...)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	effective = uniqueStrings(effective)
	sort.Strings(effective)
	user.Permissions = effective
	user.PermSet = permissionMap(effective)
	return nil
}

func (s *Server) authenticateSession(r *http.Request) (*sessionRecord, error) {
	raw := cookie(r, sessionCookieName)
	if raw == "" {
		return nil, errors.New("no session")
	}
	id := hashToken(raw)
	var sess sessionRecord
	var user adminUser
	err := s.db.QueryRow(r.Context(), `
		SELECT s.id, s.csrf_token, s.ip::text, s.user_agent, s.created_at, s.last_seen_at, s.expires_at,
		       u.id::text, u.email::text, u.name, COALESCE(u.password_hash,''), u.role, u.status,
		       COALESCE(u.totp_secret,''), COALESCE(u.totp_pending_secret,''), u.totp_enabled,
		       u.session_ttl_seconds, u.failed_attempts, u.failed_window_start, u.locked_until,
		       u.last_login_at, u.created_at
		FROM control.sessions s
		JOIN control.admin_users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now() AND u.status = 'active'
	`, id).Scan(
		&sess.ID, &sess.CSRF, &sess.IP, &sess.UserAgent, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt,
		&user.ID, &user.Email, &user.Name, &user.PasswordHash, &user.Role, &user.Status,
		&user.TOTPSecret, &user.TOTPPending, &user.TOTPEnabled, &user.SessionTTL,
		&user.FailedAttempts, &user.FailedWindow, &user.LockedUntil, &user.LastLoginAt, &user.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := s.hydratePermissions(r.Context(), &user); err != nil {
		return nil, err
	}
	sess.User = user
	if time.Since(sess.LastSeenAt) > sessionIdleRecheck {
		_, _ = s.db.Exec(r.Context(), `UPDATE control.sessions SET last_seen_at = now() WHERE id = $1`, id)
	}
	return &sess, nil
}

func (s *Server) issueSession(ctx context.Context, r *http.Request, w http.ResponseWriter, user *adminUser) (*sessionRecord, string, error) {
	raw, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	ttl := time.Duration(user.SessionTTL) * time.Second
	if ttl < 15*time.Minute {
		ttl = 8 * time.Hour
	}
	expires := time.Now().Add(ttl)
	ip := s.requestIP(r)
	ua := userAgent(r)
	_, err = s.db.Exec(ctx, `
		INSERT INTO control.sessions (id, user_id, csrf_token, ip, user_agent, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, hashToken(raw), user.ID, csrf, nullableIP(ip), ua, expires)
	if err != nil {
		return nil, "", err
	}
	setCookie(w, sessionCookieName, raw, int(ttl.Seconds()), true)
	setCookie(w, csrfCookieName, s.signCSRF(csrf), int(ttl.Seconds()), false)
	sess := &sessionRecord{
		ID: hashToken(raw), User: *user, CSRF: csrf, IP: ip, UserAgent: ua,
		CreatedAt: time.Now(), LastSeenAt: time.Now(), ExpiresAt: expires,
	}
	return sess, raw, nil
}

func sessionView(sess *sessionRecord) map[string]any {
	return map[string]any{
		"id": sess.ID, "ip": sess.IP.String(), "userAgent": sess.UserAgent,
		"createdAt":  sess.CreatedAt.UTC().Format(time.RFC3339),
		"lastSeenAt": sess.LastSeenAt.UTC().Format(time.RFC3339),
		"expiresAt":  sess.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	csrfValue := s.ensureCSRFCookie(w, r)
	sess, err := s.authenticateSession(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false, "csrfToken": csrfValue, "version": s.version,
			"service": "alt8mail-control", "env": s.cfg.Env, "serverTime": nowRFC3339(),
		})
		return
	}
	// Refresh the CSRF cookie if it was lost while the session remained valid.
	if cookie(r, csrfCookieName) == "" {
		http.SetCookie(w, &http.Cookie{
			Name: csrfCookieName, Value: s.signCSRF(sess.CSRF), Path: "/", Secure: true,
			SameSite: http.SameSiteStrictMode, HttpOnly: false,
			Expires: sess.ExpiresAt,
		})
		csrfValue = s.signCSRF(sess.CSRF)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true, "csrfToken": csrfValue, "version": s.version,
		"service": "alt8mail-control", "env": s.cfg.Env, "serverTime": nowRFC3339(),
		"user": sessionUserPayload(&sess.User), "session": sessionView(sess),
	})
}

func sessionUserPayload(user *adminUser) map[string]any {
	return map[string]any{
		"id": user.ID, "email": user.Email, "name": user.Name, "role": user.Role,
		"status": user.Status, "totpEnabled": user.TOTPEnabled,
		"sessionTtlSeconds": user.SessionTTL, "permissions": user.Permissions,
		"groups": user.Groups, "lastLoginAt": user.LastLoginAt,
		"createdAt":   user.CreatedAt.UTC().Format(time.RFC3339),
		"lockedUntil": user.LockedUntil,
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.validateCSRF(r, nil) {
		writeError(w, http.StatusForbidden, "CSRF", "Reload the sign-in form and try again")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "CREDENTIALS", "Email and password are required")
		return
	}
	user, err := s.loadAdminByEmail(r.Context(), body.Email)
	if err != nil {
		// Keep an unknown-user path expensive enough to avoid a trivial oracle.
		_ = auth.VerifyPassword(s.dummyHash, body.Password)
		s.audit(r.Context(), nil, "auth.login_failed", body.Email, "Unknown panel account", s.requestIP(r), userAgent(r), nil)
		writeError(w, http.StatusUnauthorized, "CREDENTIALS", "That email and password do not match a System Control account")
		return
	}
	if user.Status != "active" {
		writeError(w, http.StatusForbidden, "DISABLED", "This panel account is disabled")
		return
	}
	if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
		writeError(w, http.StatusTooManyRequests, "LOCKED", "Too many failed sign-ins. This account is temporarily locked.")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, body.Password) {
		s.recordLoginFailure(r.Context(), user, r)
		writeError(w, http.StatusUnauthorized, "CREDENTIALS", "That email and password do not match a System Control account")
		return
	}
	if user.TOTPEnabled {
		challenge, err := s.createAuthChallenge(r.Context(), user, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "CHALLENGE", "Could not start two-factor verification")
			return
		}
		s.audit(r.Context(), user, "auth.password_ok", user.Email, "Password verified; TOTP required", s.requestIP(r), userAgent(r), nil)
		writeJSON(w, http.StatusOK, map[string]any{"totpRequired": true, "challengeId": challenge})
		return
	}
	if err := s.hydratePermissions(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "PERMISSIONS", "Could not load panel permissions")
		return
	}
	sess, _, err := s.issueSession(r.Context(), r, w, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION", "Could not create a panel session")
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE control.admin_users SET failed_attempts=0, failed_window_start=NULL, locked_until=NULL, last_login_at=now() WHERE id=$1`, user.ID)
	s.audit(r.Context(), user, "auth.login", user.Email, "Signed in to System Control", s.requestIP(r), userAgent(r), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true, "csrfToken": s.signCSRF(sess.CSRF),
		"version": s.version, "user": sessionUserPayload(user), "session": sessionView(sess),
	})
}

func (s *Server) recordLoginFailure(ctx context.Context, user *adminUser, r *http.Request) {
	now := time.Now()
	windowStart := now
	attempts := 1
	if user.FailedWindow != nil && now.Sub(*user.FailedWindow) < loginWindow {
		windowStart = *user.FailedWindow
		attempts = user.FailedAttempts + 1
	}
	var lockedUntil any
	if attempts >= maxLoginFailures {
		value := now.Add(loginWindow)
		lockedUntil = value
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.admin_users
		SET failed_attempts=$2, failed_window_start=$3, locked_until=$4, updated_at=now()
		WHERE id=$1
	`, user.ID, attempts, windowStart, lockedUntil)
	s.audit(ctx, user, "auth.login_failed", user.Email, "Panel password rejected", s.requestIP(r), userAgent(r),
		map[string]any{"attempts": attempts, "locked": lockedUntil != nil})
	s.notifyEvent(ctx, "auth.login_failed", map[string]any{"email": user.Email, "attempts": attempts, "ip": s.requestIP(r).String()})
	if attempts >= maxLoginFailures {
		s.audit(ctx, user, "auth.lockout", user.Email, "Account locked after repeated failed sign-ins", s.requestIP(r), userAgent(r), nil)
		s.notifyEvent(ctx, "auth.lockout", map[string]any{
			"email": user.Email, "attempts": attempts, "ip": s.requestIP(r).String(),
		})
	}
}

func (s *Server) createAuthChallenge(ctx context.Context, user *adminUser, r *http.Request) (string, error) {
	raw, err := randomToken(24)
	if err != nil {
		return "", err
	}
	ip := s.requestIP(r)
	_, err = s.db.Exec(ctx, `
		INSERT INTO control.auth_challenges (id, user_id, ip, user_agent, expires_at)
		VALUES ($1,$2,$3,$4,$5)
	`, hashToken(raw), user.ID, nullableIP(ip), userAgent(r), time.Now().Add(challengeTTL))
	if err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	if !s.validateCSRF(r, nil) {
		writeError(w, http.StatusForbidden, "CSRF", "Reload the sign-in form and try again")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.ChallengeID = strings.TrimSpace(body.ChallengeID)
	body.Code = strings.TrimSpace(body.Code)
	if body.ChallengeID == "" || body.Code == "" {
		writeError(w, http.StatusBadRequest, "TOTP", "Enter the six-digit authenticator code")
		return
	}
	var userID string
	var attempts int
	var expiresAt time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT user_id::text, attempts, expires_at FROM control.auth_challenges
		WHERE id=$1 AND expires_at > now()
	`, hashToken(body.ChallengeID)).Scan(&userID, &attempts, &expiresAt)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "CHALLENGE", "That verification challenge has expired. Sign in again.")
		return
	}
	if attempts >= maxChallengeAttempts {
		_, _ = s.db.Exec(r.Context(), `DELETE FROM control.auth_challenges WHERE id=$1`, hashToken(body.ChallengeID))
		writeError(w, http.StatusTooManyRequests, "TOTP_ATTEMPTS", "Too many incorrect codes. Sign in again.")
		return
	}
	user, err := s.loadAdminByID(r.Context(), userID)
	if err != nil || user.Status != "active" {
		writeError(w, http.StatusUnauthorized, "ACCOUNT", "This panel account is unavailable")
		return
	}
	secret, err := s.secrets.Open(user.TOTPSecret)
	if err != nil || !auth.ValidateTOTP(secret, body.Code, time.Now(), 1) {
		_, _ = s.db.Exec(r.Context(), `UPDATE control.auth_challenges SET attempts=attempts+1 WHERE id=$1`, hashToken(body.ChallengeID))
		s.audit(r.Context(), user, "auth.totp_failed", user.Email, "Incorrect TOTP challenge code", s.requestIP(r), userAgent(r), nil)
		if attempts+1 >= maxChallengeAttempts {
			_, _ = s.db.Exec(r.Context(), `DELETE FROM control.auth_challenges WHERE id=$1`, hashToken(body.ChallengeID))
			writeError(w, http.StatusTooManyRequests, "TOTP_ATTEMPTS", "Too many incorrect codes. Sign in again.")
			return
		}
		writeError(w, http.StatusUnauthorized, "TOTP", "That authenticator code did not match")
		return
	}
	_, _ = s.db.Exec(r.Context(), `DELETE FROM control.auth_challenges WHERE id=$1`, hashToken(body.ChallengeID))
	if err := s.hydratePermissions(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "PERMISSIONS", "Could not load panel permissions")
		return
	}
	sess, _, err := s.issueSession(r.Context(), r, w, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION", "Could not create a panel session")
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE control.admin_users SET failed_attempts=0, failed_window_start=NULL, locked_until=NULL, last_login_at=now() WHERE id=$1`, user.ID)
	s.audit(r.Context(), user, "auth.login", user.Email, "Signed in with TOTP", s.requestIP(r), userAgent(r), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true, "csrfToken": s.signCSRF(sess.CSRF), "version": s.version,
		"user": sessionUserPayload(user), "session": sessionView(sess),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	_, _ = s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE id=$1`, sess.ID)
	clearCookie(w, sessionCookieName, true)
	clearCookie(w, csrfCookieName, false)
	s.actorAudit(r, sess, "auth.logout", sess.User.Email, "Signed out of System Control", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id, ip::text, user_agent, created_at, last_seen_at, expires_at
		FROM control.sessions WHERE user_id=$1 AND expires_at > now()
		ORDER BY last_seen_at DESC
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, ip, ua string
		var created, seen, expires time.Time
		if rows.Scan(&id, &ip, &ua, &created, &seen, &expires) == nil {
			items = append(items, map[string]any{
				"id": id, "ip": ip, "userAgent": ua, "current": id == sess.ID,
				"createdAt":  created.UTC().Format(time.RFC3339),
				"lastSeenAt": seen.UTC().Format(time.RFC3339),
				"expiresAt":  expires.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	if id == "" {
		writeError(w, http.StatusBadRequest, "ID", "Session id is required")
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE id=$1 AND user_id=$2`, id, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Session not found")
		return
	}
	s.actorAudit(r, sess, "auth.session_revoked", id, "Revoked a panel session", nil)
	if id == sess.ID {
		clearCookie(w, sessionCookieName, true)
		clearCookie(w, csrfCookieName, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSessionRevokeOthers(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE user_id=$1 AND id<>$2`, sess.User.ID, sess.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "auth.sessions_revoked", sess.User.Email, "Revoked all other panel sessions", map[string]any{"revoked": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": tag.RowsAffected()})
}

func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not generate an authenticator secret")
		return
	}
	sealed, err := s.secrets.Seal(secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not protect the authenticator secret")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE control.admin_users SET totp_pending_secret=$2, updated_at=now() WHERE id=$1
	`, sess.User.ID, sealed)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "auth.totp_setup_started", sess.User.Email, "Started authenticator setup", nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret, "otpauth": auth.TOTPURI(secret, sess.User.Email, "Alt8Mail System Control"),
	})
}

func (s *Server) handleTOTPFinish(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	user, err := s.loadAdminByID(r.Context(), sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	secret, err := s.secrets.Open(user.TOTPPending)
	if err != nil || secret == "" {
		writeError(w, http.StatusConflict, "TOTP", "Start authenticator setup again")
		return
	}
	if !auth.ValidateTOTP(secret, strings.TrimSpace(body.Code), time.Now(), 1) {
		writeError(w, http.StatusUnauthorized, "TOTP", "That authenticator code did not match")
		return
	}
	sealed, err := s.secrets.Seal(secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not protect the authenticator secret")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE control.admin_users
		SET totp_secret=$2, totp_pending_secret='', totp_enabled=true, updated_at=now()
		WHERE id=$1
	`, sess.User.ID, sealed)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "auth.totp_enabled", sess.User.Email, "Enabled TOTP two-factor authentication", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "totpEnabled": true})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !auth.VerifyPassword(sess.User.PasswordHash, body.Password) {
		writeError(w, http.StatusUnauthorized, "PASSWORD", "Current panel password is incorrect")
		return
	}
	secret, err := s.secrets.Open(sess.User.TOTPSecret)
	if err != nil || secret == "" || !auth.ValidateTOTP(secret, strings.TrimSpace(body.Code), time.Now(), 1) {
		writeError(w, http.StatusUnauthorized, "TOTP", "Enter a valid authenticator code")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE control.admin_users SET totp_secret='', totp_pending_secret='', totp_enabled=false, updated_at=now()
		WHERE id=$1
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "auth.totp_disabled", sess.User.Email, "Disabled TOTP two-factor authentication", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "totpEnabled": false})
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}
