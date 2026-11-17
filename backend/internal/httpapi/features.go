package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

const (
	FeatureRecipientScheduling = licensing.FeatureRecipientScheduling
	FeatureFollowUps           = licensing.FeatureFollowUps
	FeatureTemplateGallery     = licensing.FeatureTemplateGallery
	FeatureDeliverability      = licensing.FeatureDeliverability
	FeatureCampaignTracking    = licensing.FeatureCampaignTracking
	FeatureAITriage            = licensing.FeatureAITriage
	FeatureAIObligations       = licensing.FeatureAIObligations
	FeatureAISearch            = licensing.FeatureAISearch
	FeatureAIMeetingPrep       = licensing.FeatureAIMeetingPrep
	FeatureOfficeAI            = licensing.FeatureOfficeAI
	FeatureAIPolicyAssist      = licensing.FeatureAIPolicyAssist
	FeatureMCPGovernance       = licensing.FeatureMCPGovernance
	FeatureAIBudget            = licensing.FeatureAIBudget
	FeatureDLPAdvanced         = licensing.FeatureDLPAdvanced
	FeatureEDiscovery          = licensing.FeatureEDiscovery
	FeatureBYOK                = licensing.FeatureBYOK
	FeatureFileGovernance      = licensing.FeatureFileGovernance
	FeatureAuditExport         = licensing.FeatureAuditExport
	FeatureCRMSync             = licensing.FeatureCRMSync
	FeatureMigration           = licensing.FeatureMigration
	FeatureAnalytics           = licensing.FeatureAnalytics
	FeatureSecurityAwareness   = licensing.FeatureSecurityAwareness
	FeatureSupportOps          = licensing.FeatureSupportOps
)

var paidFeatureKeys = licensing.PaidFeatureKeys

func (s *Server) featureDetails(feature string) map[string]any {
	details := map[string]any{"feature": feature}
	if descriptor, ok := licensing.FeatureByKey(feature); ok {
		details["label"] = descriptor.Label
		details["group"] = descriptor.Group
	}
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.LicensePortalURL), "/"); base != "" {
		details["upgradeUrl"] = base + "/upgrade?feature=" + url.QueryEscape(feature)
	}
	return details
}

func (s *Server) writeFeatureRequired(w http.ResponseWriter, feature, message string) {
	if strings.TrimSpace(message) == "" {
		message = "This capability requires a paid Alt8Mail license. Contact your administrator or upgrade the license."
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
		"code": "FEATURE_REQUIRED", "message": message, "details": s.featureDetails(feature),
	}})
}

func featureGate(s *Server, feature string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.entitled(feature) {
			s.writeFeatureRequired(w, feature, "This capability ("+feature+") is not included in the active Alt8Mail license. Contact your administrator or upgrade the license.")
			return
		}
		next(w, r)
	}
}

func (s *Server) userFeature(feature string, next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(featureGate(s, feature, next))
}

func (s *Server) adminFeature(permission, feature string, next http.HandlerFunc) http.HandlerFunc {
	return s.adminRequirePermission(permission, featureGate(s, feature, next))
}

func (s *Server) requireFeature(feature string) http.HandlerFunc {
	return featureGate(s, feature, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func (s *Server) missingPaidFeatures() []string {
	missing := []string{}
	for _, feature := range paidFeatureKeys {
		if !s.entitled(feature) {
			missing = append(missing, feature)
		}
	}
	return missing
}
