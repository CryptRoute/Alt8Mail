package httpapi

import (
	"context"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

// officeAIEnabled reports whether the paid Office AI module is configured and
// allowed for the user. Community builds have no hook and report false.
func (s *Server) officeAIEnabled(ctx context.Context, user userRecord) bool {
	hook := s.officeAIEnabledHook
	if hook == nil {
		return false
	}
	return hook(ctx, enterprise.Principal{
		UserID: user.ID, TenantID: user.TenantID, Email: user.Email,
		Name: user.Name, Role: user.Role,
	})
}
