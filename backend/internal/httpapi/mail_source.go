package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

const maxMessageSourceBytes = 4 << 20

var sourceIPPattern = regexp.MustCompile(`\[(\d{1,3}(?:\.\d{1,3}){3})\]|\[IPv6:([0-9a-fA-F:]+)\]`)

// messageSource returns the raw RFC 822 source of a synced message. The full
// header block is what investigators need: Received hops, originating IPs,
// Message-ID, DKIM signatures and spam verdict headers. The body is capped so a
// very large attachment cannot exhaust the API or the browser.
func (s *Server) messageSource(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	sourceUser := sess.User
	messageID := r.PathValue("id")
	if ownerID, originalID, delegated := parseDelegatedMessageID(messageID); delegated {
		if _, allowed := s.delegationFor(r.Context(), sess.User.TenantID, ownerID, sess.User.ID); !allowed {
			writeError(w, http.StatusForbidden, "DELEGATE_FORBIDDEN", "You do not have access to this mailbox")
			return
		}
		owner, err := s.userByID(r.Context(), ownerID)
		if err != nil || owner.TenantID != sess.User.TenantID {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Delegated mailbox not found")
			return
		}
		sourceUser = owner
		messageID = originalID
	}
	state, ok := s.loadStateFor(r.Context(), sourceUser.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	// Bootstrap messages live in mail_messages, not in the small app_state
	// document. Load the row directly so the source viewer works for real IMAP
	// mail as well as older state-resident messages.
	var message map[string]any
	if loaded, _, err := s.loadMailMessagesByIDs(r.Context(), sourceUser.ID, []string{messageID}); err == nil && len(loaded) > 0 {
		message = loaded[0]
	} else {
		message = findMessageIn(state, messageID)
	}
	if message == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Message not found")
		return
	}
	raw, sourceNote, truncated := s.fetchRawMessage(r.Context(), sourceUser, state, message)
	headers, body := splitRawMessage(raw)
	payload := map[string]any{
		"messageId":   stateString(message, "id"),
		"headers":     headers,
		"source":      string(raw),
		"truncated":   truncated,
		"headerCount": countHeaderLines(headers),
		"sizeBytes":   len(raw),
		"senderIp":    headerSenderIP(headers),
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if sourceNote != "" {
		payload["note"] = sourceNote
	}
	if body == "" && sourceNote != "" {
		payload["source"] = headers
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) fetchRawMessage(ctx context.Context, user userRecord, state map[string]any, message map[string]any) ([]byte, string, bool) {
	if !s.mailAccountConfigured(user.Email) {
		return synthesizedMessageSource(message), "Live IMAP is not configured for this mailbox; headers were reconstructed from the synced message index.", false
	}
	if !s.tenantPolicyFor(ctx, user.TenantID).Mail.IMAPAccess {
		return synthesizedMessageSource(message), "IMAP access is disabled by organization policy; headers were reconstructed from the synced message index.", false
	}
	folder := stateString(message, "imapFolder")
	if folder == "" {
		folder = folderIMAPName(state, stateString(message, "folder"))
	}
	uid := stateInt(message, "imapUid")
	if folder == "" {
		return synthesizedMessageSource(message), "The source mailbox folder is unknown; headers were reconstructed from the synced message index.", false
	}
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return synthesizedMessageSource(message), "The mailbox credentials are unavailable; headers were reconstructed from the synced message index.", false
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return synthesizedMessageSource(message), "The IMAP TLS configuration is unavailable; headers were reconstructed from the synced message index.", false
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		s.logger.Warn("message source IMAP dial failed", "err", err)
		return synthesizedMessageSource(message), "The mail server could not be reached; headers were reconstructed from the synced message index.", false
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		s.logger.Warn("message source IMAP login failed", "err", err)
		return synthesizedMessageSource(message), "The mail server rejected the mailbox session; headers were reconstructed from the synced message index.", false
	}
	if _, err := conn.Select(folder, true); err != nil {
		return synthesizedMessageSource(message), "The source folder is no longer available; headers were reconstructed from the synced message index.", false
	}
	if uid == 0 {
		if found, err := findIMAPUID(conn, folder, stateString(message, "messageId")); err == nil {
			uid = int64(found)
		}
	}
	if uid <= 0 {
		return synthesizedMessageSource(message), "The message is no longer on the mail server; headers were reconstructed from the synced message index.", false
	}
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchRFC822Size, section.FetchItem()}
	set := new(imap.SeqSet)
	set.AddNum(uint32(uid))
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- conn.UidFetch(set, items, messages) }()
	var fetched *imap.Message
	for item := range messages {
		if item != nil {
			fetched = item
		}
	}
	if err := <-done; err != nil || fetched == nil {
		return synthesizedMessageSource(message), "The mail server did not return the message source; headers were reconstructed from the synced message index.", false
	}
	reader := fetched.GetBody(section)
	if reader == nil {
		return synthesizedMessageSource(message), "The mail server returned no message body; headers were reconstructed from the synced message index.", false
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxMessageSourceBytes+1))
	if err != nil {
		return synthesizedMessageSource(message), "The message source could not be read; headers were reconstructed from the synced message index.", false
	}
	truncated := len(raw) > maxMessageSourceBytes
	if truncated {
		raw = raw[:maxMessageSourceBytes]
	}
	note := ""
	if truncated {
		note = fmt.Sprintf("Source truncated at %s. Header block is complete; the message body continues beyond the preview limit.", formatBytes(maxMessageSourceBytes))
	}
	return raw, note, truncated
}

