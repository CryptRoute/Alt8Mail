package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
)

const (
	adminSessionCookie = "a8m_admin_sid"
	adminCSRFCookie    = "a8m_admin_csrf"
)

type adminTenant struct {
	ID           string
	Slug         string
	Name         string
	Status       string
	Plan         string
	Region       string
	BillingEmail string
	Settings     json.RawMessage
	CreatedAt    time.Time
	SuspendedAt  *time.Time
}

type adminUser struct {
	ID                   string
	TenantID             string
	Email                string
	Name                 string
	Initials             string
	Color                string
	Role                 string
	Status               string
	PasswordHash         string
	Security             json.RawMessage
	IsSystemAdmin        bool
	Title                string
	Department           string
	EmployeeID           string
	ManagerEmail         string
	OrgUnitID            string
	ForcePasswordChange  bool
	DeletedAt            *time.Time
	QuotaBytes           int64
	UsedBytes            int64
	MailboxStatus        string
	MaildirPath          string
	MailboxProvisionedAt *time.Time
	CreatedAt            time.Time
	LastLoginAt          *time.Time
	EnrollmentRequired   bool
	HomeTenant           adminTenant
}

func (u adminUser) userRecord() userRecord {
	return userRecord{
		ID: u.ID, TenantID: u.TenantID, Email: u.Email, Name: u.Name,
		Initials: u.Initials, Color: u.Color, Role: u.Role, Status: u.Status,
		PasswordHash: u.PasswordHash, Security: u.Security,
		QuotaBytes: u.QuotaBytes, UsedBytes: u.UsedBytes, EnrollmentRequired: u.EnrollmentRequired,
	}
}

type adminSessionRecord struct {
	ID             string
	UserID         string
	TenantID       string
	ActiveTenantID string
	CSRF           string
	ExpiresAt      time.Time
	LastSeenAt     time.Time
	User           adminUser
	ActiveTenant   adminTenant
	Permissions    map[string]bool
}

func (s *adminSessionRecord) can(permission string) bool {
	if s == nil {
		return false
	}
	return s.Permissions["*"] || s.Permissions[permission]
}

type adminSessionContextKey struct{}

