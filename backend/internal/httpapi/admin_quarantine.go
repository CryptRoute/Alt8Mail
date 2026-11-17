package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

type quarantineItem struct {
	MessageID string `json:"id"`
	UserID    string `json:"userId"`
	UserEmail string `json:"userEmail"`
	UserName  string `json:"userName"`
	Subject   string `json:"subject"`
	FromName  string `json:"fromName"`
	FromEmail string `json:"fromEmail"`
	Date      string `json:"date"`
	Snippet   string `json:"snippet"`
}

func (s *Server) adminListQuarantine(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT u.id::text, u.email::text, u.name,
		       m.id, COALESCE(NULLIF(m.subject,''), '(no subject)'),
		       m.from_name, m.from_email::text, m.sent_at, m.snippet
		FROM mail_messages m
		JOIN users u ON u.id = m.user_id
		JOIN tenants t ON t.id = u.tenant_id
		WHERE u.tenant_id = $1 AND u.deleted_at IS NULL AND t.status = 'active'
		  AND m.folder = 'spam'
		ORDER BY m.sent_at DESC
		LIMIT 200
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUARANTINE", "Could not load quarantined messages")
		return
	}
	defer rows.Close()
	items := []quarantineItem{}
	for rows.Next() {
		var item quarantineItem
		var sentAt time.Time
		if rows.Scan(&item.UserID, &item.UserEmail, &item.UserName, &item.MessageID,
			&item.Subject, &item.FromName, &item.FromEmail, &sentAt, &item.Snippet) == nil {
			item.Date = sentAt.UTC().Format(time.RFC3339)
			items = append(items, item)
		}
	}
	policy := tenantPolicyFromSettings(sess.ActiveTenant.Settings)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": len(items), "adminOnly": policy.Mail.Quarantine,
	})
}

func (s *Server) adminReleaseQuarantine(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("userId"))
	if err != nil || user.TenantID != sess.ActiveTenantID || user.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	messageID := strings.TrimSpace(r.PathValue("messageId"))
	if messageID == "" || len(messageID) > 128 {
		writeError(w, http.StatusBadRequest, "MESSAGE", "Invalid message")
		return
	}
	record := user.userRecord()
	lock := s.userMailLock(record.ID)
	lock.Lock()
	defer lock.Unlock()

	message, err := s.loadMailMessage(r.Context(), record.ID, messageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "MESSAGE_NOT_FOUND", "Message not found")
		return
	}
	if stateString(message, "folder") != "spam" {
		writeError(w, http.StatusConflict, "NOT_QUARANTINED", "This message is no longer in quarantine")
		return
	}
	if err := s.releaseQuarantineFromIMAP(record, stateString(message, "messageId"), stateInt(message, "imapUid")); err != nil {
		writeError(w, http.StatusBadGateway, "QUARANTINE_RELEASE", "Could not move the message out of Junk: "+err.Error())
		return
	}
	err = s.withMailState(r.Context(), record, []string{messageID}, false, func(current map[string]any) error {
		target := findMessageIn(current, messageID)
		if target == nil {
			return fail(http.StatusConflict, "MESSAGE_NOT_FOUND", "Message changed while it was being released")
		}
		target["folder"] = "inbox"
		target["imapFolder"] = "inbox"
		target["imapUid"] = 0
		delete(target, "spamReport")
		appendStateAudit(current, "quarantine.released", stateString(target, "subject"), sess.User.Name,
			"Quarantine released by an administrator", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUARANTINE_RELEASE", "Message moved, but the mailbox state could not be updated")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.quarantine_released", stateString(message, "subject"),
		"Released quarantined mail for "+record.Email, map[string]any{"tenantId": sess.ActiveTenantID, "userId": record.ID, "messageId": messageID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) releaseQuarantineFromIMAP(user userRecord, messageID string, sourceUID int64) error {
	if !s.mailAccountConfigured(user.Email) {
		return fmt.Errorf("no IMAP mailbox is configured for %s", user.Email)
	}
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return err
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return fmt.Errorf("connect to IMAP: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return fmt.Errorf("IMAP login: %w", err)
	}
	uid, err := findIMAPUID(conn, "Junk", messageID)
	if (err != nil || uid == 0) && sourceUID > 0 {
		if _, selectErr := conn.Select("Junk", false); selectErr == nil {
			uid = uint32(sourceUID)
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if uid == 0 {
		return fmt.Errorf("message is no longer in Junk")
	}
	if err := moveIMAPMessage(conn, "Junk", uid, "INBOX"); err != nil {
		return err
	}
	return nil
}
