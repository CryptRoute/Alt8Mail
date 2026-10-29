package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var queuePrefixPattern = regexp.MustCompile(`(?:[A-Za-z0-9_./-]+\[[0-9]+\]: )?([A-F0-9]{5,20}): `)
var fromPattern = regexp.MustCompile(`from=<([^>]*)>`)
var toPattern = regexp.MustCompile(`to=<([^>]*)>`)
var sizePattern = regexp.MustCompile(`\bsize=([0-9]+)`)
var delayPattern = regexp.MustCompile(`\bdelay=([0-9.]+)`)
var relayPattern = regexp.MustCompile(`\brelay=([^,]+)`)
var statusPattern = regexp.MustCompile(`\bstatus=([a-z]+)`)
var dsnPattern = regexp.MustCompile(`\bdsn=([0-9.]+)`)

type mailEvent struct {
	ID        int64
	EventAt   time.Time
	Source    string
	QueueID   string
	Sender    string
	Recipient string
	Domain    string
	Direction string
	Status    string
	SizeBytes int64
	Delay     int
	Relay     string
	Subject   string
	Detail    string
	Raw       string
}

type logIngestor struct {
	server *Server
	mu     sync.Mutex
	offset int64
	inode  uint64
	known  map[string]logQueueContext
	stop   chan struct{}
}

type logQueueContext struct {
	Sender    string
	Size      int64
	CreatedAt time.Time
}

func newLogIngestor(s *Server) *logIngestor {
	return &logIngestor{server: s, known: map[string]logQueueContext{}, stop: make(chan struct{})}
}

func (l *logIngestor) Start(ctx context.Context) {
	go l.run(ctx)
}

func (l *logIngestor) run(ctx context.Context) {
	ticker := time.NewTicker(l.server.cfg.IngestInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stop:
			return
		case <-ticker.C:
			source := l.server.settingString(ctx, "logs.ingest_source", "mail.log")
			if source == "journald" {
				if err := l.ingestJournaldOnce(ctx); err != nil {
					l.server.logger.Warn("journald ingestion failed", "err", err)
				}
			} else {
				if err := l.ingestOnce(ctx); err != nil {
					l.server.logger.Warn("mail.log ingestion failed", "err", err)
				}
			}
		}
	}
}

func parseLogTime(line string, now time.Time) time.Time {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return now
	}
	raw := strings.Join(fields[:3], " ")
	parsed, err := time.ParseInLocation("Jan _2 15:04:05", raw, now.Location())
	if err != nil {
		return now
	}
	parsed = time.Date(now.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, now.Location())
	if parsed.After(now.Add(24 * time.Hour)) {
		parsed = parsed.AddDate(-1, 0, 0)
	}
	return parsed
}

func extractMatch(pattern *regexp.Regexp, line string) string {
	match := pattern.FindStringSubmatch(line)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func (s *Server) localDomainSet(ctx context.Context) map[string]bool {
	domains := map[string]bool{}
	rows, err := s.db.Query(ctx, `SELECT lower(domain::text) FROM public.mail_domains`)
	if err != nil {
		return domains
	}
	defer rows.Close()
	for rows.Next() {
		var domain string
		if rows.Scan(&domain) == nil {
			domains[domain] = true
		}
	}
	return domains
}

func emailDomain(address string) string {
	if index := strings.LastIndex(address, "@"); index >= 0 {
		return strings.ToLower(strings.TrimSuffix(address[index+1:], "."))
	}
	return ""
}

func (l *logIngestor) ingestOnce(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Stat(l.server.cfg.MailLogPath)
	if err != nil {
		return nil
	}
	inode := fileInode(info)
	if l.offset == 0 && l.inode == 0 {
		offset := l.server.int64Setting(ctx, "logs.mail_offset", 0)
		storedInode := l.server.int64Setting(ctx, "logs.mail_inode", 0)
		if uint64(storedInode) == inode && offset <= info.Size() {
			l.offset = offset
			l.inode = inode
		}
	}
	if l.inode != inode || info.Size() < l.offset {
		l.offset = 0
		l.inode = inode
	}
	if info.Size()-l.offset > 8<<20 {
		l.offset = info.Size() - 8<<20
	}
	file, err := os.Open(l.server.cfg.MailLogPath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(l.offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 1<<20)
	domains := l.server.localDomainSet(ctx)
	now := time.Now()
	count := 0
	advanced := false
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			l.offset += int64(len(line))
			advanced = true
			line = strings.TrimRight(line, "\r\n")
			if event, ok := l.parseLine(ctx, line, domains, now, "mail.log"); ok {
				l.insert(ctx, event)
				count++
			}
		}
		if err != nil {
			break
		}
	}
	if advanced {
		_ = l.server.setSetting(ctx, "logs.mail_offset", l.offset, "")
		_ = l.server.setSetting(ctx, "logs.mail_inode", l.inode, "")
	}
	return nil
}