func splitRawMessage(raw []byte) (string, string) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if index := strings.Index(text, "\n\n"); index >= 0 {
		return text[:index], text[index+2:]
	}
	return text, ""
}

func countHeaderLines(headers string) int {
	count := 0
	lastWasHeader := false
	for _, line := range strings.Split(headers, "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		lastWasHeader = strings.Contains(line, ":")
		if lastWasHeader {
			count++
		}
	}
	return count
}

func headerSenderIP(headers string) string {
	// Parse the raw block directly so malformed messages still yield a
	// best-effort address. Folded continuation lines are joined first.
	values := []string{}
	var current string
	for _, line := range strings.Split(headers, "\n") {
		if line == "" {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			if current != "" {
				values[len(values)-1] += " " + strings.TrimSpace(line)
			}
			continue
		}
		current = line
		values = append(values, line)
	}
	for _, header := range values {
		lower := strings.ToLower(header)
		if !strings.HasPrefix(lower, "received:") && !strings.HasPrefix(lower, "x-originating-ip:") && !strings.HasPrefix(lower, "x-sender-ip:") {
			continue
		}
		matches := sourceIPPattern.FindStringSubmatch(header)
		if len(matches) > 1 {
			for _, match := range matches[1:] {
				if match != "" {
					return match
				}
			}
		}
	}
	return ""
}

func synthesizedMessageSource(message map[string]any) []byte {
	from, _ := message["from"].(map[string]any)
	lines := []string{
		"X-Alt8Mail-Source: reconstructed from the synced message index",
		"From: " + personHeader(from),
		"To: " + peopleHeader(message["to"]),
	}
	if cc := peopleHeader(message["cc"]); cc != "" {
		lines = append(lines, "Cc: "+cc)
	}
	lines = append(lines,
		"Date: "+stateString(message, "date"),
		"Subject: "+stateString(message, "subject"),
	)
	if id := stateString(message, "messageId"); id != "" {
		lines = append(lines, "Message-ID: "+id)
	}
	if auth := stateMap(message, "auth"); len(auth) > 0 {
		lines = append(lines, "X-Alt8Mail-Auth: SPF="+stateString(auth, "spf")+" DKIM="+stateString(auth, "dkim")+" DMARC="+stateString(auth, "dmarc"))
	}
	body := stateString(message, "bodyText")
	if body == "" {
		body = htmlToPlainText(stateString(message, "bodyHtml"))
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n\r\n" + body)
}

func personHeader(person map[string]any) string {
	name := stateString(person, "name")
	email := stateString(person, "email")
	if name != "" && email != "" {
		return fmt.Sprintf("%s <%s>", name, email)
	}
	if email != "" {
		return email
	}
	return name
}

func peopleHeader(raw any) string {
	list, _ := raw.([]any)
	values := []string{}
	for _, item := range list {
		person, _ := item.(map[string]any)
		if value := personHeader(person); value != "" {
			values = append(values, value)
		}
	}
	return strings.Join(values, ", ")
}
