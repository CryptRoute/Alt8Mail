package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
)

const (
	// Distinct cookie names from the current reference feature preview. Both
	// services share the staging origin until the Go API replaces the preview.
	sessionCookie = "a8m_system_sid"
	csrfCookie    = "a8m_system_csrf"
)

type Server struct {
	cfg             config.Config
	store           *store.Store
	logger          *slog.Logger
	version         string
	limit           *loginLimiter
	davLimit        *loginLimiter
	adminLimit      *loginLimiter
	stateLocksMu    sync.Mutex
	stateLocks      map[string]*sync.Mutex
	mailUserMu      sync.Mutex
	mailLocks       map[string]*sync.Mutex
	davAuthMu       sync.Mutex
	davAuthCache    map[string]davAuthEntry
	sieveSyncMu     sync.Mutex
	sieveSyncActive map[string]bool
	sieveSyncAgain  map[string]bool
	webAuthn        *webauthn.WebAuthn
}

type Options struct {
	Config  config.Config
	Store   *store.Store
	Logger  *slog.Logger
	Version string
}

func New(opts Options) *Server {
	server := &Server{
		cfg:             opts.Config,
		store:           opts.Store,
		logger:          opts.Logger,
		version:         opts.Version,
		limit:           newLoginLimiter(10, 10*time.Minute),
		davLimit:        newLoginLimiter(30, 10*time.Minute),
		stateLocks:      map[string]*sync.Mutex{},
		mailLocks:       map[string]*sync.Mutex{},
		davAuthCache:    map[string]davAuthEntry{},
		sieveSyncActive: map[string]bool{},
		sieveSyncAgain:  map[string]bool{},
		adminLimit:      newLoginLimiter(opts.Config.AdminRateLimitMax, opts.Config.AdminRateWindow),
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          opts.Config.WebAuthnRPID,
		RPDisplayName: "Alt8Mail",
		RPOrigins:     opts.Config.WebAuthnOrigins,
	})
	if err != nil {
		opts.Logger.Warn("WebAuthn initialization failed", "err", err)
	} else {
		server.webAuthn = wa
	}
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/mta-sts.txt", s.serveMTASTSPolicy)
	mux.HandleFunc("/.well-known/caldav", s.davWellKnown)
	mux.HandleFunc("/.well-known/carddav", s.davWellKnown)
	mux.HandleFunc("/dav/", s.handleDAV)
	mux.HandleFunc("GET /files/s/{token}", s.publicSharePage)
	mux.HandleFunc("GET /api/public/shares/{token}", s.publicShareMetadata)
	mux.HandleFunc("GET /api/public/shares/{token}/content", s.publicShareContent)
	mux.HandleFunc("POST /api/public/shares/{token}/replace", s.publicShareReplace)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/ready", s.health)
	mux.HandleFunc("POST /api/session", s.login)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("DELETE /api/session", s.requireUser(s.logout))
	mux.HandleFunc("POST /api/session/2fa", s.verifyTwoFactor)
	mux.HandleFunc("POST /api/session/2fa/webauthn/begin", s.beginWebAuthnLogin)
	mux.HandleFunc("POST /api/session/2fa/webauthn/finish", s.finishWebAuthnLogin)
	mux.HandleFunc("POST /api/enrollment/totp/begin", s.beginEnrollmentTOTP)
	mux.HandleFunc("POST /api/enrollment/totp/finish", s.finishEnrollmentTOTP)
	mux.HandleFunc("POST /api/enrollment/webauthn/begin", s.beginEnrollmentWebAuthn)
	mux.HandleFunc("POST /api/enrollment/webauthn/finish", s.finishEnrollmentWebAuthn)
	mux.HandleFunc("GET /api/bootstrap", s.requireUser(s.bootstrap))
	mux.HandleFunc("GET /api/folders", s.requireUser(s.getFolders))
	mux.HandleFunc("GET /api/aliases", s.requireUser(s.getAliases))
	mux.HandleFunc("POST /api/password/change", s.requireUser(s.changePassword))
	mux.HandleFunc("GET /api/preferences", s.requireUser(s.getPreferences))
	mux.HandleFunc("PATCH /api/preferences", s.requireUser(s.patchPreferencesState))
	mux.HandleFunc("POST /api/preferences/reset", s.requireUser(s.resetPreferencesState))

	mux.HandleFunc("GET /api/messages", s.requireUser(s.getMessages))
	mux.HandleFunc("PATCH /api/messages/{id}", s.requireUser(s.patchMessage))
	mux.HandleFunc("POST /api/messages/read", s.requireUser(s.markRead))
	mux.HandleFunc("POST /api/messages/actions", s.requireUser(s.messageActions))
	mux.HandleFunc("GET /api/messages/{id}/trust", s.requireUser(s.messageTrust))
	mux.HandleFunc("POST /api/messages/{id}/trust", s.requireUser(s.messageTrust))
	mux.HandleFunc("POST /api/undo/{token}", s.requireUser(s.undoAction))

	mux.HandleFunc("GET /api/drafts", s.requireUser(s.getDrafts))
	mux.HandleFunc("PUT /api/drafts/{id}", s.requireUser(s.saveDraft))
	mux.HandleFunc("POST /api/drafts/{id}", s.requireUser(s.saveDraft))
	mux.HandleFunc("DELETE /api/drafts/{id}", s.requireUser(s.deleteDraft))

	mux.HandleFunc("POST /api/send", s.requireUser(s.sendMessage))
	mux.HandleFunc("GET /api/outbox", s.requireUser(s.getOutbox))
	mux.HandleFunc("POST /api/outbox/{id}/cancel", s.requireUser(s.cancelSend))
	mux.HandleFunc("PATCH /api/outbox/{id}", s.requireUser(s.patchOutbox))
	mux.HandleFunc("POST /api/outbox/{id}/send-now", s.requireUser(s.sendNow))
	mux.HandleFunc("GET /api/quota", s.requireUser(s.getQuota))
	mux.HandleFunc("GET /api/outbound", s.requireUser(s.getOutboundInfo))
	mux.HandleFunc("GET /api/search", s.requireUser(s.search))
	mux.HandleFunc("GET /api/trust", s.requireUser(s.getTrustProfile))
	mux.HandleFunc("POST /api/mail/sync", s.requireUser(s.syncMailbox))
	mux.HandleFunc("GET /api/mail/sync", s.requireUser(s.mailSyncStatus))

	mux.HandleFunc("GET /api/tasks", s.requireUser(s.getTasks))
	mux.HandleFunc("POST /api/tasks", s.requireUser(s.createTask))
	mux.HandleFunc("PATCH /api/tasks/{id}", s.requireUser(s.updateTask))
	mux.HandleFunc("DELETE /api/tasks/{id}", s.requireUser(s.deleteTask))

	mux.HandleFunc("GET /api/storage", s.requireUser(s.getStorage))
	mux.HandleFunc("POST /api/storage/{id}/{action}", s.requireUser(s.storageAction))
	mux.HandleFunc("GET /api/storage/oauth/{provider}/start", s.requireUser(s.startStorageOAuth))
	mux.HandleFunc("GET /api/storage/oauth/{provider}/callback", s.storageOAuthCallback)
	mux.HandleFunc("GET /api/files", s.requireUser(s.getFiles))
	mux.HandleFunc("POST /api/files", s.requireUser(s.createFile))
	mux.HandleFunc("PATCH /api/files/{id}", s.requireUser(s.patchFile))
	mux.HandleFunc("DELETE /api/files/{id}", s.requireUser(s.deleteFile))
	mux.HandleFunc("POST /api/files/{id}/share", s.requireUser(s.shareFile))
	mux.HandleFunc("GET /api/files/{id}/content", s.requireUser(s.getFileContent))

	mux.HandleFunc("GET /api/calendar/events", s.requireUser(s.getCalendarEvents))
	mux.HandleFunc("POST /api/calendar/events", s.requireUser(s.createCalendarEvent))
	mux.HandleFunc("PATCH /api/calendar/events/{id}", s.requireUser(s.updateCalendarEvent))
	mux.HandleFunc("DELETE /api/calendar/events/{id}", s.requireUser(s.deleteCalendarEvent))

	mux.HandleFunc("GET /api/directory", s.requireUser(s.getDirectory))
	mux.HandleFunc("GET /api/contacts", s.requireUser(s.getContacts))
	mux.HandleFunc("POST /api/contacts", s.requireUser(s.createContact))
	mux.HandleFunc("PATCH /api/contacts/{id}", s.requireUser(s.updateContact))
	mux.HandleFunc("DELETE /api/contacts/{id}", s.requireUser(s.deleteContact))
	mux.HandleFunc("GET /api/workspace/audit", s.requireUser(s.getWorkspaceAudit))

	mux.HandleFunc("POST /api/attachments", s.requireUser(s.uploadAttachment))
	mux.HandleFunc("GET /api/attachments/{id}/content", s.requireUser(s.attachmentContent))
	mux.HandleFunc("GET /api/profile", s.requireUser(s.getProfile))
	mux.HandleFunc("PATCH /api/profile", s.requireUser(s.updateProfile))
	mux.HandleFunc("GET /api/security/app-passwords", s.requireUser(s.listAppPasswords))
	mux.HandleFunc("POST /api/security/app-passwords", s.requireUser(s.createAppPassword))
	mux.HandleFunc("DELETE /api/security/app-passwords/{id}", s.requireUser(s.deleteAppPassword))
	mux.HandleFunc("GET /api/security/recovery-codes", s.requireUser(s.getRecoveryState))
	mux.HandleFunc("POST /api/security/recovery-codes", s.requireUser(s.regenerateRecoveryState))
	mux.HandleFunc("POST /api/security/2fa/totp/begin", s.requireUser(s.beginTOTPEnrollment))
	mux.HandleFunc("POST /api/security/2fa/totp/finish", s.requireUser(s.finishTOTPEnrollment))
	mux.HandleFunc("POST /api/security/2fa/totp/disable", s.requireUser(s.disableTOTPHandler))
	mux.HandleFunc("POST /api/security/webauthn/register/begin", s.requireUser(s.beginWebAuthnRegistration))
	mux.HandleFunc("POST /api/security/webauthn/register/finish", s.requireUser(s.finishWebAuthnRegistration))
	mux.HandleFunc("DELETE /api/security/webauthn/{id}", s.requireUser(s.deletePasskey))
	mux.HandleFunc("GET /api/security/sessions", s.requireUser(s.listSessions))
	mux.HandleFunc("POST /api/security/sessions/revoke-others", s.requireUser(s.revokeOtherSessions))
	mux.HandleFunc("POST /api/security/sessions/{id}/revoke", s.requireUser(s.revokeSession))
	mux.HandleFunc("GET /api/security/policy", s.requireUser(s.getSecurityPolicy))
	mux.HandleFunc("PATCH /api/security/policy", s.requireUser(s.patchSecurityPolicy))

	mux.HandleFunc("GET /api/sieve", s.requireUser(s.getSieve))
	mux.HandleFunc("PUT /api/sieve", s.requireUser(s.saveSieve))
	mux.HandleFunc("GET /api/sieve/script", s.requireUser(s.getSieveScript))
	mux.HandleFunc("GET /api/forwarding", s.requireUser(s.getForwarding))
	mux.HandleFunc("PUT /api/forwarding", s.requireUser(s.saveForwarding))
	mux.HandleFunc("GET /api/identities", s.requireUser(s.getIdentities))
	mux.HandleFunc("POST /api/identities", s.requireUser(s.createIdentity))
	mux.HandleFunc("POST /api/identities/{id}/resend", s.requireUser(s.resendIdentity))
	mux.HandleFunc("DELETE /api/identities/{id}", s.requireUser(s.deleteIdentity))
	mux.HandleFunc("GET /api/identities/verify", s.identityVerifyPage)
	mux.HandleFunc("POST /api/identities/verify", s.identityVerifySubmit)
	mux.HandleFunc("POST /api/ai/messages/{id}/summary", s.requireUser(s.aiMessageSummary))
	mux.HandleFunc("POST /api/ai/messages/{id}/translate", s.requireUser(s.aiMessageTranslate))
	mux.HandleFunc("POST /api/ai/messages/{id}/reply", s.requireUser(s.aiMessageReply))
	mux.HandleFunc("POST /api/ai/compose", s.requireUser(s.aiCompose))
	mux.HandleFunc("GET /api/audit", s.requireUser(s.getAudit))
	mux.HandleFunc("GET /api/audit.csv", s.requireUser(s.getAuditCSV))
	mux.HandleFunc("/api/", s.apiNotFound)
	return s.middleware(mux)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		ww := &statusWriter{ResponseWriter: w}
		defer func() {
			s.logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"ip", clientIP(r),
			)
		}()
		next.ServeHTTP(ww, r)
	})
}