func (l *logIngestor) parseLine(ctx context.Context, line string, domains map[string]bool, now time.Time, source string) (mailEvent, bool) {
	queueMatch := queuePrefixPattern.FindStringSubmatch(line)
	if len(queueMatch) < 2 {
		return mailEvent{}, false
	}
	queueID := queueMatch[1]
	eventAt := parseLogTime(line, now)
	sender := extractMatch(fromPattern, line)
	recipient := extractMatch(toPattern, line)
	size := int64(0)
	if value := extractMatch(sizePattern, line); value != "" {
		size, _ = strconv.ParseInt(value, 10, 64)
	}
	if strings.Contains(line, "postfix/qmgr") && sender != "" {
		l.known[queueID] = logQueueContext{Sender: sender, Size: size, CreatedAt: eventAt}
		return mailEvent{}, false
	}
	if recipient == "" {
		if strings.Contains(line, "reject:") || strings.Contains(line, "NOQUEUE:") {
			recipient = extractMatch(toPattern, line)
		}
		if recipient == "" {
			return mailEvent{}, false
		}
	}
	context := l.known[queueID]
	if context.Sender == "" {
		context.Sender = sender
	}
	if context.Size == 0 {
		context.Size = size
	}
	status := "sent"
	if value := extractMatch(statusPattern, line); value != "" {
		status = value
		if status == "expired" {
			status = "expired"
		}
	}
	if strings.Contains(line, "reject:") {
		status = "rejected"
	}
	if status != "sent" && status != "deferred" && status != "bounced" && status != "expired" && status != "rejected" && status != "received" {
		status = "sent"
	}
	delay := 0
	if value := extractMatch(delayPattern, line); value != "" {
		delayFloat, _ := strconv.ParseFloat(value, 64)
		delay = int(delayFloat)
	}
	relay := extractMatch(relayPattern, line)
	senderDomain := emailDomain(context.Sender)
	recipientDomain := emailDomain(recipient)
	direction := "unknown"
	senderLocal := domains[senderDomain]
	recipientLocal := domains[recipientDomain]
	switch {
	case senderLocal && recipientLocal:
		direction = "internal"
	case senderLocal:
		direction = "outbound"
	case recipientLocal:
		direction = "inbound"
	}
	if strings.Contains(line, "postfix/smtp") && senderLocal {
		direction = "outbound"
	}
	if dsn := extractMatch(dsnPattern, line); strings.HasPrefix(dsn, "5.") && status == "sent" {
		status = "bounced"
	}
	detail := ""
	if strings.Contains(line, "status=") || strings.Contains(line, "reject:") {
		detail = truncateText(line, 2000)
	}
	return mailEvent{
		EventAt: eventAt, Source: source, QueueID: queueID, Sender: context.Sender,
		Recipient: recipient, Domain: recipientDomain, Direction: direction, Status: status,
		SizeBytes: context.Size, Delay: delay, Relay: relay, Detail: detail, Raw: truncateText(line, 8000),
	}, true
}

func (l *logIngestor) insert(ctx context.Context, event mailEvent) {
	var id int64
	err := l.server.db.QueryRow(ctx, `
		INSERT INTO control.mail_events (event_at, source, queue_id, sender, recipient, domain,
		  direction, status, size_bytes, delay_seconds, relay, subject, detail, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id
	`, event.EventAt, event.Source, event.QueueID, event.Sender, event.Recipient, event.Domain,
		event.Direction, event.Status, event.SizeBytes, event.Delay, event.Relay, event.Subject,
		event.Detail, event.Raw).Scan(&id)
	if err != nil {
		return
	}
	l.server.hub.Broadcast("mail.event", map[string]any{
		"id": id, "at": event.EventAt.UTC().Format(time.RFC3339), "queueId": event.QueueID,
		"status": event.Status, "direction": event.Direction, "sender": event.Sender,
		"recipient": event.Recipient,
	})
}

