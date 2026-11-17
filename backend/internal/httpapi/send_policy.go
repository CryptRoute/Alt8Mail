package httpapi

import (
	"context"
	"fmt"
	"strings"
)

// validateProgrammaticSend applies the same tenant outbound policy and DLP
// checks as the interactive compose path before a programmatic sender (public
// API or an approved MCP action) may queue mail.
func (s *Server) validateProgrammaticSend(ctx context.Context, user userRecord, to, cc, bcc []string, subject, bodyText, selectedClassification string) (string, []string, []dlpMatch, error) {
	policy := s.tenantPolicyFor(ctx, user.TenantID)
	recipients := []any{}
	for _, list := range [][]string{to, cc, bcc} {
		for _, email := range list {
			recipients = append(recipients, map[string]any{"email": email})
		}
	}
	if err := s.enforceOutboundPolicy(ctx, user.TenantID, recipients, nil, policy); err != nil {
		return "", nil, nil, err
	}
	if state, ok := s.loadStateWithOutbox(ctx, user.ID); ok {
		outbound := s.outboundPayload(ctx, user, state)
		if outboundCapReached(outbound, len(recipients)) {
			return "", nil, nil, fmt.Errorf("daily outbound cap reached")
		}
	}
	if selectedClassification != "" && !validDLPClassification(selectedClassification) {
		return "", nil, nil, fmt.Errorf("invalid classification")
	}
	classification, warnings, matches, blocked := evaluateOutboundDLP(policy, subject, bodyText, nil, selectedClassification)
	if blocked {
		return "", warnings, matches, fmt.Errorf("message blocked by organization data protection policy")
	}
	return classification, warnings, matches, nil
}

func mcpRecipientList(value string) []string {
	out := []string{}
	for _, raw := range strings.FieldsFunc(value, func(char rune) bool { return char == ',' || char == ';' }) {
		email := normalizeEmailValue(raw)
		if looksLikeEmail(email) {
			out = append(out, email)
		}
	}
	return apiUniqueRecipients(out)
}