func (s *Server) AdminHandler() http.Handler {
	api := http.NewServeMux()
	s.registerAdminRoutes(api)
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Admin API route not found")
	})

	root := http.NewServeMux()
	root.Handle("/admin/api/", http.StripPrefix("/admin/api", api))
	root.Handle("/api/", http.StripPrefix("/api", api))
	root.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})
	root.HandleFunc("/admin/", s.serveAdminStatic)
	root.HandleFunc("/", s.serveAdminStatic)
	return s.adminMiddleware(root)
}

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.adminHealth)
	mux.HandleFunc("POST /session", s.adminLogin)
	mux.HandleFunc("POST /session/2fa", s.adminVerifyTwoFactor)
	mux.HandleFunc("GET /session", s.adminCurrentSession)
	mux.HandleFunc("DELETE /session", s.adminRequire(s.adminLogout))
	mux.HandleFunc("GET /bootstrap", s.adminRequire(s.adminBootstrap))
	mux.HandleFunc("POST /context", s.adminRequire(s.adminSwitchTenant))
	mux.HandleFunc("GET /tenants", s.adminRequirePermission("system.tenants.manage", s.adminListTenants))
	mux.HandleFunc("POST /tenants", s.adminRequirePermission("system.tenants.manage", s.adminCreateTenant))
	mux.HandleFunc("PATCH /tenants/{id}", s.adminRequirePermission("system.tenants.manage", s.adminUpdateTenant))
	mux.HandleFunc("GET /system/admins", s.adminRequirePermission("system.admins.manage", s.adminListSystemAdmins))
	mux.HandleFunc("POST /system/admins", s.adminRequirePermission("system.admins.manage", s.adminGrantSystemAdmin))
	mux.HandleFunc("DELETE /system/admins/{id}", s.adminRequirePermission("system.admins.manage", s.adminRevokeSystemAdmin))

	mux.HandleFunc("GET /users", s.adminRequirePermission("users.read", s.adminListUsers))
	mux.HandleFunc("GET /users/domains", s.adminRequirePermission("users.create", s.adminUserCreateDomains))
	mux.HandleFunc("POST /users", s.adminRequirePermission("users.create", s.adminCreateUser))
	mux.HandleFunc("GET /users/{id}", s.adminRequirePermission("users.read", s.adminGetUser))
	mux.HandleFunc("PATCH /users/{id}", s.adminRequirePermission("users.update", s.adminUpdateUser))
	mux.HandleFunc("DELETE /users/{id}", s.adminRequirePermission("users.delete", s.adminDeleteUser))
	mux.HandleFunc("POST /users/{id}/restore", s.adminRequirePermission("users.delete", s.adminRestoreUser))
	mux.HandleFunc("POST /users/{id}/reset-password", s.adminRequirePermission("users.reset_password", s.adminResetUserPassword))
	mux.HandleFunc("POST /users/{id}/reset-mfa", s.adminRequirePermission("users.security", s.adminResetUserMFA))
	mux.HandleFunc("POST /users/{id}/revoke-sessions", s.adminRequirePermission("sessions.manage", s.adminRevokeUserSessions))
	mux.HandleFunc("POST /users/{id}/aliases", s.adminRequirePermission("users.aliases", s.adminAddUserAlias))
	mux.HandleFunc("DELETE /users/{id}/aliases/{aliasId}", s.adminRequirePermission("users.aliases", s.adminDeleteUserAlias))
	mux.HandleFunc("POST /users/bulk", s.adminRequirePermission("users.update", s.adminBulkUsers))
	mux.HandleFunc("GET /users/{id}/sessions", s.adminRequirePermission("sessions.manage", s.adminUserSessions))

	mux.HandleFunc("GET /groups", s.adminRequirePermission("groups.read", s.adminListGroups))
	mux.HandleFunc("POST /groups", s.adminRequirePermission("groups.manage", s.adminCreateGroup))
	mux.HandleFunc("GET /groups/{id}", s.adminRequirePermission("groups.read", s.adminGetGroup))
	mux.HandleFunc("PATCH /groups/{id}", s.adminRequirePermission("groups.manage", s.adminUpdateGroup))
	mux.HandleFunc("DELETE /groups/{id}", s.adminRequirePermission("groups.manage", s.adminDeleteGroup))
	mux.HandleFunc("POST /groups/{id}/members", s.adminRequirePermission("groups.manage", s.adminAddGroupMember))
	mux.HandleFunc("DELETE /groups/{id}/members/{userId}", s.adminRequirePermission("groups.manage", s.adminRemoveGroupMember))

	mux.HandleFunc("GET /org-units", s.adminRequirePermission("orgunits.read", s.adminListOrgUnits))
	mux.HandleFunc("POST /org-units", s.adminRequirePermission("orgunits.manage", s.adminCreateOrgUnit))
	mux.HandleFunc("PATCH /org-units/{id}", s.adminRequirePermission("orgunits.manage", s.adminUpdateOrgUnit))
	mux.HandleFunc("DELETE /org-units/{id}", s.adminRequirePermission("orgunits.manage", s.adminDeleteOrgUnit))

	mux.HandleFunc("GET /roles", s.adminRequirePermission("roles.read", s.adminListRoles))
	mux.HandleFunc("POST /roles", s.adminRequirePermission("roles.manage", s.adminCreateRole))
	mux.HandleFunc("PATCH /roles/{id}", s.adminRequirePermission("roles.manage", s.adminUpdateRole))
	mux.HandleFunc("DELETE /roles/{id}", s.adminRequirePermission("roles.manage", s.adminDeleteRole))
	mux.HandleFunc("POST /roles/{id}/assign", s.adminRequirePermission("roles.manage", s.adminAssignRole))
	mux.HandleFunc("DELETE /roles/{id}/assign/{userId}", s.adminRequirePermission("roles.manage", s.adminUnassignRole))

	mux.HandleFunc("GET /policies", s.adminRequirePermission("policies.read", s.adminGetPolicies))
	mux.HandleFunc("PATCH /policies", s.adminRequirePermission("policies.manage", s.adminUpdatePolicies))
	mux.HandleFunc("GET /ai", s.adminRequirePermission("policies.read", s.adminGetAI))
	mux.HandleFunc("PATCH /ai", s.adminRequirePermission("policies.manage", s.adminUpdateAI))
	mux.HandleFunc("POST /ai/test", s.adminRequirePermission("policies.manage", s.adminTestAI))
	mux.HandleFunc("POST /maintenance/retention", s.adminRequirePermission("policies.manage", s.adminRunRetention))

	mux.HandleFunc("GET /domains", s.adminRequirePermission("domains.read", s.adminListDomains))
	mux.HandleFunc("POST /domains", s.adminRequirePermission("domains.manage", s.adminCreateDomain))
	mux.HandleFunc("PATCH /domains/{id}", s.adminRequirePermission("domains.manage", s.adminUpdateDomain))
	mux.HandleFunc("DELETE /domains/{id}", s.adminRequirePermission("domains.manage", s.adminDeleteDomain))
	mux.HandleFunc("POST /domains/{id}/verify", s.adminRequirePermission("domains.manage", s.adminVerifyDomain))

	mux.HandleFunc("GET /audit", s.adminRequirePermission("audit.read", s.adminListAudit))
	mux.HandleFunc("GET /audit.csv", s.adminRequirePermission("audit.read", s.adminExportAudit))
	mux.HandleFunc("GET /quarantine", s.adminRequirePermission("users.security", s.adminListQuarantine))
	mux.HandleFunc("POST /quarantine/{userId}/{messageId}/release", s.adminRequirePermission("users.security", s.adminReleaseQuarantine))

	mux.HandleFunc("GET /security", s.adminRequire(s.adminSecurityOverview))
	mux.HandleFunc("POST /security/totp/begin", s.adminRequire(s.adminBeginTOTP))
	mux.HandleFunc("POST /security/totp/finish", s.adminRequire(s.adminFinishTOTP))
	mux.HandleFunc("POST /security/totp/disable", s.adminRequire(s.adminDisableTOTP))
	mux.HandleFunc("GET /security/sessions", s.adminRequire(s.adminListAdminSessions))
	mux.HandleFunc("POST /security/sessions/revoke-others", s.adminRequire(s.adminRevokeOtherAdminSessions))

	mux.HandleFunc("GET /settings", s.adminRequirePermission("tenant.read", s.adminGetTenantSettings))
	mux.HandleFunc("PATCH /settings", s.adminRequirePermission("tenant.manage", s.adminUpdateTenantSettings))
}

