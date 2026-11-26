package httpapi

import (
	"context"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

// officeAIEnabled reports whether the Office AI module is available. Office AI
// is a paid capability, so only the enterprise module can enable the editor
// panel; the webmail assistant does not grant it implicitly.
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
