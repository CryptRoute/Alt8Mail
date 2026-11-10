package licensing

// Canonical paid feature keys for Phases 3-6. Licenses may list these
// individually or use "all"/"*". Reissued licenses should use this vocabulary.
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
)

var PaidFeatureKeys = []string{
	FeatureRecipientScheduling, FeatureFollowUps, FeatureTemplateGallery,
	FeatureDeliverability, FeatureCampaignTracking,
	FeatureAITriage, FeatureAIObligations, FeatureAISearch, FeatureAIMeetingPrep,
	FeatureOfficeAI, FeatureAIPolicyAssist, FeatureMCPGovernance, FeatureAIBudget,
	FeatureDLPAdvanced, FeatureEDiscovery, FeatureBYOK, FeatureFileGovernance,
	FeatureAuditExport, FeatureCRMSync, FeatureMigration, FeatureAnalytics,
	FeatureSecurityAwareness, FeatureSupportOps,
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
