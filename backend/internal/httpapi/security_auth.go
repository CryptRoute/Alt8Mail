package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/auth"
)

const (
	trustedDeviceCookie = "meridian_system_trusted"
	trustedDeviceTTL    = 30 * 24 * time.Hour
	totpPendingTTL      = 10 * time.Minute
)

type storedPasskey struct {
	ID         string
	Name       string
	Credential []byte
	PublicKey  []byte
	SignCount  uint32
	Transports []string
	AAGUID     string
	CreatedAt  time.Time
	LastUsed   time.Time
}

func (s *Server) securityKey() [32]byte {
	return sha256.Sum256([]byte(s.cfg.SessionSecret + "\x00" + s.cfg.CSRFSecret))
}

func (s *Server) sealSecuritySecret(value string) (string, error) {
	key := s.securityKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), nil)
	payload := append(nonce, ciphertext...)
	return "v1:" + base64.RawURLEncoding.EncodeToString(payload), nil
}

func (s *Server) openSecuritySecret(value string) (string, error) {
	if !strings.HasPrefix(value, "v1:") {
		return "", fmt.Errorf("unsupported secret format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	if err != nil {
		return "", err
	}
	key := s.securityKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", fmt.Errorf("secret payload too short")
	}
	plain, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Server) securityState(ctx context.Context, user userRecord) (map[string]any, []string, []any, int) {
	securityMap := map[string]any{}
	_ = json.Unmarshal(user.Security, &securityMap)
	twoFactor, _ := securityMap["twoFactor"].(map[string]any)
	if twoFactor == nil {
		twoFactor = map[string]any{}
	}
	totpSecret := stateString(twoFactor, "totpSecret")
	passkeys, stored := s.loadPasskeys(ctx, user.ID)
	remaining := s.unusedRecoveryCodeCount(ctx, user.ID)
	methods := []string{}
	if len(stored) > 0 {
		methods = append(methods, "passkey")
	}
	if totpSecret != "" {
		methods = append(methods, "authenticator")
	}
	if remaining > 0 {
		methods = append(methods, "recovery")
	}
	enabled := totpSecret != "" || len(stored) > 0
	enabledAt := stateString(twoFactor, "enabledAt")
	if enabledAt == "" && len(stored) > 0 {
		enabledAt = stored[0].CreatedAt.UTC().Format(time.RFC3339)
	}
	summary := map[string]any{
		"twoFactor": map[string]any{
			"enabled": enabled, "enabledAt": nullableString(enabledAt),
			"methods": methods, "passkeys": passkeys,
			"recoveryCodesRemaining": remaining, "totpEnabled": totpSecret != "",
			"enrollmentRequired": user.EnrollmentRequired,
		},
		"enrollmentRequired": user.EnrollmentRequired,
	}
	return summary, methods, passkeys, remaining
}

func (s *Server) loadPasskeys(ctx context.Context, userID string) ([]any, []storedPasskey) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id::text, name, credential_id, public_key, sign_count, transports,
		       COALESCE(aaguid, ''), created_at, COALESCE(last_used_at, created_at)
		FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at
	`, userID)
	if err != nil {
		return []any{}, nil
	}
	defer rows.Close()
	metadata := []any{}
	stored := []storedPasskey{}
	for rows.Next() {
		var item storedPasskey
		if err := rows.Scan(&item.ID, &item.Name, &item.Credential, &item.PublicKey, &item.SignCount, &item.Transports,
			&item.AAGUID, &item.CreatedAt, &item.LastUsed); err != nil {
			continue
		}
		stored = append(stored, item)
		metadata = append(metadata, map[string]any{
			"id": item.ID, "name": item.Name,
			"createdAt":  item.CreatedAt.UTC().Format(time.RFC3339),
			"lastUsedAt": item.LastUsed.UTC().Format(time.RFC3339),
			"transports": item.Transports,
		})
	}
	return metadata, stored
}

func (s *Server) unusedRecoveryCodeCount(ctx context.Context, userID string) int {
	var count int
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID).Scan(&count)
	return count
}

func (s *Server) updateUserSecurity(ctx context.Context, userID string, mutate func(map[string]any) error) error {
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT security FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&raw); err != nil {
		return err
	}
	security := map[string]any{}
	_ = json.Unmarshal(raw, &security)
	if err := mutate(security); err != nil {
		return err
	}
	encoded, err := json.Marshal(security)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET security = $2, updated_at = now() WHERE id = $1`, userID, encoded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) enableTOTP(ctx context.Context, userID, secret string) error {
	sealed, err := s.sealSecuritySecret(secret)
	if err != nil {
		return err
	}
	return s.updateUserSecurity(ctx, userID, func(security map[string]any) error {
		twoFactor, _ := security["twoFactor"].(map[string]any)
		if twoFactor == nil {
			twoFactor = map[string]any{}
		}
		twoFactor["totpSecret"] = sealed
		twoFactor["enabledAt"] = nowISO()
		twoFactor["totpEnabledAt"] = nowISO()
		security["twoFactor"] = twoFactor
		return nil
	})
}