type userRecord struct {
	ID                  string
	TenantID            string
	Email               string
	Name                string
	Initials            string
	Color               string
	Role                string
	Status              string
	PasswordHash        string
	Preferences         json.RawMessage
	Security            json.RawMessage
	QuotaBytes          int64
	UsedBytes           int64
	EnrollmentRequired  bool
	ForcePasswordChange bool
	PasswordExpiresAt   *time.Time
}

type sessionRecord struct {
	ID        string
	UserID    string
	TenantID  string
	CSRF      string
	ExpiresAt time.Time
	User      userRecord
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Method   string `json:"method"`
		Remember *bool  `json:"remember"`
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
	if !s.limit.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many sign-in attempts. Try again later.")
		return
	}
	demoSignIn := s.demoLogin(email, body.Password)
	lookupEmail := email
	signInDetail := "Password sign-in · Go system backend"
	if demoSignIn {
		lookupEmail = s.cfg.DemoUserEmail
		signInDetail = "Staging demo sign-in · Go system backend"
	}
	user, err := s.userByEmail(r.Context(), lookupEmail)
	passwordOK := demoSignIn
	if !demoSignIn && err == nil && user.PasswordHash != "" {
		passwordOK = auth.VerifyPassword(user.PasswordHash, body.Password)
	}
	if err != nil || !passwordOK {
		s.limit.Fail(ip)
		s.audit(r, user, "auth.failed", email, "Failed password sign-in")
		writeError(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "That email and password do not match a Alt8Mail account")
		return
	}
	s.limit.Reset(ip)
	remember := true
	if body.Remember != nil {
		remember = *body.Remember
	}
	security, methods, passkeys, remaining := s.securityState(r.Context(), user)
	hasSecondFactor := false
	hasPasskey := false
	hasAuthenticator := false
	for _, method := range methods {
		if method == "passkey" {
			hasPasskey = true
			hasSecondFactor = true
		}
		if method == "authenticator" {
			hasAuthenticator = true
			hasSecondFactor = true
		}
	}
	_ = security
	policy := s.tenantPolicyFor(r.Context(), user.TenantID)
	if !adminIPAllowed(policy.Security.AllowedIPRanges, ip) {
		s.audit(r, user, "auth.ip_blocked", user.Email, "Sign-in blocked by tenant network policy")
		writeError(w, http.StatusForbidden, "IP_NOT_ALLOWED", "This network is not allowed to access this organization")
		return
	}
	policyRequiresMFA := policy.Security.MFAEnforcement == "all" ||
		(policy.Security.MFAEnforcement == "admins" && (user.Role == "owner" || user.Role == "admin"))
	if policyRequiresMFA && !hasSecondFactor {
		user.EnrollmentRequired = true
		signInDetail += " · policy MFA enrollment"
	}
	if hasSecondFactor {
		if s.trustedDeviceValid(r, user.ID) {
			s.completeLogin(w, r, user, remember, signInDetail+" · trusted device", map[string]any{
				"trustedDevice": true,
			})
			return
		}
		challenge, err := s.createAuthChallenge(r.Context(), user, "two_factor", remember, r)
		if err != nil {
			s.logger.Error("create 2fa challenge", "err", err)
			writeError(w, http.StatusInternalServerError, "TWO_FACTOR", "Could not start verification")
			return
		}
		s.audit(r, user, "auth.challenge", user.Email, "Password verified · second factor required")
		writeJSON(w, http.StatusOK, map[string]any{
			"twoFactorRequired": true,
			"challengeId":       challenge.ID,
			"expiresAt":         challenge.ExpiresAt.UTC().Format(time.RFC3339),
			"user": map[string]any{
				"name": user.Name, "email": user.Email, "initials": user.Initials,
				"color": user.Color, "role": user.Role,
			},
			"methods": map[string]any{
				"passkey": hasPasskey, "authenticator": hasAuthenticator, "recovery": remaining > 0,
			},
			"passkeys":   passkeys,
			"demoCode":   nil,
			"serverTime": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	if user.EnrollmentRequired {
		challenge, err := s.createAuthChallenge(r.Context(), user, "enrollment", remember, r)
		if err != nil {
			s.logger.Error("create enrollment challenge", "err", err)
			writeError(w, http.StatusInternalServerError, "ENROLLMENT", "Could not start enrollment")
			return
		}
		s.audit(r, user, "auth.enrollment_required", user.Email, "Password verified · first-time second-factor enrollment required")
		writeJSON(w, http.StatusOK, map[string]any{
			"enrollmentRequired": true,
			"enrollmentToken":    challenge.ID,
			"expiresAt":          challenge.ExpiresAt.UTC().Format(time.RFC3339),
			"user": map[string]any{
				"name": user.Name, "email": user.Email, "initials": user.Initials,
				"color": user.Color, "role": user.Role,
			},
			"serverTime": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	s.completeLogin(w, r, user, remember, signInDetail, nil)
}

func (s *Server) demoLogin(email, password string) bool {
	if !s.cfg.DemoMode || email != strings.ToLower(s.cfg.DemoEmail) {
		return false
	}
	provided := []byte(password)
	expected := []byte(s.cfg.DemoPassword)
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, expected) == 1
}

// verifyAccountPassword validates a re-authentication prompt. The staging
// demo alias signs in as the mapped canonical account, so its demo password
// must also work for sensitive self-service actions. Outside demo mode, or for
// any other account, only the account's real password hash is accepted.
func (s *Server) verifyAccountPassword(sess *sessionRecord, password string) bool {
	if sess == nil || password == "" {
		return false
	}
	if sess.User.PasswordHash != "" && auth.VerifyPassword(sess.User.PasswordHash, password) {
		return true
	}
	return s.cfg.DemoMode &&
		strings.EqualFold(sess.User.Email, s.cfg.DemoUserEmail) &&
		s.demoLogin(s.cfg.DemoEmail, password)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	sess, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue")
		return
	}
	userPayload, _ := s.publicUserWithState(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":      userPayload,
		"csrf":      sess.CSRF,
		"expiresAt": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sess, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE id = $1`, sess.ID)
	s.audit(r, sess.User, "auth.logout", sess.User.Email, "Signed out")
	s.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
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
		"ok":      status == http.StatusOK,
		"service": "a8m-system-backend",
		"version": s.version,
		"env":     s.cfg.Env,
		"time":    time.Now().UTC().Format(time.RFC3339),
		"checks":  map[string]string{"postgres": dbStatus, "redis": redisStatus},
	})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.authenticate(r)
	ctx := r.Context()
	state, ok := s.loadStateFor(ctx, sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	ensureStateCollections(state)
	s.attachOutbox(ctx, sess.User.ID, state)
	user, preferences := s.publicUserWithState(ctx, sess.User)
	identities, err := s.mailIdentities(ctx, sess.User)
	if err != nil {
		s.logger.Warn("identity load failed", "user", sess.User.ID, "err", err)
		identities = []map[string]any{}
	}
	aliases := identityAliases(identities)
	stateMap(state, "user")["aliases"] = aliases
	storedMessages, _, err := s.loadAllMailMessages(ctx, sess.User.ID)
	if err != nil {
		s.logger.Warn("bootstrap message load failed", "user", sess.User.ID, "err", err)
		storedMessages = nil
	}
	messages := make([]any, 0, len(storedMessages))
	for _, message := range storedMessages {
		messages = append(messages, publicStateMessage(state, message))
	}
	folders := stateSlice(state, "folders")
	if len(folders) == 0 {
		foldersAny := []any{}
		for _, folder := range defaultFolders() {
			foldersAny = append(foldersAny, folder)
		}
		folders = foldersAny
	}
	sieve := publicSieve(state, sess.User.Email)
	tasks, _ := s.loadTasks(ctx, sess.User.ID)
	files, _ := s.loadFiles(ctx, sess.User, "")
	calendarEvents, _ := s.loadCalendarEvents(ctx, sess.User.ID)
	providers, _ := s.publicStorageProviders(ctx, sess.User)
	people, _ := s.directoryPeople(ctx, sess, false)
	policy := s.tenantPolicyFor(ctx, sess.User.TenantID)
	recentAudit := stateSlice(state, "audit")
	if len(recentAudit) > 12 {
		recentAudit = recentAudit[:12]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":             user,
		"aliases":          aliases,
		"identities":       identities,
		"folders":          folders,
		"people":           people,
		"messages":         messages,
		"tasks":            tasks,
		"files":            files,
		"calendarEvents":   calendarEvents,
		"storageProviders": providers,
		"tenantPolicy": map[string]any{
			"mail": map[string]any{"externalTag": policy.Mail.ExternalTag},
			"directory": map[string]any{
				"contactSharing":    policy.Directory.ContactSharing || webmailAdminRole(sess.User.Role),
				"externalDirectory": policy.Directory.ExternalDirectory,
				"profileEditing":    policy.Directory.ProfileEditing,
			},
			"sharing": map[string]any{
				"externalSharing": policy.Sharing.ExternalSharing,
				"linkDefault":     policy.Sharing.LinkDefault,
				"allowedDomains":  policy.Sharing.AllowedDomains,
				"guestInvites":    policy.Sharing.GuestInvites,
			},
		},
		"sieve":              sieve,
		"quota":              s.mailQuota(ctx, sess.User.ID, state),
		"outbound":           stateOutbound(state),
		"pendingSends":       statePendingSends(state),
		"auditCount":         len(stateSlice(state, "audit")),
		"recentAudit":        recentAudit,
		"ai":                 s.aiPublicConfig(ctx, sess.User.TenantID),
		"capabilities":       capabilities(),
		"mailboxMode":        stateString(state, "mailMode"),
		"attachmentMaxBytes": 8 * 1024 * 1024,
		"csrf":               sess.CSRF,
		"mailClient":         s.mailClientSettings(r, sess.User.Email),
		"demo":               map[string]any{"email": sess.User.Email, "holdSeconds": 8},
		"serverTime":         time.Now().UTC().Format(time.RFC3339),
		"preferences":        preferences,
	})
}

// mailClientSettings returns the public client-facing endpoints for the
// current account. The webmail frontend renders these verbatim in Settings →
// About so a user can connect Outlook, Apple Mail or Thunderbird without
// asking an administrator for hostnames and ports.
func (s *Server) mailClientSettings(r *http.Request, email string) map[string]any {
	host := strings.TrimSpace(s.cfg.MailHostname)
	if parsed, err := url.Parse(s.cfg.PublicURL); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
	}
	if host == "" && r != nil {
		host = r.Host
		if value, _, err := net.SplitHostPort(host); err == nil {
			host = value
		}
	}
	host = strings.Trim(host, "[]")
	return map[string]any{
		"host":     host,
		"username": email,
		"imap": map[string]any{
			"host": host, "port": 993, "security": "SSL/TLS",
		},
		"pop3": map[string]any{
			"host": host, "port": 995, "security": "SSL/TLS",
		},
		"smtp": map[string]any{
			"host": host, "port": 587, "security": "STARTTLS",
		},
		"smtpSSL": map[string]any{
			"host": host, "port": 465, "security": "SSL/TLS",
		},
		"note": "Use your full Alt8Mail address as the username. Accounts with two-factor authentication must use an app password.",
	}
}

func (s *Server) getPreferences(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.authenticate(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"preferences": preferencesFor(sess.User)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": statePreferences(state, preferencesFor(sess.User))})
}

func (s *Server) patchPreferences(w http.ResponseWriter, r *http.Request) {
	sess, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue")
		return
	}
	if !s.validCSRF(r, sess) {
		writeError(w, http.StatusForbidden, "CSRF", "Missing or invalid CSRF token")
		return
	}
	var patch map[string]any
	if err := decodeJSON(w, r, &patch, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	allowed := map[string]bool{
		"language": true, "timeFormat": true, "dateFormat": true, "timeZone": true,
		"weekStart": true, "workStart": true, "workEnd": true, "workDays": true,
		"defaultFrom": true, "signatureEnabled": true, "signature": true, "autoAdvance": true,
		"inboxView": true,
	}
	current := preferencesFor(sess.User)
	changed := []string{}
	for key, value := range patch {
		if !allowed[key] {
			continue
		}
		if fmt.Sprint(current[key]) != fmt.Sprint(value) {
			current[key] = value
			changed = append(changed, key)
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PREFERENCES", "Invalid preferences")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `UPDATE users SET preferences = $2, updated_at = now() WHERE id = $1`, sess.User.ID, encoded); err != nil {
		s.logger.Error("save preferences", "err", err)
		writeError(w, http.StatusInternalServerError, "PREFERENCES", "Could not save preferences")
		return
	}
	s.audit(r, sess.User, "preferences.updated", "Workspace preferences", strings.Join(changed, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"preferences": current, "changed": changed})
}

func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue")
			return
		}
		if passwordChangeRequired(sess.User) && r.URL.Path != "/api/password/change" && r.URL.Path != "/api/bootstrap" && r.URL.Path != "/api/session" {
			writeError(w, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "Change your password before continuing")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validCSRF(r, sess) {
			writeError(w, http.StatusForbidden, "CSRF", "Missing or invalid CSRF token")
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, sess)
		next(w, r.WithContext(ctx))
	}
}

type sessionContextKey struct{}

func (s *Server) requireSessionFromContext(r *http.Request) *sessionRecord {
	sess, _ := r.Context().Value(sessionContextKey{}).(*sessionRecord)
	return sess
}

func (s *Server) authenticate(r *http.Request) (*sessionRecord, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, errors.New("no session cookie")
	}
	tokenHash := hashToken(cookie.Value)
	var sess sessionRecord
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT s.id, s.user_id::text, s.tenant_id::text, s.csrf_token, s.expires_at,
		       u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color,
		       u.role, u.status, COALESCE(u.password_hash, ''), u.preferences, u.security,
		       u.quota_bytes, u.used_bytes, u.enrollment_required, u.force_password_change, u.password_expires_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN tenants t ON t.id = u.tenant_id AND t.status = 'active'
		WHERE s.id = $1 AND s.expires_at > now() AND u.status = 'active'
	`, tokenHash).Scan(
		&sess.ID, &sess.UserID, &sess.TenantID, &sess.CSRF, &sess.ExpiresAt,
		&sess.User.ID, &sess.User.TenantID, &sess.User.Email, &sess.User.Name, &sess.User.Initials,
		&sess.User.Color, &sess.User.Role, &sess.User.Status, &sess.User.PasswordHash,
		&sess.User.Preferences, &sess.User.Security, &sess.User.QuotaBytes, &sess.User.UsedBytes,
		&sess.User.EnrollmentRequired, &sess.User.ForcePasswordChange, &sess.User.PasswordExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	sess.User.ID = sess.UserID
	sess.User.TenantID = sess.TenantID
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, sess.ID)
	return &sess, nil
}

func (s *Server) createSession(r *http.Request, user userRecord, ttl time.Duration) (*sessionRecord, string, error) {
	raw := randomToken(32)
	csrf := randomToken(32)
	sess := &sessionRecord{
		ID:        hashToken(raw),
		UserID:    user.ID,
		TenantID:  user.TenantID,
		CSRF:      csrf,
		ExpiresAt: time.Now().Add(ttl),
		User:      user,
	}
	if policy := s.tenantPolicyFor(r.Context(), user.TenantID); policy.Security.MaxSessions > 0 {
		var active int
		if err := s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM sessions WHERE user_id = $1 AND expires_at > now()`, user.ID).Scan(&active); err == nil {
			excess := active - policy.Security.MaxSessions + 1
			if excess > 0 {
				_, _ = s.store.DB.Exec(r.Context(), `
					DELETE FROM sessions WHERE id IN (
						SELECT id FROM sessions WHERE user_id = $1 AND expires_at > now()
						ORDER BY last_seen_at ASC LIMIT $2
					)
				`, user.ID, excess)
			}
		}
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO sessions (id, user_id, tenant_id, csrf_token, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5::inet, $6, $7)
	`, sess.ID, user.ID, user.TenantID, csrf, clientIP(r), sanitizeUserAgent(r.UserAgent()), sess.ExpiresAt)
	if err != nil {
		return nil, "", err
	}
	return sess, raw, nil
}

func (s *Server) validCSRF(r *http.Request, sess *sessionRecord) bool {
	provided := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	return provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(sess.CSRF)) == 1
}

func (s *Server) userByEmail(ctx context.Context, email string) (userRecord, error) {
	var user userRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color, u.role, u.status,
		       COALESCE(u.password_hash, ''), u.preferences, u.security, u.quota_bytes, u.used_bytes, u.enrollment_required,
		       u.force_password_change, u.password_expires_at
		FROM users u JOIN tenants t ON t.id = u.tenant_id AND t.status = 'active'
		WHERE u.email = $1 AND u.status = 'active' AND u.deleted_at IS NULL
	`, email).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color,
		&user.Role, &user.Status, &user.PasswordHash, &user.Preferences, &user.Security,
		&user.QuotaBytes, &user.UsedBytes, &user.EnrollmentRequired, &user.ForcePasswordChange, &user.PasswordExpiresAt,
	)
	return user, err
}

func (s *Server) aliases(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id::text, email::text, label, kind, can_send
		FROM user_aliases WHERE user_id = $1 AND active = true ORDER BY created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, email, label, kind string
		var canSend bool
		if err := rows.Scan(&id, &email, &label, &kind, &canSend); err != nil {
			return nil, err
		}
		reason := ""
		if !canSend {
			reason = "Delegated mailbox · read only"
		}
		result = append(result, map[string]any{
			"id": id, "email": email, "label": label, "kind": kind,
			"canSend": canSend, "reason": reason,
		})
	}
	return result, rows.Err()
}

func (s *Server) outboundInfo(ctx context.Context, userID, tenantID string) (map[string]any, error) {
	var committed, queued, scheduled int
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM outbound_events WHERE user_id = $1 AND sent_at > now() - interval '24 hours'`, userID).Scan(&committed)
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE user_id = $1 AND status = 'queued'`, userID).Scan(&queued)
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE user_id = $1 AND status = 'queued' AND scheduled = true`, userID).Scan(&scheduled)
	limit := 200
	used := committed
	usedWithQueued := used + queued
	return map[string]any{
		"limit": limit, "windowHours": 24, "baseline": 0,
		"committed": committed, "used": used, "queued": queued,
		"undoQueued": queued - scheduled, "scheduled": scheduled,
		"usedWithQueued": usedWithQueued,
		"remaining":      max(0, limit-usedWithQueued),
		"percent":        float64(usedWithQueued) / float64(limit) * 100,
		"source":         "postfix/a8m-outbox",
		"refreshedAt":    time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *Server) pendingSends(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, COALESCE(subject, ''), from_email::text, from_name, to_json, cc_json, bcc_json,
		       queued_at, send_at, scheduled, save_copy, thread_id
		FROM outbox WHERE user_id = $1 AND status = 'queued' ORDER BY send_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, subject, fromEmail, fromName string
		var toJSON, ccJSON, bccJSON []byte
		var queuedAt, sendAt time.Time
		var scheduled, saveCopy bool
		var threadID *string
		if err := rows.Scan(&id, &subject, &fromEmail, &fromName, &toJSON, &ccJSON, &bccJSON, &queuedAt, &sendAt, &scheduled, &saveCopy, &threadID); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"id": id, "subject": subject,
			"from": map[string]any{"name": fromName, "email": fromEmail},
			"to":   jsonRaw(toJSON), "cc": jsonRaw(ccJSON), "bcc": jsonRaw(bccJSON),
			"queuedAt": queuedAt, "sendAt": sendAt,
			"canCancel": time.Now().Before(sendAt), "scheduled": scheduled,
			"saveCopy": saveCopy, "threadId": threadID,
		})
	}
	return result, rows.Err()
}

