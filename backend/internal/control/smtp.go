package control

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

type smtpResult struct {
	Transcript string
	Err        error
}

func smtpReadReply(reader *bufio.Reader, transcript *strings.Builder) (int, error) {
	code := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return code, err
		}
		transcript.WriteString("S: " + line)
		if len(line) >= 3 {
			if parsed, parseErr := strconv.Atoi(strings.TrimSpace(line[:3])); parseErr == nil {
				code = parsed
			}
		}
		if len(line) < 4 || line[3] != '-' {
			return code, nil
		}
	}
}

func smtpCommand(writer *bufio.Writer, reader *bufio.Reader, transcript *strings.Builder, command string) (int, error) {
	if command != "" {
		transcript.WriteString("C: " + command + "\r\n")
		if _, err := writer.WriteString(command + "\r\n"); err != nil {
			return 0, err
		}
		if err := writer.Flush(); err != nil {
			return 0, err
		}
	}
	return smtpReadReply(reader, transcript)
}

func smtpReplyDetail(transcript string) string {
	lines := strings.Split(transcript, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "S: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "S: "))
		}
	}
	return ""
}

func smtpRejectError(stage string, code int, transcript string) error {
	detail := smtpReplyDetail(transcript)
	if detail == "" {
		return fmt.Errorf("%s failed with code %d", stage, code)
	}
	return fmt.Errorf("%s failed with code %d: %s", stage, code, detail)
}