func (s *Server) adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; font-src 'self'; worker-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		if strings.Contains(r.URL.Path, "/api/") || strings.HasSuffix(r.URL.Path, "/api") {
			w.Header().Set("Cache-Control", "no-store")
		}
		ww := &statusWriter{ResponseWriter: w}
		defer func() {
			s.logger.Info("admin request",
				"method", r.Method, "path", r.URL.Path, "status", ww.status,
				"duration_ms", time.Since(start).Milliseconds(), "ip", clientIP(r),
			)
		}()
		next.ServeHTTP(ww, r.WithContext(context.WithValue(r.Context(), httpRequestContextKey{}, r)))
	})
}

func (s *Server) serveAdminStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestPath := r.URL.Path
	if strings.HasPrefix(requestPath, "/admin/") {
		requestPath = strings.TrimPrefix(requestPath, "/admin/")
	} else {
		requestPath = strings.TrimPrefix(requestPath, "/")
	}
	if requestPath == "" {
		requestPath = "index.html"
	}
	if strings.Contains(requestPath, "..") || strings.Contains(requestPath, "\\") {
		http.NotFound(w, r)
		return
	}
	fullPath := filepath.Join(s.cfg.AdminDir, filepath.FromSlash(requestPath))
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if requestPath == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	http.ServeFile(w, r, fullPath)
}

