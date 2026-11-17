package httpapi

import (
	"context"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

// addCampaignTracking delegates to the paid campaign-tracking module. The
// community build has no module and returns the body unchanged.
func (s *Server) addCampaignTracking(ctx context.Context, user userRecord, mergeID, outboxID, email, bodyHTML, baseURL string) (string, string, error) {
	if !s.entitled(FeatureCampaignTracking) {
		return bodyHTML, "", nil
	}
	s.campaignTrackMu.RLock()
	track := s.campaignTrackHook
	s.campaignTrackMu.RUnlock()
	if track == nil {
		return bodyHTML, "", nil
	}
	return track(ctx, enterprise.Principal{
		UserID: user.ID, TenantID: user.TenantID, Email: user.Email,
		Name: user.Name, Role: user.Role,
	}, mergeID, outboxID, email, bodyHTML, baseURL)
}
