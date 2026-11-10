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

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/crewjam/saml/samlsp"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	// Distinct cookie names from the current reference feature preview. Both
	// services share the staging origin until the Go API replaces the preview.
	sessionCookie          = "a8m_system_sid"
	csrfCookie             = "a8m_system_csrf"
	delegatedSessionHeader = "X-A8M-Delegated-Session"
)

type Server struct {
	cfg               config.Config
	store             *store.Store
	logger            *slog.Logger
	version           string
	limit             *loginLimiter
	davLimit          *loginLimiter
	adminLimit        *loginLimiter
	accountLimit      *loginLimiter
	stateLocksMu      sync.Mutex
	stateLocks        map[string]*sync.Mutex
	mailUserMu        sync.Mutex
	mailLocks         map[string]*sync.Mutex
	davAuthMu         sync.Mutex
	davAuthCache      map[string]davAuthEntry
	sieveSyncMu       sync.Mutex
	sieveSyncActive   map[string]bool
	sieveSyncAgain    map[string]bool
	importMu          sync.Mutex
	activeImports     map[string]bool
	webAuthn          *webauthn.WebAuthn
	realtime          *realtimeHub
	avatarMu          sync.Mutex
	avatarCache       map[string]senderAvatarCacheEntry
	license           *licensing.License
	licenseError      string
	licenseConfigured bool
	licenseNow        func() time.Time
	oidcMu            sync.Mutex
	oidcCached        *oidcClient
	oidcLastErr       error
	oidcLastTry       time.Time
	oidcLimit         *loginLimiter
	scimLimit         *loginLimiter
	samlMu            sync.Mutex
	samlCached        *samlsp.Middleware
	samlLastErr       error
	samlLastTry       time.Time
	samlLimit         *loginLimiter
	// samlReplayCacheEntries overrides the replay cache size in tests; zero
	// uses samlReplayCacheSize.
	samlReplayCacheEntries int
	// samlMetadataClient is a test seam; production uses the bounded default
	// client in saml.go.
	samlMetadataClient *http.Client
	// samlFailureAudit is a test seam for the sanitized failure audit written
	// by samlOnError; production uses s.audit.
	samlFailureAudit func(*http.Request, userRecord, string, string, string)
	// adminSeatRaceHook is a test seam invoked after an admin activation has
	// pre-read its target(s) and before the seat-locked transaction. It is nil
	// in production.
	adminSeatRaceHook func()
	// automationPost is a test seam for webhook delivery; production uses the
	// bounded public-network POST in automation.go.
	automationPost      func(context.Context, string, []byte) error
	mcpLimit            *loginLimiter
	tenantKeyMu         sync.Mutex
	tenantKeys          map[string]map[int]unlockedTenantKey
	apiKeyMu            sync.Mutex
	apiKeyLimits        map[string]*loginLimiter
	webhookPost         func(context.Context, string, map[string]string, []byte) (int, error)
	webhookAllowPrivate bool
	hubspotBaseURL      string
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
		activeImports:   map[string]bool{},
		adminLimit:      newLoginLimiter(opts.Config.AdminRateLimitMax, opts.Config.AdminRateWindow),
		accountLimit:    newLoginLimiter(30, 10*time.Minute),
		oidcLimit:       newLoginLimiter(oidcCallbackRateLimit, oidcCallbackRateWindow),
		scimLimit:       newLoginLimiter(oidcCallbackRateLimit, oidcCallbackRateWindow),
		samlLimit:       newLoginLimiter(oidcCallbackRateLimit, oidcCallbackRateWindow),
		realtime:        newRealtimeHub(),
		avatarCache:     map[string]senderAvatarCacheEntry{},
		licenseNow:      time.Now,
	}
	if license, err := loadLicense(opts.Config, opts.Logger); err != nil {
		// A configured file that exists but cannot be read or verified is
		// reported as "invalid"; a configured-but-missing file is OSS mode.
		server.licenseError = err.Error()
		server.licenseConfigured = true
		if opts.Logger != nil {
			opts.Logger.Warn("license not loaded", "err", err)
		}
	} else if license != nil {
		server.license = license
		if tenant := strings.TrimSpace(license.Tenant); tenant != "" && opts.Store != nil && opts.Logger != nil {
			// A tenant claim that names no tenant would silently enforce
			// nothing. The lookup runs off the startup path so a slow database
			// never delays construction.
			go warnMissingLicenseTenant(opts.Store, opts.Logger, tenant)
		}
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

// warnMissingLicenseTenant logs at startup when a tenant-scoped license names a
// tenant that does not exist. The claim still scopes counting, so enforcement
// counts zero users (and therefore limits nobody) until that tenant is created.
func warnMissingLicenseTenant(st *store.Store, logger *slog.Logger, tenant string) {
	if st == nil || st.DB == nil || logger == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var exists bool
	if err := st.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id = $1)`, tenant).Scan(&exists); err != nil {
		logger.Warn("license tenant lookup failed", "err", err)
		return
	}
	if !exists {
		logger.Warn("license tenant claim matches no tenant; seat enforcement counts zero users", "tenant", tenant)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/mta-sts.txt", s.serveMTASTSPolicy)
	mux.HandleFunc("/.well-known/caldav", s.davWellKnown)
	mux.HandleFunc("/.well-known/carddav", s.davWellKnown)
	mux.HandleFunc("/dav/", s.handleDAV)
	mux.HandleFunc("GET /files/s/{token}", s.publicSharePage)
	mux.HandleFunc("POST /files/s/{token}/verify", s.publicShareVerifyEmail)
	mux.HandleFunc("POST /files/s/{token}/request-access", s.publicShareRequestAccess)
	mux.HandleFunc("GET /calendar/feed/{token}", s.publicCalendarFeed)
	mux.HandleFunc("GET /calendar/rsvp/{token}", s.publicCalendarRSVP)
	mux.HandleFunc("POST /calendar/rsvp/{token}", s.publicCalendarRSVPSubmit)
	mux.HandleFunc("GET /calendar/poll/{token}", s.publicCalendarPoll)
	mux.HandleFunc("POST /calendar/poll/{token}", s.publicCalendarPollSubmit)
	mux.HandleFunc("GET /api/public/shares/{token}", s.publicShareMetadata)
	mux.HandleFunc("GET /api/public/shares/{token}/content", s.publicShareContent)
	mux.HandleFunc("POST /api/public/shares/{token}/replace", s.publicShareReplace)
	mux.HandleFunc("GET /upload/{token}", s.mobileUploadPage)
	mux.HandleFunc("POST /api/public/upload/{token}", s.publicUpload)
	mux.HandleFunc("POST /api/public/upload/{token}/progress", s.publicUploadProgress)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/ready", s.health)
	mux.HandleFunc("GET /api/branding", s.publicBranding)
	mux.HandleFunc("GET /api/auth/oidc/config", s.oidcConfig)
	mux.HandleFunc("GET /api/auth/oidc/start", s.oidcStart)
	mux.HandleFunc("GET /api/auth/oidc/callback", s.oidcCallback)
	mux.HandleFunc("GET /api/auth/saml/config", s.samlConfig)
	mux.HandleFunc("GET /api/auth/saml/metadata", s.samlMetadata)
	mux.HandleFunc("GET /api/auth/saml/start", s.samlStart)
	// Register without a method so a non-POST request reaches samlACS and gets
	// a 405, rather than falling through to the /api/ catch-all.
	mux.HandleFunc("/api/auth/saml/acs", s.samlACS)
	mux.HandleFunc("GET /oauth/slack/callback", s.slackAddonOAuthCallback)
	mux.HandleFunc("GET /oauth/relay/complete", s.addonRelayComplete)
	mux.HandleFunc("POST /api/addons/webhooks/slack", s.slackAddonWebhook)
	mux.HandleFunc("POST /api/session", s.login)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("DELETE /api/session", s.logout)
	mux.HandleFunc("POST /api/session/2fa", s.verifyTwoFactor)
	mux.HandleFunc("POST /api/session/2fa/webauthn/begin", s.beginWebAuthnLogin)
	mux.HandleFunc("POST /api/session/2fa/webauthn/finish", s.finishWebAuthnLogin)
	mux.HandleFunc("POST /api/enrollment/totp/begin", s.beginEnrollmentTOTP)
	mux.HandleFunc("POST /api/enrollment/totp/finish", s.finishEnrollmentTOTP)
	mux.HandleFunc("POST /api/enrollment/webauthn/begin", s.beginEnrollmentWebAuthn)
	mux.HandleFunc("POST /api/enrollment/webauthn/finish", s.finishEnrollmentWebAuthn)
	mux.HandleFunc("GET /api/onboarding", s.requireUser(s.getOnboarding))
	mux.HandleFunc("POST /api/onboarding/complete", s.requireUser(s.completeOnboarding))
	mux.HandleFunc("GET /api/bootstrap", s.requireUser(s.bootstrap))
	mux.HandleFunc("GET /api/entitlements", s.requireUser(s.getEntitlements))
	mux.HandleFunc("GET /api/office/config", s.requireUser(s.officeConfig))
	s.registerOfficeDocumentRoutes(mux)
	mux.HandleFunc("GET /office-editor", s.officeEditorHost)
	mux.HandleFunc("GET /office-editor.js", s.officeEditorScript)
	mux.HandleFunc("GET /api/addons", s.requireUser(s.getAddons))
	mux.HandleFunc("POST /api/addons/{id}/connect", s.requireUser(s.connectAddon))
	mux.HandleFunc("POST /api/addons/{id}/oauth/start", s.requireUser(s.startAddonOAuth))
	mux.HandleFunc("POST /api/addons/{id}/install", s.requireUser(s.installAddon))
	mux.HandleFunc("DELETE /api/addons/{id}/install", s.requireUser(s.uninstallAddon))
	mux.HandleFunc("PUT /api/addons/{id}/tenant", s.requireUser(s.setTenantAddon))
	mux.HandleFunc("POST /api/addons/{id}/request", s.requireUser(s.requestAddon))
	mux.HandleFunc("POST /api/addon-requests/{id}/resolve", s.requireUser(s.resolveAddonRequest))
	mux.HandleFunc("POST /api/addons/{id}/actions/{action}", s.requireUser(s.runAddonAction))
	mux.HandleFunc("POST /api/delegation/launch", s.requireUser(s.launchDelegatedSession))
	mux.HandleFunc("POST /api/delegation/redeem", s.requireUser(s.redeemDelegatedSession))
	mux.HandleFunc("DELETE /api/delegation/session", s.requireUser(s.endDelegatedSession))
	mux.HandleFunc("GET /api/folders", s.requireUser(s.getFolders))
	mux.HandleFunc("POST /api/folders", s.requireUser(s.createFolder))
	mux.HandleFunc("PUT /api/folders/order", s.requireUser(s.reorderFolders))
	mux.HandleFunc("PATCH /api/folders/{id}", s.requireUser(s.updateFolder))
	mux.HandleFunc("DELETE /api/folders/{id}", s.requireUser(s.deleteFolder))
	mux.HandleFunc("POST /api/folders/{id}/empty", s.requireUser(s.emptyFolder))
	mux.HandleFunc("POST /api/folders/{id}/mark-read", s.requireUser(s.markFolderRead))
	mux.HandleFunc("GET /api/aliases", s.requireUser(s.getAliases))
	mux.HandleFunc("GET /api/accounts", s.requireUser(s.getMailAccounts))
	mux.HandleFunc("GET /api/accounts/summary", s.requireUser(s.getMailAccountSummaries))
	mux.HandleFunc("POST /api/accounts", s.requireUser(s.createMailAccount))
	mux.HandleFunc("POST /api/accounts/test", s.requireUser(s.testMailAccount))
	mux.HandleFunc("PATCH /api/accounts/{id}", s.requireUser(s.updateMailAccount))
	mux.HandleFunc("DELETE /api/accounts/{id}", s.requireUser(s.deleteMailAccount))
	mux.HandleFunc("POST /api/accounts/{id}/sync", s.requireUser(s.syncMailAccount))
	mux.HandleFunc("GET /api/mail/oauth/{provider}/start", s.requireUser(s.startMailOAuth))
	mux.HandleFunc("GET /api/mail/oauth/{provider}/callback", s.mailOAuthCallback)
	mux.HandleFunc("POST /api/password/change", s.requireUser(s.changePassword))
	mux.HandleFunc("GET /api/preferences", s.requireUser(s.getPreferences))
	mux.HandleFunc("PATCH /api/preferences", s.requireUser(s.patchPreferencesState))
	mux.HandleFunc("POST /api/preferences/reset", s.requireUser(s.resetPreferencesState))
	mux.HandleFunc("GET /api/push/config", s.requireUser(s.getPushConfig))
	mux.HandleFunc("POST /api/push/subscribe", s.requireUser(s.subscribePush))
	mux.HandleFunc("DELETE /api/push/subscribe", s.requireUser(s.unsubscribePush))
	mux.HandleFunc("POST /api/push/test", s.requireUser(s.testWebPush))

	mux.HandleFunc("GET /api/labels", s.requireUser(s.getMailLabels))
	mux.HandleFunc("POST /api/labels", s.requireUser(s.createMailLabel))
	mux.HandleFunc("PATCH /api/labels/{id}", s.requireUser(s.updateMailLabel))
	mux.HandleFunc("DELETE /api/labels/{id}", s.requireUser(s.deleteMailLabel))
	mux.HandleFunc("GET /api/messages", s.requireUser(s.getMessages))
	mux.HandleFunc("PATCH /api/messages/{id}", s.requireUser(s.patchMessage))
	mux.HandleFunc("POST /api/messages/read", s.requireUser(s.markRead))
	mux.HandleFunc("POST /api/messages/actions", s.requireUser(s.messageActions))
	mux.HandleFunc("GET /api/messages/{id}/trust", s.requireUser(s.messageTrust))
	mux.HandleFunc("GET /api/messages/{id}/reply-context", s.requireUser(s.messageReplyContext))
	mux.HandleFunc("GET /api/messages/{id}/calendar-card", s.requireUser(s.messageCalendarCard))
	mux.HandleFunc("POST /api/messages/{id}/calendar-import", s.requireUser(s.importMessageCalendar))
	mux.HandleFunc("POST /api/messages/{id}/trust", s.requireUser(s.messageTrust))
	mux.HandleFunc("GET /api/messages/{id}/source", s.requireUser(s.messageSource))
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
	mux.HandleFunc("GET /api/storage/quota", s.requireUser(s.getStorageQuota))
	mux.HandleFunc("PUT /api/storage/default", s.requireUser(s.setDefaultStorage))
	mux.HandleFunc("GET /api/storage/credentials", s.requireUser(s.getStorageCredentials))
	mux.HandleFunc("PUT /api/storage/credentials/{provider}", s.requireUser(s.putStorageCredentials))
	mux.HandleFunc("DELETE /api/storage/credentials/{provider}", s.requireUser(s.deleteStorageCredentials))
	mux.HandleFunc("POST /api/storage/{id}/{action}", s.requireUser(s.storageAction))
	mux.HandleFunc("GET /api/storage/oauth/{provider}/start", s.requireUser(s.startStorageOAuth))
	mux.HandleFunc("GET /api/storage/oauth/{provider}/callback", s.storageOAuthCallback)
	mux.HandleFunc("GET /api/files", s.requireUser(s.getFiles))
	mux.HandleFunc("POST /api/files", s.requireUser(s.createFile))
	mux.HandleFunc("PATCH /api/files/{id}", s.requireUser(s.patchFile))
	mux.HandleFunc("DELETE /api/files/{id}", s.requireUser(s.deleteFile))
	mux.HandleFunc("POST /api/files/{id}/share", s.requireUser(s.shareFile))
	mux.HandleFunc("POST /api/files/{id}/copy", s.requireUser(s.copyFile))
	mux.HandleFunc("GET /api/files/{id}/shares", s.requireUser(s.listFileShares))
	mux.HandleFunc("GET /api/files/share-groups", s.requireUser(s.listShareGroups))
	mux.HandleFunc("GET /api/files/{id}/permissions", s.requireUser(s.listFilePermissions))
	mux.HandleFunc("POST /api/files/{id}/permissions", s.requireUser(s.createFilePermission))
	mux.HandleFunc("DELETE /api/files/{id}/permissions/{permissionId}", s.requireUser(s.deleteFilePermission))
	mux.HandleFunc("POST /api/files/import-url", s.requireUser(s.importFileFromURL))
	mux.HandleFunc("POST /api/upload-sessions", s.requireUser(s.createUploadSession))
	mux.HandleFunc("GET /api/upload-sessions/{id}", s.requireUser(s.getUploadSession))
	mux.HandleFunc("POST /api/upload-sessions/{id}/extend", s.requireUser(s.extendUploadSession))
	mux.HandleFunc("DELETE /api/upload-sessions/{id}", s.requireUser(s.deleteUploadSession))
	mux.HandleFunc("DELETE /api/file-shares/{id}", s.requireUser(s.revokeFileShare))
	mux.HandleFunc("GET /api/files/{id}/content", s.requireUser(s.getFileContent))
	mux.HandleFunc("GET /api/file-folders", s.requireUser(s.getFileFolders))
	mux.HandleFunc("POST /api/file-folders", s.requireUser(s.createFileFolder))
	mux.HandleFunc("PATCH /api/file-folders/{id}", s.requireUser(s.updateFileFolder))
	mux.HandleFunc("DELETE /api/file-folders/{id}", s.requireUser(s.deleteFileFolder))

	mux.HandleFunc("GET /api/calendar/events", s.requireUser(s.getCalendarEvents))
	mux.HandleFunc("POST /api/calendar/events", s.requireUser(s.createCalendarEvent))
	mux.HandleFunc("PATCH /api/calendar/events/{id}", s.requireUser(s.updateCalendarEvent))
	mux.HandleFunc("DELETE /api/calendar/events/{id}", s.requireUser(s.deleteCalendarEvent))
	mux.HandleFunc("POST /api/calendar/events/{id}/rsvp", s.requireUser(s.rsvpCalendarEvent))
	mux.HandleFunc("POST /api/calendar/events/{id}/resend", s.requireUser(s.resendCalendarInvites))
	mux.HandleFunc("DELETE /api/calendar/events/{id}/invitations", s.requireUser(s.undoCalendarInvites))
	mux.HandleFunc("POST /api/calendar/events/{id}/counter", s.requireUser(s.createCalendarCounter))
	mux.HandleFunc("POST /api/calendar/events/{id}/proposals/{proposalId}/accept", s.requireUser(s.acceptCalendarProposal))
	mux.HandleFunc("POST /api/calendar/events/{id}/proposals/{proposalId}/decline", s.requireUser(s.declineCalendarProposal))
	mux.HandleFunc("GET /api/calendar/stream", s.requireUser(s.calendarStream))
	mux.HandleFunc("GET /api/mail/stream", s.requireUser(s.mailStream))
	mux.HandleFunc("POST /api/calendar/calendars", s.requireUser(s.createCalendar))
	mux.HandleFunc("PATCH /api/calendar/calendars/{id}", s.requireUser(s.updateCalendar))
	mux.HandleFunc("DELETE /api/calendar/calendars/{id}", s.requireUser(s.deleteCalendar))
	mux.HandleFunc("GET /api/calendar/calendars/{id}/shares", s.requireUser(s.getCalendarShares))
	mux.HandleFunc("POST /api/calendar/calendars/{id}/share", s.requireUser(s.shareCalendar))
	mux.HandleFunc("DELETE /api/calendar/calendars/{id}/share/{userId}", s.requireUser(s.unshareCalendar))
	mux.HandleFunc("POST /api/calendar/calendars/{id}/leave", s.requireUser(s.leaveSharedCalendar))
	mux.HandleFunc("GET /api/calendar/calendars/{id}/publish", s.requireUser(s.getCalendarPublication))
	mux.HandleFunc("POST /api/calendar/calendars/{id}/publish", s.requireUser(s.publishCalendar))
	mux.HandleFunc("DELETE /api/calendar/calendars/{id}/publish", s.requireUser(s.unpublishCalendar))
	mux.HandleFunc("POST /api/calendar/subscribe", s.requireUser(s.subscribeCalendar))
	mux.HandleFunc("POST /api/calendar/subscriptions/{id}/sync", s.requireUser(s.syncCalendarSubscription))
	mux.HandleFunc("DELETE /api/calendar/subscriptions/{id}", s.requireUser(s.deleteCalendarSubscription))
	mux.HandleFunc("POST /api/calendar/import", s.requireUser(s.importCalendarFile))
	mux.HandleFunc("GET /api/calendar/export", s.requireUser(s.exportCalendar))
	mux.HandleFunc("GET /api/calendar/resources", s.requireUser(s.getCalendarResources))
	s.registerCalendarAvailabilityRoutes(mux)
	mux.HandleFunc("POST /api/calendar/resources", s.requireUser(s.createCalendarResource))
	mux.HandleFunc("GET /api/calendar/polls", s.requireUser(s.getCalendarPolls))
	mux.HandleFunc("POST /api/calendar/polls", s.requireUser(s.createCalendarPoll))
	mux.HandleFunc("POST /api/calendar/polls/{id}/close", s.requireUser(s.closeCalendarPoll))
	mux.HandleFunc("POST /api/calendar/polls/{id}/schedule", s.requireUser(s.scheduleCalendarPoll))
	mux.HandleFunc("DELETE /api/calendar/polls/{id}", s.requireUser(s.deleteCalendarPoll))
	mux.HandleFunc("GET /api/calendar/holidays", s.requireUser(s.getHolidayCatalog))
	mux.HandleFunc("POST /api/calendar/holidays", s.requireUser(s.addHolidayCalendar))
	mux.HandleFunc("POST /api/calendar/birthdays", s.requireUser(s.addBirthdayCalendar))
	mux.HandleFunc("GET /api/address-books", s.requireUser(s.getAddressBooks))
	mux.HandleFunc("POST /api/address-books", s.requireUser(s.createAddressBook))
	mux.HandleFunc("PATCH /api/address-books/{id}", s.requireUser(s.updateAddressBook))
	mux.HandleFunc("DELETE /api/address-books/{id}", s.requireUser(s.deleteAddressBook))

	mux.HandleFunc("GET /api/directory", s.requireUser(s.getDirectory))
	mux.HandleFunc("GET /api/contacts", s.requireUser(s.getContacts))
	mux.HandleFunc("POST /api/contacts", s.requireUser(s.createContact))
	mux.HandleFunc("POST /api/contacts/auto", s.requireUser(s.autoSaveContact))
	mux.HandleFunc("PATCH /api/contacts/{id}", s.requireUser(s.updateContact))
	mux.HandleFunc("DELETE /api/contacts/{id}", s.requireUser(s.deleteContact))
	mux.HandleFunc("GET /api/workspace/audit", s.requireUser(s.getWorkspaceAudit))
	mux.HandleFunc("GET /api/workspace/audit.csv", s.requireUser(s.getWorkspaceAuditCSV))

	mux.HandleFunc("POST /api/attachments", s.requireUser(s.uploadAttachment))
	mux.HandleFunc("GET /api/attachments/{id}/content", s.requireUser(s.attachmentContent))
	mux.HandleFunc("GET /api/mail/image", s.requireUser(s.mailRemoteImage))
	mux.HandleFunc("GET /api/mail/avatar", s.requireUser(s.mailSenderAvatar))
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
	mux.HandleFunc("PUT /api/identities/signature", s.requireUser(s.putIdentitySignature))
	mux.HandleFunc("PATCH /api/identities/{id}", s.requireUser(s.updateIdentity))
	mux.HandleFunc("POST /api/identities/{id}/resend", s.requireUser(s.resendIdentity))
	mux.HandleFunc("DELETE /api/identities/{id}", s.requireUser(s.deleteIdentity))
	mux.HandleFunc("GET /api/identities/verify", s.identityVerifyPage)
	mux.HandleFunc("POST /api/identities/verify", s.identityVerifySubmit)
	mux.HandleFunc("GET /api/signatures", s.requireUser(s.getSignatures))
	mux.HandleFunc("POST /api/signatures", s.requireUser(s.createSignature))
	mux.HandleFunc("PATCH /api/signatures/{id}", s.requireUser(s.updateSignature))
	mux.HandleFunc("DELETE /api/signatures/{id}", s.requireUser(s.deleteSignature))
	mux.HandleFunc("POST /api/ai/messages/{id}/summary", s.requireUser(s.aiMessageSummary))
	mux.HandleFunc("POST /api/ai/messages/{id}/translate", s.requireUser(s.aiMessageTranslate))
	mux.HandleFunc("POST /api/ai/messages/{id}/reply", s.requireUser(s.aiMessageReply))
	mux.HandleFunc("POST /api/ai/translate", s.requireUser(s.aiTranslateSelection))
	mux.HandleFunc("POST /api/ai/compose", s.requireUser(s.aiCompose))
	mux.HandleFunc("POST /api/ai/threads/{id}/commitments", s.requireUser(s.aiThreadCommitments))
	mux.HandleFunc("POST /api/ai/tone", s.requireUser(s.aiToneSuggestion))
	mux.HandleFunc("GET /api/catch-up", s.requireUser(s.catchUpSummary))
	mux.HandleFunc("GET /api/messages/{id}/trust-explanation", s.requireUser(s.messageTrustExplanation))
	s.registerCollaborationRoutes(mux)
	s.registerTeamInboxRoutes(mux)
	s.registerSharedDraftRoutes(mux)
	s.registerTeamTemplateRoutes(mux)
	s.registerAutomationRoutes(mux)
	s.registerFileCollaborationRoutes(mux)
	s.registerSenderControlRoutes(mux)
	s.registerFollowUpRoutes(mux)
	s.registerMailMergeRoutes(mux)
	s.registerBookingRoutes(mux)
	s.registerGalleryRoutes(mux)
	s.registerCampaignTrackingRoutes(mux)
	s.registerMCPRoutes(mux)
	s.registerAIGovernanceRoutes(mux)
	s.registerAITriageRoutes(mux)
	s.registerAPIPlatformRoutes(mux)
	s.registerPhishingPublicRoutes(mux)
	s.registerAIObligationRoutes(mux)
	s.registerAISearchRoutes(mux)
	s.registerAIInsightRoutes(mux)
	s.registerFileIndexRoutes(mux)
	s.registerAIPolicyAssistRoutes(mux)
	s.registerReactionRoutes(mux)
	s.registerStickyNoteRoutes(mux)
	s.registerSubscriptionRoutes(mux)
	s.registerYearReviewRoutes(mux)
	s.registerSendTimeRoutes(mux)
	s.registerDLPRoutes(mux)
	mux.HandleFunc("GET /api/audit", s.requireUser(s.getAudit))
	mux.HandleFunc("GET /api/audit.csv", s.requireUser(s.getAuditCSV))
	// SCIM is mounted outside session auth and before the /api/ catch-all so
	// its bearer middleware owns every /scim/v2 path.
	s.registerSCIMRoutes(mux)
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
	OnboardingRequired  bool
	PasswordExpiresAt   *time.Time
}

type sessionRecord struct {
	ID               string
	UserID           string
	TenantID         string
	CSRF             string
	ExpiresAt        time.Time
	User             userRecord
	IsDelegated      bool
	ParentSessionID  string
	DelegatedByID    string
	DelegatedByEmail string
	DelegatedOwnerID string
	DelegatedCanSend bool
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
	signInDetail := "New sign-in detected on your email account"
	if demoSignIn {
		lookupEmail = s.cfg.DemoUserEmail
		signInDetail = "New sign-in detected on your email account · staging demo"
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
	if !s.delegatedAccessCurrent(r, sess) {
		writeError(w, http.StatusUnauthorized, "DELEGATION_REVOKED", "This mailbox delegation has been removed. The delegated window is now closed.")
		return
	}
	userPayload, _ := s.publicUserWithState(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":             userPayload,
		"csrf":             sess.CSRF,
		"expiresAt":        sess.ExpiresAt.UTC().Format(time.RFC3339),
		"sessionKind":      sessionKind(sess),
		"delegated":        sess.IsDelegated,
		"delegatedBy":      nullableString(sess.DelegatedByEmail),
		"delegatedCanSend": sess.DelegatedCanSend,
	})
}

func sessionKind(sess *sessionRecord) string {
	if sess != nil && sess.IsDelegated {
		return "delegated"
	}
	return "standard"
}

// logout always clears the browser cookies, even when the session has already
// expired or the CSRF token is stale. Requiring CSRF here made a failed sign
// out leave a valid cookie in place, so a reload returned to signed-in pages.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if sess, err := s.authenticate(r); err == nil && sess != nil {
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE id = $1`, sess.ID)
		if sess.IsDelegated {
			// A delegated window shares the browser cookie jar with the primary
			// mailbox. Never clear the primary sign-in when closing this window.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegated": true})
			return
		}
		s.audit(r, sess.User, "auth.logout", sess.User.Email, "Signed out")
	}
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
	repairEncodedSubjects(state)
	s.attachOutbox(ctx, sess.User.ID, state)
	security, _, _, _ := s.securityState(ctx, sess.User)
	user := publicUserFromState(state, sess.User, preferencesFor(sess.User), security)
	preferences := statePreferences(state, preferencesFor(sess.User))
	organizationName := ""
	_ = s.store.DB.QueryRow(ctx, `SELECT name FROM public.tenants WHERE id=$1`, sess.User.TenantID).Scan(&organizationName)
	identities, err := s.mailIdentities(ctx, sess.User)
	if err != nil {
		s.logger.Warn("identity load failed", "user", sess.User.ID, "err", err)
		identities = []map[string]any{}
	}
	aliases := identityAliases(identities)
	unreadSummaries, summaryErr := s.mailAccountUnreadSummaries(ctx, sess.User.ID)
	if summaryErr != nil {
		s.logger.Warn("mail unread summary failed", "user", sess.User.ID, "err", summaryErr)
		unreadSummaries = map[string]mailAccountUnreadSummary{}
	}
	accounts := []map[string]any{}
	if records, accountErr := s.mailAccountsForSession(ctx, sess); accountErr == nil {
		for _, account := range records {
			account.UnreadCount = unreadSummaries[account.ID].UnreadCount
			accounts = append(accounts, mailAccountView(account))
		}
	} else {
		s.logger.Warn("mail account load failed", "user", sess.User.ID, "err", accountErr)
	}
	policy := s.tenantPolicyFor(ctx, sess.User.TenantID)
	readScope, scopeErr := s.resolveMailReadScope(ctx, sess, requestedMailAccountID(r))
	if scopeErr != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_ACCOUNT", "Could not resolve the mailbox account")
		return
	}
	activeAccountView := unifiedMailAccountView(accounts)
	if !readScope.Unified {
		readScope.Account.UnreadCount = unreadSummaries[readScope.Account.ID].UnreadCount
		activeAccountView = mailAccountView(readScope.Account)
	}
	signatures, err := s.listSignatures(ctx, sess.User)
	if err != nil {
		s.logger.Warn("signature load failed", "user", sess.User.ID, "err", err)
		signatures = []map[string]any{}
	}
	labels := []any{}
	if records, labelErr := s.listMailLabels(ctx, sess.User.ID); labelErr == nil {
		for _, label := range records {
			labels = append(labels, label.public())
		}
	}
	stateMap(state, "user")["aliases"] = aliases
	folder := strings.TrimSpace(r.URL.Query().Get("folder"))
	if folder == "" {
		folder = "inbox"
	}
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = "all"
	}
	limit := 50
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 {
		limit = value
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if value, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && value > 0 {
		offset = value
	}
	page, err := s.loadMailThreadPage(ctx, sess.User.ID, readScope.AccountID, folder, filter, limit, offset)
	if err != nil {
		s.logger.Warn("bootstrap message load failed", "user", sess.User.ID, "err", err)
		page = mailThreadPage{Offset: offset, Limit: limit}
	}
	storedMessages := make([]map[string]any, 0, len(page.Messages)+10)
	seenMessages := map[string]bool{}
	appendStored := func(items []map[string]any) {
		for _, message := range items {
			id := stateString(message, "id")
			if id == "" || seenMessages[id] {
				continue
			}
			seenMessages[id] = true
			storedMessages = append(storedMessages, message)
		}
	}
	appendStored(page.Messages)
	// Drafts are a small, special folder: keep every open/draft message in the
	// initial bootstrap so compose restoration still works after a reload.
	if drafts, _, draftErr := s.loadMailMessagesByFolder(ctx, sess.User.ID, readScope.AccountID, "drafts", ""); draftErr == nil {
		appendStored(drafts)
	}
	messages := make([]any, 0, len(storedMessages))
	for _, message := range storedMessages {
		messages = append(messages, publicStateMessage(state, message))
	}
	folderCounts, folderUnread, countErr := s.loadMailFolderCounts(ctx, sess.User.ID, readScope.AccountID)
	if countErr != nil {
		s.logger.Warn("bootstrap folder counts failed", "user", sess.User.ID, "err", countErr)
	}
	folders := stateSlice(state, "folders")
	if !readScope.Unified && readScope.Account.Kind == mailAccountKindExternal {
		folders = []any{}
		if externalFolders, folderErr := s.externalFolderViews(ctx, readScope.Account); folderErr == nil {
			for _, folder := range externalFolders {
				folders = append(folders, folder)
			}
		}
	} else if len(folders) == 0 {
		foldersAny := []any{}
		for _, folder := range defaultFolders() {
			foldersAny = append(foldersAny, folder)
		}
		folders = foldersAny
	}
	delegatedMailboxes := []map[string]any{}
	sieve := publicSieve(state, sess.User.Email)
	// A delegated window already is the owner's mailbox. Connected accounts
	// also do not surface the owner's delegated Alt8Mail mailboxes.
	if !sess.IsDelegated && !readScope.Unified && readScope.Account.Kind == mailAccountKindNative {
		folders, messages = s.applyDelegatedMailboxes(ctx, sess.User, folders, messages)
		delegatedMailboxes = s.delegatedMailboxViews(ctx, sess.User.TenantID, sess.User.ID)
	}
	tasks, _ := s.loadTasks(ctx, sess.User.ID)
	files, _ := s.loadFiles(ctx, sess.User, "")
	fileFolders, _ := s.loadFileFolders(ctx, sess.User.ID)
	if sess.IsDelegated {
		// Media URLs carry the owner id so browser image/iframe requests (which
		// cannot attach the delegated-session header) resolve through the
		// delegate relationship on the primary cookie.
		for _, message := range messages {
			record, ok := message.(map[string]any)
			if !ok {
				continue
			}
			record["attachments"] = delegatedAttachments(record["attachments"], sess.User.ID)
		}
		for _, file := range files {
			if contentURL := stateString(file, "contentUrl"); contentURL != "" {
				file["contentUrl"] = delegatedOwnerURL(contentURL, sess.User.ID)
			}
			if downloadURL := stateString(file, "downloadUrl"); downloadURL != "" {
				file["downloadUrl"] = delegatedOwnerURL(downloadURL, sess.User.ID)
			}
		}
	}
	_ = s.ensureDefaultCalendar(ctx, sess.User)
	calendars, _ := s.loadCalendars(ctx, sess.User)
	calendarEvents, _ := s.loadCalendarEvents(ctx, sess.User)
	calendarSubscriptions, _ := s.loadCalendarSubscriptions(ctx, sess.User.ID)
	addressBooks, _ := s.loadAddressBooks(ctx, sess.User)
	providers, _ := s.publicStorageProviders(ctx, sess.User)
	storageQuota := storageQuotaPayload(s.storageUsageFor(ctx, sess.User))
	people, _ := s.directoryPeople(ctx, sess, false)
	recentAudit := stateSlice(state, "audit")
	if len(recentAudit) > 12 {
		recentAudit = recentAudit[:12]
	}
	threadMeta := s.collaborationThreadMeta(ctx, sess.User)
	vipSenders := s.vipSenderList(ctx, sess.User.ID)
	threadMutes := s.threadMuteList(ctx, sess.User.ID)
	followUps := s.followUpViews(ctx, sess.User.ID)
	teamInboxes := []map[string]any{}
	if s.entitled("shared_inbox") {
		teamInboxes = s.teamMailboxesForUser(ctx, sess.User)
	}
	serverNotifications := s.loadMailNotifications(ctx, sess.User.ID, 50)
	unsubscribeActions := s.loadUnsubscribeActions(ctx, sess.User.ID)
	mailSync := map[string]any{
		"lastError":  nullableString(stateString(stateMap(state, "mailSync"), "lastError")),
		"lastSyncAt": nullableString(stateString(stateMap(state, "mailSync"), "lastSyncAt")),
	}
	if !readScope.Unified && readScope.Account.Kind == mailAccountKindExternal {
		mailSync["lastError"] = nullableString(readScope.Account.LastError)
		mailSync["lastSyncAt"] = nullableTime(readScope.Account.LastSyncAt)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":             user,
		"organizationName": organizationName,
		"aliases":          aliases,
		"accounts":         accounts,
		"activeAccount":    activeAccountView,
		"activeAccountId":  readScope.AccountID,
		"identities":       identities,
		"signatures":       signatures,
		"labels":           labels,
		"folders":          folders,
		"teamInboxes":      teamInboxes,
		"vipSenders":       vipSenders,
		"threadMutes":      threadMutes,
		"followUps":        followUps,
		"people":           people,
		"messages":         messages,
		"mailPage": map[string]any{
			"folder": folder, "filter": filter, "offset": offset, "limit": limit,
			"total": page.Total, "totalMessages": page.TotalMessages, "hasMore": page.HasMore,
		},
		"folderCounts":          folderCounts,
		"folderUnread":          folderUnread,
		"tasks":                 tasks,
		"files":                 files,
		"fileFolders":           fileFolders,
		"calendarEvents":        calendarEvents,
		"calendars":             calendars,
		"calendarSubscriptions": calendarSubscriptions,
		"addressBooks":          addressBooks,
		"storageProviders":      providers,
		"storageQuota":          storageQuota,
		"defaultStorage":        s.defaultStorageProvider(ctx, sess.User.ID),
		"tenantPolicy": map[string]any{
			"mail": map[string]any{
				"externalTag":              policy.Mail.ExternalTag,
				"quarantine":               policy.Mail.Quarantine,
				"externalAccounts":         policy.Mail.ExternalAccounts,
				"undoSendSeconds":          policy.Mail.UndoSendSeconds,
				"recipientWindowStartHour": policy.Mail.RecipientWindowStartHour,
				"recipientWindowEndHour":   policy.Mail.RecipientWindowEndHour,
			},
			"directory": map[string]any{
				"contactSharing":    policy.Directory.ContactSharing || webmailAdminRole(sess.User.Role),
				"externalDirectory": policy.Directory.ExternalDirectory,
				"profileEditing":    policy.Directory.ProfileEditing,
				"autoAddExternal":   policy.Directory.AutoAddExternal,
			},
			"sharing": map[string]any{
				"externalSharing": policy.Sharing.ExternalSharing,
				"linkDefault":     policy.Sharing.LinkDefault,
				"allowedDomains":  policy.Sharing.AllowedDomains,
				"guestInvites":    policy.Sharing.GuestInvites,
			},
			"dlp": map[string]any{
				"enabled": policy.DLP.Enabled, "defaultClassification": policy.DLP.DefaultClassification,
				"labels": dlpClassifications,
			},
			"features": map[string]any{
				"stickyNotes": policy.Features.StickyNotes, "reactions": policy.Features.Reactions,
			},
			"office": map[string]any{
				"enabled": policy.Office.Enabled, "internalSharing": policy.Office.InternalSharing,
				"externalEditing": policy.Office.ExternalEditing, "ai": policy.Office.AI,
				"dlpOnSave": policy.Office.DLPOnSave, "versionRetention": policy.Office.VersionRetention,
			},
			"branding": map[string]any{
				"productName": policy.Branding.ProductName, "primaryColor": policy.Branding.PrimaryColor,
				"supportEmail": policy.Branding.SupportEmail, "loginMessage": policy.Branding.LoginMessage,
			},
		},
		"branding": map[string]any{
			"productName": policy.Branding.ProductName, "primaryColor": policy.Branding.PrimaryColor,
			"supportEmail": policy.Branding.SupportEmail, "loginMessage": policy.Branding.LoginMessage,
		},
		"sieve":               sieve,
		"quota":               s.mailQuota(ctx, sess.User.ID, state),
		"outbound":            s.outboundPayload(ctx, sess.User, state),
		"pendingSends":        statePendingSends(state),
		"auditCount":          len(stateSlice(state, "audit")),
		"recentAudit":         recentAudit,
		"threadMeta":          threadMeta,
		"serverNotifications": serverNotifications,
		"unsubscribeActions":  unsubscribeActions,
		"ai":                  s.aiPublicConfig(ctx, sess.User.TenantID),
		"push":                s.pushPublicConfig(ctx, sess.User.ID),
		"capabilities":        capabilities(),
		"license":             s.licenseSnapshot(ctx),
		"mailboxMode":         stateString(state, "mailMode"),
		"mailSync":            mailSync,
		"attachmentMaxBytes":  8 * 1024 * 1024,
		"csrf":                sess.CSRF,
		"sessionKind":         sessionKind(sess),
		"delegated":           sess.IsDelegated,
		"delegatedCanSend":    sess.DelegatedCanSend,
		"delegatedMailboxes":  delegatedMailboxes,
		"mailClient":          s.mailClientSettings(r, sess.User.Email),
		"demo":                map[string]any{"email": sess.User.Email, "holdSeconds": 8},
		"serverTime":          time.Now().UTC().Format(time.RFC3339),
		"preferences":         preferences,
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
		if sess.IsDelegated && !(r.Method == http.MethodDelete && r.URL.Path == "/api/delegation/session") {
			if !s.delegatedAccessCurrent(r, sess) {
				writeError(w, http.StatusUnauthorized, "DELEGATION_REVOKED", "This mailbox delegation has been removed. The delegated window is now closed.")
				return
			}
		}
		if sess.IsDelegated && !delegatedSessionAllowed(r.Method, r.URL.Path) {
			writeError(w, http.StatusForbidden, "DELEGATED_RESTRICTED", "This action is not available from a delegated mailbox window")
			return
		}
		if sess.IsDelegated && !sess.DelegatedCanSend && delegatedSendPath(r.Method, r.URL.Path) {
			writeError(w, http.StatusForbidden, "DELEGATED_READ_ONLY", "This mailbox is delegated with read-only access")
			return
		}
		if passwordChangeRequired(sess.User) && r.URL.Path != "/api/password/change" && r.URL.Path != "/api/bootstrap" && r.URL.Path != "/api/session" && r.URL.Path != "/api/onboarding" && r.URL.Path != "/api/onboarding/complete" {
			writeError(w, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "Change your password before continuing")
			return
		}
		if sess.User.OnboardingRequired && !onboardingAllowedPath(r.URL.Path) {
			writeError(w, http.StatusForbidden, "ONBOARDING_REQUIRED", "Complete your Alt8Mail setup before continuing")
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

func onboardingAllowedPath(path string) bool {
	switch path {
	case "/api/onboarding", "/api/onboarding/complete", "/api/password/change",
		"/api/bootstrap", "/api/session":
		return true
	}
	return false
}

type sessionContextKey struct{}

func (s *Server) requireSessionFromContext(r *http.Request) *sessionRecord {
	sess, _ := r.Context().Value(sessionContextKey{}).(*sessionRecord)
	return sess
}

func (s *Server) authenticate(r *http.Request) (*sessionRecord, error) {
	rawToken := ""
	isDelegated := false
	if headerToken := strings.TrimSpace(r.Header.Get(delegatedSessionHeader)); headerToken != "" {
		rawToken = headerToken
		isDelegated = true
	} else {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			return nil, errors.New("no session cookie")
		}
		rawToken = cookie.Value
	}
	tokenHash := hashToken(rawToken)
	var sess sessionRecord
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT s.id, s.user_id::text, s.tenant_id::text, s.csrf_token, s.expires_at,
		       u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color,
		       u.role, u.status, COALESCE(u.password_hash, ''), u.preferences, u.security,
		       u.quota_bytes, u.used_bytes, u.enrollment_required, u.force_password_change,
		       u.onboarding_required, u.password_expires_at,
		       COALESCE(s.parent_session_id, ''), COALESCE(s.delegated_by_user_id::text, ''),
		       COALESCE(s.delegated_owner_id::text, ''), COALESCE(s.delegated_can_send, false),
		       COALESCE(pu.email::text, '')
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN tenants t ON t.id = u.tenant_id AND t.status = 'active'
		LEFT JOIN sessions p ON p.id = s.parent_session_id
		LEFT JOIN users pu ON pu.id = p.user_id AND pu.status = 'active' AND pu.deleted_at IS NULL
		WHERE s.id = $1 AND s.expires_at > now() AND u.status = 'active'
		  AND (
		    ($2 AND s.parent_session_id IS NOT NULL AND p.id IS NOT NULL
		         AND p.user_id = s.delegated_by_user_id
		         AND p.expires_at > now() AND pu.id IS NOT NULL)
		    OR
		    (NOT $2 AND s.parent_session_id IS NULL)
		  )
	`, tokenHash, isDelegated).Scan(
		&sess.ID, &sess.UserID, &sess.TenantID, &sess.CSRF, &sess.ExpiresAt,
		&sess.User.ID, &sess.User.TenantID, &sess.User.Email, &sess.User.Name, &sess.User.Initials,
		&sess.User.Color, &sess.User.Role, &sess.User.Status, &sess.User.PasswordHash,
		&sess.User.Preferences, &sess.User.Security, &sess.User.QuotaBytes, &sess.User.UsedBytes,
		&sess.User.EnrollmentRequired, &sess.User.ForcePasswordChange, &sess.User.OnboardingRequired,
		&sess.User.PasswordExpiresAt, &sess.ParentSessionID, &sess.DelegatedByID,
		&sess.DelegatedOwnerID, &sess.DelegatedCanSend, &sess.DelegatedByEmail,
	)
	if err != nil {
		return nil, err
	}
	sess.User.ID = sess.UserID
	sess.User.TenantID = sess.TenantID
	sess.IsDelegated = isDelegated
	if sess.IsDelegated {
		// A mailbox delegate never inherits the owner's administrative role or
		// the owner's account-lifecycle prompts. The primary sign-in already
		// completed those checks.
		sess.User.Role = "member"
		sess.User.ForcePasswordChange = false
		sess.User.OnboardingRequired = false
		sess.User.PasswordExpiresAt = nil
	}
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
		       u.force_password_change, u.onboarding_required, u.password_expires_at
		FROM users u JOIN tenants t ON t.id = u.tenant_id AND t.status = 'active'
		WHERE u.email = $1 AND u.status = 'active' AND u.deleted_at IS NULL
	`, email).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color,
		&user.Role, &user.Status, &user.PasswordHash, &user.Preferences, &user.Security,
		&user.QuotaBytes, &user.UsedBytes, &user.EnrollmentRequired, &user.ForcePasswordChange,
		&user.OnboardingRequired, &user.PasswordExpiresAt,
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
	if sess, ok := r.Context().Value(sessionContextKey{}).(*sessionRecord); ok {
		detail = delegatedAuditDetail(sess, detail)
	}
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
		"onboardingRequired":     user.OnboardingRequired,
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
		"saveCopyEnabled": true, "spellCheckEnabled": true, "autoLoadImages": true,
		"theme": "system", "accent": "blue", "density": "comfortable",
		"markReadOnOpen": true, "inboxView": "grouped", "defaultPriority": "normal", "defaultReceipt": false,
		"undoSendSeconds": 8, "notifyDesktop": true, "notifySound": false,
		"notifyCalendar": true, "externalWarnings": true,
		"composeMode": "html", "composeWidth": "wide", "composePosition": "center",
		"quickActions": []string{"archive", "markRead", "delete"},
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
		{"id": "inbox", "label": "Inbox", "icon": "i-inbox", "section": "favorites", "system": true, "imapFolder": "INBOX", "order": 0, "badgeMode": "unread"},
		{"id": "starred", "label": "Starred", "icon": "i-star", "section": "favorites", "virtual": true, "system": true, "order": 1, "badgeMode": "none"},
		{"id": "snoozed", "label": "Snoozed", "icon": "i-clock", "section": "favorites", "virtual": true, "system": true, "order": 1, "badgeMode": "none"},
		{"id": "sent", "label": "Sent", "icon": "i-send", "section": "folders", "system": true, "imapFolder": "Sent", "order": 2, "badgeMode": "unread"},
		{"id": "drafts", "label": "Drafts", "icon": "i-file", "section": "folders", "system": true, "imapFolder": "Drafts", "order": 3, "badgeMode": "unread"},
		{"id": "archive", "label": "Archive", "icon": "i-archive", "section": "folders", "system": true, "imapFolder": "Archive", "order": 4, "badgeMode": "unread"},
		{"id": "spam", "label": "Spam", "icon": "i-alert", "section": "folders", "system": true, "imapFolder": "Junk", "order": 5, "badgeMode": "unread"},
		{"id": "trash", "label": "Trash", "icon": "i-trash", "section": "folders", "system": true, "imapFolder": "Trash", "order": 6, "badgeMode": "unread"},
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
		"labels", "message-filters", "calendar-realtime", "mail-realtime", "cabinet-folders", "storage-policy", "file-context-menu",
		"web-push", "mail-pulse", "sticky-notes", "reaction-catalog",
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

const (
	// loginLimiterMaxEntries bounds every per-IP limiter's map so an
	// internet-facing endpoint cannot grow it without limit.
	loginLimiterMaxEntries = 10000
	// loginLimiterMaxSweepInterval caps how often the map is scanned. Shorter
	// limiter windows sweep more often (once per window).
	loginLimiterMaxSweepInterval = time.Minute
)

type loginLimiter struct {
	mu         sync.Mutex
	attempts   map[string]limiterEntry
	limit      int
	window     time.Duration
	maxEntries int
	sweepEvery time.Duration
	lastSweep  time.Time
}

type limiterEntry struct {
	Count   int
	ResetAt time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	sweepEvery := window
	if sweepEvery <= 0 || sweepEvery > loginLimiterMaxSweepInterval {
		sweepEvery = loginLimiterMaxSweepInterval
	}
	return &loginLimiter{
		attempts:   map[string]limiterEntry{},
		limit:      limit,
		window:     window,
		maxEntries: loginLimiterMaxEntries,
		sweepEvery: sweepEvery,
		lastSweep:  time.Now(),
	}
}

func (l *loginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweepLocked(now)
	entry := l.attempts[ip]
	if now.After(entry.ResetAt) {
		delete(l.attempts, ip)
		return true
	}
	return entry.Count < l.limit
}

func (l *loginLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweepLocked(now)
	entry, exists := l.attempts[ip]
	if !exists || now.After(entry.ResetAt) {
		if !exists {
			l.ensureCapacityLocked(now)
		}
		entry = limiterEntry{ResetAt: now.Add(l.window)}
	}
	entry.Count++
	l.attempts[ip] = entry
}

func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// sweepLocked periodically deletes entries whose window has elapsed. It is
// called under the limiter lock from Allow and Fail, so the map is swept even
// when callers only ever test new addresses.
func (l *loginLimiter) sweepLocked(now time.Time) {
	interval := l.sweepEvery
	if interval <= 0 {
		interval = loginLimiterMaxSweepInterval
	}
	if now.Sub(l.lastSweep) < interval {
		return
	}
	for key, entry := range l.attempts {
		if now.After(entry.ResetAt) {
			delete(l.attempts, key)
		}
	}
	l.lastSweep = now
}

// ensureCapacityLocked makes room for one new address. Expired entries are
// evicted first; if every entry is still active, the oldest entry (earliest
// reset) is evicted so memory stays bounded. Existing counters are never
// touched unless the map is already full.
func (l *loginLimiter) ensureCapacityLocked(now time.Time) {
	maxEntries := l.maxEntries
	if maxEntries <= 0 {
		maxEntries = loginLimiterMaxEntries
	}
	if len(l.attempts) < maxEntries {
		return
	}
	for key, entry := range l.attempts {
		if now.After(entry.ResetAt) {
			delete(l.attempts, key)
		}
	}
	for len(l.attempts) >= maxEntries {
		oldestKey := ""
		var oldestReset time.Time
		for key, entry := range l.attempts {
			if oldestKey == "" || entry.ResetAt.Before(oldestReset) {
				oldestKey, oldestReset = key, entry.ResetAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(l.attempts, oldestKey)
	}
}

// retryAfterSeconds is the limiter window in whole seconds for an HTTP
// Retry-After response.
func (l *loginLimiter) retryAfterSeconds() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	seconds := int(l.window / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
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
