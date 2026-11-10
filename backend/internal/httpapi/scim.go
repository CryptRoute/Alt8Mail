package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// scimTokenPrefix is the fixed, recognisable prefix of every SCIM bearer
	// token. The raw value is returned exactly once on create/rotate; only the
	// SHA-256 hash and a short display prefix are persisted.
	scimTokenPrefix        = "a8m_scim_"
	scimTokenRandomBytes   = 32
	scimTokenDisplayLength = 16
	scimTokenMaxLength     = 256
	scimTokenNameMaxLength = 100
	scimTokenBodyLimit     = 16 * 1024
	scimTokenTouchTimeout  = 5 * time.Second

	scimErrorSchema                 = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimListResponseSchema          = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimServiceProviderConfigSchema = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	scimResourceTypeSchema          = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	scimUserSchema                  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimGroupSchema                 = "urn:ietf:params:scim:schemas:core:2.0:Group"
)

// scimTenant is the authenticated tenant identity carried by a SCIM request.
// Every SCIM query and mutation must be scoped to TenantID; the token ID and
// name exist only for audit attribution.
type scimTenant struct {
	TokenID  string
	TenantID string
	Name     string
}

type scimTenantContextKey struct{}

func scimTenantFromContext(ctx context.Context) (scimTenant, bool) {
	tenant, ok := ctx.Value(scimTenantContextKey{}).(scimTenant)
	return tenant, ok
}

// scimBaseURL returns the public SCIM base URL advertised in resource meta and
// used by IdP configuration. SCIM carries identity data and bearer tokens, so a
// cleartext origin is accepted only for loopback development and tests.
func (s *Server) scimBaseURL() (string, error) {
	raw := strings.TrimSpace(s.cfg.PublicURL)
	if raw == "" {
		return "", errors.New("ALT8MAIL_PUBLIC_URL is required for SCIM provisioning")
	}
	publicURL, err := url.Parse(raw)
	if err != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Host == "" {
		return "", errors.New("ALT8MAIL_PUBLIC_URL must be an absolute HTTP(S) URL for SCIM provisioning")
	}
	// The public URL is used as the token-authenticated provisioning origin.
	// Reject embedded credentials and parameters so they can never leak into
	// resource locations or IdP configuration. A literal "#" is rejected too:
	// url.Parse accepts a bare trailing fragment as an empty string while the
	// configured value would still contain it.
	if publicURL.User != nil || publicURL.RawQuery != "" || publicURL.ForceQuery || publicURL.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("ALT8MAIL_PUBLIC_URL must be a bare origin without userinfo, query or fragment for SCIM provisioning")
	}
	if publicURL.Scheme == "http" && !samlLoopbackHost(publicURL.Hostname()) {
		return "", errors.New("ALT8MAIL_PUBLIC_URL must use HTTPS for SCIM provisioning (HTTP is allowed only for loopback)")
	}
	return strings.TrimRight(raw, "/") + "/scim/v2", nil
}

// writeSCIMJSON writes a SCIM 2.0 response using the RFC 7644 media type.
func writeSCIMJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "internal encoding error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/scim+json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// writeSCIMError writes the RFC 7644 error envelope. Details are fixed, safe
// strings: token material, hashes and backend errors never appear here.
func writeSCIMError(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{
		"schemas": []string{scimErrorSchema},
		"status":  strconv.Itoa(status),
		"detail":  detail,
	}
	if scimType != "" {
		body["scimType"] = scimType
	}
	writeSCIMJSON(w, status, body)
}

// parseSCIMBearer accepts exactly one "Bearer <token>" authorization value.
// The fixed token prefix is checked before any database lookup so arbitrary
// garbage never reaches PostgreSQL.
func parseSCIMBearer(header string) (string, bool) {
	parts := strings.Fields(strings.TrimSpace(header))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := parts[1]
	if len(token) > scimTokenMaxLength || !strings.HasPrefix(token, scimTokenPrefix) {
		return "", false
	}
	return token, true
}

