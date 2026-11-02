package control

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

const maxMailSourceBytes = 1 << 20

type mailEventSourceEvent struct {
	ID        int64
	QueueID   string
	Sender    string
	Recipient string
	Domain    string
	Direction string
	EventAt   time.Time
	Status    string
}

func sanitizeMessageSource(raw []byte, truncated bool) (string, bool) {
	if len(raw) > maxMailSourceBytes {
		raw = raw[:maxMailSourceBytes]
		truncated = true
	}
	value := strings.ReplaceAll(string(raw), "\x00", "")
	value = strings.ToValidUTF8(value, "�")
	return value, truncated
}

func decodeMailHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoder := &mime.WordDecoder{
		CharsetReader: func(charsetName string, input io.Reader) (io.Reader, error) {
			return charset.NewReaderLabel(charsetName, input)
		},
	}
	if decoded, err := decoder.DecodeHeader(value); err == nil && strings.TrimSpace(decoded) != "" {
		return strings.TrimSpace(decoded)
	}
	return value
}

func parseMessageSourceHeaders(raw string) (subject, messageID string) {
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(strings.ToLower(line), "subject:") && subject == "" {
				subject = decodeMailHeaderValue(strings.TrimSpace(line[len("subject:"):]))
			}
			if strings.HasPrefix(strings.ToLower(line), "message-id:") && messageID == "" {
				messageID = strings.TrimSpace(line[len("message-id:"):])
			}
		}
		return subject, strings.Trim(messageID, "<>")
	}
	subject = decodeMailHeaderValue(message.Header.Get("Subject"))
	messageID = strings.Trim(strings.TrimSpace(message.Header.Get("Message-Id")), "<>")
	return subject, messageID
}

func splitRawMessage(raw string) (headers, body string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if index := strings.Index(raw, "\n\n"); index >= 0 {
		return strings.TrimRight(raw[:index], "\n"), strings.TrimLeft(raw[index+2:], "\n")
	}
	return strings.TrimRight(raw, "\n"), ""
}

func normalizePostcatSource(output string) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	if index := strings.Index(output, "*** ENVELOPE RECORDS ***"); index >= 0 {
		output = output[index+len("*** ENVELOPE RECORDS ***"):]
	}
	lines := strings.Split(output, "\n")
	headerNames := []string{"return-path:", "delivered-to:", "received:", "from:", "to:", "subject:", "date:", "message-id:", "mime-version:", "content-type:"}
	for index, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		for _, prefix := range headerNames {
			if strings.HasPrefix(lower, prefix) {
				return strings.TrimLeft(strings.Join(lines[index:], "\n"), "\n")
			}
		}
	}
	return strings.TrimLeft(output, "\n")
}

