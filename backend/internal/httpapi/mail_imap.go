package httpapi

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

const (
	// A full sync is incremental and budgeted. Bodies are fetched only for UIDs
	// the API has not imported yet; each pass imports the newest missing messages
	// first and records how many remain. This lets a very large mailbox converge
	// over successive passes instead of being permanently capped at the newest
	// 1,000 messages.
	syncBodyBatchSize         = 50
	syncBodyBudget            = 45 * time.Second
	quickSyncBodyBudget       = 15 * time.Second
	flagReconcileInterval     = 15 * time.Minute
	flagFetchBatchSize        = 500
	maxFlagReconcilePerFolder = 20000
	maxBodyPartBytes          = 4 * 1024 * 1024
	maxAttachmentSize         = 20 * 1024 * 1024
)

var standardIMAPFolders = []string{"INBOX", "Sent", "Drafts", "Archive", "Junk", "Trash"}

type fetchedMail struct {
	UID          uint32
	UIDValidity  uint32
	Folder       string
	Flags        []string
	Size         uint32
	InternalDate time.Time
	Date         time.Time
	Subject      string
	MessageID    string
	InReplyTo    string
	References   []string
	From         []map[string]any
	To           []map[string]any
	CC           []map[string]any
	BCC          []map[string]any
	BodyText     string
	BodyHTML     string
	Attachments  []parsedMailAttachment
	Auth         map[string]any
	External     bool
}

type parsedMailAttachment struct {
	Name        string
	MIMEType    string
	ContentID   string
	Disposition string
	Data        []byte
}

func (s *Server) mailTLSConfig(serverName string) (*tls.Config, error) {
	if s.cfg.MailCAFile == "" {
		return nil, fmt.Errorf("MERIDIAN_MAIL_CA_FILE is not configured")
	}
	pemData, err := os.ReadFile(s.cfg.MailCAFile)
	if err != nil {
		return nil, fmt.Errorf("read mail CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("mail CA file contains no certificates")
	}
	return &tls.Config{
		RootCAs:    roots,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}, nil
}

func (s *Server) mailMasterConfigured() bool {
	return s.cfg.IMAPMasterUser != "" && s.cfg.IMAPMasterPassword != ""
}

func (s *Server) mailSyncConfigured() bool {
	return s.mailMasterConfigured() || (s.cfg.IMAPUser != "" && s.cfg.IMAPPassword != "")
}

func (s *Server) mailAccountConfigured(email string) bool {
	if s.mailMasterConfigured() {
		return email != ""
	}
	return email != "" && strings.EqualFold(email, s.cfg.IMAPUser) && s.cfg.IMAPPassword != ""
}

func (s *Server) mailLoginForUser(email string) (string, string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", "", fmt.Errorf("mailbox has no identity")
	}
	if s.mailMasterConfigured() {
		target := email
		var canonical string
		if err := s.store.DB.QueryRow(context.Background(), `
			SELECT u.email::text FROM user_aliases a JOIN users u ON u.id = a.user_id
			WHERE lower(a.email::text) = lower($1) AND a.active = true
			LIMIT 1
		`, email).Scan(&canonical); err == nil && canonical != "" {
			target = canonical
		}
		return target + "*" + s.cfg.IMAPMasterUser, s.cfg.IMAPMasterPassword, nil
	}
	if strings.EqualFold(email, s.cfg.IMAPUser) && s.cfg.IMAPPassword != "" {
		return s.cfg.IMAPUser, s.cfg.IMAPPassword, nil
	}
	return "", "", fmt.Errorf("no mailbox credentials are configured for %s", email)
}

func (s *Server) activeMailUsers(ctx context.Context) ([]userRecord, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT DISTINCT u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color,
		       u.role, u.status, u.preferences, u.security, u.quota_bytes, u.used_bytes,
		       u.enrollment_required, u.force_password_change, u.password_expires_at
		FROM users u
		JOIN mailboxes m ON m.user_id = u.id
		JOIN tenants t ON t.id = u.tenant_id
		WHERE m.active = true AND u.status = 'active' AND u.deleted_at IS NULL AND t.status = 'active'
		ORDER BY u.email::text
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []userRecord{}
	for rows.Next() {
		var user userRecord
		if err := rows.Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color,
			&user.Role, &user.Status, &user.Preferences, &user.Security, &user.QuotaBytes, &user.UsedBytes,
			&user.EnrollmentRequired, &user.ForcePasswordChange, &user.PasswordExpiresAt); err == nil {
			users = append(users, user)
		}
	}
	return users, rows.Err()
}

func (s *Server) syncMailbox(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.mailAccountConfigured(sess.User.Email) {
		writeError(w, http.StatusConflict, "MAIL_NOT_CONFIGURED", "No IMAP mailbox is connected for this account")
		return
	}
	result, err := s.syncUserMailbox(r.Context(), sess.User)
	if err != nil {
		s.logger.Warn("mail sync failed", "user", sess.User.Email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok": false, "error": map[string]any{"code": "MAIL_SYNC", "message": err.Error()}, "sync": result,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync": result})
}

func (s *Server) mailSyncStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"configured": s.mailAccountConfigured(sess.User.Email), "sync": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.mailAccountConfigured(sess.User.Email),
		"account":    sess.User.Email, "sync": stateMap(state, "mailSync"),
	})
}

