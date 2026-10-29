package httpapi

import (
	"time"

	"context"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

// persistDraftToIMAP replaces the server-side copy of a compose draft in
// Dovecot's Drafts mailbox. The state draft uses a stable Message-ID so the
// next sync updates the same UI row instead of creating a duplicate.
func (s *Server) persistDraftToIMAP(user userRecord, draft map[string]any, attachmentRecords map[string]any) {
	if !s.mailAccountConfigured(user.Email) || !s.tenantPolicyFor(context.Background(), user.TenantID).Mail.IMAPAccess {
		return
	}
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		s.logger.Warn("draft IMAP credentials unavailable", "err", err)
		return
	}
	messageID := stateString(draft, "messageId")
	if messageID == "" {
		return
	}
	attachments, err := s.outboundAttachments(attachmentRecords, draft)
	if err != nil {
		s.logger.Warn("draft attachment read failed", "err", err)
		return
	}
	raw, err := buildMIMEMessage(draft, messageID, attachments)
	if err != nil {
		s.logger.Warn("draft MIME build failed", "err", err)
		return
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		s.logger.Warn("draft IMAP TLS config failed", "err", err)
		return
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		s.logger.Warn("draft IMAP connect failed", "err", err)
		return
	}
	defer conn.Logout()
	conn.Timeout = 20 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		s.logger.Warn("draft IMAP login failed", "err", err)
		return
	}
	ensureIMAPFolders(conn)
	if uid, err := findIMAPUID(conn, "Drafts", messageID); err == nil && uid > 0 {
		set := new(imap.SeqSet)
		set.AddNum(uid)
		_ = conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil)
		_ = conn.Expunge(nil)
	}
	if err := conn.Append("Drafts", []string{imap.DraftFlag, imap.SeenFlag}, time.Now(), &imapLiteral{data: raw}); err != nil {
		s.logger.Warn("draft IMAP append failed", "err", err)
	}
}

func (s *Server) deleteIMAPDraft(user userRecord, messageID string) {
	if !s.mailAccountConfigured(user.Email) || messageID == "" || !s.tenantPolicyFor(context.Background(), user.TenantID).Mail.IMAPAccess {
		return
	}
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return
	}
	defer conn.Logout()
	conn.Timeout = 20 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return
	}
	uid, err := findIMAPUID(conn, "Drafts", messageID)
	if err != nil || uid == 0 {
		return
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	_ = conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil)
	_ = conn.Expunge(nil)
}
