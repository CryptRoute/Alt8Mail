package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

// aiBudgetCheck applies the paid budget policy when the private module is
// installed. Community builds allow every AI call.
func (s *Server) aiBudgetCheck(ctx context.Context, tenantID string) error {
	s.aiBudgetMu.RLock()
	hook := s.aiBudgetCheckHook
	s.aiBudgetMu.RUnlock()
	if hook == nil {
		return nil
	}
	return hook(ctx, tenantID)
}

// recordAIUsage records estimated usage when the paid module is installed.
func (s *Server) recordAIUsage(ctx context.Context, tenantID string, inputChars, outputChars int) {
	s.aiBudgetMu.RLock()
	hook := s.aiUsageHook
	s.aiBudgetMu.RUnlock()
	if hook != nil {
		hook(ctx, tenantID, inputChars, outputChars)
	}
}

func aiBudgetError(err error) bool { return errors.Is(err, enterprise.ErrAIBudget) }

func aiBudgetMessage(err error) string {
	if aiBudgetError(err) {
		return "The organization's monthly AI budget is exhausted. An administrator can raise it in System Control."
	}
	return fmt.Sprint(err)
}

func (s *Server) callAIForTenant(ctx context.Context, tenantID string, settings aiSettings, system, user string, maxTokens int, temperature float64) (string, error) {
	if settings.TenantID == "" {
		settings.TenantID = tenantID
	}
	return s.callAI(ctx, settings, system, user, maxTokens, temperature)
}