func safeMaildirCandidate(root, address string) (string, bool) {
	email := normalizeEmailAddress(address)
	local, domain := splitMailAddress(email)
	if local == "" || domain == "" || !validDomain(normalizeDomain(domain)) {
		return "", false
	}
	if strings.ContainsAny(local, `/\`) || local == "." || local == ".." || strings.ContainsRune(local, 0) {
		return "", false
	}
	base := filepath.Join(root, normalizeDomain(domain), local, "Maildir")
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	cleanBase := filepath.Clean(base)
	if !strings.HasPrefix(cleanBase, cleanRoot) {
		return "", false
	}
	if info, err := os.Stat(cleanBase); err != nil || !info.IsDir() {
		return "", false
	}
	return cleanBase, true
}

func normalizeEmailAddress(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func splitMailAddress(value string) (string, string) {
	value = normalizeEmailAddress(value)
	index := strings.LastIndex(value, "@")
	if index <= 0 || index == len(value)-1 {
		return "", ""
	}
	return value[:index], value[index+1:]
}

func (s *Server) findMaildirMessageSource(ctx context.Context, event mailEventSourceEvent) (string, string, bool) {
	type sourceCandidate struct {
		address string
		folders []string
	}
	candidates := []sourceCandidate{}
	if event.Recipient != "" {
		candidates = append(candidates, sourceCandidate{address: event.Recipient, folders: []string{
			"new", "cur", filepath.Join(".Junk", "new"), filepath.Join(".Junk", "cur"),
		}})
	}
	// Only an outbound event may read the sender's mailbox. An inbound message
	// can carry a spoofed From address that matches a local mailbox; searching
	// it would leak an unrelated user's message into the mail log. The sent
	// folder is the only relevant copy for an outbound event.
	if event.Direction == "outbound" && event.Sender != "" && !strings.EqualFold(event.Sender, event.Recipient) {
		candidates = append(candidates, sourceCandidate{address: event.Sender, folders: []string{
			filepath.Join(".Sent", "new"), filepath.Join(".Sent", "cur"),
			filepath.Join(".Junk", "new"), filepath.Join(".Junk", "cur"),
		}})
	}
	type recentCandidate struct {
		path, label string
		modTime     time.Time
	}
	recent := []recentCandidate{}
	for _, candidate := range candidates {
		maildir, ok := safeMaildirCandidate(s.cfg.VmailRoot, candidate.address)
		if !ok {
			continue
		}
		for _, folder := range candidate.folders {
			select {
			case <-ctx.Done():
				return "", "", false
			default:
			}
			directory := filepath.Join(maildir, folder)
			entries, err := os.ReadDir(directory)
			if err != nil {
				continue
			}
			type candidateFile struct {
				path    string
				modTime time.Time
			}
			files := make([]candidateFile, 0, len(entries))
			for _, entry := range entries {
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				info, err := entry.Info()
				if err != nil || info.Size() == 0 {
					continue
				}
				// The delivery event and Maildir write normally happen within
				// seconds. Allow a generous window for delayed deliveries.
				if delta := info.ModTime().Sub(event.EventAt); delta > 30*time.Minute || delta < -2*time.Hour {
					continue
				}
				files = append(files, candidateFile{path: filepath.Join(directory, entry.Name()), modTime: info.ModTime()})
			}
			sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
			if len(files) > 300 {
				files = files[:300]
			}
			for _, file := range files {
				content, err := os.Open(file.path)
				if err != nil {
					continue
				}
				head, err := io.ReadAll(io.LimitReader(content, 128<<10))
				_ = content.Close()
				if err != nil {
					continue
				}
				if event.QueueID != "" && !bytes.Contains(head, []byte(event.QueueID)) {
					// Sent-mail copies usually do not contain the Postfix queue
					// ID. Keep a unique, time-adjacent candidate so an outbound
					// event can still show its delivered message.
					if delta := file.modTime.Sub(event.EventAt); delta <= 2*time.Minute && delta >= -2*time.Minute {
						recent = append(recent, recentCandidate{path: file.path, label: "Maildir " + folder, modTime: file.modTime})
					}
					continue
				}
				opened, err := os.Open(file.path)
				if err != nil {
					continue
				}
				raw, err := io.ReadAll(io.LimitReader(opened, maxMailSourceBytes+1))
				_ = opened.Close()
				if err != nil {
					continue
				}
				truncated := len(raw) > maxMailSourceBytes
				source, truncated := sanitizeMessageSource(raw, truncated)
				return source, "Maildir " + folder, truncated
			}
		}
	}
	if len(recent) == 1 {
		opened, err := os.Open(recent[0].path)
		if err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(opened, maxMailSourceBytes+1))
			_ = opened.Close()
			if readErr == nil {
				truncated := len(raw) > maxMailSourceBytes
				source, truncated := sanitizeMessageSource(raw, truncated)
				return source, recent[0].label, truncated
			}
		}
	}
	return "", "", false
}

func (s *Server) readPostfixQueueSource(ctx context.Context, queueID string) (string, bool) {
	if queueID == "" {
		return "", false
	}
	queueCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result, err := runCommand(queueCtx, 7*time.Second, "postcat", "-q", queueID)
	if err != nil || strings.TrimSpace(result.Stdout) == "" {
		return "", false
	}
	source := normalizePostcatSource(result.Stdout)
	truncated := len(source) > maxMailSourceBytes
	if truncated {
		source = source[:maxMailSourceBytes]
	}
	source, truncated = sanitizeMessageSource([]byte(source), truncated)
	if strings.TrimSpace(source) == "" {
		return "", false
	}
	return source, truncated
}

// enrichMailEventSource locates the delivered message and caches a bounded raw
// copy plus the real Subject header. The lookup is best-effort: Postfix may
// have already removed the queue file and a recipient may have deleted the
// Maildir copy.
func (s *Server) enrichMailEventSource(ctx context.Context, id int64) error {
	var event mailEventSourceEvent
	var existing string
	err := s.db.QueryRow(ctx, `
		SELECT id,queue_id,COALESCE(sender::text,''),COALESCE(recipient::text,''),
		       COALESCE(domain::text,''),direction,event_at,status,raw_message
		FROM control.mail_events WHERE id=$1
	`, id).Scan(&event.ID, &event.QueueID, &event.Sender, &event.Recipient, &event.Domain,
		&event.Direction, &event.EventAt, &event.Status, &existing)
	if err != nil {
		return err
	}
	if strings.TrimSpace(existing) != "" {
		return nil
	}
	source, sourceLabel, truncated := "", "", false
	if event.Status == "deferred" || event.Status == "expired" {
		source, truncated = s.readPostfixQueueSource(ctx, event.QueueID)
		if source != "" {
			sourceLabel = "Postfix queue"
		}
	}
	if source == "" {
		source, sourceLabel, truncated = s.findMaildirMessageSource(ctx, event)
	}
	if source == "" {
		return fmt.Errorf("message source is no longer available")
	}
	subject, messageID := parseMessageSourceHeaders(source)
	_, err = s.db.Exec(ctx, `
		UPDATE control.mail_events SET
		  subject=CASE WHEN $2 <> '' THEN $2 ELSE subject END,
		  message_id=CASE WHEN $3 <> '' THEN $3 ELSE message_id END,
		  raw_message=$4, raw_message_truncated=$5, message_source=$6, message_enriched_at=now()
		WHERE id=$1 AND raw_message=''
	`, id, subject, messageID, source, truncated, sourceLabel)
	return err
}
