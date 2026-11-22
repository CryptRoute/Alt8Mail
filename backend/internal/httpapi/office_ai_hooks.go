package httpapi

import (
	"context"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

// officeAIEnabled reports whether the Office AI module is available. The paid
// Office AI module can grant access through its hook; otherwise the in-editor
// assistant uses the same tenant AI configuration and credentials as the
// webmail assistant. That keeps one AI provider setup for the whole product.
func (s *Server) officeAIEnabled(ctx context.Context, user userRecord) bool {
	if hook := s.officeAIEnabledHook; hook != nil {
		if hook(ctx, enterprise.Principal{
			UserID: user.ID, TenantID: user.TenantID, Email: user.Email,
			Name: user.Name, Role: user.Role,
		}) {
			return true
		}
	}
	settings, err := s.aiSettingsFor(ctx, user.TenantID)
	return err == nil && settings.Enabled && strings.TrimSpace(settings.Model) != ""
}