func (s *Server) adminHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	dbStatus, redisStatus := "ok", "ok"
	if err := s.store.DB.Ping(ctx); err != nil {
		dbStatus = "error"
	}
	if err := s.store.Redis.Ping(ctx).Err(); err != nil {
		redisStatus = "error"
	}
	status := http.StatusOK
	if dbStatus != "ok" || redisStatus != "ok" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"ok": status == http.StatusOK, "service": "a8m-admin-backend",
		"version": s.version, "env": s.cfg.Env, "time": time.Now().UTC().Format(time.RFC3339),
		"checks": map[string]string{"postgres": dbStatus, "redis": redisStatus},
	})
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "BAD_CREDENTIALS", "Email and password are required")
		return
	}
	ip := clientIP(r)
	if !s.adminLimit.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many admin sign-in attempts. Try again later.")
		return
	}
	user, err := s.adminUserByEmail(r.Context(), email)
	passwordOK := false
	if err == nil && user.PasswordHash != "" {
		passwordOK = auth.VerifyPassword(user.PasswordHash, body.Password)
	}
	if err != nil || !passwordOK {
		s.adminLimit.Fail(ip)
		s.recordAdminAudit(r.Context(), nil, user, "admin.login_failed", email, "Failed admin password sign-in", nil)
		writeError(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "That email and password do not match a Alt8Mail account")
		return
	}
	if user.Status != "active" || user.DeletedAt != nil {
		s.adminLimit.Fail(ip)
		writeError(w, http.StatusForbidden, "ACCOUNT_DISABLED", "This account is suspended or closed")
		return
	}
	permissions, err := s.adminPermissions(r.Context(), user.ID, user.TenantID, user.IsSystemAdmin, user.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PERMISSIONS", "Could not load admin permissions")
		return
	}
	if len(permissions) == 0 {
		s.recordAdminAudit(r.Context(), nil, user, "admin.login_denied", email, "Account has no admin entitlement", nil)
		writeError(w, http.StatusForbidden, "NOT_ADMIN", "This account does not have access to the administration console")
		return
	}
	policy := tenantPolicyFromSettings(user.HomeTenant.Settings)
	if !user.IsSystemAdmin && !adminIPAllowed(policy.Security.AllowedIPRanges, ip) {
		s.recordAdminAudit(r.Context(), nil, user, "admin.login_denied", email, "Client IP is outside the tenant allowlist", map[string]any{"ip": ip})
		writeError(w, http.StatusForbidden, "IP_NOT_ALLOWED", "This network is not allowed to access the administration console")
		return
	}
	s.adminLimit.Reset(ip)
	userRecordValue := user.userRecord()
	_, methods, _, remaining := s.securityState(r.Context(), userRecordValue)
	hasAuthenticator, hasPasskey := false, false
	for _, method := range methods {
		if method == "authenticator" {
			hasAuthenticator = true
		}
		if method == "passkey" {
			hasPasskey = true
		}
	}
	if hasAuthenticator {
		challenge, err := s.createAdminChallenge(r.Context(), user, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "MFA", "Could not start second-factor verification")
			return
		}
		s.recordAdminAudit(r.Context(), nil, user, "admin.challenge", email, "Password verified; authenticator code required", nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"twoFactorRequired": true,
			"challengeId":       challenge,
			"expiresAt":         time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
			"email":             user.Email, "name": user.Name,
			"methods": map[string]any{"authenticator": true, "recovery": remaining > 0},
		})
		return
	}
	requiresMFA := policy.Security.MFAEnforcement == "admins" || policy.Security.MFAEnforcement == "all"
	if requiresMFA {
		s.recordAdminAudit(r.Context(), nil, user, "admin.mfa_required", email, "Tenant policy requires an admin second factor", nil)
		writeError(w, http.StatusForbidden, "MFA_ENROLLMENT_REQUIRED", "Your organization requires multi-factor authentication for admins. Add an authenticator in the webmail Security settings, then sign in here.")
		return
	}
	if hasPasskey {
		writeError(w, http.StatusForbidden, "ADMIN_MFA_METHOD", "This admin account uses a passkey only. Add an authenticator app in webmail Security settings to access the admin console, or ask another system administrator for access.")
		return
	}
	s.completeAdminLogin(w, r, user, "Password sign-in")
}