func (s *Server) audit(r *http.Request, user userRecord, action, target, detail string) {
	ip := clientIP(r)
	userAgent := sanitizeUserAgent(r.UserAgent())
	if user.ID != "" {
		if err := s.withState(user.ID, user.TenantID, func(state map[string]any) error {
			ensureStateCollections(state)
			appendStateAudit(state, action, target, user.Name, detail, ip, userAgent)
			return nil
		}); err != nil {
			s.logger.Warn("state audit failed", "err", err)
		}
	}
	s.insertAudit(user, action, target, detail, ip, userAgent)
}

func (s *Server) setSessionCookies(w http.ResponseWriter, token, csrf string, ttl time.Duration) {
	maxAge := int(ttl.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/", HttpOnly: false,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name == sessionCookie, Secure: s.cfg.CookieSecure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *Server) notImplemented(code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, code, message)
	}
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", "API route not found")
}

func publicUser(user userRecord) map[string]any {
	return map[string]any{
		"id":                     user.ID,
		"tenantId":               user.TenantID,
		"email":                  user.Email,
		"name":                   user.Name,
		"initials":               user.Initials,
		"color":                  user.Color,
		"role":                   user.Role,
		"company":                "Alt8Mail",
		"preferences":            preferencesFor(user),
		"security":               jsonRaw(user.Security),
		"forcePasswordChange":    user.ForcePasswordChange,
		"passwordChangeRequired": passwordChangeRequired(user),
		"passwordExpiresAt":      nullableTime(user.PasswordExpiresAt),
	}
}

