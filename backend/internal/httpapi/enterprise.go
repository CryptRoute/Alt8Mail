package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type enterpriseHost struct {
	server *Server
	mux    *http.ServeMux
	admin  bool
}

func (h enterpriseHost) Handle(pattern string, handler http.HandlerFunc) {
	h.mux.HandleFunc(pattern, handler)
}

func (h enterpriseHost) HandleUser(pattern, feature string, handler http.HandlerFunc) {
	h.mux.HandleFunc(pattern, h.server.requireUser(featureGate(h.server, feature, handler)))
}

func (h enterpriseHost) HandleUserSession(pattern string, handler http.HandlerFunc) {
	h.mux.HandleFunc(pattern, h.server.requireUser(handler))
}

func (h enterpriseHost) HandleAdmin(pattern, permission, feature string, handler http.HandlerFunc) {
	h.mux.HandleFunc(pattern, h.server.adminRequirePermission(permission, featureGate(h.server, feature, handler)))
}

func (h enterpriseHost) Admin() bool {
	return h.admin
}

func (h enterpriseHost) Principal(r *http.Request) (enterprise.Principal, bool) {
	if h.admin {
		sess := h.server.requireAdminSession(r)
		if sess == nil || sess.User.ID == "" {
			return enterprise.Principal{}, false
		}
		return enterprise.Principal{
			UserID:      sess.User.ID,
			TenantID:    sess.ActiveTenantID,
			Email:       sess.User.Email,
			Name:        sess.User.Name,
			Role:        sess.User.Role,
			SystemAdmin: sess.User.IsSystemAdmin,
		}, true
	}
	sess := h.server.requireSessionFromContext(r)
	if sess == nil || sess.User.ID == "" {
		return enterprise.Principal{}, false
	}
	return enterprise.Principal{
		UserID:   sess.User.ID,
		TenantID: sess.User.TenantID,
		Email:    sess.User.Email,
		Name:     sess.User.Name,
		Role:     sess.User.Role,
	}, true
}

func (h enterpriseHost) AuditAdmin(r *http.Request, action, target, detail string, metadata map[string]any) {
	sess := h.server.requireAdminSession(r)
	if sess == nil {
		return
	}
	h.server.recordAdminAudit(r.Context(), sess, sess.User, action, target, detail, metadata)
}

func (h enterpriseHost) ClientSideEncryption(ctx context.Context, tenantID string) bool {
	return h.server.tenantPolicyFor(ctx, tenantID).Encryption.Mode == "client_side"
}

func (h enterpriseHost) RegisterWorker(worker func(context.Context)) {
	if worker == nil {
		return
	}
	h.server.enterpriseWorkersMu.Lock()
	h.server.enterpriseWorkers = append(h.server.enterpriseWorkers, worker)
	h.server.enterpriseWorkersMu.Unlock()
}

func (h enterpriseHost) RegisterUI(ui enterprise.PaidUI) {
	if ui.Feature == "" || ui.Path == "" {
		return
	}
	h.server.paidUIMu.Lock()
	h.server.paidUI = append(h.server.paidUI, ui)
	h.server.paidUIMu.Unlock()
}

func (h enterpriseHost) SeatPolicy() enterprise.SeatPolicy {
	return enterprise.SeatPolicy{
		Seats:  h.server.licenseSeatLimit(),
		Tenant: h.server.licenseSeatTenant(),
		Source: h.server.seatSource(),
	}
}

func (h enterpriseHost) SealSecret(value string) (string, error) {
	return h.server.sealSecuritySecret(value)
}

func (h enterpriseHost) OpenSecret(value string) (string, error) {
	return h.server.openSecuritySecret(value)
}

func (h enterpriseHost) WorkspaceKey() []byte {
	return h.server.workspaceKey()
}

func (h enterpriseHost) RegisterFileHooks(hooks enterprise.FileHooks) {
	h.server.fileHooksMu.Lock()
	h.server.fileHooks = hooks
	h.server.fileHooksMu.Unlock()
}

func (h enterpriseHost) LoadFileContent(ctx context.Context, principal enterprise.Principal, fileID string) (enterprise.FileRef, []byte, error) {
	user := userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}
	file, data, err := h.server.readAccessibleFileContent(ctx, user, fileID)
	if err != nil {
		return enterprise.FileRef{}, nil, err
	}
	return enterprise.FileRef{
		ID: file.ID, Name: file.Name, MIME: file.MIME, OwnerID: file.OwnerID,
		TenantID: file.TenantID, Classification: fileClassification(file),
	}, data, nil
}