func (s *Server) adminVerifyTwoFactor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
		Method      string `json:"method"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	challenge, err := s.adminChallengeByID(r.Context(), body.ChallengeID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MFA_CHALLENGE", "This verification request has expired. Sign in again.")
		return
	}
	if time.Now().After(challenge.expires) {
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_auth_challenges WHERE id = $1`, challenge.id)
		writeError(w, http.StatusGone, "MFA_EXPIRED", "This verification request has expired. Sign in again.")
		return
	}
	if challenge.attempts >= 5 {
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_auth_challenges WHERE id = $1`, challenge.id)
		writeError(w, http.StatusTooManyRequests, "MFA_LOCKED", "Too many verification attempts. Sign in again.")
		return
	}
	user, err := s.adminUserByID(r.Context(), challenge.userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MFA_CHALLENGE", "This account is no longer available")
		return
	}
	userRecordValue := user.userRecord()
	verified := false
	switch body.Method {
	case "", "totp", "authenticator":
		secret, err := s.decryptTOTPSecret(userRecordValue)
		if err == nil && auth.ValidateTOTP(secret, body.Code, time.Now(), 1) {
			verified = true
		}
	case "recovery":
		if s.consumeRecoveryCode(r.Context(), user.ID, body.Code) {
			verified = true
		}
	default:
		writeError(w, http.StatusBadRequest, "MFA_METHOD", "Choose a valid verification method")
		return
	}
	if !verified {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE admin_auth_challenges SET attempts = attempts + 1 WHERE id = $1`, challenge.id)
		s.recordAdminAudit(r.Context(), nil, user, "admin.mfa_failed", user.Email, "Invalid admin second factor", nil)
		writeError(w, http.StatusUnauthorized, "MFA_INVALID", "That verification code did not match")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_auth_challenges WHERE id = $1`, challenge.id)
	s.completeAdminLogin(w, r, user, "Password + second factor sign-in")
}

func (s *Server) completeAdminLogin(w http.ResponseWriter, r *http.Request, user adminUser, detail string) {
	activeTenantID := user.TenantID
	if user.IsSystemAdmin {
		var first string
		_ = s.store.DB.QueryRow(r.Context(), `SELECT id::text FROM tenants ORDER BY created_at LIMIT 1`).Scan(&first)
		if first != "" {
			activeTenantID = first
		}
	}
	token := randomToken(32)
	csrf := randomToken(24)
	ttl := s.cfg.AdminSessionTTL
	if activeTenant, err := s.adminTenantByID(r.Context(), activeTenantID); err == nil {
		if minutes := tenantPolicyFromSettings(activeTenant.Settings).Security.SessionTimeoutMinutes; minutes > 0 && time.Duration(minutes)*time.Minute < ttl {
			ttl = time.Duration(minutes) * time.Minute
		}
	}
	expires := time.Now().Add(ttl)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO admin_sessions (id, user_id, tenant_id, active_tenant_id, csrf_token, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::inet, $7, $8)
	`, hashToken(token), user.ID, user.TenantID, activeTenantID, csrf, clientIP(r), sanitizeUserAgent(r.UserAgent()), expires)
	if err != nil {
		s.logger.Error("create admin session", "err", err)
		writeError(w, http.StatusInternalServerError, "SESSION", "Could not create an admin session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(ttl.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: adminCSRFCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(ttl.Seconds())})
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1`, user.ID)
	sess := &adminSessionRecord{User: user, Permissions: map[string]bool{"*": true}, ActiveTenantID: activeTenantID}
	s.recordAdminAudit(r.Context(), sess, user, "admin.login", user.Email, detail, map[string]any{"systemAdmin": user.IsSystemAdmin})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "csrf": csrf, "expiresAt": expires.UTC().Format(time.RFC3339),
		"admin":          map[string]any{"id": user.ID, "email": user.Email, "name": user.Name, "isSystemAdmin": user.IsSystemAdmin},
		"activeTenantId": activeTenantID,
		"serverTime":     time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) adminCurrentSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.adminAuthenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to the administration console")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"admin": s.adminPayload(sess), "csrf": sess.CSRF, "expiresAt": sess.ExpiresAt.UTC().Format(time.RFC3339)})
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_sessions WHERE id = $1`, sess.ID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.logout", sess.User.Email, "Signed out of admin console", nil)
	s.clearAdminCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminBootstrap(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tenants, err := s.adminTenantsFor(sess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TENANTS", "Could not load organizations")
		return
	}
	stats, err := s.adminTenantStats(r.Context(), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STATS", "Could not load organization statistics")
		return
	}
	policy := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	writeJSON(w, http.StatusOK, map[string]any{
		"admin":        s.adminPayload(sess),
		"tenants":      tenants,
		"activeTenant": adminTenantPayload(sess.ActiveTenant),
		"stats":        stats,
		"policy":       policy,
		"permissions":  sortedPermissionNames(sess.Permissions),
		"catalog":      adminPermissionCatalog,
		"server": map[string]any{
			"version": s.version, "env": s.cfg.Env, "time": time.Now().UTC().Format(time.RFC3339),
			"adminURL": s.cfg.AdminPublicURL,
		},
		"capabilities": adminCapabilities(),
	})
}

func (s *Server) adminSwitchTenant(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	if !sess.User.IsSystemAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only system administrators can switch organizations")
		return
	}
	var body struct {
		TenantID string `json:"tenantId"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	tenant, err := s.adminTenantByID(r.Context(), body.TenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "TENANT_NOT_FOUND", "Organization not found")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `UPDATE admin_sessions SET active_tenant_id = $2, last_seen_at = now() WHERE id = $1`, sess.ID, tenant.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "CONTEXT", "Could not switch organization")
		return
	}
	sess.ActiveTenant = tenant
	sess.ActiveTenantID = tenant.ID
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.tenant_switch", tenant.Name, "Switched active organization", map[string]any{"tenantId": tenant.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "activeTenant": adminTenantPayload(tenant)})
}

func (s *Server) adminTenantsFor(sess *adminSessionRecord) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(context.Background(), `
		SELECT t.id::text, t.slug, t.name, t.status, t.plan, t.region, COALESCE(t.billing_email::text, ''), t.settings, t.created_at, t.suspended_at
		FROM tenants t
		WHERE ($1::boolean OR t.id = $2)
		ORDER BY t.name
	`, sess.User.IsSystemAdmin, sess.User.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var t adminTenant
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Plan, &t.Region, &t.BillingEmail, &t.Settings, &t.CreatedAt, &t.SuspendedAt); err != nil {
			continue
		}
		result = append(result, adminTenantPayload(t))
	}
	return result, rows.Err()
}

func adminTenantPayload(t adminTenant) map[string]any {
	payload := map[string]any{
		"id": t.ID, "slug": t.Slug, "name": t.Name, "status": t.Status,
		"plan": t.Plan, "region": t.Region, "billingEmail": t.BillingEmail, "createdAt": t.CreatedAt.UTC().Format(time.RFC3339),
	}
	if t.SuspendedAt != nil {
		payload["suspendedAt"] = t.SuspendedAt.UTC().Format(time.RFC3339)
	}
	return payload
}

func (s *Server) adminPayload(sess *adminSessionRecord) map[string]any {
	security := s.mustSecuritySummary(context.Background(), sess.User.userRecord())
	payload := map[string]any{
		"id": sess.User.ID, "email": sess.User.Email, "name": sess.User.Name,
		"initials": sess.User.Initials, "color": sess.User.Color, "role": sess.User.Role,
		"title": sess.User.Title, "department": sess.User.Department,
		"isSystemAdmin": sess.User.IsSystemAdmin, "activeTenantId": sess.ActiveTenantID,
		"permissions": sortedPermissionNames(sess.Permissions), "security": security,
	}
	return payload
}

func (s *Server) adminAuthenticate(r *http.Request) (*adminSessionRecord, error) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, errors.New("no admin session cookie")
	}
	var sess adminSessionRecord
	var deletedAt *time.Time
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT s.id, s.user_id::text, s.tenant_id::text, s.active_tenant_id::text, s.csrf_token,
		       s.expires_at, s.last_seen_at, u.id::text, u.tenant_id::text, u.email::text, u.name,
		       u.initials, u.color, u.role, u.status, COALESCE(u.password_hash, ''), u.security,
		       u.is_system_admin, u.title, u.department, u.employee_id,
		       COALESCE(u.manager_email::text, ''), COALESCE(u.org_unit_id::text, ''),
		       u.force_password_change, u.deleted_at
		FROM admin_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now()
	`, hashToken(cookie.Value)).Scan(
		&sess.ID, &sess.UserID, &sess.TenantID, &sess.ActiveTenantID, &sess.CSRF,
		&sess.ExpiresAt, &sess.LastSeenAt, &sess.User.ID, &sess.User.TenantID,
		&sess.User.Email, &sess.User.Name, &sess.User.Initials, &sess.User.Color,
		&sess.User.Role, &sess.User.Status, &sess.User.PasswordHash, &sess.User.Security,
		&sess.User.IsSystemAdmin, &sess.User.Title, &sess.User.Department, &sess.User.EmployeeID,
		&sess.User.ManagerEmail, &sess.User.OrgUnitID, &sess.User.ForcePasswordChange, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	sess.User.DeletedAt = deletedAt
	if sess.User.Status != "active" || deletedAt != nil {
		return nil, errors.New("admin account is not active")
	}
	homeTenant, err := s.adminTenantByID(r.Context(), sess.User.TenantID)
	if err != nil {
		return nil, err
	}
	sess.User.HomeTenant = homeTenant
	if !sess.User.IsSystemAdmin && sess.ActiveTenantID != sess.User.TenantID {
		sess.ActiveTenantID = sess.User.TenantID
	}
	activeTenant, err := s.adminTenantByID(r.Context(), sess.ActiveTenantID)
	if err != nil {
		return nil, err
	}
	sess.ActiveTenant = activeTenant
	policy := tenantPolicyFromSettings(activeTenant.Settings)
	if policy.Security.SessionTimeoutMinutes > 0 && time.Since(sess.LastSeenAt) > time.Duration(policy.Security.IdleTimeoutMinutes)*time.Minute {
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM admin_sessions WHERE id = $1`, sess.ID)
		return nil, errors.New("admin session idle timeout")
	}
	permissions, err := s.adminPermissions(r.Context(), sess.User.ID, activeTenant.ID, sess.User.IsSystemAdmin, sess.User.Role)
	if err != nil {
		return nil, err
	}
	if len(permissions) == 0 {
		return nil, errors.New("no admin entitlement")
	}
	sess.Permissions = permissions
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE admin_sessions SET last_seen_at = now() WHERE id = $1`, sess.ID)
	return &sess, nil
}

func (s *Server) adminRequire(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.adminAuthenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to the administration console")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !adminCSRFValid(r, sess) {
				writeError(w, http.StatusForbidden, "CSRF", "Missing or invalid CSRF token")
				return
			}
		}
		ctx := context.WithValue(r.Context(), adminSessionContextKey{}, sess)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) adminRequirePermission(permission string, next http.HandlerFunc) http.HandlerFunc {
	return s.adminRequire(func(w http.ResponseWriter, r *http.Request) {
		sess := s.requireAdminSession(r)
		if !sess.can(permission) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this action")
			return
		}
		next(w, r)
	})
}