func preferencesFor(user userRecord) map[string]any {
	prefs := map[string]any{}
	if len(user.Preferences) > 0 {
		_ = json.Unmarshal(user.Preferences, &prefs)
	}
	defaults := map[string]any{
		"language": "en-US", "timeFormat": "12h", "dateFormat": "auto",
		"timeZone": "UTC", "weekStart": "monday", "workStart": "09:00",
		"workEnd": "17:00", "workDays": []int{1, 2, 3, 4, 5},
		"signatureEnabled": true, "signature": "", "autoAdvance": false,
		"theme": "system", "accent": "blue", "density": "comfortable",
		"markReadOnOpen": true, "inboxView": "grouped", "defaultPriority": "normal", "defaultReceipt": false,
		"undoSendSeconds": 8, "notifyDesktop": true, "notifySound": false,
		"notifyCalendar": true, "externalWarnings": true,
		"composeMode": "html", "composeWidth": "wide", "composePosition": "center",
	}
	for key, value := range defaults {
		if _, exists := prefs[key]; !exists {
			prefs[key] = value
		}
	}
	return prefs
}

func quotaFor(user userRecord) map[string]any {
	return map[string]any{
		"source": "dovecot", "limitBytes": user.QuotaBytes, "usedBytes": user.UsedBytes,
		"dynamicBytes": user.UsedBytes, "percent": percent(user.UsedBytes, user.QuotaBytes),
		"label":   fmt.Sprintf("%.2f GB of %.0f GB", float64(user.UsedBytes)/1e9, float64(user.QuotaBytes)/1e9),
		"folders": []any{}, "refreshedAt": time.Now().UTC().Format(time.RFC3339),
	}
}

