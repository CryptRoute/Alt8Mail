package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

var apiSupportedScopes = []string{"mail.read", "mail.send", "tasks.read", "tasks.write", "calendar.read"}

var apiSupportedWebhookEvents = []string{"message.queued", "task.created", "calendar.created", "file.shared", "crm.synced", "webhook.test"}

type apiPrincipal struct {
	KeyID    string
	TenantID string
	UserID   string
	User     userRecord
	Scopes   []string
}

type apiPrincipalContextKey struct{}

func apiScopeSupported(scope string) bool {
	for _, allowed := range apiSupportedScopes {
		if scope == allowed {
			return true
		}
	}
	return false
}

func apiHasScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func apiWebhookEventSupported(event string) bool {
	for _, allowed := range apiSupportedWebhookEvents {
		if event == allowed {
			return true
		}
	}
	return false
}

func newAPIKeyToken() (string, string) {
	prefixBytes := make([]byte, 6)
	_, _ = rand.Read(prefixBytes)
	prefix := strings.ToLower(base64.RawURLEncoding.EncodeToString(prefixBytes))
	secret := randomToken(32)
	return prefix, "a8m_" + prefix + "_" + secret
}

func (s *Server) apiLimiterFor(keyID string, limit int) *loginLimiter {
	s.apiKeyMu.Lock()
	defer s.apiKeyMu.Unlock()
	if s.apiKeyLimits == nil {
		s.apiKeyLimits = map[string]*loginLimiter{}
	}
	limiter := s.apiKeyLimits[keyID]
	if limiter == nil || limit <= 0 {
		if limit <= 0 {
			limit = 120
		}
		limiter = newLoginLimiter(limit, time.Minute)
		s.apiKeyLimits[keyID] = limiter
	}
	return limiter
}

func (s *Server) requireAPIKey(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if raw == "" || !strings.HasPrefix(raw, "a8m_") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="alt8mail-api"`)
			writeError(w, http.StatusUnauthorized, "API_KEY", "A tenant API key is required")
			return
		}
		var principal apiPrincipal
		var keyLimit int
		var expiresAt *time.Time
		err := s.store.DB.QueryRow(r.Context(), `
			SELECT k.id, k.tenant_id::text, COALESCE(k.created_by::text,''), k.scopes, k.rate_limit_per_minute, k.expires_at
			FROM public.api_keys k
			WHERE k.token_hash=$1 AND k.revoked_at IS NULL
		`, mcpHash(raw)).Scan(&principal.KeyID, &principal.TenantID, &principal.UserID, &principal.Scopes, &keyLimit, &expiresAt)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "API_KEY", "The API key is invalid or revoked")
			return
		}
		if expiresAt != nil && expiresAt.Before(time.Now()) {
			writeError(w, http.StatusUnauthorized, "API_KEY", "The API key has expired")
			return
		}
		if scope != "" && !apiHasScope(principal.Scopes, scope) {
			writeError(w, http.StatusForbidden, "API_SCOPE", "This API key does not grant "+scope)
			return
		}
		limiter := s.apiLimiterFor(principal.KeyID, keyLimit)
		if !limiter.Allow(principal.KeyID) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "API_RATE_LIMIT", "API rate limit reached")
			return
		}
		limiter.Fail(principal.KeyID)
		user, err := s.userByID(r.Context(), principal.UserID)
		if err != nil || user.TenantID != principal.TenantID {
			writeError(w, http.StatusUnauthorized, "API_KEY", "The API key owner is unavailable")
			return
		}
		principal.User = user
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.api_keys SET last_used_at=now() WHERE id=$1`, principal.KeyID)
		r = r.WithContext(context.WithValue(r.Context(), apiPrincipalContextKey{}, principal))
		next(w, r)
	}
}

func apiPrincipalFrom(r *http.Request) apiPrincipal {
	principal, _ := r.Context().Value(apiPrincipalContextKey{}).(apiPrincipal)
	return principal
}