// checkNewInboxForUser performs a cheap UID-based INBOX check between the full
// mailbox scans. It fetches only messages above the last known UID, so a new
// message reaches the API and the open webmail within the poll interval without
// re-reading every body in every folder. The per-user worker calls this method.
func (s *Server) checkNewInboxForUser(ctx context.Context, user userRecord) (int, error) {
	lock := s.userMailLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return 0, err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return 0, fmt.Errorf("connect to IMAP: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return 0, err
	}
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return 0, fmt.Errorf("IMAP login: %w", err)
	}
	status, err := conn.Select("INBOX", true)
	if err != nil {
		return 0, fmt.Errorf("select INBOX: %w", err)
	}
	state, err := s.loadState(ctx, user.ID)
	if err != nil {
		if stateErr := s.ensureUserState(ctx, user.Email); stateErr != nil {
			return 0, fmt.Errorf("load mailbox state: %w", err)
		}
		state, err = s.loadState(ctx, user.ID)
		if err != nil {
			return 0, fmt.Errorf("load mailbox state: %w", err)
		}
	}
	syncMeta := stateMap(state, "mailSync")
	folderMeta := stateMap(syncMeta, "folders")
	inboxMeta := stateMap(folderMeta, "inbox")
	lastUID := stateInt(inboxMeta, "lastUid")
	validityChanged := stateInt(inboxMeta, "uidValidity") != 0 &&
		stateInt(inboxMeta, "uidValidity") != int64(status.UidValidity)
	if stateInt(inboxMeta, "uidValidity") != int64(status.UidValidity) {
		lastUID = 0
	}
	uids, err := imapUIDs(conn, lastUID)
	if err != nil {
		return 0, fmt.Errorf("search new INBOX mail: %w", err)
	}
	if len(uids) == 0 {
		return 0, nil
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
	knownBefore := s.countMailMessages(ctx, user.ID, "inbox", int64(status.UidValidity))
	fetched := []fetchedMail{}
	deadline := time.Now().Add(quickSyncBodyBudget)
	for start := 0; start < len(uids) && time.Now().Before(deadline); {
		end := start + syncBodyBatchSize
		if end > len(uids) {
			end = len(uids)
		}
		batch := uids[start:end]
		messages, _, err := fetchIMAPMessages(conn, batch, "inbox", status.UidValidity)
		if err != nil {
			return 0, fmt.Errorf("fetch new INBOX mail: %w", err)
		}
		fetched = append(fetched, messages...)
		start = end
	}
	highest := lastUID
	for _, uid := range uids {
		if int64(uid) > highest {
			highest = int64(uid)
		}
	}
	pending := int(status.Messages) - knownBefore - len(fetched)
	if pending < 0 {
		pending = 0
	}
	imported := 0
	err = s.withMailState(ctx, user, nil, true, func(current map[string]any) error {
		resetFolders := map[string]bool{}
		if validityChanged {
			resetFolders["inbox"] = true
		}
		imported, _, _ = mergeFetchedMailAdvanced(current, user, fetched, s.cfg.UploadDir, nil, nil, nil, resetFolders)
		meta := stateMap(current, "mailSync")
		folders := stateMap(meta, "folders")
		if folders == nil {
			folders = map[string]any{}
		}
		otherPending := 0
		for name, raw := range folders {
			if name == "inbox" {
				continue
			}
			if entry, ok := raw.(map[string]any); ok {
				otherPending += int(stateInt(entry, "pending"))
			}
		}
		folders["inbox"] = map[string]any{
			"lastUid": highest, "uidValidity": status.UidValidity,
			"uidNext": status.UidNext, "messages": status.Messages, "unseen": status.Unseen,
			"pending": pending, "truncated": pending > 0,
		}
		meta["folders"] = folders
		meta["pending"] = pending + otherPending
		meta["lastNewMailAt"] = nowISO()
		meta["lastError"] = nil
		current["mailSync"] = meta
		if imported > 0 {
			appendStateAudit(current, "mail.received", fmt.Sprintf("%d new message", imported), user.Name,
				fmt.Sprintf("New INBOX mail · %d imported", imported), "127.0.0.1", "meridian-imap-worker")
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("save new INBOX mail: %w", err)
	}
	return imported, nil
}

func (s *Server) syncUserMailbox(ctx context.Context, user userRecord) (map[string]any, error) {
	lock := s.userMailLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		s.recordSyncFailure(ctx, user, err)
		return nil, err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		s.recordSyncFailure(ctx, user, err)
		return nil, fmt.Errorf("connect to IMAP: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		s.recordSyncFailure(ctx, user, err)
		return nil, err
	}
	if err := conn.Login(loginUser, loginPassword); err != nil {
		s.recordSyncFailure(ctx, user, err)
		return nil, fmt.Errorf("IMAP login: %w", err)
	}
	ensureIMAPFolders(conn)

	state, err := s.loadState(ctx, user.ID)
	if err != nil {
		if stateErr := s.ensureUserState(ctx, user.Email); stateErr != nil {
			return nil, fmt.Errorf("load mailbox state: %w", err)
		}
		state, err = s.loadState(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("load mailbox state: %w", err)
		}
	}
	ensureStateCollections(state)
	syncMeta := stateMap(state, "mailSync")
	folderMeta, _ := syncMeta["folders"].(map[string]any)
	if folderMeta == nil {
		folderMeta = map[string]any{}
	}

	mailboxes, err := listIMAPMailboxes(conn)
	if err != nil {
		s.recordSyncFailure(ctx, user, err)
		return nil, err
	}
	fetched := []fetchedMail{}
	flagUpdates := []imapFlagUpdate{}
	folderCount := 0
	pendingTotal := 0
	coveredFolders := map[string]bool{}
	liveKeys := map[string]bool{}
	resetFolders := map[string]bool{}
	deadline := time.Now().Add(syncBodyBudget)
	remainingFolders := 0
	for _, mailbox := range mailboxes {
		if _, ok := mapIMAPFolder(mailbox); ok {
			remainingFolders++
		}
	}
	for _, mailbox := range mailboxes {
		appFolder, ok := mapIMAPFolder(mailbox)
		if !ok {
			continue
		}
		// Divide the remaining time fairly across folders still to scan. Each
		// folder always imports at least one batch, so a very large Archive or
		// INBOX cannot starve Sent, Drafts, Junk or Trash indefinitely.
		folderDeadline := time.Now()
		if remainingFolders > 0 {
			folderDeadline = time.Now().Add(time.Until(deadline) / time.Duration(remainingFolders))
		}
		remainingFolders--
		status, err := conn.Select(mailbox, true)
		if err != nil {
			s.logger.Warn("select mailbox", "mailbox", mailbox, "err", err)
			continue
		}
		folderCount++
		// The UID list is the authoritative set of messages in the folder. Fetch
		// it in full so deletions and moves made by other clients reconcile
		// exactly, regardless of mailbox size. Only bodies for missing UIDs are
		// downloaded, so the previous 1,000-message ceiling no longer applies.
		uids, err := imapUIDs(conn, 0)
		if err != nil {
			s.logger.Warn("list mailbox UIDs", "mailbox", mailbox, "err", err)
			continue
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		previous := stateMap(folderMeta, appFolder)
		previousValidity := stateInt(previous, "uidValidity")
		if previousValidity != 0 && previousValidity != int64(status.UidValidity) {
			// IMAP invalidated every UID in this folder. Drop the old local copies
			// and re-import from the new UID space.
			resetFolders[appFolder] = true
			s.logger.Info("mailbox UIDVALIDITY changed", "user", user.Email, "folder", appFolder,
				"old", previousValidity, "new", status.UidValidity)
		}
		known := s.loadFolderUIDs(ctx, user.ID, appFolder, int64(status.UidValidity))
		missing := make([]uint32, 0, len(uids))
		for _, uid := range uids {
			if !known[uid] {
				missing = append(missing, uid)
			}
		}
		// Newest messages are visible first. Older missing messages remain in
		// `pending` and are imported by later passes.
		firstBodyBatch := true
		for start := len(missing); start > 0; {
			if !firstBodyBatch && (time.Now().After(folderDeadline) || time.Now().After(deadline)) {
				break
			}
			end := start
			start -= syncBodyBatchSize
			if start < 0 {
				start = 0
			}
			batch := missing[start:end]
			messages, _, err := fetchIMAPMessages(conn, batch, appFolder, status.UidValidity)
			if err != nil {
				s.logger.Warn("fetch mailbox", "mailbox", mailbox, "err", err)
				break
			}
			firstBodyBatch = false
			fetched = append(fetched, messages...)
			for _, message := range messages {
				known[message.UID] = true
			}
		}
		pending := 0
		for _, uid := range uids {
			if !known[uid] {
				pending++
			}
		}
		pendingTotal += pending
		flagUIDs := make([]uint32, 0, len(known))
		for uid := range known {
			flagUIDs = append(flagUIDs, uid)
		}
		sort.Slice(flagUIDs, func(i, j int) bool { return flagUIDs[i] < flagUIDs[j] })
		if len(flagUIDs) > maxFlagReconcilePerFolder {
			flagUIDs = flagUIDs[len(flagUIDs)-maxFlagReconcilePerFolder:]
		}
		lastFlags, _ := time.Parse(time.RFC3339, stateString(previous, "lastFlagsSyncAt"))
		needFlags := previousValidity != int64(status.UidValidity) ||
			stateInt(previous, "uidNext") != int64(status.UidNext) ||
			stateInt(previous, "messages") != int64(status.Messages) ||
			lastFlags.IsZero() || time.Since(lastFlags) >= flagReconcileInterval
		if needFlags && len(flagUIDs) > 0 && time.Now().Before(deadline) {
			updates, err := fetchIMAPFlagUpdates(conn, flagUIDs, appFolder, status.UidValidity)
			if err != nil {
				s.logger.Warn("reconcile mailbox flags", "mailbox", mailbox, "err", err)
			} else {
				flagUpdates = append(flagUpdates, updates...)
				previous["lastFlagsSyncAt"] = nowISO()
			}
		}
		for _, uid := range uids {
			liveKeys[imapMessageKey(appFolder, int64(status.UidValidity), int64(uid))] = true
		}
		coveredFolders[appFolder] = true
		highestUID := int64(0)
		if len(uids) > 0 {
			highestUID = int64(uids[len(uids)-1])
		}
		previous["lastUid"] = highestUID
		previous["uidValidity"] = status.UidValidity
		previous["uidNext"] = status.UidNext
		previous["messages"] = status.Messages
		previous["unseen"] = status.Unseen
		previous["pending"] = pending
		previous["truncated"] = pending > 0
		previous["lastFullSyncAt"] = nowISO()
		folderMeta[appFolder] = previous
	}
	syncMeta["folders"] = folderMeta
	syncMeta["account"] = user.Email
	syncMeta["lastSyncAt"] = nowISO()
	syncMeta["lastError"] = nil
	syncMeta["folderCount"] = folderCount
	syncMeta["pending"] = pendingTotal
	syncMeta["truncated"] = pendingTotal > 0
	state["mailSync"] = syncMeta

	imported, updated, removed := 0, 0, 0
	err = s.withMailState(ctx, user, nil, true, func(current map[string]any) error {
		imported, updated, removed = mergeFetchedMailAdvanced(current, user, fetched, s.cfg.UploadDir,
			coveredFolders, liveKeys, flagUpdates, resetFolders)
		meta := stateMap(current, "mailSync")
		for key, value := range syncMeta {
			meta[key] = value
		}
		meta["imported"] = imported
		meta["updated"] = updated
		meta["removed"] = removed
		current["mailSync"] = meta
		if imported > 0 || removed > 0 {
			appendStateAudit(current, "mail.synced", fmt.Sprintf("%d new message", imported), user.Name,
				fmt.Sprintf("IMAP sync · %d imported · %d refreshed · %d removed · %d pending",
					imported, updated, removed, pendingTotal), "127.0.0.1", "meridian-imap-worker")
		}
		return nil
	})
	if err != nil {
		s.recordSyncFailure(ctx, user, err)
		return nil, fmt.Errorf("save mailbox state: %w", err)
	}
	return map[string]any{
		"account": user.Email, "lastSyncAt": nowISO(), "folderCount": folderCount,
		"imported": imported, "updated": updated, "removed": removed, "fetched": len(fetched),
		"pending": pendingTotal,
	}, nil
}

func (s *Server) recordSyncFailure(ctx context.Context, user userRecord, syncErr error) {
	_ = s.withState(user.ID, user.TenantID, func(state map[string]any) error {
		meta := stateMap(state, "mailSync")
		meta["account"] = user.Email
		meta["lastError"] = syncErr.Error()
		meta["lastAttemptAt"] = nowISO()
		state["mailSync"] = meta
		return nil
	})
}

func ensureIMAPFolders(conn *imapclient.Client) {
	for _, name := range standardIMAPFolders {
		if name == "INBOX" {
			continue
		}
		// CREATE returns an error when the mailbox already exists; it is safe to
		// ignore and keeps provisioning idempotent.
		_ = conn.Create(name)
	}
}

func hasIMAPFlag(flags []string, wanted string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, wanted) {
			return true
		}
	}
	return false
}

func listIMAPMailboxes(conn *imapclient.Client) ([]string, error) {
	channel := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- conn.List("", "*", channel) }()
	result := []string{}
	for mailbox := range channel {
		if mailbox != nil && mailbox.Name != "" {
			result = append(result, mailbox.Name)
		}
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("list IMAP folders: %w", err)
	}
	sort.Strings(result)
	return result, nil
}