func (s *Server) disableTOTP(ctx context.Context, userID string) error {
	return s.updateUserSecurity(ctx, userID, func(security map[string]any) error {
		twoFactor, _ := security["twoFactor"].(map[string]any)
		if twoFactor == nil {
			return nil
		}
		delete(twoFactor, "totpSecret")
		delete(twoFactor, "totpEnabledAt")
		return nil
	})
}

func (s *Server) decryptTOTPSecret(user userRecord) (string, error) {
	security := map[string]any{}
	_ = json.Unmarshal(user.Security, &security)
	twoFactor, _ := security["twoFactor"].(map[string]any)
	sealed := stateString(twoFactor, "totpSecret")
	if sealed == "" {
		return "", fmt.Errorf("authenticator is not configured")
	}
	return s.openSecuritySecret(sealed)
}

func (s *Server) replaceRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	for _, code := range codes {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2)`, userID, auth.HashRecoveryCode(code)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Server) consumeRecoveryCode(ctx context.Context, userID, code string) bool {
	hash := auth.HashRecoveryCode(code)
	tag, err := s.store.DB.Exec(ctx, `
		UPDATE recovery_codes SET used_at = now()
		WHERE id = (
			SELECT id FROM recovery_codes
			WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
			LIMIT 1
		)
	`, userID, hash)
	return err == nil && tag.RowsAffected() > 0
}

type authChallenge struct {
	ID        string
	UserID    string
	Remember  bool
	Attempts  int
	ExpiresAt time.Time
	Kind      string
}

func (s *Server) createAuthChallenge(ctx context.Context, user userRecord, kind string, remember bool, r *http.Request) (*authChallenge, error) {
	id := randomToken(24)
	expires := time.Now().Add(5 * time.Minute)
	if kind == "enrollment" {
		expires = time.Now().Add(10 * time.Minute)
	}
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO auth_challenges (id, user_id, tenant_id, remember, ip, user_agent, expires_at, kind)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::inet, $6, $7, $8)
	`, id, user.ID, user.TenantID, remember, clientIP(r), sanitizeUserAgent(r.UserAgent()), expires, kind)
	if err != nil {
		return nil, err
	}
	return &authChallenge{ID: id, UserID: user.ID, Remember: remember, ExpiresAt: expires, Kind: kind}, nil
}

func (s *Server) authChallengeByID(ctx context.Context, id, kind string) (*authChallenge, error) {
	var challenge authChallenge
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, user_id::text, remember, attempts, expires_at, kind
		FROM auth_challenges WHERE id = $1 AND kind = $2
	`, id, kind).Scan(&challenge.ID, &challenge.UserID, &challenge.Remember, &challenge.Attempts, &challenge.ExpiresAt, &challenge.Kind)
	if err != nil {
		return nil, err
	}
	return &challenge, nil
}

func (s *Server) deleteAuthChallenge(ctx context.Context, id string) {
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM auth_challenges WHERE id = $1`, id)
}

