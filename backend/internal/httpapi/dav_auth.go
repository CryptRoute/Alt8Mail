package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
)

const (
	maxAppPasswords      = 20
	davPasswordCacheTTL  = 5 * time.Minute
	appPasswordTokenSize = 24
)

type davAuthEntry struct {
	userID    string
	email     string
	expiresAt time.Time
}

func newAppPasswordToken() (string, error) {
	raw := make([]byte, appPasswordTokenSize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "a8m-app-" + base64URL(raw), nil
}

func hashAppPassword(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) appPasswordConfigured(ctx context.Context, userID, token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	var id string
	err := s.store.DB.QueryRow(ctx, `
		SELECT id FROM app_passwords
		WHERE user_id = $1 AND password_hash = $2
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		LIMIT 1
	`, userID, hashAppPassword(token)).Scan(&id)
	if err != nil {
		return false
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE app_passwords SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '10 minutes')
	`, id)
	return true
}

func davAuthKey(email, password string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + "\x00" + password))
	return hex.EncodeToString(sum[:])
}

func (s *Server) clearDAVAuthCache(email string) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.davAuthMu.Lock()
	defer s.davAuthMu.Unlock()
	for key, entry := range s.davAuthCache {
		if entry.email == email {
			delete(s.davAuthCache, key)
		}
	}
}

// davAuthenticate validates HTTP Basic credentials for CalDAV/CardDAV. App
// passwords are checked first and are mandatory for accounts with MFA enabled:
// a DAV client cannot complete a WebAuthn or TOTP challenge, so accepting the
// interactive password would silently bypass the second factor.
func (s *Server) davAuthenticate(r *http.Request) (userRecord, bool) {
	email, password, ok := r.BasicAuth()
	email = strings.ToLower(strings.TrimSpace(email))
	if !ok || email == "" || password == "" {
		return userRecord{}, false
	}
	var user userRecord
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color, u.role, u.status,
		       COALESCE(u.password_hash, ''), u.preferences, u.security, u.quota_bytes, u.used_bytes,
		       u.enrollment_required, u.force_password_change, u.password_expires_at
		FROM users u JOIN tenants t ON t.id = u.tenant_id
		WHERE lower(u.email::text) = $1 AND u.status = 'active' AND u.deleted_at IS NULL
		  AND t.status = 'active'
		LIMIT 1
	`, email).Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color,
		&user.Role, &user.Status, &user.PasswordHash, &user.Preferences, &user.Security,
		&user.QuotaBytes, &user.UsedBytes, &user.EnrollmentRequired, &user.ForcePasswordChange,
		&user.PasswordExpiresAt)
	if err != nil || user.ID == "" {
		return userRecord{}, false
	}
	if s.appPasswordConfigured(r.Context(), user.ID, password) {
		return user, true
	}
	_, methods, _, _ := s.securityState(r.Context(), user)
	if len(methods) > 0 {
		return userRecord{}, false
	}
	cacheKey := davAuthKey(email, password)
	s.davAuthMu.Lock()
	entry, found := s.davAuthCache[cacheKey]
	s.davAuthMu.Unlock()
	if found && time.Now().Before(entry.expiresAt) && entry.userID == user.ID {
		return user, true
	}
	if user.PasswordHash == "" || !auth.VerifyPassword(user.PasswordHash, password) {
		return userRecord{}, false
	}
	s.davAuthMu.Lock()
	if len(s.davAuthCache) > 4096 {
		for key, value := range s.davAuthCache {
			if time.Now().After(value.expiresAt) {
				delete(s.davAuthCache, key)
			}
		}
	}
	s.davAuthCache[cacheKey] = davAuthEntry{userID: user.ID, email: email, expiresAt: time.Now().Add(davPasswordCacheTTL)}
	s.davAuthMu.Unlock()
	return user, true
}

func (s *Server) davRequireAuth(w http.ResponseWriter, r *http.Request) (userRecord, bool) {
	ip := clientIP(r)
	if !s.davLimit.Allow(ip) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "Too many failed CalDAV/CardDAV authentication attempts.", http.StatusTooManyRequests)
		return userRecord{}, false
	}
	user, ok := s.davAuthenticate(r)
	if ok {
		s.davLimit.Reset(ip)
		return user, true
	}
	s.davLimit.Fail(ip)
	w.Header().Set("WWW-Authenticate", `Basic realm="Alt8Mail CalDAV/CardDAV", charset="UTF-8"`)
	http.Error(w, "Authentication required. Accounts with two-factor authentication must use an app password.", http.StatusUnauthorized)
	return userRecord{}, false
}

