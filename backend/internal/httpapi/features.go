package httpapi

import (
	"net/http"

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

func featureGate(s *Server, feature string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.entitled(feature) {
			writeError(w, http.StatusForbidden, "LICENSE_FEATURE",
				"This capability ("+feature+") is not included in the active Alt8Mail license. Contact your administrator or upgrade the license.")
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