func percent(used, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(used) / float64(limit) * 100
}

func defaultFolders() []map[string]any {
	return []map[string]any{
		{"id": "inbox", "label": "Inbox", "icon": "i-inbox", "section": "favorites"},
		{"id": "starred", "label": "Starred", "icon": "i-star", "section": "favorites", "virtual": true},
		{"id": "sent", "label": "Sent", "icon": "i-send", "section": "folders"},
		{"id": "drafts", "label": "Drafts", "icon": "i-file", "section": "folders"},
		{"id": "archive", "label": "Archive", "icon": "i-archive", "section": "folders"},
		{"id": "spam", "label": "Spam", "icon": "i-alert", "section": "folders"},
		{"id": "trash", "label": "Trash", "icon": "i-trash", "section": "folders"},
	}
}

func defaultSieve() map[string]any {
	return map[string]any{
		"revision": 1,
		"rules":    []any{},
		"vacation": map[string]any{
			"enabled": false, "start": "", "end": "", "days": 7,
			"internal": map[string]any{"subject": "Out of office", "message": ""},
			"external": map[string]any{"enabled": false, "subject": "Away from the office", "message": ""},
		},
		"forwarding": map[string]any{
			"enabled": false, "target": "", "keepCopy": true,
			"permanent": true, "start": "", "end": "",
		},
		"script": "",
	}
}

