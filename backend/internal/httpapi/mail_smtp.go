package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

type deliveredMail struct {
	MessageID string
	Raw       []byte
}

type outboundAttachment struct {
	Name     string
	MIMEType string
	Data     []byte
}

type imapLiteral struct {
	data []byte
	pos  int
}

func (l *imapLiteral) Read(p []byte) (int, error) {
	if l.pos >= len(l.data) {
		return 0, io.EOF
	}
	n := copy(p, l.data[l.pos:])
	l.pos += n
	return n, nil
}

func (l *imapLiteral) Len() int { return len(l.data) }

func (s *Server) mailDeliveryConfigured() bool {
	return s.mailSyncConfigured() && s.cfg.SMTPAddr != ""
}

func (s *Server) processOutboxItem(ctx context.Context, userID, tenantID, itemID string, requireScheduled bool) (map[string]any, error) {
	var itemCopy map[string]any
	var attachmentRecords map[string]any
	err := s.withMailState(ctx, userRecord{ID: userID, TenantID: tenantID}, nil, false, func(state map[string]any) error {
		item := stateObjectByID(stateSlice(state, "outbox"), itemID)
		if item == nil {
			return fail(http.StatusConflict, "SEND_ALREADY_COMMITTED", "This message has already been sent")
		}
		s.preloadAttachments(ctx, userID, state, attachmentIDsFromValue(item["attachments"]))
		if requireScheduled && !stateBool(item, "scheduled") {
			return fail(http.StatusConflict, "NOT_SCHEDULED", "This message is already in the undo-send window")
		}
		if stateString(item, "deliveryState") == "sending" {
			if claimed, ok := parseStateTime(stateString(item, "deliveryClaimedAt")); ok && time.Since(claimed) < 2*time.Minute {
				return fail(http.StatusConflict, "SEND_IN_PROGRESS", "This message is already being delivered")
			}
		}
		if retryAt := stateString(item, "retryAt"); retryAt != "" {
			if stamp, ok := parseStateTime(retryAt); ok && stamp.After(time.Now()) {
				return fail(http.StatusConflict, "SEND_RETRY", "Delivery is being retried shortly")
			}
		}
		itemCopy = deepCopyMap(item)
		item["deliveryState"] = "sending"
		item["deliveryClaimedAt"] = nowISO()
		item["attempts"] = stateInt(item, "attempts") + 1
		attachmentRecords, _ = state["attachments"].(map[string]any)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !s.mailDeliveryConfigured() {
		sentID, err := s.finalizeOutboxItem(userID, tenantID, itemID, "", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sentMessageId": sentID, "subject": firstNonEmpty(stateString(itemCopy, "subject"), "(no subject)")}, nil
	}

	attachments, err := s.outboundAttachments(attachmentRecords, itemCopy)
	if err != nil {
		s.markDeliveryRetry(userID, tenantID, itemID, err)
		return nil, err
	}
	delivered, err := s.deliverSMTP(itemCopy, attachments)
	if err != nil {
		s.markDeliveryRetry(userID, tenantID, itemID, err)
		s.logger.Warn("outbound delivery failed", "item", itemID, "err", err)
		return nil, err
	}
	sentID, err := s.finalizeOutboxItem(userID, tenantID, itemID, delivered.MessageID, delivered.Raw)
	if err != nil {
		return nil, err
	}
	fromIdentity, _ := itemCopy["from"].(map[string]any)
	s.appendToSent(delivered.Raw, stateString(fromIdentity, "email"))
	return map[string]any{
		"sentMessageId": sentID, "subject": firstNonEmpty(stateString(itemCopy, "subject"), "(no subject)"),
		"messageId": delivered.MessageID,
	}, nil
}

func (s *Server) finalizeOutboxItem(userID, tenantID, itemID, messageID string, raw []byte) (string, error) {
	sentID := ""
	err := s.withMailState(context.Background(), userRecord{ID: userID, TenantID: tenantID}, nil, false, func(state map[string]any) error {
		item := stateObjectByID(stateSlice(state, "outbox"), itemID)
		if item == nil {
			return fail(http.StatusConflict, "SEND_ALREADY_COMMITTED", "This message has already been sent")
		}
		if messageID != "" {
			item["mimeMessageId"] = messageID
		}
		sentID = commitQueued(state, item)
		return nil
	})
	return sentID, err
}

func (s *Server) markDeliveryRetry(userID, tenantID, itemID string, deliveryErr error) {
	_ = s.withMailState(context.Background(), userRecord{ID: userID, TenantID: tenantID}, nil, false, func(state map[string]any) error {
		item := stateObjectByID(stateSlice(state, "outbox"), itemID)
		if item == nil {
			return nil
		}
		attempts := stateInt(item, "attempts")
		delay := time.Duration(attempts*attempts) * 5 * time.Second
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		item["deliveryState"] = "retry"
		item["lastError"] = truncate(deliveryErr.Error(), 500)
		item["retryAt"] = time.Now().Add(delay).UTC().Format(time.RFC3339)
		return nil
	})
}

func (s *Server) outboundAttachments(records map[string]any, item map[string]any) ([]outboundAttachment, error) {
	result := []outboundAttachment{}
	for _, raw := range stateSlice(item, "attachments") {
		attachment, _ := raw.(map[string]any)
		if attachment == nil {
			continue
		}
		name := stateString(attachment, "name")
		if name == "" {
			name = "attachment"
		}
		mimeType := stateString(attachment, "mime")
		if mimeType == "" {
			mimeType = mimeForExtension(name)
		}
		id := stateString(attachment, "id")
		var data []byte
		if records != nil {
			if record, _ := records[id].(map[string]any); record != nil {
				storedName := stateString(record, "storedName")
				if storedName != "" && !strings.ContainsAny(storedName, `/\`) {
					read, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, storedName))
					if err == nil {
						data = read
					}
				}
			}
		}
		if len(data) == 0 {
			contentURL := stateString(attachment, "contentUrl")
			if strings.HasPrefix(contentURL, "/samples/") && !strings.Contains(contentURL, "..") {
				read, err := os.ReadFile(filepath.Join(s.cfg.PublicDir, strings.TrimPrefix(contentURL, "/")))
				if err == nil {
					data = read
				}
			}
		}
		if len(data) == 0 {
			continue
		}
		result = append(result, outboundAttachment{Name: name, MIMEType: mimeType, Data: data})
	}
	return result, nil
}

func (s *Server) deliverSMTP(item map[string]any, attachments []outboundAttachment) (*deliveredMail, error) {
	from, _ := item["from"].(map[string]any)
	fromEmail := stateString(from, "email")
	if fromEmail == "" {
		return nil, fmt.Errorf("message has no From identity")
	}
	recipients := []string{}
	for _, key := range []string{"to", "cc", "bcc"} {
		for _, raw := range stateSlice(item, key) {
			person, _ := raw.(map[string]any)
			if email := stateString(person, "email"); email != "" {
				recipients = append(recipients, email)
			}
		}
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("message has no recipients")
	}
	messageID := fmt.Sprintf("<%s@%s>", newID("mail"), s.cfg.MailDomain)
	raw, err := buildMIMEMessage(item, messageID, attachments)
	if err != nil {
		return nil, err
	}
	if err := s.submitSMTPMessage(fromEmail, recipients, raw); err != nil {
		return nil, err
	}
	return &deliveredMail{MessageID: messageID, Raw: raw}, nil
}

// submitSMTPMessage delivers one already-built RFC 822 message through the
// authenticated submission service. It is shared by the compose pipeline and
// the account-notification mailer used by user and mailbox imports.
func (s *Server) submitSMTPMessage(fromEmail string, recipients []string, raw []byte) error {
	tlsConfig, err := s.mailTLSConfig(s.cfg.SMTPTLSName)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", s.cfg.SMTPAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to submission service: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	client, err := smtp.NewClient(conn, s.cfg.SMTPTLSName)
	if err != nil {
		return fmt.Errorf("submission greeting: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return fmt.Errorf("submission service does not offer STARTTLS")
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("submission TLS: %w", err)
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		return fmt.Errorf("submission service does not offer authentication")
	}
	loginUser, loginPassword, err := s.mailLoginForUser(fromEmail)
	if err != nil {
		return err
	}
	auth := smtp.PlainAuth("", loginUser, loginPassword, s.cfg.SMTPTLSName)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("submission authentication: %w", err)
	}
	if err := client.Mail(fromEmail); err != nil {
		return fmt.Errorf("submission MAIL FROM: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("submission RCPT TO %s: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("submission DATA: %w", err)
	}
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("submission quit: %w", err)
	}
	return nil
}

func buildMIMEMessage(item map[string]any, messageID string, attachments []outboundAttachment) ([]byte, error) {
	from, _ := item["from"].(map[string]any)
	fromName := stateString(from, "name")
	fromEmail := stateString(from, "email")
	toPeople := stateSlice(item, "to")
	ccPeople := stateSlice(item, "cc")
	subject := stateString(item, "subject")
	bodyText := stateString(item, "bodyText")
	bodyHTML := sanitizeComposeHTML(stateString(item, "bodyHtml"))
	if bodyHTML == "" {
		bodyHTML = textToHTML(bodyText)
	}
	if bodyText == "" {
		bodyText = htmlToPlainText(bodyHTML)
	}
	var buffer bytes.Buffer
	writeHeaderLine(&buffer, "From", formatMailbox(fromName, fromEmail))
	if len(toPeople) > 0 {
		writeHeaderLine(&buffer, "To", formatPeopleHeader(toPeople))
	}
	if len(ccPeople) > 0 {
		writeHeaderLine(&buffer, "Cc", formatPeopleHeader(ccPeople))
	}
	writeHeaderLine(&buffer, "Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeaderLine(&buffer, "Date", time.Now().Format(time.RFC1123Z))
	writeHeaderLine(&buffer, "Message-ID", messageID)
	if inReplyTo := stateString(item, "inReplyTo"); inReplyTo != "" {
		writeHeaderLine(&buffer, "In-Reply-To", inReplyTo)
	}
	writeHeaderLine(&buffer, "MIME-Version", "1.0")
	altBoundary := "alt_" + randomToken(12)
	if len(attachments) > 0 {
		boundary := "mix_" + randomToken(12)
		writeHeaderLine(&buffer, "Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
		buffer.WriteString("\r\n")
		buffer.WriteString("--" + boundary + "\r\n")
		writeAlternativePart(&buffer, altBoundary, bodyText, bodyHTML)
		for _, attachment := range attachments {
			buffer.WriteString("--" + boundary + "\r\n")
			contentType := mime.FormatMediaType(attachment.MIMEType, map[string]string{"name": attachment.Name})
			writeHeaderLine(&buffer, "Content-Type", contentType)
			writeHeaderLine(&buffer, "Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name}))
			writeHeaderLine(&buffer, "Content-Transfer-Encoding", "base64")
			buffer.WriteString("\r\n")
			writeBase64Lines(&buffer, attachment.Data)
			buffer.WriteString("\r\n")
		}
		buffer.WriteString("--" + boundary + "--\r\n")
	} else {
		writeAlternativePart(&buffer, altBoundary, bodyText, bodyHTML)
	}
	return buffer.Bytes(), nil
}

func writeAlternativePart(buffer *bytes.Buffer, boundary, bodyText, bodyHTML string) {
	writeHeaderLine(buffer, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	buffer.WriteString("\r\n")
	buffer.WriteString("--" + boundary + "\r\n")
	writeHeaderLine(buffer, "Content-Type", "text/plain; charset=UTF-8")
	writeHeaderLine(buffer, "Content-Transfer-Encoding", "base64")
	buffer.WriteString("\r\n")
	writeBase64Lines(buffer, []byte(bodyText))
	buffer.WriteString("\r\n--" + boundary + "\r\n")
	writeHeaderLine(buffer, "Content-Type", "text/html; charset=UTF-8")
	writeHeaderLine(buffer, "Content-Transfer-Encoding", "base64")
	buffer.WriteString("\r\n")
	writeBase64Lines(buffer, []byte(bodyHTML))
	buffer.WriteString("\r\n--" + boundary + "--\r\n")
}

func writeHeaderLine(buffer *bytes.Buffer, name, value string) {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	buffer.WriteString(name + ": " + value + "\r\n")
}

func writeBase64Lines(buffer *bytes.Buffer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		buffer.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	if encoded != "" {
		buffer.WriteString(encoded + "\r\n")
	}
}

func formatMailbox(name, email string) string {
	if name == "" {
		return "<" + email + ">"
	}
	return (&mail.Address{Name: name, Address: email}).String()
}

func formatPeopleHeader(people []any) string {
	parts := []string{}
	for _, raw := range people {
		person, _ := raw.(map[string]any)
		if person == nil {
			continue
		}
		parts = append(parts, formatMailbox(stateString(person, "name"), stateString(person, "email")))
	}
	return strings.Join(parts, ", ")
}

func (s *Server) appendToSent(raw []byte, fromEmail string) {
	if !s.mailAccountConfigured(fromEmail) || len(raw) == 0 {
		return
	}
	loginUser, loginPassword, err := s.mailLoginForUser(fromEmail)
	if err != nil {
		s.logger.Warn("append sent: credentials", "err", err)
		return
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		s.logger.Warn("append sent: TLS config", "err", err)
		return
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		s.logger.Warn("append sent: connect", "err", err)
		return
	}
	defer conn.Logout()
	conn.Timeout = 20 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		s.logger.Warn("append sent: login", "err", err)
		return
	}
	literal := &imapLiteral{data: raw}
	if err := conn.Append("Sent", []string{`\Seen`}, time.Now(), literal); err != nil {
		_ = conn.Create("Sent")
		literal = &imapLiteral{data: raw}
		if retryErr := conn.Append("Sent", []string{`\Seen`}, time.Now(), literal); retryErr != nil {
			s.logger.Warn("append sent: append", "err", retryErr)
		}
	}
}