// scimAuth gates every /scim/v2 request. The license check comes first: an
// unlicensed, expired or misconfigured deployment returns 404 and never asks
// for credentials, so it cannot advertise SCIM to an unauthenticated caller.
// Failed attempts are rate limited per client IP; the limiter is consulted
// before the token lookup so an over-limit caller never reaches PostgreSQL.
func (s *Server) scimAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.entitled("scim") {
			http.NotFound(w, r)
			return
		}
		if _, err := s.scimBaseURL(); err != nil {
			http.NotFound(w, r)
			return
		}
		ip := clientIP(r)
		if !s.scimLimit.Allow(ip) {
			w.Header().Set("Retry-After", strconv.Itoa(s.scimLimit.retryAfterSeconds()))
			writeSCIMError(w, http.StatusTooManyRequests, "tooMany", "Too many SCIM authentication attempts. Try again later.")
			return
		}
		raw, ok := parseSCIMBearer(r.Header.Get("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.scimLimit.Fail(ip)
			writeSCIMError(w, http.StatusUnauthorized, "", "A valid SCIM bearer token is required")
			return
		}
		tenant, err := s.scimTokenTenant(r.Context(), raw)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.scimLimit.Fail(ip)
			writeSCIMError(w, http.StatusUnauthorized, "", "A valid SCIM bearer token is required")
			return
		}
		s.scimLimit.Reset(ip)
		s.touchSCIMToken(tenant.TokenID, tenant.TenantID)
		ctx := context.WithValue(r.Context(), scimTenantContextKey{}, tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// scimTokenTenant resolves a raw bearer token to its tenant. Revoked, expired
// and cross-tenant tokens simply produce no row: the caller returns the same
// 401 for every failure so token state is not disclosed.
func (s *Server) scimTokenTenant(ctx context.Context, raw string) (scimTenant, error) {
	var tenant scimTenant
	err := s.store.DB.QueryRow(ctx, `
		SELECT st.id::text, st.tenant_id::text, st.name
		FROM scim_tokens st
		JOIN tenants t ON t.id = st.tenant_id AND t.status = 'active'
		WHERE st.token_hash = $1
		  AND st.revoked_at IS NULL
		  AND (st.expires_at IS NULL OR st.expires_at > now())
	`, hashToken(raw)).Scan(&tenant.TokenID, &tenant.TenantID, &tenant.Name)
	return tenant, err
}

// touchSCIMToken records token usage asynchronously so a slow or contended
// write can never delay or fail an IdP request. The update is tenant-scoped and
// detached from the request context, which may be cancelled first.
func (s *Server) touchSCIMToken(tokenID, tenantID string) {
	if s.store == nil || s.store.DB == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scimTokenTouchTimeout)
		defer cancel()
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE scim_tokens SET last_used_at = now() WHERE id = $1 AND tenant_id = $2
		`, tokenID, tenantID)
	}()
}

func (s *Server) registerSCIMRoutes(mux *http.ServeMux) {
	// Register the concrete discovery endpoints before the subtree handler so
	// ServeMux's specificity rules always pick them; the subtree handler keeps
	// unsupported SCIM paths behind authentication and fails closed. The bare
	// /scim/v2 pattern is explicit so it reaches scimAuth instead of a
	// ServeMux 307 redirect before the license gate.
	mux.Handle("/scim/v2", s.scimAuth(http.HandlerFunc(s.scimNotFound)))
	mux.Handle("/scim/v2/ServiceProviderConfig", s.scimAuth(s.scimAllowMethod(http.MethodGet, s.scimServiceProviderConfig)))
	mux.Handle("/scim/v2/ResourceTypes", s.scimAuth(s.scimAllowMethod(http.MethodGet, s.scimResourceTypes)))
	mux.Handle("/scim/v2/ResourceTypes/User", s.scimAuth(s.scimAllowMethod(http.MethodGet, s.scimResourceType("User"))))
	mux.Handle("/scim/v2/ResourceTypes/Group", s.scimAuth(s.scimAllowMethod(http.MethodGet, s.scimResourceType("Group"))))
	s.registerSCIMUserRoutes(mux)
	mux.Handle("/scim/v2/", s.scimAuth(http.HandlerFunc(s.scimNotFound)))
}

// scimAllowMethod rejects unsupported methods with a SCIM 405 and an Allow
// header instead of falling through to the subtree 404.
func (s *Server) scimAllowMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeSCIMError(w, http.StatusMethodNotAllowed, "", "Method not allowed")
			return
		}
		next(w, r)
	}
}

func (s *Server) scimNotFound(w http.ResponseWriter, r *http.Request) {
	writeSCIMError(w, http.StatusNotFound, "", "SCIM endpoint not found")
}

func (s *Server) scimServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	base, err := s.scimBaseURL()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeSCIMJSON(w, http.StatusOK, map[string]any{
		"schemas": []string{scimServiceProviderConfigSchema},
		"patch":   map[string]any{"supported": true},
		"bulk": map[string]any{
			"supported": false, "maxOperations": 0, "maxPayloadSize": 0,
		},
		"filter":         map[string]any{"supported": true, "maxResults": 500},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type":        "oauthbearertoken",
			"name":        "Bearer Token",
			"description": "Per-tenant SCIM bearer token issued in the Alt8Mail admin console",
			"specUri":     "https://www.rfc-editor.org/info/rfc6750",
			"primary":     true,
		}},
		"meta": map[string]any{
			"resourceType": "ServiceProviderConfig",
			"location":     base + "/ServiceProviderConfig",
		},
	})
}

// scimResourceTypeObject builds one RFC 7643 ResourceType document. It returns
// false for an unknown type so callers can answer with a SCIM 404.
func scimResourceTypeObject(base, id string) (map[string]any, bool) {
	var name, endpoint, description, schema string
	switch id {
	case "User":
		name, endpoint, description, schema = "User", "/Users", "User Account", scimUserSchema
	case "Group":
		name, endpoint, description, schema = "Group", "/Groups", "Group", scimGroupSchema
	default:
		return nil, false
	}
	return map[string]any{
		"schemas":          []string{scimResourceTypeSchema},
		"id":               id,
		"name":             name,
		"endpoint":         endpoint,
		"description":      description,
		"schema":           schema,
		"schemaExtensions": []any{},
		"meta": map[string]any{
			"resourceType": "ResourceType",
			"location":     base + "/ResourceTypes/" + id,
		},
	}, true
}

func (s *Server) scimResourceTypes(w http.ResponseWriter, r *http.Request) {
	base, err := s.scimBaseURL()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resources := make([]map[string]any, 0, 2)
	for _, id := range []string{"User", "Group"} {
		resource, ok := scimResourceTypeObject(base, id)
		if !ok {
			continue
		}
		resources = append(resources, resource)
	}
	writeSCIMJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{scimListResponseSchema},
		"totalResults": len(resources),
		"itemsPerPage": len(resources),
		"startIndex":   1,
		"Resources":    resources,
	})
}

func (s *Server) scimResourceType(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base, err := s.scimBaseURL()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		resource, ok := scimResourceTypeObject(base, id)
		if !ok {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM resource type not found")
			return
		}
		writeSCIMJSON(w, http.StatusOK, resource)
	}
}

// scimTokenRecord is the non-secret projection of a scim_tokens row. The token
// hash and raw value are never part of this type.
type scimTokenRecord struct {
	ID         string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

func scimTokenStatus(token scimTokenRecord) string {
	if token.RevokedAt != nil {
		return "revoked"
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now()) {
		return "expired"
	}
	return "active"
}

func scimTokenPayload(token scimTokenRecord) map[string]any {
	return map[string]any{
		"id":         token.ID,
		"name":       token.Name,
		"prefix":     token.Prefix,
		"createdAt":  token.CreatedAt.UTC().Format(time.RFC3339),
		"lastUsedAt": nullableTime(token.LastUsedAt),
		"expiresAt":  nullableTime(token.ExpiresAt),
		"revokedAt":  nullableTime(token.RevokedAt),
		"status":     scimTokenStatus(token),
	}
}

func newSCIMTokenMaterial() (raw, prefix, hash string) {
	raw = scimTokenPrefix + randomToken(scimTokenRandomBytes)
	prefix = raw
	if len(prefix) > scimTokenDisplayLength {
		prefix = prefix[:scimTokenDisplayLength]
	}
	return raw, prefix, hashToken(raw)
}

func (s *Server) adminListSCIMTokens(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id::text, name, token_prefix, created_at, last_used_at, expires_at, revoked_at
		FROM scim_tokens
		WHERE tenant_id = $1
		ORDER BY created_at DESC, id
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SCIM_TOKENS", "Could not load SCIM tokens")
		return
	}
	defer rows.Close()
	tokens := []map[string]any{}
	for rows.Next() {
		var token scimTokenRecord
		if err := rows.Scan(&token.ID, &token.Name, &token.Prefix, &token.CreatedAt, &token.LastUsedAt, &token.ExpiresAt, &token.RevokedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "SCIM_TOKENS", "Could not load SCIM tokens")
			return
		}
		tokens = append(tokens, scimTokenPayload(token))
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "SCIM_TOKENS", "Could not load SCIM tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) adminCreateSCIMToken(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeJSON(w, r, &body, scimTokenBodyLimit); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "NAME", "Token name is required")
		return
	}
	if len(name) > scimTokenNameMaxLength {
		writeError(w, http.StatusBadRequest, "NAME", "Token name is too long")
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(body.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "EXPIRES_AT", "expiresAt must be an RFC3339 timestamp")
			return
		}
		if !parsed.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "EXPIRES_AT", "expiresAt must be in the future")
			return
		}
		parsed = parsed.UTC()
		expiresAt = &parsed
	}
	rawToken, prefix, tokenHash := newSCIMTokenMaterial()
	var expiresParam any
	if expiresAt != nil {
		expiresParam = *expiresAt
	}
	record := scimTokenRecord{Name: name, Prefix: prefix, ExpiresAt: expiresAt}
	if err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO scim_tokens (tenant_id, name, token_hash, token_prefix, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, created_at
	`, sess.ActiveTenantID, name, tokenHash, prefix, sess.User.ID, expiresParam).Scan(&record.ID, &record.CreatedAt); err != nil {
		// Never log the database error: a unique-violation detail can include
		// the token hash.
		s.logger.Warn("create SCIM token failed")
		writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not create the SCIM token")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.scim_token_created", name, "Created SCIM provisioning token", map[string]any{"tokenId": record.ID})
	payload := scimTokenPayload(record)
	// The raw token is intentionally returned exactly once, on creation.
	payload["token"] = rawToken
	writeJSON(w, http.StatusCreated, payload)
}

