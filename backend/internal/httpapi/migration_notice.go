package httpapi

import (
	"context"
	"strings"
)

// sendMigrationNotice is the core host capability used by the private mail
// migration module to tell an imported user that their mailbox is ready. The
// message body and delivery mechanics stay in the community edition; the
// migration workflow that triggers it is a paid feature.
func (s *Server) sendMigrationNotice(ctx context.Context, tenantID, preferredUserID, to, name, mailbox, password string) error {
	fromEmail, fromName, productName, err := s.noticeSenderFor(ctx, tenantID, preferredUserID)
	if err != nil {
		return err
	}
	loginURL := strings.TrimRight(s.cfg.PublicURL, "/") + "/"
	return s.sendAccountNotice(ctx, accountNotice{
		TenantID: tenantID, FromEmail: fromEmail, FromName: fromName,
		To: to, ToName: name, Subject: "Your mailbox has moved to " + productName,
		Heading: "Your mailbox is ready — with your old mail",
		Intro:   "Your organization has moved your mailbox to Alt8Mail. Your existing messages have been copied across, so you can sign in and pick up where you left off.",
		Mailbox: mailbox, Password: password, Kind: "migrated",
		Steps: []string{
			"Sign in at " + loginURL + " with your mailbox address.",
			"Set a new password and complete the short setup.",
			"Check your folders — your inbox and sent mail are already there.",
		},
		FooterNote: "Your previous provider was not changed by this import. Keep using Alt8Mail from now on.",
	})
}
