package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type permissionDescriptor struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

var adminPermissionCatalog = []permissionDescriptor{
	{"tenant.read", "View organization", "Organization", "Read organization profile, plan and region.", "low"},
	{"tenant.manage", "Manage organization", "Organization", "Change organization profile, branding and lifecycle settings.", "high"},
	{"users.read", "View users", "Users", "List users, profiles, aliases and last activity.", "low"},
	{"users.create", "Create users", "Users", "Create accounts and temporary passwords.", "high"},
	{"users.update", "Update users", "Users", "Edit profile, role, status, quota and org unit.", "high"},
	{"users.delete", "Delete or restore users", "Users", "Close and restore accounts.", "critical"},
	{"users.reset_password", "Reset passwords", "Users", "Issue a temporary password and revoke active sessions.", "critical"},
	{"users.security", "Manage user security", "Users", "Reset MFA factors, lockouts and authentication state.", "critical"},
	{"users.aliases", "Manage aliases", "Users", "Add and remove sendable email aliases.", "medium"},
	{"groups.read", "View groups", "Directory", "List groups and membership.", "low"},
	{"groups.manage", "Manage groups", "Directory", "Create groups, change membership and archives.", "medium"},
	{"orgunits.read", "View org units", "Directory", "View the organization unit tree.", "low"},
	{"orgunits.manage", "Manage org units", "Directory", "Create, move and delete organizational units.", "medium"},
	{"roles.read", "View admin roles", "Access control", "Read roles and their permission matrices.", "low"},
	{"roles.manage", "Manage admin roles", "Access control", "Create roles, assign permissions and grant access.", "critical"},
	{"policies.read", "View policies", "Policy", "Read security, mail, sharing and directory policy.", "low"},
	{"policies.manage", "Manage policies", "Policy", "Change organization-wide policy and restrictions.", "critical"},
	{"domains.read", "View domains", "Domains", "Read domain ownership and DNS verification state.", "low"},
	{"domains.manage", "Manage domains", "Domains", "Add domains, select primary domains and verify DNS.", "high"},
	{"audit.read", "View audit log", "Audit", "Read and export administrative activity.", "medium"},
	{"legal.read", "View compliance archive", "Compliance", "Read legal holds and run eDiscovery searches.", "medium"},
	{"legal.manage", "Manage legal holds", "Compliance", "Place and release holds that preserve mailbox data.", "critical"},
	{"siem.read", "View SIEM integration", "Integrations", "Read audit webhook configuration and delivery state.", "low"},
	{"siem.manage", "Manage SIEM integration", "Integrations", "Configure signed audit webhooks and rotate signing secrets.", "critical"},
	{"sessions.manage", "Manage sessions", "Security", "Revoke user or admin sign-in sessions.", "high"},
	{"system.tenants.manage", "Manage all organizations", "System", "Create, suspend and configure any tenant.", "critical"},
	{"system.admins.manage", "Manage system administrators", "System", "Grant or revoke platform-wide administrator access.", "critical"},
	{"system.health", "View system health", "System", "Read platform health and capacity data.", "low"},
}

var tenantAdminPermissionKeys = []string{
	"tenant.read", "tenant.manage",
	"users.read", "users.create", "users.update", "users.delete", "users.reset_password", "users.security", "users.aliases",
	"groups.read", "groups.manage", "orgunits.read", "orgunits.manage",
	"roles.read", "roles.manage", "policies.read", "policies.manage",
	"domains.read", "domains.manage", "audit.read", "sessions.manage",
	"legal.read", "legal.manage", "siem.read", "siem.manage",
}

func adminPermissionKeys() map[string]bool {
	keys := map[string]bool{}
	for _, item := range adminPermissionCatalog {
		keys[item.Key] = true
	}
	return keys
}

