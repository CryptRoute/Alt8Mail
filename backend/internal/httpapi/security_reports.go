package httpapi

import (
	"context"
	"strings"
)

func normalizeReportReason(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "phishing":
		return "phishing"
	case "malware":
		return "malware"
	case "unwanted":
		return "unwanted"
	case "other":
		return "other"
	default:
		return "spam"
	}
}

// recordSecurityReport queues a message for System Control review. The Postfix
// policy daemon reads pending and confirmed rows directly, so the sender is
// blocked system-wide as soon as this insert commits, even before an analyst
// reviews the report.
func (s *Server) recordSecurityReport(ctx context.Context, user userRecord, message map[string]any, reason, note string) error {
	senderEmail := ""
	senderName := ""
	if sender, ok := message["from"].(map[string]any); ok {
		senderEmail = strings.ToLower(strings.TrimSpace(stateString(sender, "email")))
		senderName = truncate(stateString(sender, "name"), 160)
	}
	if senderEmail == "" {
		return nil
	}
	messageID := stateString(message, "id")
	if messageID == "" {
		return nil
	}
	reportID := newID("security")
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO security_reports (
			id, tenant_id, user_id, user_email, message_id, sender_email, sender_name,
			sender_domain, subject, snippet, reason, note
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (user_id, message_id) DO UPDATE
		SET reason=EXCLUDED.reason, note=EXCLUDED.note, sender_email=EXCLUDED.sender_email,
		    sender_name=EXCLUDED.sender_name, sender_domain=EXCLUDED.sender_domain,
		    subject=EXCLUDED.subject, snippet=EXCLUDED.snippet, status='pending',
		    block_scope='address', notified_at=NULL, reviewed_by='', reviewed_at=NULL,
		    resolution='', created_at=now(), updated_at=now()
	`, reportID, user.TenantID, user.ID, user.Email, messageID, senderEmail, senderName,
		strings.ToLower(domainOf(senderEmail)), truncate(stateString(message, "subject"), 300),
		truncate(stateString(message, "snippet"), 500), normalizeReportReason(reason),
		truncate(strings.TrimSpace(note), 500))
	return err
}