func (h enterpriseHost) FilePolicy(ctx context.Context, tenantID string) enterprise.FilePolicy {
	policy := h.server.tenantPolicyFor(ctx, tenantID).Files
	retention := make(map[string]int, len(policy.RetentionByClassification))
	for class, days := range policy.RetentionByClassification {
		retention[class] = days
	}
	return enterprise.FilePolicy{
		RetentionDays:             policy.RetentionDays,
		VersionRetentionDays:      policy.VersionRetentionDays,
		RetentionByClassification: retention,
		WatermarkDownloads:        policy.WatermarkDownloads,
	}
}

func (h enterpriseHost) PurgeFileRecord(ctx context.Context, fileID, ownerID, tenantID string) error {
	return h.server.purgeFileRecord(ctx, fileID, ownerID, tenantID)
}

func (h enterpriseHost) DeleteOfficeVersion(ctx context.Context, versionID, storageKey string) error {
	return h.server.deleteOfficeVersion(ctx, versionID, storageKey)
}

func (h enterpriseHost) RegisterCampaignTracking(track enterprise.CampaignTrackFunc) {
	h.server.campaignTrackMu.Lock()
	h.server.campaignTrackHook = track
	h.server.campaignTrackMu.Unlock()
}

func (h enterpriseHost) ClientIP(r *http.Request) string {
	return clientIP(r)
}

func (h enterpriseHost) SanitizeUserAgent(value string) string {
	return sanitizeUserAgent(value)
}

func (h enterpriseHost) AddonSecret(ctx context.Context, tenantID, addonID string) (string, bool, error) {
	connection, ok := h.server.addonActiveConnection(ctx, tenantID, addonID, "", "tenant")
	if !ok {
		return "", false, nil
	}
	plain, err := h.server.openWorkspaceToken(connection.Sealed)
	if err != nil {
		return "", true, err
	}
	return plain, true, nil
}

func (h enterpriseHost) EnqueueWebhookEvent(ctx context.Context, tenantID, event string, payload map[string]any) {
	h.server.enqueueWebhookEvent(ctx, tenantID, event, payload)
}

func (h enterpriseHost) RegisterLegalHold(check enterprise.LegalHoldCheck, snapshot enterprise.LegalHoldSnapshot) {
	if check != nil {
		h.server.legalHoldCheck = check
	}
	if snapshot != nil {
		h.server.legalHoldSnapshot = snapshot
	}
}

func (h enterpriseHost) CollaborationOwner(ctx context.Context, principal enterprise.Principal, ownerID string) (enterprise.UserRef, error) {
	owner, _, err := h.server.collaborationOwner(ctx, userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}, ownerID)
	if err != nil {
		return enterprise.UserRef{}, err
	}
	return enterprise.UserRef{ID: owner.ID, TenantID: owner.TenantID, Email: owner.Email}, nil
}

func (h enterpriseHost) ThreadBelongsTo(ctx context.Context, ownerID, threadID string) bool {
	return h.server.threadBelongsTo(ctx, ownerID, threadID)
}

func (h enterpriseHost) AuditUser(r *http.Request, action, target, detail string) {
	sess := h.server.requireSessionFromContext(r)
	if sess == nil {
		return
	}
	h.server.audit(r, sess.User, action, target, detail)
}

func (h enterpriseHost) AuditEvent(r *http.Request, principal enterprise.Principal, action, target, detail string) {
	h.server.audit(r, userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}, action, target, detail)
}

func (h enterpriseHost) AuditWorkspace(r *http.Request, principal enterprise.Principal, action, target, detail string) {
	sess := h.server.requireSessionFromContext(r)
	if sess == nil {
		return
	}
	h.server.recordWorkspaceAudit(r.Context(), sess, action, target, detail)
}

func (h enterpriseHost) PublicBaseURL(r *http.Request) string {
	return h.server.publicBaseURL(r)
}

func (h enterpriseHost) MailPolicy(ctx context.Context, tenantID string) enterprise.MailPolicy {
	policy := h.server.tenantPolicyFor(ctx, tenantID)
	return enterprise.MailPolicy{
		CampaignTrackingEnabled: policy.Mail.CampaignTrackingEnabled,
		PaymentLinksEnabled:     policy.Mail.PaymentLinksEnabled,
		PaymentLinkDomains:      append([]string(nil), policy.Mail.PaymentLinkDomains...),
	}
}

