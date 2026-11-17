package httpapi

import (
	"context"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

// officeDLPResult is recorded on every scanned Office save. A blocked result
// means the new version must not replace the current document.
type officeDLPResult struct {
	Scanned        bool
	Classification string
	Warnings       []string
	Matches        []dlpMatch
	Blocked        bool
}

// evaluateOfficeSaveDLP inspects the saved bytes using the tenant's
// classification rules. The file name is treated as the subject, extracted
// document text as the body, and the name is also supplied as an attachment
// name so existing attachment rules keep working.
func (s *Server) evaluateOfficeSaveDLP(ctx context.Context, tenantID, name string, data []byte) officeDLPResult {
	result := officeDLPResult{Warnings: []string{}, Matches: []dlpMatch{}}
	policy := s.tenantPolicyFor(ctx, tenantID)
	if !policy.Office.DLPOnSave || !policy.DLP.Enabled {
		return result
	}
	result.Scanned = true
	text, err := office.ExtractText(name, data)
	if err != nil {
		result.Classification = canonicalDLPClassification(policy.DLP.DefaultClassification)
		if result.Classification == "" {
			result.Classification = "Internal"
		}
		result.Warnings = append(result.Warnings, "Document text could not be inspected: "+err.Error())
		return result
	}
	classification, warnings, matches, blocked := evaluateOutboundDLP(policy, name, text, []string{name}, "")
	result.Classification = classification
	if warnings != nil {
		result.Warnings = warnings
	}
	if matches != nil {
		result.Matches = matches
	}
	result.Blocked = blocked
	return result
}