func imapUIDs(conn *imapclient.Client, lastUID int64) ([]uint32, error) {
	criteria := imap.NewSearchCriteria()
	if lastUID > 0 {
		set := new(imap.SeqSet)
		set.AddRange(uint32(lastUID)+1, ^uint32(0))
		criteria.Uid = set
	}
	return conn.UidSearch(criteria)
}

type imapFlagUpdate struct {
	Folder       string
	UIDValidity  uint32
	UID          uint32
	Flags        []string
	Size         uint32
	InternalDate time.Time
}

// fetchIMAPFlagUpdates downloads only flags and metadata for existing messages.
// It is used to reconcile changes made by other mail clients without
// re-downloading message bodies.
func fetchIMAPFlagUpdates(conn *imapclient.Client, uids []uint32, folder string, uidValidity uint32) ([]imapFlagUpdate, error) {
	updates := []imapFlagUpdate{}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchRFC822Size, imap.FetchInternalDate}
	for start := 0; start < len(uids); start += flagFetchBatchSize {
		end := start + flagFetchBatchSize
		if end > len(uids) {
			end = len(uids)
		}
		set := new(imap.SeqSet)
		set.AddNum(uids[start:end]...)
		messages := make(chan *imap.Message, 32)
		done := make(chan error, 1)
		go func() { done <- conn.UidFetch(set, items, messages) }()
		for message := range messages {
			if message == nil {
				continue
			}
			updates = append(updates, imapFlagUpdate{
				Folder: folder, UIDValidity: uidValidity, UID: message.Uid,
				Flags: message.Flags, Size: message.Size, InternalDate: message.InternalDate,
			})
		}
		if err := <-done; err != nil {
			return updates, err
		}
	}
	return updates, nil
}

