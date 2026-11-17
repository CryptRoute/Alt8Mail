package httpapi

import (
	"context"
	"strings"
)

// userOnLegalHold delegates to the paid legal-hold provider when an enterprise
// extension is installed. Community builds keep the permissive default.
func (s *Server) userOnLegalHold(ctx context.Context, tenantID, userID string) (bool, error) {
	if s.legalHoldCheck != nil {
		return s.legalHoldCheck(ctx, tenantID, userID)
	}
	return false, nil
}

// tenantLegalHoldSnapshot delegates to the paid legal-hold provider when one is
// installed.
func (s *Server) tenantLegalHoldSnapshot(ctx context.Context, tenantID string) (bool, map[string]bool, error) {
	if s.legalHoldSnapshot != nil {
		return s.legalHoldSnapshot(ctx, tenantID)
	}
	return false, map[string]bool{}, nil
}

func escapeLikePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
