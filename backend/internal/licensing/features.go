package licensing

import "strings"

// Canonical paid feature keys. Licenses may list these individually or use
// "all"/"*". Reissued licenses should use this vocabulary.
const (
	FeatureRecipientScheduling = "recipient_scheduling"
	FeatureFollowUps           = "mail_followups"
	FeatureTemplateGallery     = "template_gallery"
	FeatureDeliverability      = "deliverability"
	FeatureCampaignTracking    = "campaign_tracking"
	FeatureAITriage            = "ai_triage"
	FeatureAIObligations       = "ai_obligations"
	FeatureAISearch            = "ai_search"
	FeatureAIMeetingPrep       = "ai_meeting_prep"
	FeatureOfficeAI            = "office_ai"
	FeatureAIPolicyAssist      = "ai_policy_assist"
	FeatureMCPGovernance       = "mcp_governance"
	FeatureAIBudget            = "ai_budget"
	FeatureDLPAdvanced         = "dlp_advanced"
	FeatureEDiscovery          = "ediscovery"
	FeatureBYOK                = "byok"
	FeatureFileGovernance      = "file_governance"
	FeatureAuditExport         = "audit_export"
	FeatureCRMSync             = "crm_sync"
	FeatureMigration           = "migration"
	FeatureAnalytics           = "analytics"
	FeatureSecurityAwareness   = "security_awareness"
	FeatureSupportOps          = "support_ops"

	// Features that existed as literal entitlement strings before the catalog
	// was completed. Keeping them in this list prevents a valid paid license
	// from silently disabling SSO, SCIM, shared inboxes and the other modules
	// below.
	FeatureSSO           = "sso"
	FeatureSAML          = "saml"
	FeatureSCIM          = "scim"
	FeatureWhiteLabel    = "white_label"
	FeatureSharedInbox   = "shared_inbox"
	FeatureSharedDrafts  = "shared_drafts"
	FeatureTeamTemplates = "team_templates"
	FeatureAutomation    = "automation"
	FeatureMailMerge     = "mail_merge"
	FeatureBookingPages  = "booking_pages"

	// Paid enterprise administration. Control backup/restore is a root-level
	// capability and must be gated even though it lives in a different service.
	FeatureBackupRestore = "backup_restore"
)

const (
	GroupIdentity   = "identity"
	GroupTeam       = "team"
	GroupScheduling = "scheduling"
	GroupAI         = "ai"
	GroupCompliance = "compliance"
	GroupEcosystem  = "ecosystem"
	GroupOperations = "operations"
	PlanEnterprise  = "enterprise"
)