func fetchIMAPMessages(conn *imapclient.Client, uids []uint32, folder string, uidValidity uint32) ([]fetchedMail, int64, error) {
	if len(uids) == 0 {
		return nil, 0, nil
	}
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{
		imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate,
		imap.FetchRFC822Size, imap.FetchUid, section.FetchItem(),
	}
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	messages := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- conn.UidFetch(set, items, messages) }()
	result := []fetchedMail{}
	highest := int64(0)
	for message := range messages {
		if message == nil {
			continue
		}
		if hasIMAPFlag(message.Flags, `\Deleted`) {
			continue
		}
		raw, err := io.ReadAll(message.GetBody(section))
		if err != nil {
			continue
		}
		fetched := parseIMAPMessage(message, raw, folder)
		fetched.UIDValidity = uidValidity
		result = append(result, fetched)
		if int64(message.Uid) > highest {
			highest = int64(message.Uid)
		}
	}
	if err := <-done; err != nil {
		return nil, highest, fmt.Errorf("fetch messages: %w", err)
	}
	return result, highest, nil
}

func parseIMAPMessage(message *imap.Message, raw []byte, folder string) fetchedMail {
	fetched := fetchedMail{
		UID: message.Uid, Folder: folder, Flags: message.Flags, Size: message.Size,
		InternalDate: message.InternalDate, Auth: map[string]any{
			"spf": "none", "dkim": "none", "dmarc": "none", "tls": "TLS 1.3",
			"ip": "—", "signer": "", "firstContact": false, "warnings": []any{},
		},
	}
	if message.Envelope != nil {
		fetched.Date = message.Envelope.Date
		fetched.Subject = decodeHeader(message.Envelope.Subject)
		fetched.MessageID = strings.TrimSpace(message.Envelope.MessageId)
		fetched.InReplyTo = strings.TrimSpace(message.Envelope.InReplyTo)
		fetched.From = imapAddresses(message.Envelope.From)
		fetched.To = imapAddresses(message.Envelope.To)
		fetched.CC = imapAddresses(message.Envelope.Cc)
		fetched.BCC = imapAddresses(message.Envelope.Bcc)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err == nil {
		headers := textproto.MIMEHeader(parsed.Header)
		if fetched.Subject == "" {
			fetched.Subject = decodeHeader(parsed.Header.Get("Subject"))
		}
		if fetched.MessageID == "" {
			fetched.MessageID = strings.TrimSpace(parsed.Header.Get("Message-Id"))
		}
		if fetched.InReplyTo == "" {
			fetched.InReplyTo = strings.TrimSpace(parsed.Header.Get("In-Reply-To"))
		}
		if references := strings.Fields(parsed.Header.Get("References")); len(references) > 0 {
			fetched.References = references
		}
		if len(fetched.From) == 0 {
			if list, err := parsed.Header.AddressList("From"); err == nil {
				fetched.From = addressListToPeople(list)
			}
		}
		if len(fetched.To) == 0 {
			if list, err := parsed.Header.AddressList("To"); err == nil {
				fetched.To = addressListToPeople(list)
			}
		}
		if len(fetched.CC) == 0 {
			if list, err := parsed.Header.AddressList("Cc"); err == nil {
				fetched.CC = addressListToPeople(list)
			}
		}
		parsedBody := &parsedBody{}
		_ = walkMIMEPart(headers, parsed.Body, parsedBody, 0)
		fetched.BodyText = strings.ToValidUTF8(parsedBody.text.String(), "")
		fetched.BodyHTML = strings.ToValidUTF8(parsedBody.html.String(), "")
		fetched.Attachments = parsedBody.attachments
		applyAuthenticationResults(&fetched, parsed.Header.Get("Authentication-Results"))
	}
	if fetched.Date.IsZero() {
		fetched.Date = message.InternalDate
	}
	if fetched.Date.IsZero() {
		fetched.Date = time.Now().UTC()
	}
	if fetched.BodyHTML == "" && fetched.BodyText != "" {
		fetched.BodyHTML = textToHTML(fetched.BodyText)
	}
	if fetched.BodyText == "" && fetched.BodyHTML != "" {
		fetched.BodyText = stripHTMLToText(fetched.BodyHTML)
	}
	fetched.Subject = truncate(fetched.Subject, 500)
	if fetched.Subject == "" {
		fetched.Subject = "(no subject)"
	}
	return fetched
}

type parsedBody struct {
	text        strings.Builder
	html        strings.Builder
	attachments []parsedMailAttachment
}

func walkMIMEPart(headers textproto.MIMEHeader, body io.Reader, result *parsedBody, depth int) error {
	if depth > 10 {
		return nil
	}
	contentType := headers.Get("Content-Type")
	mediaType := "text/plain"
	params := map[string]string{}
	if contentType != "" {
		if parsedType, parsedParams, err := mime.ParseMediaType(contentType); err == nil {
			mediaType = strings.ToLower(parsedType)
			params = parsedParams
		}
	}
	transfer := strings.ToLower(headers.Get("Content-Transfer-Encoding"))
	var reader io.Reader = body
	switch transfer {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		reader = quotedprintable.NewReader(body)
	}
	disposition, dispositionParams, _ := mime.ParseMediaType(headers.Get("Content-Disposition"))
	fileName := dispositionParams["filename"]
	if fileName == "" {
		fileName = params["name"]
	}
	fileName = decodeHeader(strings.TrimSpace(fileName))
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(reader, maxAttachmentSize))
			return nil
		}
		multipartReader := multipart.NewReader(reader, boundary)
		for {
			part, err := multipartReader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			partHeaders := textproto.MIMEHeader(part.Header)
			if err := walkMIMEPart(partHeaders, part, result, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	isAttachment := strings.EqualFold(disposition, "attachment") || fileName != ""
	if isAttachment {
		data, _ := io.ReadAll(io.LimitReader(reader, maxAttachmentSize+1))
		if len(data) > maxAttachmentSize {
			return nil
		}
		name := safeFileName(fileName)
		if name == "" {
			name = "attachment"
		}
		if result.text.Len() == 0 && depth > 0 && strings.EqualFold(disposition, "inline") && strings.HasPrefix(mediaType, "image/") {
			// Inline images remain attachments; the HTML references them by CID.
		}
		result.attachments = append(result.attachments, parsedMailAttachment{
			Name: name, MIMEType: mediaType, ContentID: strings.Trim(headers.Get("Content-Id"), "<>"),
			Disposition: disposition, Data: data,
		})
		return nil
	}
	switch mediaType {
	case "text/plain":
		data, _ := io.ReadAll(io.LimitReader(reader, maxBodyPartBytes))
		if result.text.Len() == 0 {
			result.text.WriteString(strings.ToValidUTF8(string(data), ""))
		}
	case "text/html":
		data, _ := io.ReadAll(io.LimitReader(reader, maxBodyPartBytes))
		if result.html.Len() == 0 {
			result.html.WriteString(strings.ToValidUTF8(string(data), ""))
		}
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(reader, maxAttachmentSize))
	}
	return nil
}