func (s *Server) requireAdminSession(r *http.Request) *adminSessionRecord {
	sess, _ := r.Context().Value(adminSessionContextKey{}).(*adminSessionRecord)
	return sess
}

func adminCSRFValid(r *http.Request, sess *adminSessionRecord) bool {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRF)) == 1
}

func (s *Server) clearAdminCookies(w http.ResponseWriter) {
	for _, name := range []string{adminSessionCookie, adminCSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == adminSessionCookie, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	}
}

func (s *Server) adminUserByEmail(ctx context.Context, email string) (adminUser, error) {
	return s.adminUserQuery(ctx, `lower(u.email) = lower($1)`, email)
}

func (s *Server) adminUserByID(ctx context.Context, id string) (adminUser, error) {
	return s.adminUserQuery(ctx, `u.id = $1`, id)
}

func (s *Server) adminUserQuery(ctx context.Context, where string, arg any) (adminUser, error) {
	var user adminUser
	var deletedAt *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color,
		       u.role, u.status, COALESCE(u.password_hash, ''), u.security, u.is_system_admin,
		       u.title, u.department, u.employee_id, COALESCE(u.manager_email::text, ''),
		       COALESCE(u.org_unit_id::text, ''), u.force_password_change, u.deleted_at,
		       u.quota_bytes, u.used_bytes, u.created_at, u.last_login_at, u.enrollment_required,
		       COALESCE(m.provision_status, ''), COALESCE(m.maildir_path, ''), m.provisioned_at,
		       t.id::text, t.slug, t.name, t.status, t.plan, t.region, COALESCE(t.billing_email::text, ''), t.settings, t.created_at, t.suspended_at
		FROM users u JOIN tenants t ON t.id = u.tenant_id
		LEFT JOIN mailboxes m ON m.user_id = u.id
		WHERE `+where+`
	`, arg).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color,
		&user.Role, &user.Status, &user.PasswordHash, &user.Security, &user.IsSystemAdmin,
		&user.Title, &user.Department, &user.EmployeeID, &user.ManagerEmail, &user.OrgUnitID,
		&user.ForcePasswordChange, &deletedAt, &user.QuotaBytes, &user.UsedBytes,
		&user.CreatedAt, &user.LastLoginAt, &user.EnrollmentRequired,
		&user.MailboxStatus, &user.MaildirPath, &user.MailboxProvisionedAt,
		&user.HomeTenant.ID, &user.HomeTenant.Slug, &user.HomeTenant.Name, &user.HomeTenant.Status,
		&user.HomeTenant.Plan, &user.HomeTenant.Region, &user.HomeTenant.BillingEmail, &user.HomeTenant.Settings,
		&user.HomeTenant.CreatedAt, &user.HomeTenant.SuspendedAt,
	)
	user.DeletedAt = deletedAt
	return user, err
}

func (s *Server) adminTenantByID(ctx context.Context, id string) (adminTenant, error) {
	var t adminTenant
	err := s.store.DB.QueryRow(ctx, `
		SELECT id::text, slug, name, status, plan, region, COALESCE(billing_email::text, ''), settings, created_at, suspended_at
		FROM tenants WHERE id = $1
	`, id).Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Plan, &t.Region, &t.BillingEmail, &t.Settings, &t.CreatedAt, &t.SuspendedAt)
	return t, err
}

type adminChallenge struct {
	id, userID string
	attempts   int
	expires    time.Time
}

func (s *Server) createAdminChallenge(ctx context.Context, user adminUser, r *http.Request) (string, error) {
	id := randomToken(24)
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO admin_auth_challenges (id, user_id, tenant_id, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, NULLIF($4, '')::inet, $5, $6)
	`, id, user.ID, user.TenantID, clientIP(r), sanitizeUserAgent(r.UserAgent()), time.Now().Add(5*time.Minute))
	return id, err
}