func (s *Server) failAuthChallenge(ctx context.Context, challenge *authChallenge) int {
	_, _ = s.store.DB.Exec(ctx, `UPDATE auth_challenges SET attempts = attempts + 1 WHERE id = $1`, challenge.ID)
	remaining := 4 - challenge.Attempts
	if remaining < 0 {
		remaining = 0
	}
	if remaining == 0 {
		s.deleteAuthChallenge(ctx, challenge.ID)
	}
	return remaining
}

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, user userRecord, remember bool, detail string, extra map[string]any) {
	ttl := s.cfg.SessionTTL
	if remember {
		ttl = s.cfg.RememberTTL
	}
	sess, rawToken, err := s.createSession(r, user, ttl)
	if err != nil {
		s.logger.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "SESSION", "Could not create a session")
		return
	}
	s.setSessionCookies(w, rawToken, sess.CSRF, ttl)
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1`, user.ID)
	s.audit(r, user, "auth.login", user.Email, detail)
	userPayload, _ := s.publicUserWithState(r.Context(), user)
	response := map[string]any{
		"user":       userPayload,
		"csrf":       sess.CSRF,
		"expiresAt":  sess.ExpiresAt.UTC().Format(time.RFC3339),
		"method":     "password",
		"serverTime": time.Now().UTC().Format(time.RFC3339),
	}
	for key, value := range extra {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) trustedDeviceValid(r *http.Request, userID string) bool {
	cookie, err := r.Cookie(trustedDeviceCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	var id string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT id::text FROM trusted_devices
		WHERE user_id = $1 AND token_hash = $2 AND expires_at > now() AND revoked_at IS NULL
	`, userID, hashToken(cookie.Value)).Scan(&id)
	if err != nil {
		return false
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE trusted_devices SET last_used_at = now() WHERE id = $1`, id)
	return true
}

func (s *Server) issueTrustedDevice(w http.ResponseWriter, r *http.Request, user userRecord) {
	token := randomToken(32)
	expires := time.Now().Add(trustedDeviceTTL)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO trusted_devices (user_id, token_hash, name, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, NULLIF($4, '')::inet, $5, $6)
	`, user.ID, hashToken(token), describeSessionDevice(r.UserAgent()), clientIP(r), sanitizeUserAgent(r.UserAgent()), expires)
	if err != nil {
		s.logger.Warn("trusted device insert failed", "err", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: trustedDeviceCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: int(trustedDeviceTTL.Seconds()),
	})
}

func (s *Server) beginTOTPEnrollment(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not start authenticator setup")
		return
	}
	key := "totp-enroll:" + sess.User.ID
	if err := s.store.Redis.Set(r.Context(), key, secret, totpPendingTTL).Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not store authenticator setup state")
		return
	}
	issuer := "Meridian Mail"
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret, "issuer": issuer, "account": sess.User.Email,
		"otpauth":   auth.TOTPURI(secret, sess.User.Email, issuer),
		"expiresAt": time.Now().Add(totpPendingTTL).UTC().Format(time.RFC3339),
	})
}

func (s *Server) finishTOTPEnrollment(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Code string `json:"code"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	key := "totp-enroll:" + sess.User.ID
	secret, err := s.store.Redis.Get(r.Context(), key).Result()
	if err != nil || secret == "" {
		writeError(w, http.StatusGone, "TOTP_EXPIRED", "Authenticator setup expired. Start again.")
		return
	}
	if !auth.ValidateTOTP(secret, body.Code, time.Now(), 1) {
		s.auditRequest(r, sess, "security.totp_failed", "Authenticator setup", "Rejected an invalid authenticator code")
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
		writeError(w, http.StatusInternalServerError, "TOTP", "Authenticator saved, but recovery codes could not be generated")
		return
	}
	_ = s.store.Redis.Del(r.Context(), key).Err()
	s.auditRequest(r, sess, "security.totp_enabled", "Authenticator app", "Enabled TOTP two-factor authentication")
	refreshed, err := s.userByID(r.Context(), sess.User.ID)
	if err != nil {
		refreshed = sess.User
	}
	_, _, _, remaining := s.securityState(r.Context(), refreshed)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "codes": codes, "remaining": remaining,
		"security": s.mustSecuritySummary(r.Context(), refreshed),
	})
}

func (s *Server) disableTOTPHandler(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Password string `json:"password"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	if !s.verifyAccountPassword(sess, body.Password) {
		writeError(w, http.StatusUnauthorized, "BAD_PASSWORD", "Enter your current password to disable the authenticator")
		return
	}
	if err := s.disableTOTP(r.Context(), sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not disable the authenticator")
		return
	}
	refreshed, err := s.userByID(r.Context(), sess.User.ID)
	if err != nil {
		refreshed = sess.User
	}
	if _, stored := s.loadPasskeys(r.Context(), sess.User.ID); len(stored) == 0 {
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM recovery_codes WHERE user_id = $1`, sess.User.ID)
	}
	s.auditRequest(r, sess, "security.totp_disabled", "Authenticator app", "Disabled TOTP two-factor authentication")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "security": s.mustSecuritySummary(r.Context(), refreshed)})
}

func (s *Server) revokeTrustedDevices(ctx context.Context, userID string) {
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM trusted_devices WHERE user_id = $1`, userID)
}