func capabilities() []string {
	return []string{
		"threads", "instant-search", "bulk-actions", "undo", "server-drafts", "undo-send",
		"scheduled-send", "sieve", "automatic-replies", "sender-trust", "outbound-caps",
		"quota", "audit-log", "pwa", "calendar", "directory", "tasks", "files",
		"storage-connectors", "attachment-preview", "two-factor", "passkeys", "preferences",
	}
}

func jsonRaw(value json.RawMessage) any {
	if len(value) == 0 {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal(value, &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}

func randomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" && isLoopback(r.RemoteAddr) {
		first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sanitizeUserAgent(ua string) string {
	ua = strings.ReplaceAll(ua, "\n", " ")
	ua = strings.ReplaceAll(ua, "\r", " ")
	if len(ua) > 240 {
		return ua[:240]
	}
	return ua
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Send JSON with Content-Type: application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "internal encoding error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]limiterEntry
	limit    int
	window   time.Duration
}

type limiterEntry struct {
	Count   int
	ResetAt time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{attempts: map[string]limiterEntry{}, limit: limit, window: window}
}

func (l *loginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.attempts[ip]
	if time.Now().After(entry.ResetAt) {
		delete(l.attempts, ip)
		return true
	}
	return entry.Count < l.limit
}

func (l *loginLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.attempts[ip]
	if time.Now().After(entry.ResetAt) {
		entry = limiterEntry{ResetAt: time.Now().Add(l.window)}
	}
	entry.Count++
	l.attempts[ip] = entry
}

func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

func passwordChangeRequired(user userRecord) bool {
	if user.ForcePasswordChange {
		return true
	}
	return user.PasswordExpiresAt != nil && user.PasswordExpiresAt.Before(time.Now())
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !s.verifyAccountPassword(sess, body.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "BAD_PASSWORD", "Your current password is incorrect")
		return
	}
	if body.NewPassword == "" || body.NewPassword == body.CurrentPassword {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Choose a new password that is different from the current one")
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	if errors := validatePasswordAgainstPolicy(body.NewPassword, policy.Security); len(errors) > 0 {
		writeError(w, http.StatusBadRequest, "PASSWORD_POLICY", strings.Join(errors, "; "))
		return
	}
	if s.passwordReused(r.Context(), sess.User, body.NewPassword, policy.Security.PasswordHistory) {
		writeError(w, http.StatusBadRequest, "PASSWORD_HISTORY", "That password was used recently. Choose a different one.")
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not hash the password")
		return
	}
	// Webmail and Dovecot use separate password hashes. Changing the web
	// password must rotate the mailbox credential too; otherwise the old
	// password would remain valid for IMAP, POP3 and submission.
	var mailHash any
	if s.mailAccountConfigured(sess.User.Email) {
		hashed, err := hashMailPassword(body.NewPassword)
		if err != nil {
			writeError(w, http.StatusBadGateway, "MAIL_PASSWORD", "Could not synchronize the mailbox password")
			return
		}
		mailHash = hashed
	}
	var expires any
	if policy.Security.PasswordExpiryDays > 0 {
		expires = time.Now().Add(time.Duration(policy.Security.PasswordExpiryDays) * 24 * time.Hour)
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not change the password")
		return
	}
	defer tx.Rollback(ctx)
	if policy.Security.PasswordHistory > 0 && sess.User.PasswordHash != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO password_history (user_id, password_hash) VALUES ($1, $2)
		`, sess.User.ID, sess.User.PasswordHash); err != nil {
			writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not record password history")
			return
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET password_hash = $2, mail_password_hash = COALESCE($3, mail_password_hash),
		       password_changed_at = now(), password_expires_at = $4,
		       force_password_change = false, updated_at = now() WHERE id = $1
	`, sess.User.ID, hash, mailHash, expires); err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not save the new password")
		return
	}
	if policy.Security.PasswordHistory > 0 {
		_, _ = tx.Exec(ctx, `
			DELETE FROM password_history WHERE user_id = $1 AND id NOT IN (
				SELECT id FROM password_history WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2
			)
		`, sess.User.ID, policy.Security.PasswordHistory)
	} else {
		_, _ = tx.Exec(ctx, `DELETE FROM password_history WHERE user_id = $1`, sess.User.ID)
	}
	_, _ = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, sess.User.ID, sess.ID)
	_, _ = tx.Exec(ctx, `DELETE FROM trusted_devices WHERE user_id = $1`, sess.User.ID)
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not commit the password change")
		return
	}
	s.clearDAVAuthCache(sess.User.Email)
	refreshed, _ := s.userByID(ctx, sess.User.ID)
	_, _ = s.store.DB.Exec(ctx, `UPDATE app_states SET updated_at = now() WHERE user_id = $1`, sess.User.ID)
	s.auditRequest(r, &sessionRecord{User: refreshed, CSRF: sess.CSRF}, "auth.password_changed", refreshed.Email, "Changed account password")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "passwordChangeRequired": false, "passwordExpiresAt": nullableTime(refreshed.PasswordExpiresAt)})
}

func (s *Server) passwordReused(ctx context.Context, user userRecord, newPassword string, history int) bool {
	if history <= 0 {
		return false
	}
	if user.PasswordHash != "" && auth.VerifyPassword(user.PasswordHash, newPassword) {
		return true
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT password_hash FROM password_history WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2
	`, user.ID, history)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		if rows.Scan(&hash) == nil && auth.VerifyPassword(hash, newPassword) {
			return true
		}
	}
	return false
}