func (s *Server) listAppPasswords(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, label, last_used_at, expires_at, revoked_at, created_at
		FROM app_passwords WHERE user_id = $1 ORDER BY created_at DESC
	`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "APP_PASSWORDS", "Could not load app passwords")
		return
	}
	defer rows.Close()
	passwords := []map[string]any{}
	active := 0
	for rows.Next() {
		var id, label string
		var lastUsed, expires, revoked, created *time.Time
		if rows.Scan(&id, &label, &lastUsed, &expires, &revoked, &created) == nil {
			status := "active"
			if expires != nil && !expires.After(time.Now()) {
				status = "expired"
			} else if revoked != nil {
				status = "revoked"
			} else {
				active++
			}
			entry := map[string]any{
				"id": id, "label": label, "status": status,
				"createdAt": created.UTC().Format(time.RFC3339),
			}
			if lastUsed != nil {
				entry["lastUsedAt"] = lastUsed.UTC().Format(time.RFC3339)
			}
			if expires != nil {
				entry["expiresAt"] = expires.UTC().Format(time.RFC3339)
			}
			if revoked != nil {
				entry["revokedAt"] = revoked.UTC().Format(time.RFC3339)
			}
			passwords = append(passwords, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"appPasswords": passwords, "count": active, "historyCount": len(passwords),
	})
}

func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Label         string `json:"label"`
		Password      string `json:"password"`
		ExpiresInDays *int   `json:"expiresInDays"`
		ExpiresAt     string `json:"expiresAt"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	if !s.verifyAccountPassword(sess, body.Password) {
		writeError(w, http.StatusForbidden, "PASSWORD", "Enter your current account password")
		return
	}
	label := truncate(strings.TrimSpace(body.Label), 80)
	if label == "" {
		label = "Mail client"
	}
	var expiresAt *time.Time
	if body.ExpiresInDays != nil {
		days := *body.ExpiresInDays
		if days < 1 || days > 730 {
			writeError(w, http.StatusBadRequest, "EXPIRY", "Expiry must be between 1 and 730 days")
			return
		}
		value := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
		expiresAt = &value
	} else if strings.TrimSpace(body.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(body.ExpiresAt))
		if err != nil || !parsed.After(time.Now().UTC()) || parsed.After(time.Now().UTC().Add(730*24*time.Hour)) {
			writeError(w, http.StatusBadRequest, "EXPIRY", "Choose an expiry between now and two years")
			return
		}
		value := parsed.UTC()
		expiresAt = &value
	}
	var count int
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FROM app_passwords
		WHERE user_id = $1 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
	`, sess.User.ID).Scan(&count)
	if count >= maxAppPasswords {
		writeError(w, http.StatusConflict, "APP_PASSWORDS", "Revoke an app password before creating another")
		return
	}
	token, err := newAppPasswordToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "APP_PASSWORDS", "Could not generate an app password")
		return
	}
	id := newID("apppw")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO app_passwords (id, tenant_id, user_id, label, password_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, id, sess.User.TenantID, sess.User.ID, label, hashAppPassword(token), expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "APP_PASSWORDS", "Could not save the app password")
		return
	}
	s.auditRequest(r, sess, "security.app_password_created", label, "Created an app password for a mail client")
	response := map[string]any{
		"ok": true, "id": id, "label": label, "token": token,
		"note": "Store this token now. It is shown once and cannot be recovered.",
	}
	if expiresAt != nil {
		response["expiresAt"] = expiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) deleteAppPassword(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var label string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE app_passwords SET revoked_at = COALESCE(revoked_at, now())
		WHERE id = $1 AND user_id = $2 RETURNING label
	`, r.PathValue("id"), sess.User.ID).Scan(&label)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "App password not found")
		return
	}
	s.auditRequest(r, sess, "security.app_password_revoked", firstNonEmpty(label, "App password"), "Revoked an app password")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) cleanupAppPasswords(ctx context.Context) {
	// Keep expired credentials for the user's security history, but mark them
	// revoked so active-token limits and DAV authentication never count them.
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE app_passwords SET revoked_at = COALESCE(revoked_at, now())
		WHERE expires_at IS NOT NULL AND expires_at <= now() AND revoked_at IS NULL
	`)
}
