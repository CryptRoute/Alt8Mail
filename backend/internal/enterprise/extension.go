// Package enterprise defines the narrow bridge between the public Alt8Mail
// core and the private paid implementation. Community builds never link an
// extension. Enterprise builds import the private module for its init-time
// registration and use the same core server.
package enterprise

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Principal is the authenticated actor a private module may attribute work to.
// The raw session token, CSRF secret and password material never cross the
// bridge.
type Principal struct {
	UserID      string
	TenantID    string
	Email       string
	Name        string
	Role        string
	SystemAdmin bool
}

// PaidUI describes a paid console surface the private module owns. The public
// shell can render navigation and upgrade metadata without importing the
// private implementation.
type PaidUI struct {
	Feature     string `json:"feature"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Admin       bool   `json:"admin"`
}

// SeatPolicy is the effective active-user policy the private module can read.
// It is informational: the core remains the enforcement point.
type SeatPolicy struct {
	Seats  int
	Tenant string
	Source string
}

// FileRef is the file metadata a paid file module may see. File content is
// returned separately by LoadFileContent and never cached by the host.
type FileRef struct {
	ID             string
	Name           string
	MIME           string
	OwnerID        string
	TenantID       string
	Classification string
}

// FilePolicy is the tenant's file policy projected into the private module.
type FilePolicy struct {
	RetentionDays             int
	VersionRetentionDays      int
	RetentionByClassification map[string]int
	WatermarkDownloads        bool
}

// FileHooks are installed by the private file-governance module. Core file
// download and delete paths call them when present; community builds keep the
// permissive defaults.
type FileHooks struct {
	// Watermark returns the download watermark for a file, or "" for none.
	Watermark func(ctx context.Context, file FileRef, actorEmail string) string
	// OnLegalHold reports whether a file is protected by an active hold.
	OnLegalHold func(ctx context.Context, tenantID, fileID, ownerID string) (bool, error)
}

// FileAccess is the core capability a paid file module needs. It is separate
// from the generic Host so file storage internals never leak into other
// modules.
type FileAccess interface {
	RegisterFileHooks(hooks FileHooks)
	LoadFileContent(ctx context.Context, principal Principal, fileID string) (FileRef, []byte, error)
	FilePolicy(ctx context.Context, tenantID string) FilePolicy
	PurgeFileRecord(ctx context.Context, fileID, ownerID, tenantID string) error
	DeleteOfficeVersion(ctx context.Context, versionID, storageKey string) error
}

// CampaignTrackFunc wraps an outbound campaign body with open/click tracking.
// It returns the rewritten HTML and the tracking row id.
type CampaignTrackFunc func(ctx context.Context, principal Principal, mergeID, outboxID, email, bodyHTML, baseURL string) (renderedHTML, trackingID string, err error)

// TrackingAccess is the core capability used by paid campaign tracking.
type TrackingAccess interface {
	RegisterCampaignTracking(track CampaignTrackFunc)
	// ClientIP returns the request's client address using the core proxy rules.
	ClientIP(r *http.Request) string
	// SanitizeUserAgent bounds and flattens a user-agent header for storage.
	SanitizeUserAgent(value string) string
}

// IntegrationAccess exposes tenant add-on credentials and outbound webhook
// events to private integration modules without leaking storage internals.
type IntegrationAccess interface {
	// AddonSecret returns the decrypted credential JSON for an active tenant
	// add-on connection. found is false when no connection is active.
	AddonSecret(ctx context.Context, tenantID, addonID string) (plaintext string, found bool, err error)
	// EnqueueWebhookEvent queues a tenant webhook delivery.
	EnqueueWebhookEvent(ctx context.Context, tenantID, event string, payload map[string]any)
}

// MailPolicy is the tenant mail policy projection private send modules need.
type MailPolicy struct {
	CampaignTrackingEnabled bool
	PaymentLinksEnabled     bool
	PaymentLinkDomains      []string
}

// MailAccess exposes the public URL, mail policy and outbound policy checks to
// paid send modules.
type MailAccess interface {
	// PublicBaseURL returns the configured public URL or the request origin.
	PublicBaseURL(r *http.Request) string
	// MailPolicy returns the tenant's mail policy.
	MailPolicy(ctx context.Context, tenantID string) MailPolicy
	// ValidateOutbound applies recipient, rate and DLP policy to a programmatic
	// message. It returns the resulting classification.
	ValidateOutbound(ctx context.Context, principal Principal, recipients []string, subject, bodyText, classification string) (string, error)
}

// DirectoryAccess resolves active tenant users for paid collaboration and
// scheduling modules.
type DirectoryAccess interface {
	WorkspaceUser(ctx context.Context, tenantID, email string) (UserRef, error)
	UserByID(ctx context.Context, userID string) (UserRef, error)
	UserByEmail(ctx context.Context, email string) (UserRef, error)
}

// TeamRef identifies a team inbox without exposing its storage model.
type TeamRef struct {
	ID            string
	TenantID      string
	Name          string
	Address       string
	OwnerID       string
	OwnerName     string
	OwnerEmail    string
	GroupID       string
	GroupName     string
	Strategy      string
	SLAFirst      int
	SLAResolution int
	CanSend       bool
	Active        bool
	Members       int
}

// ErrTeamNotFound is returned when a team does not exist or the principal is
// not a member of it.
var ErrTeamNotFound = errors.New("team not found")

// TeamAccess authorizes paid team collaboration modules.
type TeamAccess interface {
	TeamMailboxForUser(ctx context.Context, principal Principal, teamID string) (TeamRef, error)
	TeamMailbox(ctx context.Context, tenantID, teamID string) (TeamRef, error)
	TeamCanManage(ctx context.Context, principal Principal, team TeamRef) bool
}

// MailAttachment is one attachment read from a local mailbox by a paid module.
type MailAttachment struct {
	Name        string
	MIMEType    string
	ContentID   string
	Disposition string
	Data        []byte
}

// MailboxAccess lets private deliverability modules read report mail without
// exposing IMAP credentials or connection types.
type MailboxAccess interface {
	MailSyncConfigured() bool
	WorkspaceMailboxEmail(ctx context.Context, tenantID, email string) (string, error)
	// FetchMailboxAttachments reads attachments from messages with UID greater
	// than afterUID and returns the highest UID that was successfully read.
	FetchMailboxAttachments(ctx context.Context, email, folder string, afterUID int64, limit int) ([]MailAttachment, int64, error)
}

// LicenseInfo is the license state a paid support module may report. The
// token and signature never cross the bridge.
type LicenseInfo struct {
	Loaded       bool
	TrustAnchor  string
	ID           string
	Edition      string
	Plan         string
	State        string
	Seats        int
	Trial        bool
	DeploymentID string
	ExpiresAt    time.Time
}

// SupportAccess exposes read-only deployment facts to paid support modules.
type SupportAccess interface {
	Version() string
	PublicURL() string
	LicenseInfo() LicenseInfo
	AIConfigured(ctx context.Context, tenantID string) bool
	ValidateWebhookURL(raw string) error
	PostWebhook(ctx context.Context, endpoint string, headers map[string]string, body []byte) (int, error)
}

// ErrAIBudget is returned when a tenant's monthly AI budget is exhausted.
var ErrAIBudget = errors.New("AI monthly budget is exhausted")

// AISettings is the tenant AI configuration a paid module may use.
type AISettings struct {
	TenantID            string
	Enabled             bool
	Provider            string
	Endpoint            string
	Model               string
	APIKey              string
	MaxTokens           int
	Temperature         float64
	SystemPrompt        string
	TimeoutSeconds      int
	MonthlyBudgetCents  int
	TriageAutoLabel     bool
	TriageMinConfidence float64
	ObligationsAutoTask bool
}

// AIService exposes the core AI client to private AI modules.
type AIService interface {
	AISettings(ctx context.Context, tenantID string) (AISettings, error)
	CallAI(ctx context.Context, settings AISettings, system, user string, maxTokens int, temperature float64) (string, error)
}

// AIBudgetCheck rejects an AI call when the tenant budget is exhausted.
type AIBudgetCheck func(ctx context.Context, tenantID string) error

// AIRecordUsage records estimated AI usage for the current month.
type AIRecordUsage func(ctx context.Context, tenantID string, inputChars, outputChars int)

// AIBudgetAccess lets the private budget module install enforcement hooks used
// by every core AI call.
type AIBudgetAccess interface {
	RegisterAIBudgetHooks(check AIBudgetCheck, record AIRecordUsage)
}

// DLPMatch is a sanitized DLP rule match.
type DLPMatch struct {
	RuleID         string `json:"ruleId"`
	RuleName       string `json:"ruleName"`
	Detector       string `json:"detector"`
	Term           string `json:"term"`
	Field          string `json:"field"`
	Action         string `json:"action"`
	Classification string `json:"classification"`
}

// DLPAccess exposes the core outbound DLP evaluation to paid modules.
type DLPAccess interface {
	EvaluateDLP(ctx context.Context, tenantID, subject, body string, attachments []string) (classification string, warnings []string, matches []DLPMatch, blocked bool)
	// RedactForAI removes configured DLP terms and detector values from text
	// before it is sent to an external model.
	RedactForAI(ctx context.Context, tenantID, text string) string
}

// OfficeAIAccess exposes Office policy and document text to the paid Office AI
// module.
type OfficeAIAccess interface {
	OfficeAIAllowed(ctx context.Context, principal Principal) bool
	OfficeDocumentText(ctx context.Context, principal Principal, documentID string) (name, text string, err error)
	RegisterOfficeAIEnabled(check func(ctx context.Context, principal Principal) bool)
}

// MCPPolicy is the tenant MCP governance policy projection.
type MCPPolicy struct {
	AllowedScopes             []string
	WriteApprovalRequired     bool
	MaxRequestsPerHour        int
	ConnectionRequestsPerHour int
	ToolApprovals             map[string]string
}

// MCPAccess exposes MCP governance policy to the private MCP module.
type MCPAccess interface {
	MCPPolicy(ctx context.Context, tenantID string) MCPPolicy
	// MCPRateLimit consumes one in-memory request permit for the client IP.
	MCPRateLimit(ip string) bool
}

// SSOConfig is the deployment OIDC and SAML configuration.
type SSOConfig struct {
	Enabled      bool
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	DisplayName  string
	EmailDomains []string
	SessionTTL   time.Duration
	RememberTTL  time.Duration
	CookieSecure bool

	SAMLEnabled         bool
	SAMLDisplayName     string
	SAMLPublicURL       string
	SAMLSPEntityID      string
	SAMLSPCertFile      string
	SAMLSPKeyFile       string
	SAMLIDPMetadataURL  string
	SAMLIDPMetadataFile string
	SAMLEmailAttribute  string
	SAMLEmailDomains    []string
	SAMLMFAContexts     []string
}

// SSOSecurityPolicy is the tenant sign-in policy projection.
type SSOSecurityPolicy struct {
	AllowedIPRanges []string
	MFAEnforcement  string
}

// SSOAccess exposes OIDC configuration, session issuance and signed state to
// the private SSO module. Session and CSRF secrets never cross the bridge.
type SSOAccess interface {
	SSOConfig() SSOConfig
	SSOSecurityPolicy(ctx context.Context, tenantID string) SSOSecurityPolicy
	IssueSession(w http.ResponseWriter, r *http.Request, principal Principal, ttl time.Duration) error
	SSORateLimitAllow(ip string) bool
	SSORateLimitFail(ip string)
	ResetSSORateLimit(ip string)
	SAMLRateLimitAllow(ip string) bool
	SAMLRateLimitFail(ip string)
	ResetSAMLRateLimit(ip string)
	ClearSessionCookies(w http.ResponseWriter)
	SignSSOPayload(payload string) (string, error)
	VerifySSOPayload(value string) (string, error)
}

// BrandingFields is the tenant branding policy projection.
type BrandingFields struct {
	ProductName     string
	PrimaryColor    string
	SupportEmail    string
	LoginMessage    string
	LogoURL         string
	SupportURL      string
	LegalURL        string
	HideAttribution bool
	WebmailHosts    []string
}

// BrandingAccess exposes white-label rendering and hostname ownership to the
// private branding module.
type BrandingAccess interface {
	PublicBrandingPayload(fields BrandingFields) map[string]any
	ValidateBrandingHosts(ctx context.Context, tenantID string, hosts []string) []string
	SyncBrandingHosts(ctx context.Context, tx pgx.Tx, tenantID string, hosts []string) error
}

// BrandingRegistration installs the private branding implementation.
type BrandingRegistration interface {
	RegisterBrandingHooks(
		public func(BrandingFields) map[string]any,
		validate func(context.Context, string, []string) []string,
		sync func(context.Context, pgx.Tx, string, []string) error,
	)
}

// TenantKeyHooks implement BYOK sealing. handled=false means the tenant has no
// active customer key and the core default should be used.
type TenantKeyHooks struct {
	Active     func(ctx context.Context, tenantID string) bool
	SealBlob   func(ctx context.Context, tenantID string, data []byte) (sealed []byte, version int, handled bool, err error)
	OpenBlob   func(ctx context.Context, data []byte) (plaintext []byte, handled bool, err error)
	SealSecret func(ctx context.Context, tenantID, plaintext string) (sealed string, handled bool, err error)
	OpenSecret func(ctx context.Context, tenantID, value string) (plaintext string, handled bool, err error)
}

// TenantKeyAccess installs the private BYOK implementation.
type TenantKeyAccess interface {
	RegisterTenantKeyHooks(hooks TenantKeyHooks)
}

// DLPEngineHooks implement the paid outbound DLP rule engine.
type DLPEngineHooks struct {
	Evaluate func(ctx context.Context, tenantID, subject, body string, attachments []string, selected string) (string, []string, []DLPMatch, bool)
	Redact   func(ctx context.Context, tenantID, text string) string
}

// DLPEngineAccess installs the private DLP engine.
type DLPEngineAccess interface {
	RegisterDLPEngine(hooks DLPEngineHooks)
}

// MailImportProvisionRequest describes one destination account created by a
// paid mailbox migration.
type MailImportProvisionRequest struct {
	TenantID    string
	CreatedByID string
	CreatedBy   string
	Email       string
	Name        string
	Role        string
	Quota       int64
}

// MailImportAccess exposes the core account, mailbox, notice and scanner
// primitives a paid migration module needs.
type MailImportAccess interface {
	ProvisionImportedUser(ctx context.Context, req MailImportProvisionRequest) (webPassword, mailPassword, userID string, err error)
	ReissueImportedUserCredentials(ctx context.Context, userID, email string) (webPassword, mailPassword string, err error)
	LocalIMAPConfig(email string) (addr string, tlsConfig *tls.Config, loginUser, password string, err error)
	SendMigrationNotice(ctx context.Context, tenantID, preferredUserID, to, name, mailbox, password string) error
	ImportScan(ctx context.Context, data []byte) (infected bool, err error)
	// AdminCan reports whether the admin session may perform a permission.
	AdminCan(r *http.Request, permission string) bool
}

// SCIMRoleChange is one user whose role was recomputed from SCIM group
// mappings. NewRole is empty when the mapping was removed and the role was
// reset to the default member role.
type SCIMRoleChange struct {
	UserID      string
	Email       string
	OldRole     string
	NewRole     string
	RoleChanged bool
}

// SCIMProvisionRequest describes an IdP-provisioned user. Password hashes are
// generated by the core so private modules never handle web or mail password
// material.
type SCIMProvisionRequest struct {
	TenantID            string
	Name                string
	Email               string
	Role                string
	WebPasswordHash     string
	MailPasswordHash    string
	Quota               int64
	Status              string
	ForcePasswordChange bool
}

// SCIMAccess exposes the core identity, seat and group-role capabilities the
// private SCIM module needs. Every operation is tenant-scoped by the caller
// from the token-authenticated tenant.
type SCIMAccess interface {
	// SCIMBaseURL returns the public SCIM base URL or an error when the
	// deployment has no valid public HTTPS origin.
	SCIMBaseURL() (string, error)
	// SCIMRateLimitAllow reports whether a SCIM auth attempt from ip is
	// allowed; Fail and Reset update the shared limiter.
	SCIMRateLimitAllow(ip string) bool
	SCIMRateLimitFail(ip string)
	ResetSCIMRateLimit(ip string)
	SCIMRateLimitRetryAfter() int
	// AuditSCIM records an IdP-originated audit event attributed to the token.
	AuditSCIM(r *http.Request, tenantName, tenantID, action, target, detail string)
	// SCIMSeatApplies reports whether the tenant consumes licensed seats.
	SCIMSeatApplies(tenantID string) bool
	// SCIMSeatLimit returns the license seat limit.
	SCIMSeatLimit() int
	// SCIMLockSeatScopeTx locks the tenant seat scope inside tx.
	SCIMLockSeatScopeTx(ctx context.Context, tx pgx.Tx, tenantID string) error
	// SCIMEnsureSeatCapacityTx returns the used seats or an error when one more
	// active user would exceed the license. The sentinel is detected with
	// SCIMSeatLimitReached.
	SCIMEnsureSeatCapacityTx(ctx context.Context, tx pgx.Tx, tenantID string, additional int) (int, error)
	SCIMSeatLimitReached(err error) bool
	// SCIMEnsureTenantDomain verifies the address belongs to a verified or
	// manually approved tenant domain.
	SCIMEnsureTenantDomain(ctx context.Context, tx pgx.Tx, tenantID, email string) error
	// SCIMGenerateCredentialHashes creates random web and mail password hashes
	// for a newly provisioned user.
	SCIMGenerateCredentialHashes() (webHash, mailHash string, err error)
	// SCIMProvisionUserTx creates the user, mailbox and maildir in tx.
	SCIMProvisionUserTx(ctx context.Context, tx pgx.Tx, req SCIMProvisionRequest) (userID string, err error)
	// SCIMEnsureUserState initializes per-user application state after commit.
	// A failure is logged and never fails provisioning.
	SCIMEnsureUserState(ctx context.Context, email string)
	// SCIMRecomputeRolesTx applies group→role mappings to users inside tx.
	SCIMRecomputeRolesTx(ctx context.Context, tx pgx.Tx, tenantID string, userIDs []string) ([]SCIMRoleChange, error)
}

// Host is implemented by the core HTTP server. It deliberately exposes only
// the pieces a paid module needs: route registration with the core auth and
// entitlement wrappers, the authenticated principal, database access,
// configuration, logging and extension registries.
type Host interface {
	// Handle registers an already self-authenticated or public handler.
	Handle(pattern string, handler http.HandlerFunc)
	// HandleUser registers a user-session route and enforces the paid feature.
	HandleUser(pattern, feature string, handler http.HandlerFunc)
	// HandleUserSession registers a user-session route whose handler performs
	// its own entitlement check (for features satisfied by more than one key).
	HandleUserSession(pattern string, handler http.HandlerFunc)
	// HandleAdmin registers an admin route with a permission and feature gate.
	HandleAdmin(pattern, permission, feature string, handler http.HandlerFunc)

	// Admin reports whether this host is the administration mux. A module may
	// register console routes only on the admin host and public routes only on
	// the webmail host.
	Admin() bool
	// Principal returns the admin or user principal attached to r by the core
	// authentication middleware.
	Principal(r *http.Request) (Principal, bool)
	// AuditAdmin records an admin-console audit event for the session on r.
	AuditAdmin(r *http.Request, action, target, detail string, metadata map[string]any)
	// ClientSideEncryption reports whether the tenant runs in client-side
	// encryption mode, where server-side delivery features must refuse to send.
	ClientSideEncryption(ctx context.Context, tenantID string) bool
	// RegisterWorker adds a background worker run when the core starts workers.
	RegisterWorker(worker func(context.Context))
	// RegisterUI adds a paid console descriptor to the public shell.
	RegisterUI(ui PaidUI)
	// SeatPolicy returns the effective active-user policy.
	SeatPolicy() SeatPolicy
	// SealSecret and OpenSecret protect integration secrets at rest.
	SealSecret(value string) (string, error)
	OpenSecret(value string) (string, error)
	// WorkspaceKey returns the deployment's audit-export HMAC key.
	WorkspaceKey() []byte
	// RegisterLegalHold installs the paid legal-hold lookups used by core
	// retention, deletion and offboarding paths. Community builds keep the
	// permissive default.
	RegisterLegalHold(check LegalHoldCheck, snapshot LegalHoldSnapshot)

	UserAccess
	FileAccess
	TrackingAccess
	IntegrationAccess
	MailAccess
	DirectoryAccess
	TeamAccess
	MailboxAccess
	SupportAccess
	AIService
	AIBudgetAccess
	DLPAccess
	OfficeAIAccess
	MCPAccess
	SSOAccess
	BrandingAccess
	BrandingRegistration
	TenantKeyAccess
	DLPEngineAccess
	MailImportAccess
	SCIMAccess

	DB() *pgxpool.Pool
	Logger() *slog.Logger
	Config() config.Config
	Entitled(feature string) bool
}

// LegalHoldCheck reports whether a user is under an active legal hold.
type LegalHoldCheck func(ctx context.Context, tenantID, userID string) (bool, error)

// LegalHoldSnapshot returns the tenant-wide hold and held user ids.
type LegalHoldSnapshot func(ctx context.Context, tenantID string) (bool, map[string]bool, error)

// UserRef identifies another user in the same tenant for collaboration.
type UserRef struct {
	ID       string
	TenantID string
	Email    string
	Name     string
	Role     string
}

// FollowUpViews returns the paid follow-up panel for a user.
type FollowUpViews func(ctx context.Context, userID string) []map[string]any

// Host additions for user-scoped paid modules.
type UserAccess interface {
	// CollaborationOwner resolves a delegated/shared mailbox owner.
	CollaborationOwner(ctx context.Context, principal Principal, ownerID string) (UserRef, error)
	// ThreadBelongsTo reports whether a thread has messages for the owner.
	ThreadBelongsTo(ctx context.Context, ownerID, threadID string) bool
	// AuditUser records a user-session audit event for the request.
	AuditUser(r *http.Request, action, target, detail string)
	// AuditEvent records an audit event for an explicit principal. It is used
	// by public flows such as unsubscribe links that have no session.
	AuditEvent(r *http.Request, principal Principal, action, target, detail string)
	// AuditWorkspace records a workspace audit event for the request session.
	AuditWorkspace(r *http.Request, principal Principal, action, target, detail string)
	// RegisterFollowUpViews installs the paid bootstrap follow-up panel.
	RegisterFollowUpViews(views FollowUpViews)
	// RecipientWindow returns the tenant's recipient-local working hours.
	RecipientWindow(ctx context.Context, tenantID string) (startHour, endHour int)
	// UserTimeLocation returns the user's configured timezone.
	UserTimeLocation(ctx context.Context, principal Principal) *time.Location
}

// Extension is implemented by the private enterprise module.
type Extension interface {
	Register(Host) error
}

var registered Extension

// Register is called from the private module's init function. Only one
// extension may be installed in a process.
func Register(extension Extension) {
	registered = extension
}

// Registered returns the private extension, or nil in a community build.
func Registered() Extension {
	return registered
}

// ResetForTest clears the process-wide registration.
func ResetForTest() {
	registered = nil
}

// Stub is the community-build provider. It registers nothing and exists so the
// public core always has a provider to call, rather than relying on a nil
// special case.
type Stub struct{}

func (Stub) Register(Host) error { return nil }