func (h enterpriseHost) WorkspaceUser(ctx context.Context, tenantID, email string) (enterprise.UserRef, error) {
	user, err := h.server.workspaceUserByEmail(ctx, tenantID, email)
	if err != nil {
		return enterprise.UserRef{}, err
	}
	return enterprise.UserRef{ID: user.ID, TenantID: user.TenantID, Email: user.Email, Name: user.Name}, nil
}

func (h enterpriseHost) UserByID(ctx context.Context, userID string) (enterprise.UserRef, error) {
	user, err := h.server.userByID(ctx, userID)
	if err != nil {
		return enterprise.UserRef{}, err
	}
	return enterprise.UserRef{ID: user.ID, TenantID: user.TenantID, Email: user.Email, Name: user.Name, Role: user.Role}, nil
}

func (h enterpriseHost) UserByEmail(ctx context.Context, email string) (enterprise.UserRef, error) {
	user, err := h.server.userByEmail(ctx, email)
	if err != nil {
		return enterprise.UserRef{}, err
	}
	return enterprise.UserRef{ID: user.ID, TenantID: user.TenantID, Email: user.Email, Name: user.Name, Role: user.Role}, nil
}

func (h enterpriseHost) TeamMailbox(ctx context.Context, tenantID, teamID string) (enterprise.TeamRef, error) {
	team, err := h.server.teamMailboxRecord(ctx, tenantID, teamID)
	if err != nil {
		return enterprise.TeamRef{}, enterprise.ErrTeamNotFound
	}
	return enterprise.TeamRef{
		ID: team.ID, TenantID: team.TenantID, Name: team.Name, Address: team.Address,
		OwnerID: team.OwnerID, OwnerName: team.OwnerName, OwnerEmail: team.OwnerEmail,
		GroupID: team.GroupID, GroupName: team.GroupName, Strategy: team.Strategy,
		SLAFirst: team.SLAFirst, SLAResolution: team.SLAResolution, CanSend: team.CanSend,
		Active: team.Active, Members: team.Members,
	}, nil
}

func (h enterpriseHost) ValidateOutbound(ctx context.Context, principal enterprise.Principal, recipients []string, subject, bodyText, classification string) (string, error) {
	user := userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}
	result, _, _, err := h.server.validateProgrammaticSend(ctx, user, recipients, nil, nil, subject, bodyText, classification)
	return result, err
}

func (h enterpriseHost) TeamMailboxForUser(ctx context.Context, principal enterprise.Principal, teamID string) (enterprise.TeamRef, error) {
	user := userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}
	team, err := h.server.teamMailboxForUser(ctx, user, teamID)
	if err != nil {
		return enterprise.TeamRef{}, enterprise.ErrTeamNotFound
	}
	return enterprise.TeamRef{
		ID: team.ID, TenantID: team.TenantID, Name: team.Name, Address: team.Address,
		OwnerID: team.OwnerID, OwnerName: team.OwnerName, OwnerEmail: team.OwnerEmail,
		GroupID: team.GroupID, GroupName: team.GroupName, Strategy: team.Strategy,
		SLAFirst: team.SLAFirst, SLAResolution: team.SLAResolution, CanSend: team.CanSend,
		Active: team.Active, Members: team.Members,
	}, nil
}

func (h enterpriseHost) TeamCanManage(ctx context.Context, principal enterprise.Principal, team enterprise.TeamRef) bool {
	return h.server.teamCanManage(ctx, teamMailboxRecord{
		ID: team.ID, TenantID: team.TenantID, OwnerID: team.OwnerID, GroupID: team.GroupID,
	}, userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	})
}

func (h enterpriseHost) MailSyncConfigured() bool {
	return h.server.mailSyncConfigured()
}

func (h enterpriseHost) WorkspaceMailboxEmail(ctx context.Context, tenantID, email string) (string, error) {
	user, err := h.server.workspaceUserByEmail(ctx, tenantID, email)
	if err != nil {
		return "", err
	}
	return user.Email, nil
}

func (h enterpriseHost) FetchMailboxAttachments(ctx context.Context, email, folder string, afterUID int64, limit int) ([]enterprise.MailAttachment, int64, error) {
	return h.server.fetchMailboxAttachments(ctx, email, folder, afterUID, limit)
}