func (s *Server) adminRotateSCIMToken(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tokenID := r.PathValue("id")
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not rotate the SCIM token")
		return
	}
	defer tx.Rollback(ctx)

	var name string
	var expiresAt, revokedAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT name, expires_at, revoked_at
		FROM scim_tokens
		WHERE id = $1 AND tenant_id = $2
		FOR UPDATE
	`, tokenID, sess.ActiveTenantID).Scan(&name, &expiresAt, &revokedAt); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not rotate the SCIM token")
			return
		}
		writeError(w, http.StatusNotFound, "SCIM_TOKEN_NOT_FOUND", "SCIM token not found")
		return
	}
	if revokedAt != nil {
		writeError(w, http.StatusConflict, "SCIM_TOKEN_REVOKED", "This SCIM token is already revoked")
		return
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		writeError(w, http.StatusConflict, "SCIM_TOKEN_EXPIRED", "This SCIM token has expired; create a new token instead")
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE scim_tokens SET revoked_at = now() WHERE id = $1 AND tenant_id = $2
	`, tokenID, sess.ActiveTenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not rotate the SCIM token")
		return
	}
	rawToken, prefix, tokenHash := newSCIMTokenMaterial()
	var expiresParam any
	if expiresAt != nil {
		expiresParam = *expiresAt
	}
	record := scimTokenRecord{Name: name, Prefix: prefix, ExpiresAt: expiresAt}
	if err := tx.QueryRow(ctx, `
		INSERT INTO scim_tokens (tenant_id, name, token_hash, token_prefix, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, created_at
	`, sess.ActiveTenantID, name, tokenHash, prefix, sess.User.ID, expiresParam).Scan(&record.ID, &record.CreatedAt); err != nil {
		// Never log the database error: a unique-violation detail can include
		// the token hash.
		s.logger.Warn("rotate SCIM token failed")
		writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not rotate the SCIM token")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not rotate the SCIM token")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.scim_token_rotated", name, "Rotated SCIM provisioning token", map[string]any{
		"tokenId": record.ID, "replacedTokenId": tokenID,
	})
	payload := scimTokenPayload(record)
	// The raw token is intentionally returned exactly once, on rotation.
	payload["token"] = rawToken
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) adminRevokeSCIMToken(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tokenID := r.PathValue("id")
	var name string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE scim_tokens SET revoked_at = now()
		WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL
		RETURNING name
	`, tokenID, sess.ActiveTenantID).Scan(&name)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "SCIM_TOKEN", "Could not revoke the SCIM token")
			return
		}
		writeError(w, http.StatusNotFound, "SCIM_TOKEN_NOT_FOUND", "SCIM token not found or already revoked")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.scim_token_revoked", name, "Revoked SCIM provisioning token", map[string]any{"tokenId": tokenID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