func (s *Server) int64Setting(ctx context.Context, key string, fallback int64) int64 {
	raw, err := s.settingRaw(ctx, key)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var value int64
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return fallback
}

func (s *Server) registerLogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/logs", s.require("logs.read", s.handleLogs))
	mux.HandleFunc("GET /control/api/logs/{id}", s.require("logs.read", s.handleLogDetail))
	mux.HandleFunc("GET /control/api/logs/stream", s.require("logs.read", s.handleSSE))
	mux.HandleFunc("POST /control/api/logs/prune", s.require("settings.write", s.handleLogPrune))
	mux.HandleFunc("GET /control/api/logs/settings", s.require("logs.read", s.handleLogSettings))
	mux.HandleFunc("PUT /control/api/logs/settings", s.require("settings.write", s.handleLogSettingsSave))
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	limit := queryInt(r, "limit", 100, 1, 500)
	page := queryInt(r, "page", 1, 1, 10000)
	offset := (page - 1) * limit
	rows, err := s.db.Query(r.Context(), `
		SELECT id,event_at,source,queue_id,sender::text,recipient::text,domain::text,direction,
		       status,size_bytes,delay_seconds,relay,subject,detail,raw
		FROM control.mail_events
		WHERE ($1='' OR raw ILIKE '%'||$1||'%' OR sender::text ILIKE '%'||$1||'%' OR recipient::text ILIKE '%'||$1||'%' OR queue_id ILIKE '%'||$1||'%')
		  AND ($2='' OR source=$2) AND ($3='' OR status=$3) AND ($4='' OR direction=$4)
		ORDER BY event_at DESC, id DESC LIMIT $5 OFFSET $6
	`, q, source, status, direction, limit, offset)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var at time.Time
		var src, queueID, sender, recipient, domain, dir, state, relay, subject, detail, raw string
		var size int64
		var delay int
		if rows.Scan(&id, &at, &src, &queueID, &sender, &recipient, &domain, &dir, &state, &size, &delay, &relay, &subject, &detail, &raw) == nil {
			items = append(items, map[string]any{
				"id": id, "at": at.UTC().Format(time.RFC3339), "source": src, "queueId": queueID,
				"sender": sender, "recipient": recipient, "domain": domain, "direction": dir,
				"status": state, "sizeBytes": size, "delaySeconds": delay, "relay": relay,
				"subject": subject, "detail": detail, "raw": raw,
			})
		}
	}
	var total int64
	_ = s.db.QueryRow(r.Context(), `
		SELECT count(*) FROM control.mail_events
		WHERE ($1='' OR raw ILIKE '%'||$1||'%' OR sender::text ILIKE '%'||$1||'%' OR recipient::text ILIKE '%'||$1||'%' OR queue_id ILIKE '%'||$1||'%')
		  AND ($2='' OR source=$2) AND ($3='' OR status=$3) AND ($4='' OR direction=$4)
	`, q, source, status, direction).Scan(&total)
	writeJSON(w, http.StatusOK, map[string]any{"events": items, "total": total, "page": page, "limit": limit,
		"retentionDays": s.logRetentionDays(r.Context())})
}

func (s *Server) logRetentionDays(ctx context.Context) int {
	days := s.settingInt(ctx, "logs.retention_days", s.cfg.RetentionDays)
	if days < 1 || days > 3650 {
		days = 365
	}
	return days
}