func imapAddresses(addresses []*imap.Address) []map[string]any {
	result := []map[string]any{}
	for _, address := range addresses {
		if address == nil {
			continue
		}
		email := strings.ToLower(address.MailboxName + "@" + address.HostName)
		name := decodeHeader(address.PersonalName)
		result = append(result, personFor(nil, email, name))
	}
	return result
}

func addressListToPeople(addresses []*mail.Address) []map[string]any {
	result := []map[string]any{}
	for _, address := range addresses {
		if address == nil {
			continue
		}
		email := strings.ToLower(address.Address)
		result = append(result, personFor(nil, email, address.Name))
	}
	return result
}

var authResultPattern = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)\s*=\s*([a-z]+)`)
var dkimDomainPattern = regexp.MustCompile(`(?i)\bheader\.d\s*=\s*([A-Za-z0-9._-]+)`)

func applyAuthenticationResults(fetched *fetchedMail, header string) {
	if strings.TrimSpace(header) == "" {
		return
	}
	auth := fetched.Auth
	for _, match := range authResultPattern.FindAllStringSubmatch(header, -1) {
		auth[strings.ToLower(match[1])] = strings.ToLower(match[2])
	}
	if domain := dkimDomainPattern.FindStringSubmatch(header); len(domain) > 1 {
		auth["signer"] = domain[1]
	}
}

func mergeFetchedMail(state map[string]any, user userRecord, fetched []fetchedMail, uploadDir string, coveredFolders map[string]bool) (int, int, int) {
	return mergeFetchedMailAdvanced(state, user, fetched, uploadDir, coveredFolders, nil, nil, nil)
}

func mergeFetchedMailAdvanced(state map[string]any, user userRecord, fetched []fetchedMail, uploadDir string,
	coveredFolders map[string]bool, liveKeys map[string]bool, flagUpdates []imapFlagUpdate,
	resetFolders map[string]bool) (int, int, int) {
	imported, updated, removed := 0, 0, 0
	userDomain := domainOf(user.Email)
	if len(resetFolders) > 0 {
		kept := []any{}
		for _, raw := range stateMessages(state) {
			message, _ := raw.(map[string]any)
			if message != nil && resetFolders[stateString(message, "imapFolder")] {
				removed++
				continue
			}
			kept = append(kept, raw)
		}
		state["messages"] = kept
	}
	existingByUID := map[string]map[string]any{}
	existingByFolderMessageID := map[string]map[string]any{}
	existingByMessageID := map[string][]map[string]any{}
	for _, raw := range stateMessages(state) {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		messageID := messageIDKey(stateString(message, "messageId"))
		folder := stateString(message, "folder")
		if messageID != "" {
			existingByFolderMessageID[folder+"\x00"+messageID] = message
			existingByMessageID[messageID] = append(existingByMessageID[messageID], message)
		}
		if uid := stateInt(message, "imapUid"); uid > 0 {
			existingByUID[imapMessageKey(stateString(message, "imapFolder"), stateInt(message, "imapUidValidity"), uid)] = message
		}
	}
	// Flag-only reconciliation for messages that already exist locally.
	for _, update := range flagUpdates {
		existing := existingByUID[imapMessageKey(update.Folder, int64(update.UIDValidity), int64(update.UID))]
		if existing == nil {
			continue
		}
		unread := !hasIMAPFlag(update.Flags, `\Seen`)
		starred := hasIMAPFlag(update.Flags, `\Flagged`)
		changed := stateBool(existing, "unread") != unread || stateBool(existing, "starred") != starred
		if stateInt(existing, "sizeBytes") != int64(update.Size) {
			existing["sizeBytes"] = update.Size
			changed = true
		}
		if changed {
			existing["unread"] = unread
			existing["starred"] = starred
			existing["flagged"] = starred
			updated++
		}
	}

	fetchedKeys := map[string]bool{}
	for _, item := range fetched {
		messageID := messageIDKey(item.MessageID)
		if messageID != "" {
			fetchedKeys[item.Folder+"\x00"+messageID] = true
		}
	}
	seenUIDs := map[string]bool{}
	seenFolderMessageIDs := map[string]bool{}

	for _, item := range fetched {
		messageID := item.MessageID
		if messageID == "" {
			messageID = fmt.Sprintf("<imap-%d-%d@%s>", item.UIDValidity, item.UID, userDomain)
		}
		normalizedID := messageIDKey(messageID)
		folderMessageKey := item.Folder + "\x00" + normalizedID
		uidKey := imapMessageKey(item.Folder, int64(item.UIDValidity), int64(item.UID))
		seenUIDs[uidKey] = true
		seenFolderMessageIDs[folderMessageKey] = true

		// A Message-ID is not unique across mailboxes: a message sent to
		// yourself exists once in Sent and once in INBOX with the same
		// Message-ID. Match the physical IMAP copy first, then the folder-local
		// copy, and only then treat an absent copy as a move.
		existing := existingByUID[uidKey]
		if existing == nil {
			existing = existingByFolderMessageID[folderMessageKey]
		}
		if existing == nil && normalizedID != "" {
			moved := []map[string]any{}
			for _, candidate := range existingByMessageID[normalizedID] {
				candidateFolder := stateString(candidate, "folder")
				candidateKey := candidateFolder + "\x00" + messageIDKey(stateString(candidate, "messageId"))
				if coveredFolders[candidateFolder] && !fetchedKeys[candidateKey] {
					moved = append(moved, candidate)
				}
			}
			if len(moved) == 1 {
				existing = moved[0]
			}
		}

		unread := !hasIMAPFlag(item.Flags, `\Seen`)
		starred := hasIMAPFlag(item.Flags, `\Flagged`)
		if existing != nil {
			existing["folder"] = item.Folder
			existing["imapUid"] = item.UID
			existing["imapFolder"] = item.Folder
			existing["imapUidValidity"] = item.UIDValidity
			existing["unread"] = unread
			existing["starred"] = starred
			existing["flagged"] = starred
			existing["draft"] = item.Folder == "drafts"
			existing["date"] = item.Date.UTC().Format(time.RFC3339)
			existing["sizeBytes"] = item.Size
			existingByFolderMessageID[folderMessageKey] = existing
			updated++
			continue
		}
		root := messageID
		if len(item.References) > 0 {
			root = item.References[0]
		} else if item.InReplyTo != "" {
			root = item.InReplyTo
		}
		threadID := importedThreadID(existingByMessageID, normalizedID, item, root)
		// Message-ID is not globally unique: mail sent to several users, or
		// present in both Sent and INBOX, shares the same value. The normalized
		// table uses a global primary key, so scope the row id by user and IMAP
		// folder. Otherwise one recipient's import can own (and be overwritten
		// by) another recipient's message content.
		id := imapMessageID(user.ID, messageID, item.Folder)
		if findMessageIn(state, id) != nil {
			continue
		}
		from := map[string]any{"name": "Unknown sender", "email": "unknown@" + userDomain, "initials": "?", "color": "#64748b"}
		if len(item.From) > 0 {
			from = item.From[0]
		}
		toPeople := peopleToAny(item.To)
		ccPeople := peopleToAny(item.CC)
		bccPeople := peopleToAny(item.BCC)
		attachments := []any{}
		known, _ := state["attachments"].(map[string]any)
		if known == nil {
			known = map[string]any{}
		}
		for _, attachment := range item.Attachments {
			record, err := storeImportedAttachment(uploadDir, attachment, user)
			if err != nil {
				continue
			}
			known[stateString(record, "id")] = record
			attachments = append(attachments, publicAttachment(record))
		}
		state["attachments"] = known
		external := !strings.EqualFold(domainOf(stateString(from, "email")), userDomain)
		classification := "Internal"
		if external {
			classification = "External"
		}
		auth := item.Auth
		if auth == nil {
			auth = map[string]any{"spf": "none", "dkim": "none", "dmarc": "none", "tls": "TLS 1.3", "ip": "—", "signer": "", "firstContact": false, "warnings": []any{}}
		}
		message := map[string]any{
			"id": id, "threadId": threadID, "folder": item.Folder, "from": from,
			"to": toPeople, "cc": ccPeople, "bcc": bccPeople,
			"subject": item.Subject, "snippet": snippet(firstNonEmpty(item.BodyText, item.Subject)),
			"bodyHtml": item.BodyHTML, "bodyText": item.BodyText,
			"date": item.Date.UTC().Format(time.RFC3339), "unread": unread, "starred": starred,
			"draft": item.Folder == "drafts", "attachments": attachments,
			"categories": []any{}, "classification": classification, "external": external,
			"auth": auth, "event": nil, "sizeBytes": item.Size,
			"messageId": messageID, "inReplyTo": nullableString(item.InReplyTo),
			"priority": "normal", "receipt": false, "delivery": false, "saveCopy": true,
			"viaApi": true, "windowMode": "floating",
			"imapUid": item.UID, "imapFolder": item.Folder, "imapUidValidity": item.UIDValidity,
		}
		state["messages"] = append(stateMessages(state), message)
		existingByUID[uidKey] = message
		existingByFolderMessageID[folderMessageKey] = message
		existingByMessageID[normalizedID] = append(existingByMessageID[normalizedID], message)
		imported++
	}
	// Message IDs that still have a live IMAP copy, used for the SMTP save-copy
	// grace period and for cross-folder local records.
	liveFolderMessageIDs := map[string]map[string]bool{}
	if liveKeys != nil {
		for _, raw := range stateMessages(state) {
			message, _ := raw.(map[string]any)
			if message == nil {
				continue
			}
			uid := stateInt(message, "imapUid")
			folder := stateString(message, "imapFolder")
			messageID := messageIDKey(stateString(message, "messageId"))
			if uid <= 0 || messageID == "" ||
				!liveKeys[imapMessageKey(folder, stateInt(message, "imapUidValidity"), uid)] {
				continue
			}
			if liveFolderMessageIDs[folder] == nil {
				liveFolderMessageIDs[folder] = map[string]bool{}
			}
			liveFolderMessageIDs[folder][messageID] = true
		}
	}
	kept := []any{}
	for _, raw := range stateMessages(state) {
		message, _ := raw.(map[string]any)
		if message == nil {
			kept = append(kept, raw)
			continue
		}
		imapUID := stateInt(message, "imapUid")
		imapFolder := stateString(message, "imapFolder")
		messageID := messageIDKey(stateString(message, "messageId"))
		if imapUID > 0 && coveredFolders[imapFolder] {
			if liveKeys != nil {
				if !liveKeys[imapMessageKey(imapFolder, stateInt(message, "imapUidValidity"), imapUID)] {
					removed++
					continue
				}
			} else if !seenUIDs[imapMessageKey(imapFolder, stateInt(message, "imapUidValidity"), imapUID)] &&
				!seenFolderMessageIDs[imapFolder+"\x00"+messageID] {
				removed++
				continue
			}
		}
		seenInFolder := seenFolderMessageIDs[imapFolder+"\x00"+messageID]
		if liveKeys != nil {
			seenInFolder = liveFolderMessageIDs[imapFolder][messageID]
		}
		if imapUID == 0 && stateString(message, "folder") == "sent" && coveredFolders["sent"] && !seenInFolder {
			if stamp, err := time.Parse(time.RFC3339, stateString(message, "date")); err == nil && time.Since(stamp) > 5*time.Minute {
				removed++
				continue
			}
		}
		kept = append(kept, raw)
	}
	state["messages"] = kept
	return imported, updated, removed
}

func messageIDKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func importedThreadID(existingByMessageID map[string][]map[string]any, normalizedID string, item fetchedMail, root string) string {
	if messages := existingByMessageID[normalizedID]; len(messages) > 0 {
		for _, message := range messages {
			if threadID := stateString(message, "threadId"); threadID != "" {
				return threadID
			}
		}
	}
	references := append([]string{}, item.References...)
	if item.InReplyTo != "" {
		references = append([]string{item.InReplyTo}, references...)
	}
	for _, reference := range references {
		for _, message := range existingByMessageID[messageIDKey(reference)] {
			if threadID := stateString(message, "threadId"); threadID != "" {
				return threadID
			}
		}
	}
	return "t_imap_" + hashShort(root)
}

func imapMessageKey(folder string, uidValidity, uid int64) string {
	return fmt.Sprintf("%s\x00%d\x00%d", folder, uidValidity, uid)
}

func peopleToAny(people []map[string]any) []any {
	result := make([]any, 0, len(people))
	for _, person := range people {
		result = append(result, person)
	}
	return result
}

func storeImportedAttachment(uploadDir string, attachment parsedMailAttachment, user userRecord) (map[string]any, error) {
	if len(attachment.Data) == 0 || len(attachment.Data) > maxAttachmentSize {
		return nil, fmt.Errorf("empty or oversized attachment")
	}
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		return nil, err
	}
	id := newID("att")
	target := filepath.Join(uploadDir, id)
	if err := os.WriteFile(target, attachment.Data, 0o640); err != nil {
		return nil, err
	}
	mimeType := attachment.MIMEType
	if mimeType == "" {
		mimeType = mimeForExtension(attachment.Name)
	}
	previewable := mimeType == "application/pdf" || strings.HasPrefix(mimeType, "image/")
	return map[string]any{
		"id": id, "name": safeFileName(attachment.Name), "type": attachmentBadgeType(mimeType, attachment.Name),
		"mime": mimeType, "size": formatBytes(int64(len(attachment.Data))), "sizeBytes": len(attachment.Data),
		"storedName": id, "previewable": previewable, "uploadedAt": nowISO(),
		"owner": map[string]any{"name": user.Name, "email": user.Email, "initials": user.Initials, "color": user.Color},
	}, nil
}

func mapIMAPFolder(name string) (string, bool) {
	base := name
	if index := strings.LastIndexAny(name, "/."); index >= 0 {
		base = name[index+1:]
	}
	switch strings.ToLower(base) {
	case "inbox":
		return "inbox", true
	case "sent", "sent messages", "sent items", "sent-mail":
		return "sent", true
	case "drafts":
		return "drafts", true
	case "archive", "archives", "all mail":
		return "archive", true
	case "junk", "spam", "junk e-mail":
		return "spam", true
	case "trash", "deleted items", "deleted messages":
		return "trash", true
	}
	return "", false
}

func hashShort(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

// imapMessageID is scoped by user and folder because mail_messages.id is a
// global primary key. A bare Message-ID collides for multi-recipient mail and
// for a user's Sent/INBOX copies of the same message.
func imapMessageID(userID, messageID, folder string) string {
	return "msg_imap_" + hashShort(userID+"|"+folder+"|"+messageID)
}

func decodeHeader(value string) string {
	if value == "" {
		return ""
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func domainOf(email string) string {
	index := strings.LastIndex(email, "@")
	if index < 0 {
		return ""
	}
	return strings.ToLower(email[index+1:])
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

var htmlTagPattern = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<[^>]+>`)
var htmlEntityPattern = regexp.MustCompile(`&(nbsp|amp|lt|gt|quot|#39);`)

func stripHTMLToText(value string) string {
	text := htmlTagPattern.ReplaceAllString(value, " ")
	text = htmlEntityPattern.ReplaceAllStringFunc(text, func(entity string) string {
		switch entity {
		case "&nbsp;":
			return " "
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return `"`
		case "&#39;":
			return "'"
		}
		return entity
	})
	return strings.Join(strings.Fields(text), " ")
}