func sortedPermissionNames(permissions map[string]bool) []string {
	result := []string{}
	if permissions["*"] {
		for _, item := range adminPermissionCatalog {
			result = append(result, item.Key)
		}
		return result
	}
	for key, allowed := range permissions {
		if allowed {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func (s *Server) adminPermissions(ctx context.Context, userID, tenantID string, isSystem bool, role string) (map[string]bool, error) {
	permissions := map[string]bool{}
	if isSystem {
		permissions["*"] = true
		return permissions, nil
	}
	if role == "owner" || role == "admin" {
		for _, key := range tenantAdminPermissionKeys {
			permissions[key] = true
		}
		return permissions, nil
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT r.permissions
		FROM admin_role_assignments a
		JOIN admin_roles r ON r.id = a.role_id
		WHERE a.user_id = $1 AND a.tenant_id = $2
		  AND (a.expires_at IS NULL OR a.expires_at > now())
	`, userID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalog := adminPermissionKeys()
	for rows.Next() {
		var granted []string
		if err := rows.Scan(&granted); err != nil {
			continue
		}
		for _, key := range granted {
			if catalog[key] {
				permissions[key] = true
			}
		}
	}
	return permissions, rows.Err()
}

type adminSecurityPolicy struct {
	PasswordMinLength      int      `json:"passwordMinLength"`
	PasswordRequiresUpper  bool     `json:"passwordRequiresUpper"`
	PasswordRequiresLower  bool     `json:"passwordRequiresLower"`
	PasswordRequiresNumber bool     `json:"passwordRequiresNumber"`
	PasswordRequiresSymbol bool     `json:"passwordRequiresSymbol"`
	PasswordHistory        int      `json:"passwordHistory"`
	PasswordExpiryDays     int      `json:"passwordExpiryDays"`
	MFAEnforcement         string   `json:"mfaEnforcement"`
	SessionTimeoutMinutes  int      `json:"sessionTimeoutMinutes"`
	IdleTimeoutMinutes     int      `json:"idleTimeoutMinutes"`
	MaxSessions            int      `json:"maxSessions"`
	AllowedIPRanges        []string `json:"allowedIPRanges"`
	LoginAlerts            bool     `json:"loginAlerts"`
	DataResidencyRegion    string   `json:"dataResidencyRegion"`
}

type adminMailPolicy struct {
	ExternalForwarding        bool           `json:"externalForwarding"`
	AutoForwarding            bool           `json:"autoForwarding"`
	IMAPAccess                bool           `json:"imapAccess"`
	POPAccess                 bool           `json:"popAccess"`
	SMTPAccess                bool           `json:"smtpAccess"`
	ExternalAccounts          bool           `json:"externalAccounts"`
	MaxAttachmentMB           int            `json:"maxAttachmentMB"`
	BlockedExtensions         []string       `json:"blockedExtensions"`
	AllowedExternalRecipients []string       `json:"allowedExternalRecipients"`
	SpamSensitivity           string         `json:"spamSensitivity"`
	RetentionDays             int            `json:"retentionDays"`
	Quarantine                bool           `json:"quarantine"`
	ExternalTag               bool           `json:"externalTag"`
	UndoSendSeconds           int            `json:"undoSendSeconds"`
	RecipientWindowStartHour  int            `json:"recipientWindowStartHour"`
	RecipientWindowEndHour    int            `json:"recipientWindowEndHour"`
	PaymentLinksEnabled       bool           `json:"paymentLinksEnabled"`
	PaymentLinkDomains        []string       `json:"paymentLinkDomains"`
	CampaignTrackingEnabled   bool           `json:"campaignTrackingEnabled"`
	PreventForward            bool           `json:"preventForward"`
	WatermarkExternal         bool           `json:"watermarkExternal"`
	RetentionByClassification map[string]int `json:"retentionByClassification"`
}

type adminFilesPolicy struct {
	RetentionDays         int  `json:"retentionDays"`
	VersionRetentionDays  int  `json:"versionRetentionDays"`
	ShareApprovalRequired bool `json:"shareApprovalRequired"`
	DLPOnShare            bool `json:"dlpOnShare"`
	WatermarkDownloads    bool `json:"watermarkDownloads"`
	LegalHoldDefault      bool `json:"legalHoldDefault"`
}

type adminSharingPolicy struct {
	ExternalSharing bool     `json:"externalSharing"`
	LinkDefault     string   `json:"linkDefault"`
	AllowedDomains  []string `json:"allowedDomains"`
	GuestInvites    bool     `json:"guestInvites"`
}

type adminDirectoryPolicy struct {
	ContactSharing    bool `json:"contactSharing"`
	ExternalDirectory bool `json:"externalDirectory"`
	ProfileEditing    bool `json:"profileEditing"`
	AutoAddExternal   bool `json:"autoAddExternal"`
}

type adminBrandingPolicy struct {
	ProductName     string   `json:"productName"`
	PrimaryColor    string   `json:"primaryColor"`
	SupportEmail    string   `json:"supportEmail"`
	LoginMessage    string   `json:"loginMessage"`
	LogoURL         string   `json:"logoUrl"`
	SupportURL      string   `json:"supportUrl"`
	LegalURL        string   `json:"legalUrl"`
	HideAttribution bool     `json:"hideAttribution"`
	WebmailHosts    []string `json:"webmailHosts"`
}

type adminDLPRule struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Detector       string   `json:"detector"`
	Terms          []string `json:"terms"`
	Pattern        string   `json:"pattern"`
	Fields         []string `json:"fields"`
	Classification string   `json:"classification"`
	Action         string   `json:"action"`
	Direction      string   `json:"direction"`
	Enabled        bool     `json:"enabled"`
}

type adminDLPPolicy struct {
	Enabled               bool           `json:"enabled"`
	DefaultClassification string         `json:"defaultClassification"`
	NotifySender          bool           `json:"notifySender"`
	Rules                 []adminDLPRule `json:"rules"`
}

type adminFeaturePolicy struct {
	StickyNotes bool `json:"stickyNotes"`
	Reactions   bool `json:"reactions"`
}

type adminOfficePolicy struct {
	Enabled          bool     `json:"enabled"`
	InternalSharing  bool     `json:"internalSharing"`
	ExternalEditing  bool     `json:"externalEditing"`
	AI               bool     `json:"ai"`
	DLPOnSave        bool     `json:"dlpOnSave"`
	VersionRetention int      `json:"versionRetention"`
	AllowedGroups    []string `json:"allowedGroups"`
	AllowedDomains   []string `json:"allowedDomains"`
}

type adminMCPPolicy struct {
	AllowedScopes         []string `json:"allowedScopes"`
	WriteApprovalRequired bool     `json:"writeApprovalRequired"`
	MaxRequestsPerHour    int      `json:"maxRequestsPerHour"`
}

type adminTenantPolicy struct {
	Security  adminSecurityPolicy  `json:"security"`
	Mail      adminMailPolicy      `json:"mail"`
	Sharing   adminSharingPolicy   `json:"sharing"`
	Directory adminDirectoryPolicy `json:"directory"`
	Branding  adminBrandingPolicy  `json:"branding"`
	DLP       adminDLPPolicy       `json:"dlp"`
	Features  adminFeaturePolicy   `json:"features"`
	Office    adminOfficePolicy    `json:"office"`
	Files     adminFilesPolicy     `json:"files"`
	MCP       adminMCPPolicy       `json:"mcp"`
}

func defaultTenantPolicy() adminTenantPolicy {
	return adminTenantPolicy{
		Security: adminSecurityPolicy{
			PasswordMinLength: 12, PasswordRequiresUpper: true, PasswordRequiresLower: true,
			PasswordRequiresNumber: true, PasswordRequiresSymbol: false,
			PasswordHistory: 3, PasswordExpiryDays: 0, MFAEnforcement: "off",
			SessionTimeoutMinutes: 720, IdleTimeoutMinutes: 120, MaxSessions: 8,
			AllowedIPRanges: []string{}, LoginAlerts: true,
		},
		Mail: adminMailPolicy{
			ExternalForwarding: false, AutoForwarding: true, IMAPAccess: true, POPAccess: false,
			SMTPAccess: true, ExternalAccounts: true, MaxAttachmentMB: 25,
			BlockedExtensions:         []string{"exe", "js", "vbs", "bat", "cmd", "scr", "ps1", "jar", "msi"},
			AllowedExternalRecipients: []string{}, SpamSensitivity: "medium",
			// Users release their own Junk by default. Organizations that need
			// review can turn on "Restrict Junk release to administrators".
			RetentionDays: 0, Quarantine: false, ExternalTag: true,
			UndoSendSeconds: 8, RecipientWindowStartHour: 9, RecipientWindowEndHour: 17,
			PaymentLinksEnabled: false, PaymentLinkDomains: []string{"buy.stripe.com", "checkout.stripe.com", "paypal.me"},
			CampaignTrackingEnabled: false, PreventForward: false, WatermarkExternal: false,
			RetentionByClassification: map[string]int{},
		},
		Sharing:   adminSharingPolicy{ExternalSharing: true, LinkDefault: "view", AllowedDomains: []string{}, GuestInvites: false},
		Directory: adminDirectoryPolicy{ContactSharing: true, ExternalDirectory: false, ProfileEditing: true, AutoAddExternal: true},
		Branding:  adminBrandingPolicy{ProductName: "Alt8Mail", PrimaryColor: "#0f6cbd", SupportEmail: "", LoginMessage: "", WebmailHosts: []string{}},
		DLP: adminDLPPolicy{
			Enabled: true, DefaultClassification: "Internal", NotifySender: false,
			Rules: []adminDLPRule{},
		},
		Features: adminFeaturePolicy{StickyNotes: true, Reactions: true},
		Office: adminOfficePolicy{
			Enabled: true, InternalSharing: true, ExternalEditing: true, AI: true, DLPOnSave: true,
			VersionRetention: 0, AllowedGroups: []string{}, AllowedDomains: []string{},
		},
		Files: adminFilesPolicy{
			RetentionDays: 0, VersionRetentionDays: 0, ShareApprovalRequired: false,
			DLPOnShare: true, WatermarkDownloads: false, LegalHoldDefault: false,
		},
		MCP: adminMCPPolicy{
			AllowedScopes: append([]string{}, mcpSupportedScopes...), WriteApprovalRequired: true,
			MaxRequestsPerHour: 120,
		},
	}
}

func tenantPolicyFromSettings(raw json.RawMessage) adminTenantPolicy {
	policy := defaultTenantPolicy()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &policy)
	}
	if policy.Security.AllowedIPRanges == nil {
		policy.Security.AllowedIPRanges = []string{}
	}
	if policy.Mail.BlockedExtensions == nil {
		policy.Mail.BlockedExtensions = []string{}
	}
	if policy.Mail.AllowedExternalRecipients == nil {
		policy.Mail.AllowedExternalRecipients = []string{}
	}
	if policy.Mail.RetentionByClassification == nil {
		policy.Mail.RetentionByClassification = map[string]int{}
	}
	if policy.Sharing.AllowedDomains == nil {
		policy.Sharing.AllowedDomains = []string{}
	}
	if policy.DLP.DefaultClassification == "" {
		policy.DLP.DefaultClassification = "Internal"
	}
	if policy.DLP.Rules == nil {
		policy.DLP.Rules = []adminDLPRule{}
	}
	if policy.Office.AllowedGroups == nil {
		policy.Office.AllowedGroups = []string{}
	}
	if policy.Office.AllowedDomains == nil {
		policy.Office.AllowedDomains = []string{}
	}
	if policy.MCP.AllowedScopes == nil {
		policy.MCP.AllowedScopes = append([]string{}, mcpSupportedScopes...)
	}
	if policy.MCP.MaxRequestsPerHour <= 0 {
		policy.MCP.MaxRequestsPerHour = 120
	}
	for index := range policy.DLP.Rules {
		if policy.DLP.Rules[index].Fields == nil {
			policy.DLP.Rules[index].Fields = []string{"subject", "body"}
		}
		if policy.DLP.Rules[index].Direction == "" {
			policy.DLP.Rules[index].Direction = "outbound"
		}
		if policy.DLP.Rules[index].Action == "" {
			policy.DLP.Rules[index].Action = "warn"
		}
		if policy.DLP.Rules[index].Detector == "" {
			policy.DLP.Rules[index].Detector = "terms"
		}
		if policy.DLP.Rules[index].Classification == "" {
			policy.DLP.Rules[index].Classification = policy.DLP.DefaultClassification
		}
	}
	return policy
}

var dlpClassifications = []string{"Public", "Internal", "Confidential", "Restricted"}

func validDLPClassification(value string) bool {
	for _, item := range dlpClassifications {
		if strings.EqualFold(item, strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func dlpClassificationRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "restricted":
		return 4
	case "confidential":
		return 3
	case "internal":
		return 2
	case "public":
		return 1
	}
	return 0
}

func canonicalDLPClassification(value string) string {
	for _, item := range dlpClassifications {
		if strings.EqualFold(item, strings.TrimSpace(value)) {
			return item
		}
	}
	return ""
}

func validateTenantPolicy(policy adminTenantPolicy) (adminTenantPolicy, []string) {
	errors := []string{}
	if policy.Security.PasswordMinLength < 8 || policy.Security.PasswordMinLength > 64 {
		errors = append(errors, "Password minimum length must be between 8 and 64")
	}
	if policy.Security.PasswordHistory < 0 || policy.Security.PasswordHistory > 24 {
		errors = append(errors, "Password history must be between 0 and 24")
	}
	if policy.Security.PasswordExpiryDays < 0 || policy.Security.PasswordExpiryDays > 365 {
		errors = append(errors, "Password expiry must be between 0 and 365 days")
	}
	switch policy.Security.MFAEnforcement {
	case "off", "admins", "all":
	default:
		errors = append(errors, "MFA enforcement must be off, admins or all")
	}
	if policy.Security.SessionTimeoutMinutes < 15 || policy.Security.SessionTimeoutMinutes > 1440 {
		errors = append(errors, "Session timeout must be between 15 and 1440 minutes")
	}
	if policy.Security.IdleTimeoutMinutes < 5 || policy.Security.IdleTimeoutMinutes > 240 {
		errors = append(errors, "Idle timeout must be between 5 and 240 minutes")
	}
	if policy.Security.IdleTimeoutMinutes > policy.Security.SessionTimeoutMinutes {
		errors = append(errors, "Idle timeout cannot exceed the session timeout")
	}
	if policy.Security.MaxSessions < 1 || policy.Security.MaxSessions > 20 {
		errors = append(errors, "Maximum sessions must be between 1 and 20")
	}
	for _, raw := range policy.Security.AllowedIPRanges {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(strings.TrimSpace(raw)); err != nil {
			if net.ParseIP(strings.TrimSpace(raw)) == nil {
				errors = append(errors, fmt.Sprintf("Allowed network %q is not a valid IP or CIDR range", raw))
			}
		}
	}
	if policy.Mail.MaxAttachmentMB < 1 || policy.Mail.MaxAttachmentMB > 100 {
		errors = append(errors, "Maximum attachment size must be between 1 and 100 MB")
	}
	switch policy.Mail.SpamSensitivity {
	case "low", "medium", "high":
	default:
		errors = append(errors, "Spam sensitivity must be low, medium or high")
	}
	if policy.Mail.RetentionDays < 0 || policy.Mail.RetentionDays > 3650 {
		errors = append(errors, "Retention must be between 0 and 3650 days")
	}
	retentionByClass := map[string]int{}
	for rawClass, days := range policy.Mail.RetentionByClassification {
		class := canonicalDLPClassification(rawClass)
		if class == "" {
			errors = append(errors, "Retention classification "+rawClass+" is not valid")
			continue
		}
		if days < 0 || days > 3650 {
			errors = append(errors, "Classification retention must be between 0 and 3650 days")
			continue
		}
		retentionByClass[class] = days
	}
	policy.Mail.RetentionByClassification = retentionByClass
	if policy.Files.RetentionDays < 0 || policy.Files.RetentionDays > 3650 {
		errors = append(errors, "File retention must be between 0 and 3650 days")
	}
	if policy.Files.VersionRetentionDays < 0 || policy.Files.VersionRetentionDays > 3650 {
		errors = append(errors, "File version retention must be between 0 and 3650 days")
	}
	policy.Security.DataResidencyRegion = strings.ToUpper(strings.TrimSpace(policy.Security.DataResidencyRegion))
	if policy.Security.DataResidencyRegion != "" {
		validRegion := len(policy.Security.DataResidencyRegion) <= 40
		for _, char := range policy.Security.DataResidencyRegion {
			if !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' {
				validRegion = false
				break
			}
		}
		if !validRegion {
			errors = append(errors, "Data residency region must be an uppercase region code")
		}
	}
	blocked := []string{}
	seen := map[string]bool{}
	for _, extension := range policy.Mail.BlockedExtensions {
		extension = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))
		if extension == "" || len(extension) > 16 {
			continue
		}
		if !seen[extension] {
			seen[extension] = true
			blocked = append(blocked, extension)
		}
	}
	policy.Mail.BlockedExtensions = blocked
	allowed := []string{}
	for _, raw := range policy.Mail.AllowedExternalRecipients {
		raw = strings.ToLower(strings.TrimSpace(raw))
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "@") && !strings.HasPrefix(raw, "*.") {
			errors = append(errors, fmt.Sprintf("External recipient %q must be an email or *.<domain>", raw))
			continue
		}
		allowed = append(allowed, raw)
	}
	policy.Mail.AllowedExternalRecipients = allowed
	switch policy.Sharing.LinkDefault {
	case "view", "edit", "disabled":
	default:
		errors = append(errors, "Link sharing default must be view, edit or disabled")
	}
	domains := []string{}
	for _, raw := range policy.Sharing.AllowedDomains {
		raw = strings.ToLower(strings.TrimSpace(raw))
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, ".") {
			errors = append(errors, fmt.Sprintf("Sharing domain %q is not valid", raw))
			continue
		}
		domains = append(domains, raw)
	}
	policy.Sharing.AllowedDomains = domains
	if len(policy.DLP.Rules) > 30 {
		errors = append(errors, "DLP supports at most 30 classification rules")
	}
	if canonical := canonicalDLPClassification(policy.DLP.DefaultClassification); canonical != "" {
		policy.DLP.DefaultClassification = canonical
	} else {
		errors = append(errors, "DLP default classification is not valid")
	}
	seenDLPIDs := map[string]bool{}
	for index := range policy.DLP.Rules {
		rule := &policy.DLP.Rules[index]
		rule.Name = truncate(strings.TrimSpace(rule.Name), 120)
		canonicalClass := canonicalDLPClassification(rule.Classification)
		if canonicalClass != "" {
			rule.Classification = canonicalClass
		} else {
			errors = append(errors, fmt.Sprintf("DLP rule %q has an invalid classification", rule.Name))
		}
		rule.Action = strings.ToLower(strings.TrimSpace(rule.Action))
		rule.Direction = strings.ToLower(strings.TrimSpace(rule.Direction))
		if rule.ID == "" {
			rule.ID = newID("dlp")
		}
		if seenDLPIDs[rule.ID] {
			errors = append(errors, "DLP rule identifiers must be unique")
		}
		seenDLPIDs[rule.ID] = true
		if rule.Name == "" {
			errors = append(errors, fmt.Sprintf("DLP rule %d needs a name", index+1))
		}
		switch rule.Action {
		case "label", "warn", "block":
		default:
			errors = append(errors, fmt.Sprintf("DLP rule %q action must be label, warn or block", rule.Name))
		}
		switch rule.Direction {
		case "outbound", "both":
		default:
			errors = append(errors, fmt.Sprintf("DLP rule %q direction must be outbound or both", rule.Name))
		}
		rule.Detector = strings.ToLower(strings.TrimSpace(rule.Detector))
		if rule.Detector == "" {
			rule.Detector = "terms"
		}
		switch rule.Detector {
		case "terms", "dictionary", "regex", "credit_card", "iban", "national_id":
		default:
			errors = append(errors, fmt.Sprintf("DLP rule %q detector must be terms, dictionary, regex, credit_card, iban or national_id", rule.Name))
			rule.Detector = "terms"
		}
		rule.Pattern = truncate(strings.TrimSpace(rule.Pattern), 500)
		if rule.Detector == "regex" {
			if rule.Pattern == "" {
				errors = append(errors, fmt.Sprintf("DLP rule %q needs a regular expression", rule.Name))
			} else if _, err := regexp.Compile(rule.Pattern); err != nil {
				errors = append(errors, fmt.Sprintf("DLP rule %q regular expression is invalid", rule.Name))
			}
		}
		terms := []string{}
		seenTerms := map[string]bool{}
		for _, raw := range rule.Terms {
			term := truncate(strings.TrimSpace(raw), 80)
			key := strings.ToLower(term)
			if term == "" || seenTerms[key] {
				continue
			}
			seenTerms[key] = true
			terms = append(terms, term)
		}
		if (rule.Detector == "terms" || rule.Detector == "dictionary") && len(terms) == 0 {
			errors = append(errors, fmt.Sprintf("DLP rule %q needs at least one term", rule.Name))
		}
		if len(terms) > 20 {
			terms = terms[:20]
		}
		rule.Terms = terms
		fields := []string{}
		seenFields := map[string]bool{}
		for _, raw := range rule.Fields {
			field := strings.ToLower(strings.TrimSpace(raw))
			if field != "subject" && field != "body" && field != "attachment" {
				errors = append(errors, fmt.Sprintf("DLP rule %q has an invalid match field", rule.Name))
				continue
			}
			if !seenFields[field] {
				seenFields[field] = true
				fields = append(fields, field)
			}
		}
		if len(fields) == 0 {
			fields = []string{"subject", "body"}
		}
		rule.Fields = fields
	}
	var brandingErrors []string
	policy.Branding, brandingErrors = normalizeBrandingPolicy(policy.Branding)
	errors = append(errors, brandingErrors...)
	if policy.Branding.PrimaryColor != "" && !strings.HasPrefix(policy.Branding.PrimaryColor, "#") {
		errors = append(errors, "Brand color must be a hex color")
	}
	if len(policy.Branding.ProductName) > 60 {
		errors = append(errors, "Product name is too long")
	}
	if policy.Branding.SupportEmail != "" && !strings.Contains(policy.Branding.SupportEmail, "@") {
		errors = append(errors, "Support email is not valid")
	}
	if policy.Office.VersionRetention < 0 || policy.Office.VersionRetention > 1000 {
		errors = append(errors, "Office version retention must be between 0 and 1000")
		if policy.Office.VersionRetention < 0 {
			policy.Office.VersionRetention = 0
		} else {
			policy.Office.VersionRetention = 1000
		}
	}
	officeGroups := []string{}
	seenOfficeGroups := map[string]bool{}
	for _, raw := range policy.Office.AllowedGroups {
		raw = truncate(strings.TrimSpace(raw), 120)
		key := strings.ToLower(raw)
		if raw == "" || seenOfficeGroups[key] {
			continue
		}
		seenOfficeGroups[key] = true
		officeGroups = append(officeGroups, raw)
	}
	if len(officeGroups) > 100 {
		errors = append(errors, "Office policies support at most 100 allowed groups")
		officeGroups = officeGroups[:100]
	}
	policy.Office.AllowedGroups = officeGroups
	officeDomains := []string{}
	seenOfficeDomains := map[string]bool{}
	for _, raw := range policy.Office.AllowedDomains {
		raw = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "@")))
		if raw == "" || seenOfficeDomains[raw] {
			continue
		}
		if !strings.Contains(raw, ".") {
			errors = append(errors, fmt.Sprintf("Office domain %q is not valid", raw))
			continue
		}
		seenOfficeDomains[raw] = true
		officeDomains = append(officeDomains, raw)
	}
	if len(officeDomains) > 100 {
		errors = append(errors, "Office policies support at most 100 allowed domains")
		officeDomains = officeDomains[:100]
	}
	policy.Office.AllowedDomains = officeDomains
	mcpScopes := []string{}
	seenMCPScopes := map[string]bool{}
	for _, raw := range policy.MCP.AllowedScopes {
		scope := strings.TrimSpace(raw)
		if scope == "" || seenMCPScopes[scope] {
			continue
		}
		if !mcpScopeSupported(scope) {
			errors = append(errors, fmt.Sprintf("MCP scope %q is not supported", scope))
			continue
		}
		seenMCPScopes[scope] = true
		mcpScopes = append(mcpScopes, scope)
	}
	if len(mcpScopes) == 0 {
		mcpScopes = append([]string{}, mcpSupportedScopes...)
	}
	policy.MCP.AllowedScopes = mcpScopes
	if policy.MCP.MaxRequestsPerHour < 1 || policy.MCP.MaxRequestsPerHour > 10000 {
		errors = append(errors, "MCP request rate must be between 1 and 10,000 per hour")
		if policy.MCP.MaxRequestsPerHour < 1 {
			policy.MCP.MaxRequestsPerHour = 120
		} else {
			policy.MCP.MaxRequestsPerHour = 10000
		}
	}
	return policy, errors
}

func (s *Server) adminGetPolicies(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	policy := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy, "defaults": defaultTenantPolicy(), "tenant": adminTenantPayload(sess.ActiveTenant)})
}

func (s *Server) adminUpdatePolicies(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body map[string]any
	if err := decodeJSON(w, r, &body, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	encodedCurrent, _ := json.Marshal(current)
	merged := map[string]any{}
	_ = json.Unmarshal(encodedCurrent, &merged)
	deepMergeAdmin(merged, body)
	encoded, _ := json.Marshal(merged)
	updated := defaultTenantPolicy()
	if err := json.Unmarshal(encoded, &updated); err != nil {
		writeError(w, http.StatusBadRequest, "POLICY", "Policy payload is not valid")
		return
	}
	updated, errors := validateTenantPolicy(updated)
	if len(errors) > 0 {
		writeError(w, http.StatusBadRequest, "POLICY_INVALID", strings.Join(errors, "; "))
		return
	}
	policyJSON, err := json.Marshal(updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "POLICY", "Could not encode policy")
		return
	}
	var settings map[string]any
	_ = json.Unmarshal(sess.ActiveTenant.Settings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}
	for key, value := range updatedPolicyMap(policyJSON) {
		settings[key] = value
	}
	rawSettings, _ := json.Marshal(settings)
	if _, err := s.store.DB.Exec(r.Context(), `UPDATE tenants SET settings = $2, updated_at = now() WHERE id = $1`, sess.ActiveTenantID, rawSettings); err != nil {
		writeError(w, http.StatusInternalServerError, "POLICY", "Could not save policy")
		return
	}
	// Apply a changed expiry policy to existing accounts without overwriting an
	// expiry already set by an administrator or a previous password reset.
	if updated.Security.PasswordExpiryDays > 0 {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE users SET password_expires_at = COALESCE(password_changed_at, now()) + ($2 * interval '1 day'),
			       updated_at = now()
			WHERE tenant_id = $1 AND password_expires_at IS NULL AND deleted_at IS NULL
		`, sess.ActiveTenantID, updated.Security.PasswordExpiryDays)
	} else {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE users SET password_expires_at = NULL, updated_at = now()
			WHERE tenant_id = $1 AND password_expires_at IS NOT NULL AND deleted_at IS NULL
		`, sess.ActiveTenantID)
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.policy_updated", "Organization policy", "Updated security, mail, sharing or directory policy", map[string]any{"tenantId": sess.ActiveTenantID})
	if current.Mail.AutoForwarding != updated.Mail.AutoForwarding || current.Mail.ExternalForwarding != updated.Mail.ExternalForwarding {
		s.reapplyTenantSieve(sess.ActiveTenantID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": updated})
}

func updatedPolicyMap(raw json.RawMessage) map[string]any {
	result := map[string]any{}
	_ = json.Unmarshal(raw, &result)
	delete(result, "unknown")
	return result
}

func deepMergeAdmin(dst, src map[string]any) {
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			existing, _ := dst[key].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			deepMergeAdmin(existing, nested)
			dst[key] = existing
			continue
		}
		dst[key] = value
	}
}

func (s *Server) adminGetTenantSettings(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":   adminTenantPayload(sess.ActiveTenant),
		"branding": tenantPolicyFromSettings(sess.ActiveTenant.Settings).Branding,
		"limits":   map[string]any{"supportEmail": "support@a8m.test"},
	})
}

func (s *Server) adminUpdateTenantSettings(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body map[string]any
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	allowed := map[string]bool{"name": true, "plan": true, "region": true, "billingEmail": true, "branding": true}
	for key := range body {
		if !allowed[key] {
			writeError(w, http.StatusBadRequest, "SETTINGS", "Unknown setting: "+key)
			return
		}
	}
	if sess.User.IsSystemAdmin {
		// System admins can change plan and region; tenant admins can change name,
		// billing email and branding only within their own organization.
	} else {
		delete(body, "plan")
		delete(body, "region")
	}
	// Validate a complete candidate policy for branding changes.
	settings := map[string]any{}
	_ = json.Unmarshal(sess.ActiveTenant.Settings, &settings)
	whiteLabel := s.entitled("white_label")
	if branding, ok := body["branding"].(map[string]any); ok {
		if brandingExtendedRequested(branding) && !whiteLabel {
			writeError(w, http.StatusForbidden, "WHITE_LABEL", "White-label branding requires a valid white_label license entitlement")
			return
		}
		if !whiteLabel {
			// Do not let an unentitled save clear extended branding that a
			// renewed license may re-enable.
			for _, key := range []string{"logoUrl", "supportUrl", "legalUrl", "hideAttribution", "webmailHosts"} {
				delete(branding, key)
			}
		}
		existing, _ := settings["branding"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		deepMergeAdmin(existing, branding)
		settings["branding"] = existing
	}
	candidate := tenantPolicyFromSettings(mustJSON(settings))
	candidate, errors := validateTenantPolicy(candidate)
	if whiteLabel {
		errors = append(errors, s.validateBrandingHostsDB(r.Context(), sess.ActiveTenantID, candidate.Branding.WebmailHosts)...)
	}
	if len(errors) > 0 {
		writeError(w, http.StatusBadRequest, "SETTINGS_INVALID", strings.Join(errors, "; "))
		return
	}
	settings["branding"] = candidate.Branding
	encodedSettings, _ := json.Marshal(settings)
	name := sess.ActiveTenant.Name
	if value, ok := body["name"].(string); ok && strings.TrimSpace(value) != "" {
		name = truncate(strings.TrimSpace(value), 120)
	}
	plan := sess.ActiveTenant.Plan
	if value, ok := body["plan"].(string); ok && sess.User.IsSystemAdmin {
		if value != "starter" && value != "business" && value != "enterprise" && value != "education" {
			writeError(w, http.StatusBadRequest, "PLAN", "Invalid plan")
			return
		}
		plan = value
	}
	region := sess.ActiveTenant.Region
	if value, ok := body["region"].(string); ok && sess.User.IsSystemAdmin {
		switch value {
		case "us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-1":
			region = value
		default:
			writeError(w, http.StatusBadRequest, "REGION", "Invalid region")
			return
		}
	}
	var billing any
	if value, ok := body["billingEmail"].(string); ok {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !strings.Contains(value, "@") {
			writeError(w, http.StatusBadRequest, "BILLING_EMAIL", "Billing email is invalid")
			return
		}
		if value != "" {
			billing = value
		}
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS", "Could not save organization settings")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		UPDATE tenants SET name = $2, plan = $3, region = $4, billing_email = COALESCE($5, billing_email),
		       settings = $6, updated_at = now() WHERE id = $1
	`, sess.ActiveTenantID, name, plan, region, billing, encodedSettings); err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS", "Could not save organization settings")
		return
	}
	if whiteLabel {
		if err := s.syncTenantWebmailHosts(r.Context(), tx, sess.ActiveTenantID, candidate.Branding.WebmailHosts); err != nil {
			writeError(w, http.StatusConflict, "SETTINGS", "A custom webmail hostname is already in use")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS", "Could not save organization settings")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.tenant_updated", sess.ActiveTenant.Name, "Updated organization profile or branding", map[string]any{"tenantId": sess.ActiveTenantID})
	tenant, _ := s.adminTenantByID(r.Context(), sess.ActiveTenantID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tenant": adminTenantPayload(tenant), "policy": candidate})
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (s *Server) tenantPolicyFor(ctx context.Context, tenantID string) adminTenantPolicy {
	var raw json.RawMessage
	if err := s.store.DB.QueryRow(ctx, `SELECT settings FROM tenants WHERE id = $1`, tenantID).Scan(&raw); err != nil {
		return defaultTenantPolicy()
	}
	return tenantPolicyFromSettings(raw)
}