func (s *Server) handleLogDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id, _ := strconv.ParseInt(pathID(r), 10, 64)
	var at time.Time
	var src, queueID, sender, recipient, domain, direction, status, relay, subject, detail, raw string
	var size int64
	var delay int
	err := s.db.QueryRow(r.Context(), `
		SELECT event_at,source,queue_id,sender::text,recipient::text,domain::text,direction,status,
		       size_bytes,delay_seconds,relay,subject,detail,raw
		FROM control.mail_events WHERE id=$1
	`, id).Scan(&at, &src, &queueID, &sender, &recipient, &domain, &direction, &status, &size, &delay, &relay, &subject, &detail, &raw)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "at": at.UTC().Format(time.RFC3339), "source": src,
		"queueId": queueID, "sender": sender, "recipient": recipient, "domain": domain,
		"direction": direction, "status": status, "sizeBytes": size, "delaySeconds": delay,
		"relay": relay, "subject": subject, "detail": detail, "raw": raw})
}

func (s *Server) handleLogPrune(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	days := s.logRetentionDays(r.Context())
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.mail_events WHERE event_at < now() - ($1::int || ' days')::interval`, days)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "logs.pruned", "Mail event log", fmt.Sprintf("Pruned events older than %d days", days), map[string]any{"deleted": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": tag.RowsAffected(), "retentionDays": days})
}

func (s *Server) pruneEvents(ctx context.Context) {
	days := s.logRetentionDays(ctx)
	_, _ = s.db.Exec(ctx, `DELETE FROM control.mail_events WHERE event_at < now() - ($1::int || ' days')::interval`, days)
}

func (l *logIngestor) ingestJournaldOnce(ctx context.Context) error {
	since := l.server.settingString(ctx, "logs.journal_since", time.Now().UTC().Add(-15*time.Minute).Format(time.RFC3339Nano))
	sinceTime, parseErr := time.Parse(time.RFC3339Nano, since)
	if parseErr != nil {
		sinceTime = time.Now().UTC().Add(-15 * time.Minute)
	}
	// journalctl 252 (Debian 12) does not accept RFC3339 with a trailing Z or
	// +0000 offset. A fractional Unix epoch is accepted by older and newer
	// systemd releases and keeps microsecond precision.
	sinceArg := fmt.Sprintf("@%d.%06d", sinceTime.Unix(), sinceTime.Nanosecond()/1000)
	result, err := runCommand(ctx, 30*time.Second, "journalctl",
		"-u", "postfix@-.service", "-u", "postfix.service", "-u", "dovecot", "-u", "rspamd",
		"--since", sinceArg, "--no-pager", "-o", "short-iso", "--utc")
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("journalctl: %s", detail)
	}
	domains := l.server.localDomainSet(ctx)
	latest := time.Now().UTC()
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-- ") {
			continue
		}
		eventAt := time.Now()
		if fields := strings.Fields(line); len(fields) > 0 {
			if parsed, err := time.Parse(time.RFC3339, fields[0]); err == nil {
				eventAt = parsed
				latest = parsed
			} else if parsed, err := time.Parse("2006-01-02T15:04:05-0700", fields[0]); err == nil {
				eventAt = parsed
				latest = parsed
			}
		}
		if event, ok := l.parseLine(ctx, line, domains, eventAt, "journald"); ok {
			l.insert(ctx, event)
		}
	}
	_ = l.server.setSetting(ctx, "logs.journal_since", latest.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano), "")
	return nil
}

func (s *Server) handleLogSettings(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	writeJSON(w, http.StatusOK, map[string]any{
		"source":        s.settingString(r.Context(), "logs.ingest_source", "mail.log"),
		"retentionDays": s.logRetentionDays(r.Context()),
		"mailLog":       s.cfg.MailLogPath,
	})
}

func (s *Server) handleLogSettingsSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Source        string `json:"source"`
		RetentionDays int    `json:"retentionDays"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Source != "mail.log" && body.Source != "journald" {
		writeError(w, http.StatusBadRequest, "SOURCE", "Source must be mail.log or journald")
		return
	}
	if body.RetentionDays < 1 || body.RetentionDays > 3650 {
		writeError(w, http.StatusBadRequest, "RETENTION", "Retention must be between 1 and 3650 days")
		return
	}
	_ = s.setSetting(r.Context(), "logs.ingest_source", body.Source, sess.User.ID)
	_ = s.setSetting(r.Context(), "logs.retention_days", body.RetentionDays, sess.User.ID)
	s.actorAudit(r, sess, "logs.settings_updated", "Mail event ingestion", "Updated log source and retention",
		map[string]any{"source": body.Source, "retentionDays": body.RetentionDays})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