func (s *Server) runSMTPTransaction(ctx context.Context, from, recipient, subject, body string) smtpResult {
	transcript := &strings.Builder{}
	fromAddress, err := mail.ParseAddress(from)
	if err != nil || strings.ContainsAny(from, "\r\n") {
		return smtpResult{Transcript: transcript.String(), Err: fmt.Errorf("invalid sender address")}
	}
	toAddress, err := mail.ParseAddress(recipient)
	if err != nil || strings.ContainsAny(recipient, "\r\n") {
		return smtpResult{Transcript: transcript.String(), Err: fmt.Errorf("invalid recipient address")}
	}
	subject = strings.ReplaceAll(strings.ReplaceAll(subject, "\r", " "), "\n", " ")
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.cfg.SMTPAddr)
	if err != nil {
		return smtpResult{Transcript: transcript.String(), Err: fmt.Errorf("SMTP connection failed: %w", err)}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	code, err := smtpCommand(writer, reader, transcript, "")
	if err != nil || code/100 != 2 {
		return smtpResult{Transcript: transcript.String(), Err: smtpRejectError("SMTP greeting", code, transcript.String())}
	}
	hostname := s.mailHostname(ctx)
	code, err = smtpCommand(writer, reader, transcript, "EHLO "+hostname)
	if err != nil || code/100 != 2 {
		return smtpResult{Transcript: transcript.String(), Err: smtpRejectError("SMTP EHLO", code, transcript.String())}
	}
	code, err = smtpCommand(writer, reader, transcript, "MAIL FROM:<"+fromAddress.Address+">")
	if err != nil || code/100 != 2 {
		return smtpResult{Transcript: transcript.String(), Err: smtpRejectError("MAIL FROM", code, transcript.String())}
	}
	code, err = smtpCommand(writer, reader, transcript, "RCPT TO:<"+toAddress.Address+">")
	if err != nil || code/100 != 2 {
		return smtpResult{Transcript: transcript.String(), Err: smtpRejectError("RCPT TO", code, transcript.String())}
	}
	code, err = smtpCommand(writer, reader, transcript, "DATA")
	if err != nil || code != 354 {
		return smtpResult{Transcript: transcript.String(), Err: smtpRejectError("DATA", code, transcript.String())}
	}
	messageID, _ := randomToken(18)
	date := time.Now().Format(time.RFC1123Z)
	var message strings.Builder
	message.WriteString("From: " + fromAddress.String() + "\r\n")
	message.WriteString("To: " + toAddress.String() + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("Date: " + date + "\r\n")
	message.WriteString("Message-ID: <" + messageID + "@" + hostname + ">\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	message.WriteString("X-Alt8Mail-Test: system-control\r\n\r\n")
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		message.WriteString(line + "\r\n")
	}
	transcript.WriteString("C: DATA payload (" + strconv.Itoa(message.Len()) + " bytes)\r\n")
	if _, err := writer.WriteString(message.String() + ".\r\n"); err != nil {
		return smtpResult{Transcript: transcript.String(), Err: err}
	}
	if err := writer.Flush(); err != nil {
		return smtpResult{Transcript: transcript.String(), Err: err}
	}
	code, err = smtpReadReply(reader, transcript)
	if err != nil {
		return smtpResult{Transcript: transcript.String(), Err: err}
	}
	_, _ = smtpCommand(writer, reader, transcript, "QUIT")
	if code/100 != 2 {
		return smtpResult{Transcript: transcript.String(), Err: smtpRejectError("Message", code, transcript.String())}
	}
	return smtpResult{Transcript: transcript.String()}
}

func (s *Server) handleSMTPTests(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, recipient::text, subject, status, error, created_at, finished_at
		FROM control.smtp_tests ORDER BY created_at DESC LIMIT 100
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, recipient, subject, status, errText string
		var created time.Time
		var finished *time.Time
		if rows.Scan(&id, &recipient, &subject, &status, &errText, &created, &finished) == nil {
			item := map[string]any{"id": id, "recipient": recipient, "subject": subject,
				"status": status, "error": errText, "createdAt": created.UTC().Format(time.RFC3339)}
			if finished != nil {
				item["finishedAt"] = finished.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tests": items})
}

func (s *Server) handleSMTPTestDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var recipient, subject, status, transcript, errText string
	var created time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT recipient::text, subject, status, transcript, error, created_at
		FROM control.smtp_tests WHERE id=$1
	`, id).Scan(&recipient, &subject, &status, &transcript, &errText, &created)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "recipient": recipient, "subject": subject,
		"status": status, "transcript": transcript, "error": errText, "createdAt": created.UTC().Format(time.RFC3339)})
}

func (s *Server) handleSMTPTestSend(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Recipient string `json:"recipient"`
		From      string `json:"from"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
	}
	if err := decodeJSON(w, r, &body, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Recipient = strings.TrimSpace(body.Recipient)
	if body.Recipient == "" {
		body.Recipient = sess.User.Email
	}
	if body.From == "" {
		body.From = s.defaultMailFrom(r.Context())
	}
	if body.Subject == "" {
		body.Subject = "Alt8Mail SMTP test " + time.Now().UTC().Format("2006-01-02 15:04:05")
	}
	if body.Body == "" {
		body.Body = "This is a delivery test from Alt8Mail System Control.\r\n\r\nIf you received it, the local Postfix submission path is working."
	}
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.smtp_tests (actor_id, recipient, subject, status) VALUES ($1,$2,$3,'running')
		RETURNING id::text
	`, sess.User.ID, body.Recipient, body.Subject).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	result := s.runSMTPTransaction(r.Context(), body.From, body.Recipient, body.Subject, body.Body)
	status := "sent"
	errText := ""
	if result.Err != nil {
		status, errText = "failed", result.Err.Error()
	}
	_, _ = s.db.Exec(r.Context(), `
		UPDATE control.smtp_tests SET status=$2, transcript=$3, error=$4, finished_at=now() WHERE id=$1
	`, id, status, result.Transcript, errText)
	s.actorAudit(r, sess, "delivery.smtp_test", body.Recipient, "Ran SMTP test-send to "+body.Recipient,
		map[string]any{"status": status, "from": body.From})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": result.Err == nil, "id": id, "status": status, "transcript": result.Transcript,
		"error": errText, "from": body.From, "recipient": body.Recipient,
	})
}

func (s *Server) defaultMailFrom(ctx context.Context) string {
	if value := s.settingString(ctx, "server.mail_from", ""); value != "" {
		return value
	}
	var domain string
	_ = s.db.QueryRow(ctx, `SELECT domain::text FROM public.mail_domains WHERE is_primary ORDER BY created_at LIMIT 1`).Scan(&domain)
	if domain == "" {
		_ = s.db.QueryRow(ctx, `SELECT domain::text FROM public.mail_domains ORDER BY created_at LIMIT 1`).Scan(&domain)
	}
	if domain == "" {
		domain = s.cfg.MailHostname
	}
	return "postmaster@" + domain
}

// sendSMTPMessage sends a transactional multipart/alternative message through
// the local Postfix submission listener and returns the generated Message-ID
// plus the recorded SMTP transcript.
func (s *Server) sendSMTPMessage(ctx context.Context, from string, recipients []string, subject, textBody, htmlBody string, headers map[string]string) (string, string, error) {
	transcript := &strings.Builder{}
	fromAddress, err := mail.ParseAddress(from)
	if err != nil || strings.ContainsAny(from, "\r\n") {
		return "", transcript.String(), fmt.Errorf("invalid sender address")
	}
	if len(recipients) == 0 {
		return "", transcript.String(), fmt.Errorf("at least one recipient is required")
	}
	toHeaders := []string{}
	rcptAddresses := []string{}
	for _, recipient := range recipients {
		address, err := mail.ParseAddress(strings.TrimSpace(recipient))
		if err != nil || strings.ContainsAny(recipient, "\r\n") {
			return "", transcript.String(), fmt.Errorf("invalid recipient address %q", recipient)
		}
		rcptAddresses = append(rcptAddresses, address.Address)
		toHeaders = append(toHeaders, address.String())
	}
	subject = strings.ReplaceAll(strings.ReplaceAll(subject, "\r", " "), "\n", " ")
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.cfg.SMTPAddr)
	if err != nil {
		return "", transcript.String(), fmt.Errorf("SMTP connection failed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	code, err := smtpCommand(writer, reader, transcript, "")
	if err != nil || code/100 != 2 {
		return "", transcript.String(), smtpRejectError("SMTP greeting", code, transcript.String())
	}
	hostname := s.mailHostname(ctx)
	code, err = smtpCommand(writer, reader, transcript, "EHLO "+hostname)
	if err != nil || code/100 != 2 {
		return "", transcript.String(), smtpRejectError("SMTP EHLO", code, transcript.String())
	}
	code, err = smtpCommand(writer, reader, transcript, "MAIL FROM:<"+fromAddress.Address+">")
	if err != nil || code/100 != 2 {
		return "", transcript.String(), smtpRejectError("MAIL FROM", code, transcript.String())
	}
	for _, address := range rcptAddresses {
		code, err = smtpCommand(writer, reader, transcript, "RCPT TO:<"+address+">")
		if err != nil || code/100 != 2 {
			return "", transcript.String(), smtpRejectError("RCPT TO "+address, code, transcript.String())
		}
	}
	code, err = smtpCommand(writer, reader, transcript, "DATA")
	if err != nil || code != 354 {
		return "", transcript.String(), smtpRejectError("DATA", code, transcript.String())
	}
	messageID, _ := randomToken(20)
	boundary := "alt8mail-" + messageID
	var message strings.Builder
	message.WriteString("From: " + fromAddress.String() + "\r\n")
	message.WriteString("To: " + strings.Join(toHeaders, ", ") + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	message.WriteString("Message-ID: <" + messageID + "@" + hostname + ">\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	for key, value := range headers {
		value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")
		message.WriteString(key + ": " + value + "\r\n")
	}
	message.WriteString("\r\n")
	message.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	message.WriteString(wrapBase64(base64.StdEncoding.EncodeToString([]byte(textBody))) + "\r\n")
	if strings.TrimSpace(htmlBody) != "" {
		message.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		message.WriteString(wrapBase64(base64.StdEncoding.EncodeToString([]byte(htmlBody))) + "\r\n")
	}
	message.WriteString("--" + boundary + "--\r\n")
	for _, line := range strings.Split(strings.ReplaceAll(message.String(), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return "", transcript.String(), err
		}
	}
	if _, err := writer.WriteString(".\r\n"); err != nil {
		return "", transcript.String(), err
	}
	if err := writer.Flush(); err != nil {
		return "", transcript.String(), err
	}
	code, err = smtpReadReply(reader, transcript)
	if err != nil {
		return "", transcript.String(), err
	}
	_, _ = smtpCommand(writer, reader, transcript, "QUIT")
	if code/100 != 2 {
		return "", transcript.String(), smtpRejectError("Message", code, transcript.String())
	}
	return messageID + "@" + hostname, transcript.String(), nil
}

func wrapBase64(value string) string {
	var b strings.Builder
	for len(value) > 76 {
		b.WriteString(value[:76] + "\r\n")
		value = value[76:]
	}
	b.WriteString(value)
	return b.String()
}