func (s *Server) mustSecuritySummary(ctx context.Context, user userRecord) map[string]any {
	summary, _, _, _ := s.securityState(ctx, user)
	return summary
}

func (s *Server) getRecoveryState(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	remaining := s.unusedRecoveryCodeCount(r.Context(), sess.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{"codes": []string{}, "remaining": remaining})
}

func (s *Server) regenerateRecoveryState(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	_, methods, _, _ := s.securityState(r.Context(), sess.User)
	if len(methods) == 0 || (len(methods) == 1 && methods[0] == "recovery") {
		writeError(w, http.StatusConflict, "TWO_FACTOR_DISABLED", "Enable an authenticator or passkey before generating recovery codes")
		return
	}
	codes, err := s.replaceRecoveryCodes(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RECOVERY", "Could not generate recovery codes")
		return
	}
	s.auditRequest(r, sess, "security.recovery_regenerated", "Recovery codes", "Generated a new set of recovery codes")
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes, "remaining": len(codes)})
}

func (s *Server) verifyTwoFactor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string `json:"challengeId"`
		Method      string `json:"method"`
		Code        string `json:"code"`
		TrustDevice bool   `json:"trustDevice"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), body.ChallengeID, "two_factor")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "TWO_FACTOR_CHALLENGE", "This verification request has expired. Sign in again.")
		return
	}
	if time.Now().After(challenge.ExpiresAt) {
		s.deleteAuthChallenge(r.Context(), challenge.ID)
		writeError(w, http.StatusGone, "TWO_FACTOR_EXPIRED", "This verification request has expired. Sign in again.")
		return
	}
	if challenge.Attempts >= 5 {
		s.deleteAuthChallenge(r.Context(), challenge.ID)
		writeError(w, http.StatusTooManyRequests, "TWO_FACTOR_LOCKED", "Too many verification attempts. Sign in again.")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "TWO_FACTOR_CHALLENGE", "This verification request has expired. Sign in again.")
		return
	}
	verified := false
	detail := "Second factor verified"
	switch body.Method {
	case "totp", "authenticator":
		secret, err := s.decryptTOTPSecret(user)
		if err == nil && auth.ValidateTOTP(secret, body.Code, time.Now(), 1) {
			verified = true
			detail = "Password + authenticator code"
		}
	case "recovery":
		if s.consumeRecoveryCode(r.Context(), user.ID, body.Code) {
			verified = true
			detail = "Password + recovery code"
		}
	case "passkey":
		writeError(w, http.StatusBadRequest, "PASSKEY_CEREMONY", "Use the WebAuthn passkey endpoint for this sign-in")
		return
	default:
		writeError(w, http.StatusBadRequest, "TWO_FACTOR_METHOD", "Choose a valid second-factor method")
		return
	}
	if !verified {
		remaining := s.failAuthChallenge(r.Context(), challenge)
		s.audit(r, user, "auth.2fa.failed", user.Email, "Failed second factor · "+body.Method)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{
			"code": "TWO_FACTOR_INVALID", "message": "That verification code did not match.",
			"details": map[string]any{"attemptsRemaining": remaining},
		}})
		return
	}
	s.deleteAuthChallenge(r.Context(), challenge.ID)
	if body.TrustDevice {
		s.issueTrustedDevice(w, r, user)
	}
	detailSuffix := ""
	if body.TrustDevice {
		detailSuffix = " · trusted device"
	}
	s.audit(r, user, "auth.2fa", user.Email, detail+detailSuffix)
	s.completeLogin(w, r, user, challenge.Remember, detail+detailSuffix, map[string]any{
		"twoFactorMethod": body.Method, "trustedDevice": body.TrustDevice,
	})
}