func (h enterpriseHost) Version() string {
	return h.server.version
}

func (h enterpriseHost) PublicURL() string {
	return h.server.cfg.PublicURL
}

func (h enterpriseHost) LicenseInfo() enterprise.LicenseInfo {
	info := enterprise.LicenseInfo{TrustAnchor: h.server.licenseTrustSource()}
	license := h.server.license
	if license == nil {
		return info
	}
	status := license.Status(h.server.licenseTime())
	info.Loaded = true
	info.ID = license.ID
	info.Edition = license.Edition
	info.Plan = license.EffectivePlan()
	info.State = status.State
	info.Seats = status.SeatLimit
	info.Trial = license.Trial
	info.DeploymentID = license.DeploymentID
	info.ExpiresAt = license.ExpiresAt
	return info
}

func (h enterpriseHost) AIConfigured(ctx context.Context, tenantID string) bool {
	settings, err := h.server.aiSettingsFor(ctx, tenantID)
	return err == nil && settings.Enabled && settings.Model != ""
}

func (h enterpriseHost) ValidateWebhookURL(raw string) error {
	_, err := h.server.validateWebhookURL(raw)
	return err
}

func (h enterpriseHost) PostWebhook(ctx context.Context, endpoint string, headers map[string]string, body []byte) (int, error) {
	return h.server.postWebhook(ctx, endpoint, headers, body)
}

func (h enterpriseHost) AISettings(ctx context.Context, tenantID string) (enterprise.AISettings, error) {
	settings, err := h.server.aiSettingsFor(ctx, tenantID)
	if err != nil {
		return enterprise.AISettings{}, err
	}
	return enterprise.AISettings{
		TenantID: settings.TenantID, Enabled: settings.Enabled, Provider: settings.Provider,
		Endpoint: settings.Endpoint, Model: settings.Model, APIKey: settings.APIKey,
		MaxTokens: settings.MaxTokens, Temperature: settings.Temperature,
		SystemPrompt: settings.SystemPrompt, TimeoutSeconds: settings.TimeoutSeconds,
		MonthlyBudgetCents: settings.MonthlyBudgetCents, TriageAutoLabel: settings.TriageAutoLabel,
		TriageMinConfidence: settings.TriageMinConfidence, ObligationsAutoTask: settings.ObligationsAutoTask,
	}, nil
}

func (h enterpriseHost) CallAI(ctx context.Context, settings enterprise.AISettings, system, user string, maxTokens int, temperature float64) (string, error) {
	return h.server.callAI(ctx, aiSettings{
		TenantID: settings.TenantID, Enabled: settings.Enabled, Provider: settings.Provider,
		Endpoint: settings.Endpoint, Model: settings.Model, APIKey: settings.APIKey,
		MaxTokens: settings.MaxTokens, Temperature: settings.Temperature,
		SystemPrompt: settings.SystemPrompt, TimeoutSeconds: settings.TimeoutSeconds,
		MonthlyBudgetCents: settings.MonthlyBudgetCents, TriageAutoLabel: settings.TriageAutoLabel,
		TriageMinConfidence: settings.TriageMinConfidence, ObligationsAutoTask: settings.ObligationsAutoTask,
	}, system, user, maxTokens, temperature)
}

func (h enterpriseHost) RegisterAIBudgetHooks(check enterprise.AIBudgetCheck, record enterprise.AIRecordUsage) {
	h.server.aiBudgetMu.Lock()
	h.server.aiBudgetCheckHook = check
	h.server.aiUsageHook = record
	h.server.aiBudgetMu.Unlock()
}

func (h enterpriseHost) EvaluateDLP(ctx context.Context, tenantID, subject, body string, attachments []string) (string, []string, []enterprise.DLPMatch, bool) {
	policy := h.server.tenantPolicyFor(ctx, tenantID)
	classification, warnings, matches, blocked := evaluateOutboundDLP(policy, subject, body, attachments, "")
	out := make([]enterprise.DLPMatch, 0, len(matches))
	for _, match := range matches {
		out = append(out, enterprise.DLPMatch{
			RuleID: match.RuleID, RuleName: match.RuleName, Detector: match.Detector,
			Field: match.Field, Action: match.Action, Classification: match.Classification,
		})
	}
	return classification, warnings, out, blocked
}