func (s *Server) adminListAPIKeys(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, name, prefix, scopes, rate_limit_per_minute, expires_at, revoked_at, last_used_at, created_at
		FROM public.api_keys WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 500
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_KEY", "Could not load API keys")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, prefix string
		var scopes []string
		var limit int
		var created time.Time
		var expires, revoked, lastUsed *time.Time
		if rows.Scan(&id, &name, &prefix, &scopes, &limit, &expires, &revoked, &lastUsed, &created) == nil {
			item := map[string]any{
				"id": id, "name": name, "prefix": prefix, "scopes": scopes,
				"rateLimitPerMinute": limit, "createdAt": created.UTC().Format(time.RFC3339),
				"revoked": revoked != nil,
			}
			if expires != nil {
				item["expiresAt"] = expires.UTC().Format(time.RFC3339)
			}
			if lastUsed != nil {
				item["lastUsedAt"] = lastUsed.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": items, "scopes": apiSupportedScopes})
}

func (s *Server) adminCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name               string   `json:"name"`
		Scopes             []string `json:"scopes"`
		RateLimitPerMinute int      `json:"rateLimitPerMinute"`
		ExpiresInDays      int      `json:"expiresInDays"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 120)
	if name == "" {
		writeError(w, http.StatusBadRequest, "API_KEY", "Give the key a name")
		return
	}
	scopes := []string{}
	seen := map[string]bool{}
	for _, raw := range body.Scopes {
		scope := strings.TrimSpace(raw)
		if seen[scope] {
			continue
		}
		if !apiScopeSupported(scope) {
			writeError(w, http.StatusBadRequest, "API_KEY", "Unsupported scope "+scope)
			return
		}
		seen[scope] = true
		scopes = append(scopes, scope)
	}
	if len(scopes) == 0 {
		writeError(w, http.StatusBadRequest, "API_KEY", "Choose at least one scope")
		return
	}
	limit := body.RateLimitPerMinute
	if limit <= 0 || limit > 5000 {
		limit = 120
	}
	var expiresAt *time.Time
	if body.ExpiresInDays > 0 {
		if body.ExpiresInDays > 3650 {
			body.ExpiresInDays = 3650
		}
		value := time.Now().UTC().AddDate(0, 0, body.ExpiresInDays)
		expiresAt = &value
	}
	prefix, token := newAPIKeyToken()
	keyID := newID("apikey")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.api_keys (id, tenant_id, created_by, name, prefix, token_hash, scopes, rate_limit_per_minute, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, keyID, sess.ActiveTenantID, sess.User.ID, name, prefix, mcpHash(token), scopes, limit, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_KEY", "Could not create the API key")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.api_key_created", name, "Created a tenant API key",
		map[string]any{"keyId": keyID, "scopes": scopes})
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "id": keyID, "token": token, "prefix": prefix,
		"scopes": scopes, "rateLimitPerMinute": limit,
		"message": "Store this token now. It cannot be shown again.",
	})
}

func (s *Server) adminRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.api_keys SET revoked_at=now()
		WHERE id=$1 AND tenant_id=$2 AND revoked_at IS NULL
	`, r.PathValue("id"), sess.ActiveTenantID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "API_KEY", "Active API key not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.api_key_revoked", r.PathValue("id"), "Revoked a tenant API key", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func apiIdempotencyLookup(ctx context.Context, s *Server, principal apiPrincipal, key, method, path string) (int, map[string]any, bool) {
	key = truncate(strings.TrimSpace(key), 200)
	if key == "" {
		return 0, nil, false
	}
	var status int
	var raw []byte
	if err := s.store.DB.QueryRow(ctx, `
		SELECT status, response FROM public.api_idempotency
		WHERE tenant_id=$1 AND idempotency_key=$2 AND method=$3 AND path=$4
	`, principal.TenantID, key, method, path).Scan(&status, &raw); err != nil {
		return 0, nil, false
	}
	var payload map[string]any
	if jsonUnmarshal(raw, &payload) != nil {
		return 0, nil, false
	}
	return status, payload, true
}

func apiIdempotencyStore(ctx context.Context, s *Server, principal apiPrincipal, key, method, path string, status int, payload map[string]any) {
	key = truncate(strings.TrimSpace(key), 200)
	if key == "" {
		return
	}
	raw, _ := json.Marshal(payload)
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.api_idempotency (tenant_id, idempotency_key, method, path, status, response)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)
		ON CONFLICT (tenant_id, idempotency_key, method, path) DO NOTHING
	`, principal.TenantID, key, method, path, status, raw)
}

func apiIdempotencyKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("idempotencyKey"))
}