// Feature is the public, non-secret description of a paid capability. The
// product and the licensing portal use this shape so labels, groups and
// upgrade links do not drift between services.
type Feature struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Group       string   `json:"group"`
	Plan        string   `json:"plan"`
	Includes    []string `json:"includes,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
}

// FeatureCatalog is the single source of truth for paid feature metadata.
// Ordering is stable for UI rendering and snapshot tests.
var FeatureCatalog = []Feature{
	{Key: FeatureSSO, Label: "Single sign-on (OIDC)", Description: "OIDC identity-provider sign-in and JIT provisioning.", Group: GroupIdentity, Plan: PlanEnterprise},
	{Key: FeatureSAML, Label: "Single sign-on (SAML 2.0)", Description: "SAML identity-provider sign-in and metadata handling.", Group: GroupIdentity, Plan: PlanEnterprise},
	{Key: FeatureSCIM, Label: "SCIM 2.0 provisioning", Description: "Users and groups provisioned from an identity provider.", Group: GroupIdentity, Plan: PlanEnterprise},
	{Key: FeatureWhiteLabel, Label: "White-label branding", Description: "Custom logo, support/legal links, attribution removal and webmail hostnames.", Group: GroupIdentity, Plan: PlanEnterprise},
	{Key: FeatureBackupRestore, Label: "Backup and mailbox restore", Description: "Per-mailbox and per-folder backup restore with preview and audit.", Group: GroupOperations, Plan: PlanEnterprise},

	{Key: FeatureSharedInbox, Label: "Shared inboxes", Description: "Team assignment, round-robin, SLA timers and collision detection.", Group: GroupTeam, Plan: PlanEnterprise},
	{Key: FeatureSharedDrafts, Label: "Shared drafts", Description: "Lease-based collaborative drafts, comments and version restore.", Group: GroupTeam, Plan: PlanEnterprise},
	{Key: FeatureTeamTemplates, Label: "Team templates", Description: "Shared, versioned canned responses and team variables.", Group: GroupTeam, Plan: PlanEnterprise, Includes: []string{FeatureSharedInbox}},
	{Key: FeatureAutomation, Label: "Visual automation", Description: "No-code rules with delivery-time and asynchronous actions.", Group: GroupTeam, Plan: PlanEnterprise},

	{Key: FeatureRecipientScheduling, Label: "Recipient-time scheduling", Description: "Send-time suggestions in the recipient's local working hours.", Group: GroupScheduling, Plan: PlanEnterprise},
	{Key: FeatureFollowUps, Label: "Follow-ups and nudges", Description: "Reply reminders, follow-up tracking and task creation.", Group: GroupScheduling, Plan: PlanEnterprise},
	{Key: FeatureMailMerge, Label: "Mail merge", Description: "Personalized bulk send, suppression, throttling and campaign reporting.", Group: GroupScheduling, Plan: PlanEnterprise},
	{Key: FeatureBookingPages, Label: "Booking pages", Description: "Public availability pages, buffers and round-robin booking.", Group: GroupScheduling, Plan: PlanEnterprise},
	{Key: FeatureTemplateGallery, Label: "Template gallery and payment links", Description: "Packaged templates and payment-link validation.", Group: GroupScheduling, Plan: PlanEnterprise},
	{Key: FeatureDeliverability, Label: "Deliverability and DMARC", Description: "DMARC ingestion, polling and deliverability reporting.", Group: GroupScheduling, Plan: PlanEnterprise},
	{Key: FeatureCampaignTracking, Label: "Campaign tracking", Description: "Opt-in read and link tracking for outbound campaigns.", Group: GroupScheduling, Plan: PlanEnterprise},

	{Key: FeatureAITriage, Label: "AI triage", Description: "Priority classification and feedback for inbound mail.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureAIObligations, Label: "AI obligations", Description: "Extract commitments, deadlines and follow-up tasks.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureAISearch, Label: "AI search", Description: "Natural-language search over mail with structured results.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureAIMeetingPrep, Label: "AI meeting prep", Description: "Sender intelligence and meeting preparation from mail and calendar.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureOfficeAI, Label: "Office AI", Description: "Summaries, drafting and document Q&A inside OnlyOffice.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureAIPolicyAssist, Label: "AI policy assist", Description: "Plain-language policy and DLP classification assistance.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureMCPGovernance, Label: "MCP governance", Description: "Governed MCP scopes, approvals and action controls.", Group: GroupAI, Plan: PlanEnterprise},
	{Key: FeatureAIBudget, Label: "AI budget controls", Description: "Tenant AI usage quotas and budget enforcement.", Group: GroupAI, Plan: PlanEnterprise},

	{Key: FeatureDLPAdvanced, Label: "Advanced DLP", Description: "Detector reviews, simulation and advanced classification actions.", Group: GroupCompliance, Plan: PlanEnterprise},
	{Key: FeatureEDiscovery, Label: "eDiscovery and legal hold", Description: "Cross-document discovery, holds, review and MBOX/JSON production.", Group: GroupCompliance, Plan: PlanEnterprise},
	{Key: FeatureBYOK, Label: "BYOK and encryption keys", Description: "Tenant-managed encryption keys, rotation and locked-key handling.", Group: GroupCompliance, Plan: PlanEnterprise},
	{Key: FeatureFileGovernance, Label: "File governance", Description: "File holds, retention, share requests and indexed file search.", Group: GroupCompliance, Plan: PlanEnterprise},
	{Key: FeatureAuditExport, Label: "Audit export and SIEM", Description: "Signed audit bundles and SIEM delivery integrations.", Group: GroupCompliance, Plan: PlanEnterprise},

	{Key: FeatureCRMSync, Label: "CRM sync", Description: "CRM record sync and sender intelligence connectors.", Group: GroupEcosystem, Plan: PlanEnterprise},
	{Key: FeatureMigration, Label: "Migration and importers", Description: "Mail, calendar, contacts and drive migration jobs.", Group: GroupEcosystem, Plan: PlanEnterprise},
	{Key: FeatureAnalytics, Label: "Usage analytics", Description: "Tenant and platform usage analytics and reports.", Group: GroupEcosystem, Plan: PlanEnterprise},
	{Key: FeatureSecurityAwareness, Label: "Security awareness", Description: "Phishing simulation campaigns and reporting.", Group: GroupEcosystem, Plan: PlanEnterprise},
	{Key: FeatureSupportOps, Label: "Support operations", Description: "Support bundles, telemetry and premium operations tooling.", Group: GroupOperations, Plan: PlanEnterprise},
}

var paidFeatureKeys = func() []string {
	keys := make([]string, 0, len(FeatureCatalog))
	for _, feature := range FeatureCatalog {
		keys = append(keys, feature.Key)
	}
	return keys
}()

// PaidFeatureKeys is the ordered canonical vocabulary used by licenses,
// snapshots and the Control Plane reissue list.
var PaidFeatureKeys = append([]string(nil), paidFeatureKeys...)

// FeatureByKey returns the catalog entry for a key. Lookup is case-insensitive
// so license payloads and older literal checks remain compatible.
func FeatureByKey(key string) (Feature, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, feature := range FeatureCatalog {
		if feature.Key == key {
			return feature, true
		}
	}
	return Feature{}, false
}

// Catalog returns a copy so callers cannot mutate the shared catalog.
func Catalog() []Feature {
	return append([]Feature(nil), FeatureCatalog...)
}

// MissingFeatures returns canonical paid keys not present in the license.
func (l License) MissingFeatures() []string {
	missing := []string{}
	for _, feature := range PaidFeatureKeys {
		if !l.HasFeature(feature) {
			missing = append(missing, feature)
		}
	}
	return missing
}

// HasFeature reports whether a verified license includes a feature name.
// Feature names are case-insensitive. Team templates are included with a
// shared-inbox entitlement because the team template UI is part of that bundle.
func (l License) HasFeature(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, feature := range l.Features {
		normalized := strings.ToLower(strings.TrimSpace(feature))
		if normalized == "all" || normalized == "*" || normalized == name {
			return true
		}
	}
	if name == FeatureTeamTemplates {
		for _, feature := range l.Features {
			if strings.EqualFold(strings.TrimSpace(feature), FeatureSharedInbox) {
				return true
			}
		}
	}
	return false
}