func (s *Server) adminChallengeByID(ctx context.Context, id string) (*adminChallenge, error) {
	var challenge adminChallenge
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, user_id::text, attempts, expires_at FROM admin_auth_challenges WHERE id = $1
	`, id).Scan(&challenge.id, &challenge.userID, &challenge.attempts, &challenge.expires)
	return &challenge, err
}

func (s *Server) recordAdminAudit(ctx context.Context, sess *adminSessionRecord, user adminUser, action, target, detail string, metadata map[string]any) {
	tenantID := ""
	actorID := ""
	actorEmail := user.Email
	if sess != nil {
		tenantID = sess.ActiveTenantID
		actorID = sess.User.ID
		actorEmail = sess.User.Email
	} else if user.ID != "" {
		tenantID = user.TenantID
		actorID = user.ID
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, _ := json.Marshal(metadata)
	var tenant any
	if tenantID != "" {
		tenant = tenantID
	}
	var actor any
	if actorID != "" {
		actor = actorID
	}
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO admin_audit_log (tenant_id, actor_id, actor_email, action, target, detail, ip, user_agent, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::inet, $8, $9)
	`, tenant, actor, actorEmail, action, truncate(target, 200), truncate(detail, 1000), clientIPFromContext(ctx), "", encoded)
	if err != nil {
		s.logger.Warn("admin audit insert failed", "err", err, "action", action)
	}
}

func clientIPFromContext(ctx context.Context) string {
	if r, ok := ctx.Value(httpRequestContextKey{}).(*http.Request); ok && r != nil {
		return clientIP(r)
	}
	return ""
}

type httpRequestContextKey struct{}

func adminIPAllowed(ranges []string, ip string) bool {
	if len(ranges) == 0 || ip == "" {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, raw := range ranges {
		if _, network, err := net.ParseCIDR(raw); err == nil && network.Contains(parsed) {
			return true
		}
		if candidate := net.ParseIP(raw); candidate != nil && candidate.Equal(parsed) {
			return true
		}
	}
	return false
}

func adminCapabilities() map[string]any {
	return map[string]any{
		"tenantSwitch": true, "customRoles": true, "groups": true, "orgUnits": true,
		"domainVerification": true, "auditExport": true, "bulkUsers": true,
		"mfaAdmin": true, "policyManagement": true, "softDelete": true,
	}
}

func formatAdminTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

var _ = fmt.Sprintf
var _ = strings.TrimSpace