func (h enterpriseHost) RedactForAI(ctx context.Context, tenantID, text string) string {
	if hook := dlpEngineHooks.Redact; hook != nil {
		return hook(ctx, tenantID, text)
	}
	return text
}

func (h enterpriseHost) OfficeAIAllowed(ctx context.Context, principal enterprise.Principal) bool {
	user := userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}
	policy, allowed := h.server.officeUserAllowed(ctx, user)
	return allowed && policy.AI
}

func (h enterpriseHost) OfficeDocumentText(ctx context.Context, principal enterprise.Principal, documentID string) (string, string, error) {
	return h.server.officeDocumentText(ctx, userRecord{
		ID: principal.UserID, TenantID: principal.TenantID, Email: principal.Email,
		Name: principal.Name, Role: principal.Role,
	}, documentID)
}

func (h enterpriseHost) RegisterOfficeAIEnabled(check func(ctx context.Context, principal enterprise.Principal) bool) {
	h.server.officeAIEnabledHook = check
}

func (h enterpriseHost) MCPPolicy(ctx context.Context, tenantID string) enterprise.MCPPolicy {
	policy := h.server.tenantPolicyFor(ctx, tenantID).MCP
	approvals := make(map[string]string, len(policy.ToolApprovals))
	for tool, decision := range policy.ToolApprovals {
		approvals[tool] = decision
	}
	return enterprise.MCPPolicy{
		AllowedScopes:             append([]string(nil), policy.AllowedScopes...),
		WriteApprovalRequired:     policy.WriteApprovalRequired,
		MaxRequestsPerHour:        policy.MaxRequestsPerHour,
		ConnectionRequestsPerHour: policy.ConnectionRequestsPerHour,
		ToolApprovals:             approvals,
	}
}

func (h enterpriseHost) MCPRateLimit(ip string) bool {
	if h.server.mcpLimit == nil {
		return true
	}
	allowed := h.server.mcpLimit.Allow(ip)
	h.server.mcpLimit.Fail(ip)
	return allowed
}

type identityProviderSettings struct {
	OIDCEnabled      bool
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCDisplayName  string
	OIDCEmailDomains []string
	SAMLEnabled      bool
	SAMLDisplayName  string
	SAMLMetadataURL  string
	SAMLSPEntityID   string
	SAMLSPCertPEM    string
	SAMLSPKeyPEM     string
	SAMLEmailAttr    string
	SAMLEmailDomains []string
}

// identityProviderSettings reads the optional admin-managed identity provider
// row. The table is created by migration 0108; an upgraded binary that starts
// before the migration runs (or a community build without the table) falls back
// to the environment configuration instead of failing sign-in.
func (h enterpriseHost) identityProviderSettings() (identityProviderSettings, bool) {
	if h.server.store == nil || h.server.store.DB == nil {
		return identityProviderSettings{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var row identityProviderSettings
	err := h.server.store.DB.QueryRow(ctx, `
		SELECT oidc_enabled, oidc_issuer, oidc_client_id, oidc_client_secret,
		       oidc_redirect_url, oidc_display_name, oidc_email_domains,
		       saml_enabled, saml_display_name, saml_idp_metadata_url,
		       saml_sp_entity_id, saml_sp_cert_pem, saml_sp_key_pem,
		       saml_email_attribute, saml_email_domains
		FROM public.identity_provider_settings WHERE id='default'
	`).Scan(
		&row.OIDCEnabled, &row.OIDCIssuer, &row.OIDCClientID, &row.OIDCClientSecret,
		&row.OIDCRedirectURL, &row.OIDCDisplayName, &row.OIDCEmailDomains,
		&row.SAMLEnabled, &row.SAMLDisplayName, &row.SAMLMetadataURL,
		&row.SAMLSPEntityID, &row.SAMLSPCertPEM, &row.SAMLSPKeyPEM,
		&row.SAMLEmailAttr, &row.SAMLEmailDomains,
	)
	if err != nil {
		return identityProviderSettings{}, false
	}
	return row, true
}

func (h enterpriseHost) openIdentitySecret(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	plain, err := h.server.openSecuritySecret(value)
	if err != nil {
		if h.server.logger != nil {
			h.server.logger.Warn("identity provider secret could not be opened; the provider stays disabled")
		}
		return ""
	}
	return plain
}

func (h enterpriseHost) SSOConfig() enterprise.SSOConfig {
	cfg := h.server.cfg
	if row, ok := h.identityProviderSettings(); ok {
		secret := h.openIdentitySecret(row.OIDCClientSecret)
		keyPEM := h.openIdentitySecret(row.SAMLSPKeyPEM)
		enabled := h.server.entitled("sso") &&
			row.OIDCEnabled &&
			strings.TrimSpace(row.OIDCIssuer) != "" &&
			strings.TrimSpace(row.OIDCClientID) != "" &&
			strings.TrimSpace(secret) != "" &&
			strings.TrimSpace(row.OIDCRedirectURL) != ""
		samlEnabled := h.server.entitled("saml") &&
			row.SAMLEnabled &&
			strings.TrimSpace(row.SAMLMetadataURL) != "" &&
			strings.TrimSpace(row.SAMLSPCertPEM) != "" &&
			strings.TrimSpace(keyPEM) != ""
		return enterprise.SSOConfig{
			Enabled: enabled, Issuer: row.OIDCIssuer, ClientID: row.OIDCClientID,
			ClientSecret: secret, RedirectURL: row.OIDCRedirectURL,
			DisplayName: row.OIDCDisplayName, EmailDomains: append([]string(nil), row.OIDCEmailDomains...),
			SessionTTL: cfg.SessionTTL, RememberTTL: cfg.RememberTTL, CookieSecure: cfg.CookieSecure,
			SAMLEnabled:        samlEnabled,
			SAMLDisplayName:    row.SAMLDisplayName,
			SAMLPublicURL:      cfg.PublicURL,
			SAMLSPEntityID:     row.SAMLSPEntityID,
			SAMLSPCertPEM:      row.SAMLSPCertPEM,
			SAMLSPKeyPEM:       keyPEM,
			SAMLIDPMetadataURL: row.SAMLMetadataURL,
			SAMLEmailAttribute: row.SAMLEmailAttr,
			SAMLEmailDomains:   append([]string(nil), row.SAMLEmailDomains...),
			SAMLMFAContexts:    append([]string(nil), cfg.SAMLMFAContexts...),
		}
	}
	enabled := h.server.entitled("sso") &&
		strings.TrimSpace(cfg.OIDCIssuer) != "" &&
		strings.TrimSpace(cfg.OIDCClientID) != "" &&
		strings.TrimSpace(cfg.OIDCClientSecret) != "" &&
		strings.TrimSpace(cfg.OIDCRedirectURL) != ""
	return enterprise.SSOConfig{
		Enabled: enabled, Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.OIDCRedirectURL,
		DisplayName: cfg.OIDCDisplayName, EmailDomains: append([]string(nil), cfg.OIDCEmailDomains...),
		SessionTTL: cfg.SessionTTL, RememberTTL: cfg.RememberTTL, CookieSecure: cfg.CookieSecure,
		SAMLEnabled:         h.server.entitled("saml") && strings.TrimSpace(cfg.SAMLIDPMetadataURL) != "",
		SAMLDisplayName:     cfg.SAMLDisplayName,
		SAMLPublicURL:       cfg.PublicURL,
		SAMLSPEntityID:      cfg.SAMLSPEntityID,
		SAMLSPCertFile:      cfg.SAMLSPCertFile,
		SAMLSPKeyFile:       cfg.SAMLSPKeyFile,
		SAMLIDPMetadataURL:  cfg.SAMLIDPMetadataURL,
		SAMLIDPMetadataFile: cfg.SAMLIDPMetadataFile,
		SAMLEmailAttribute:  cfg.SAMLEmailAttribute,
		SAMLEmailDomains:    append([]string(nil), cfg.SAMLEmailDomains...),
		SAMLMFAContexts:     append([]string(nil), cfg.SAMLMFAContexts...),
	}
}

func (h enterpriseHost) SSOSecurityPolicy(ctx context.Context, tenantID string) enterprise.SSOSecurityPolicy {
	policy := h.server.tenantPolicyFor(ctx, tenantID).Security
	return enterprise.SSOSecurityPolicy{
		AllowedIPRanges: append([]string(nil), policy.AllowedIPRanges...),
		MFAEnforcement:  policy.MFAEnforcement,
	}
}

func (h enterpriseHost) IssueSession(w http.ResponseWriter, r *http.Request, principal enterprise.Principal, ttl time.Duration) error {
	user, err := h.server.userByID(r.Context(), principal.UserID)
	if err != nil {
		return err
	}
	session, rawToken, err := h.server.createSession(r, user, ttl)
	if err != nil || session == nil || rawToken == "" {
		return fmt.Errorf("create session")
	}
	h.server.setSessionCookies(w, rawToken, session.CSRF, ttl)
	return nil
}

func (h enterpriseHost) ResetSSORateLimit(ip string) {
	if h.server.oidcLimit != nil {
		h.server.oidcLimit.Reset(ip)
	}
}

func (h enterpriseHost) SSORateLimitAllow(ip string) bool {
	if h.server.oidcLimit == nil {
		return true
	}
	return h.server.oidcLimit.Allow(ip)
}

func (h enterpriseHost) SSORateLimitFail(ip string) {
	if h.server.oidcLimit != nil {
		h.server.oidcLimit.Fail(ip)
	}
}

func (h enterpriseHost) SAMLRateLimitAllow(ip string) bool {
	if h.server.samlLimit == nil {
		return true
	}
	return h.server.samlLimit.Allow(ip)
}

func (h enterpriseHost) SAMLRateLimitFail(ip string) {
	if h.server.samlLimit != nil {
		h.server.samlLimit.Fail(ip)
	}
}

func (h enterpriseHost) ResetSAMLRateLimit(ip string) {
	if h.server.samlLimit != nil {
		h.server.samlLimit.Reset(ip)
	}
}

func (h enterpriseHost) ClearSessionCookies(w http.ResponseWriter) {
	h.server.clearSessionCookies(w)
}

func (h enterpriseHost) ssoStateKey() []byte {
	sum := sha256.Sum256([]byte("alt8mail-oidc-state\x00" + h.server.cfg.SessionSecret + "\x00" + h.server.cfg.CSRFSecret))
	return sum[:]
}

func (h enterpriseHost) SignSSOPayload(payload string) (string, error) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, h.ssoStateKey())
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (h enterpriseHost) VerifySSOPayload(value string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("malformed SSO state")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("malformed SSO state")
	}
	mac := hmac.New(sha256.New, h.ssoStateKey())
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(mac.Sum(nil), provided) {
		return "", fmt.Errorf("invalid SSO state signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("malformed SSO state")
	}
	return string(raw), nil
}

func (h enterpriseHost) RegisterBrandingHooks(
	public func(enterprise.BrandingFields) map[string]any,
	validate func(context.Context, string, []string) []string,
	sync func(context.Context, pgx.Tx, string, []string) error,
) {
	h.server.brandingPublicHook = public
	h.server.brandingValidateHook = validate
	h.server.brandingSyncHook = sync
}

func (h enterpriseHost) PublicBrandingPayload(fields enterprise.BrandingFields) map[string]any {
	if h.server.brandingPublicHook != nil {
		return h.server.brandingPublicHook(fields)
	}
	return freeBrandingPayload(fields)
}

func (h enterpriseHost) ValidateBrandingHosts(ctx context.Context, tenantID string, hosts []string) []string {
	if h.server.brandingValidateHook != nil {
		return h.server.brandingValidateHook(ctx, tenantID, hosts)
	}
	return []string{"White-label hostname validation is unavailable without the enterprise module"}
}

func (h enterpriseHost) SyncBrandingHosts(ctx context.Context, tx pgx.Tx, tenantID string, hosts []string) error {
	if h.server.brandingSyncHook != nil {
		return h.server.brandingSyncHook(ctx, tx, tenantID, hosts)
	}
	return fmt.Errorf("white-label hostname storage is unavailable without the enterprise module")
}

func (h enterpriseHost) RegisterTenantKeyHooks(hooks enterprise.TenantKeyHooks) {
	h.server.tenantKeyHooks = hooks
}

func (h enterpriseHost) RegisterDLPEngine(hooks enterprise.DLPEngineHooks) {
	dlpEngineHooks = hooks
}

func (h enterpriseHost) ProvisionImportedUser(ctx context.Context, req enterprise.MailImportProvisionRequest) (string, string, string, error) {
	job := userImportJobRecord{TenantID: req.TenantID, CreatedByID: req.CreatedByID, CreatedBy: req.CreatedBy}
	return h.server.provisionImportedUser(ctx, job, "", req.Email, req.Name, req.Role, req.Quota)
}

func (h enterpriseHost) ReissueImportedUserCredentials(ctx context.Context, userID, email string) (string, string, error) {
	return h.server.reissueImportedUserCredentials(ctx, userID, email)
}

func (h enterpriseHost) LocalIMAPConfig(email string) (string, *tls.Config, string, string, error) {
	loginUser, password, err := h.server.mailLoginForUser(email)
	if err != nil {
		return "", nil, "", "", err
	}
	tlsConfig, err := h.server.mailTLSConfig(h.server.cfg.IMAPTLSName)
	if err != nil {
		return "", nil, "", "", err
	}
	return h.server.cfg.IMAPAddr, tlsConfig, loginUser, password, nil
}

func (h enterpriseHost) SendMigrationNotice(ctx context.Context, tenantID, preferredUserID, to, name, mailbox, password string) error {
	return h.server.sendMigrationNotice(ctx, tenantID, preferredUserID, to, name, mailbox, password)
}

func (h enterpriseHost) ImportScan(ctx context.Context, data []byte) (bool, error) {
	cfg := h.server.cfg
	if !cfg.ImportScan || strings.TrimSpace(cfg.ClamAVAddr) == "" {
		return false, nil
	}
	limit := int64(cfg.ImportScanMaxMB)
	if limit <= 0 {
		limit = 24
	}
	if int64(len(data)) > limit<<20 {
		return false, nil
	}
	if err := h.server.scanUpload(ctx, data); err != nil {
		if errors.Is(err, errUploadInfected) {
			return true, nil
		}
		return false, fmt.Errorf("scan imported message: %w", err)
	}
	return false, nil
}

func (h enterpriseHost) AdminCan(r *http.Request, permission string) bool {
	sess := h.server.requireAdminSession(r)
	return sess != nil && permission != "" && sess.can(permission)
}

func (h enterpriseHost) RegisterFollowUpViews(views enterprise.FollowUpViews) {
	if views != nil {
		h.server.followUpViewsHook = views
	}
}

func (s *Server) followUpViews(ctx context.Context, userID string) []map[string]any {
	if s.followUpViewsHook != nil {
		return s.followUpViewsHook(ctx, userID)
	}
	return []map[string]any{}
}

func (h enterpriseHost) RecipientWindow(ctx context.Context, tenantID string) (int, int) {
	policy := h.server.tenantPolicyFor(ctx, tenantID)
	return policy.Mail.RecipientWindowStartHour, policy.Mail.RecipientWindowEndHour
}

func (h enterpriseHost) UserTimeLocation(ctx context.Context, principal enterprise.Principal) *time.Location {
	user, err := h.server.userByID(ctx, principal.UserID)
	if err != nil {
		return time.UTC
	}
	return userTimeLocation(user)
}

func (h enterpriseHost) DB() *pgxpool.Pool {
	if h.server.store == nil {
		return nil
	}
	return h.server.store.DB
}

func (h enterpriseHost) Logger() *slog.Logger {
	return h.server.logger
}

func (h enterpriseHost) Config() config.Config {
	return h.server.cfg
}

func (h enterpriseHost) Entitled(feature string) bool {
	return h.server.entitled(feature)
}

// registerEnterpriseExtension gives the private module a chance to add paid
// routes to the shared core mux. The community build has no registered
// extension and stays unchanged.
func (s *Server) registerEnterpriseExtension(mux *http.ServeMux, admin bool) {
	extension := enterprise.Registered()
	if extension == nil {
		extension = enterprise.Stub{}
	}
	if err := extension.Register(enterpriseHost{server: s, mux: mux, admin: admin}); err != nil && s.logger != nil {
		s.logger.Error("enterprise extension registration failed", "admin", admin, "err", err)
	}
}

// enterpriseAvailable reports whether the private enterprise module is linked
// into this build. Community builds have no paid routes or paid console pages.
func (s *Server) enterpriseAvailable() bool {
	return enterprise.Registered() != nil
}

func (s *Server) paidUIDescriptors() []enterprise.PaidUI {
	s.paidUIMu.Lock()
	defer s.paidUIMu.Unlock()
	return append([]enterprise.PaidUI(nil), s.paidUI...)
}

func (s *Server) startEnterpriseWorkers(ctx context.Context) {
	s.enterpriseWorkersMu.Lock()
	workers := append([]func(context.Context){}, s.enterpriseWorkers...)
	s.enterpriseWorkersMu.Unlock()
	for _, worker := range workers {
		go worker(ctx)
	}
}
