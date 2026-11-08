package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"
)

const (
	externalSyncDays          = 90
	externalInitialMessages   = 200
	externalIncrementalBatch  = 300
	externalFetchBatchSize    = 20
	externalFlagReconcileSize = 200
	externalCacheDays         = 365
	externalCacheMessages     = 3000
	externalMaxFolders        = 40
)

type externalMailFolder struct {
	RemoteName  string
	DisplayName string
	Role        string
	SpecialUse  string
	Delimiter   string
	Sync        bool
}

func externalFolderRole(info *imap.MailboxInfo) string {
	attributes := map[string]bool{}
	for _, attribute := range info.Attributes {
		attributes[strings.ToLower(strings.TrimSpace(attribute))] = true
	}
	if attributes[`\noselect`] || attributes[`\flagged`] || attributes[`\chat`] {
		return ""
	}
	if attributes[`\all`] {
		return "archive"
	}
	switch {
	case attributes[`\inbox`] || strings.EqualFold(info.Name, "INBOX"):
		return "inbox"
	case attributes[`\sent`]:
		return "sent"
	case attributes[`\drafts`]:
		return "drafts"
	case attributes[`\trash`]:
		return "trash"
	case attributes[`\junk`]:
		return "junk"
	case attributes[`\archive`]:
		return "archive"
	}
	name := strings.ToLower(info.Name)
	switch {
	case name == "sent" || name == "sent items" || strings.HasSuffix(name, "/sent") || strings.HasSuffix(name, "/sent mail"):
		return "sent"
	case name == "drafts" || name == "draft" || strings.HasSuffix(name, "/drafts"):
		return "drafts"
	case name == "trash" || name == "deleted items" || strings.HasSuffix(name, "/trash") || strings.HasSuffix(name, "/deleted"):
		return "trash"
	case name == "junk" || name == "spam" || name == "junk email" || strings.HasSuffix(name, "/junk") || strings.HasSuffix(name, "/spam"):
		return "junk"
	case name == "archive" || strings.HasSuffix(name, "/archive"):
		return "archive"
	}
	return "custom"
}

func externalAppFolder(role string) string {
	switch role {
	case "inbox":
		return "inbox"
	case "sent":
		return "sent"
	case "drafts":
		return "drafts"
	case "archive":
		return "archive"
	case "junk":
		return "spam"
	case "trash":
		return "trash"
	}
	return ""
}

func externalFolderLabel(folder externalMailFolder) string {
	if folder.DisplayName != "" && !strings.EqualFold(folder.DisplayName, folder.RemoteName) {
		return folder.DisplayName
	}
	switch folder.Role {
	case "inbox":
		return "Inbox"
	case "sent":
		return "Sent"
	case "drafts":
		return "Drafts"
	case "archive":
		return "Archive"
	case "junk":
		return "Junk"
	case "trash":
		return "Trash"
	}
	return folder.RemoteName
}

func listExternalMailFolders(conn *imapclient.Client) ([]externalMailFolder, error) {
	channel := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- conn.List("", "*", channel) }()
	result := []externalMailFolder{}
	indexByRole := map[string]int{}
	for info := range channel {
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		role := externalFolderRole(info)
		if role == "" {
			continue
		}
		isAll := false
		for _, attribute := range info.Attributes {
			if strings.EqualFold(strings.TrimSpace(attribute), `\All`) {
				isAll = true
			}
		}
		syncFolder := !isAll
		displayName := info.Name
		if role == "inbox" {
			displayName = "Inbox"
		}
		folder := externalMailFolder{
			RemoteName: info.Name, DisplayName: displayName, Role: role,
			SpecialUse: externalRoleSpecialUse(role), Delimiter: info.Delimiter, Sync: syncFolder,
		}
		if role != "custom" {
			if index, exists := indexByRole[role]; exists {
				// A real archive folder is preferable to Gmail's virtual All Mail.
				if !result[index].Sync && syncFolder {
					result[index] = folder
				}
				continue
			}
			indexByRole[role] = len(result)
		}
		result = append(result, folder)
	}
	if err := <-done; err != nil {
		return nil, err
	}
	order := map[string]int{"inbox": 0, "sent": 1, "drafts": 2, "archive": 3, "junk": 4, "trash": 5, "custom": 6}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := order[result[i].Role], order[result[j].Role]
		if left != right {
			return left < right
		}
		return strings.ToLower(externalFolderLabel(result[i])) < strings.ToLower(externalFolderLabel(result[j]))
	})
	return result, nil
}

func externalRoleSpecialUse(role string) string {
	switch role {
	case "inbox":
		return `\Inbox`
	case "sent":
		return `\Sent`
	case "drafts":
		return `\Drafts`
	case "archive":
		return `\Archive`
	case "junk":
		return `\Junk`
	case "trash":
		return `\Trash`
	}
	return ""
}

func (s *Server) upsertExternalFolder(ctx context.Context, account mailAccountRecord, folder externalMailFolder, status *imap.MailboxStatus) (string, error) {
	var id string
	err := s.store.DB.QueryRow(ctx, `
		SELECT id FROM public.mail_folders WHERE account_id=$1 AND remote_name=$2
	`, account.ID, folder.RemoteName).Scan(&id)
	if err != nil && !errorsIsNoRows(err) {
		return "", err
	}
	if id == "" {
		id = newID("mfld")
		if _, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_folders (
				id, tenant_id, user_id, account_id, remote_name, display_name, delimiter,
				role, special_use, selectable, sync_enabled
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,true,$10)
		`, id, account.TenantID, account.UserID, account.ID, folder.RemoteName,
			externalFolderLabel(folder), folder.Delimiter, folder.Role, folder.SpecialUse, folder.Sync); err != nil {
			return "", err
		}
	} else if _, err := s.store.DB.Exec(ctx, `
		UPDATE public.mail_folders SET display_name=$3, delimiter=$4, role=$5, special_use=$6,
			sync_enabled=$7, updated_at=now()
		WHERE id=$1 AND account_id=$2
	`, id, account.ID, externalFolderLabel(folder), folder.Delimiter, folder.Role, folder.SpecialUse, folder.Sync); err != nil {
		return "", err
	}
	if status != nil {
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE public.mail_folders SET uid_validity=$3, uid_next=$4, last_synced_at=now(), updated_at=now()
			WHERE id=$1 AND account_id=$2
		`, id, account.ID, status.UidValidity, status.UidNext)
	}
	return id, nil
}

func (s *Server) externalFolderValidity(ctx context.Context, accountID, remoteName string) int64 {
	var validity int64
	_ = s.store.DB.QueryRow(ctx, `
		SELECT uid_validity FROM public.mail_folders WHERE account_id=$1 AND remote_name=$2
	`, accountID, remoteName).Scan(&validity)
	return validity
}

func (s *Server) externalKnownUIDs(ctx context.Context, accountID, remoteName string, uidValidity int64) (map[uint32]bool, uint32) {
	known := map[uint32]bool{}
	var highest uint32
	rows, err := s.store.DB.Query(ctx, `
		SELECT uid FROM public.mail_messages
		WHERE account_id=$1 AND imap_folder=$2 AND uid_validity=$3 AND uid > 0 AND uid <= 4294967295
	`, accountID, remoteName, uidValidity)
	if err != nil {
		return known, 0
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		if rows.Scan(&uid) == nil {
			known[uint32(uid)] = true
			if uint32(uid) > highest {
				highest = uint32(uid)
			}
		}
	}
	return known, highest
}

func externalUIDsSince(conn *imapclient.Client, since time.Time) ([]uint32, error) {
	criteria := imap.NewSearchCriteria()
	criteria.Since = since
	return conn.UidSearch(criteria)
}

func capExternalUIDs(uids []uint32, limit int, newest bool) []uint32 {
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	if limit <= 0 || len(uids) <= limit {
		return uids
	}
	if newest {
		return uids[len(uids)-limit:]
	}
	return uids[:limit]
}

func (s *Server) syncExternalAccount(ctx context.Context, account mailAccountRecord, user userRecord) error {
	lock := s.userMailLock(account.ID)
	lock.Lock()
	defer lock.Unlock()

	conn, err := s.openExternalIMAP(ctx, account)
	if err != nil {
		s.recordExternalSyncFailure(ctx, account, err)
		return err
	}
	defer conn.Logout()
	folders, err := listExternalMailFolders(conn)
	if err != nil {
		s.recordExternalSyncFailure(ctx, account, err)
		return fmt.Errorf("list IMAP folders: %w", err)
	}
	if len(folders) == 0 {
		err := fmt.Errorf("the mail server did not expose any usable folders")
		s.recordExternalSyncFailure(ctx, account, err)
		return err
	}
	type syncFolder struct {
		folder    externalMailFolder
		appFolder string
	}
	prepared := []syncFolder{}
	for _, folder := range folders {
		if folder.Role == "" {
			continue
		}
		// Persist every selectable folder first so custom folders get a stable
		// local id and the sidebar can show them even when a pass is interrupted.
		folderID, folderErr := s.upsertExternalFolder(ctx, account, folder, nil)
		if folderErr != nil {
			s.logger.Warn("external folder save failed", "account", account.Email, "folder", folder.RemoteName, "err", folderErr)
			continue
		}
		if !folder.Sync {
			continue
		}
		appFolder := externalAppFolder(folder.Role)
		if appFolder == "" {
			appFolder = folderID
		}
		prepared = append(prepared, syncFolder{folder: folder, appFolder: appFolder})
	}
	if len(prepared) > externalMaxFolders {
		prepared = prepared[:externalMaxFolders]
	}
	imported := 0
	calendarFetched := []fetchedMail{}
	for _, item := range prepared {
		folder := item.folder
		appFolder := item.appFolder
		status, selectErr := conn.Select(folder.RemoteName, true)
		if selectErr != nil {
			s.logger.Warn("external IMAP select failed", "account", account.Email, "folder", folder.RemoteName, "err", selectErr)
			continue
		}
		previousValidity := s.externalFolderValidity(ctx, account.ID, folder.RemoteName)
		if _, folderErr := s.upsertExternalFolder(ctx, account, folder, status); folderErr != nil {
			s.logger.Warn("external folder save failed", "account", account.Email, "folder", folder.RemoteName, "err", folderErr)
			continue
		}
		if previousValidity != 0 && previousValidity != int64(status.UidValidity) {
			_, _ = s.store.DB.Exec(ctx, `
				DELETE FROM public.mail_messages
				WHERE account_id=$1 AND imap_folder=$2 AND uid_validity=$3
			`, account.ID, folder.RemoteName, previousValidity)
		}
		known, highest := s.externalKnownUIDs(ctx, account.ID, folder.RemoteName, int64(status.UidValidity))
		var uids []uint32
		if len(known) == 0 {
			uids, err = externalUIDsSince(conn, time.Now().AddDate(0, 0, -externalSyncDays))
			uids = capExternalUIDs(uids, externalInitialMessages, true)
		} else {
			uids, err = imapUIDs(conn, int64(highest))
			uids = capExternalUIDs(uids, externalIncrementalBatch, false)
		}
		if err != nil {
			s.logger.Warn("external IMAP search failed", "account", account.Email, "folder", folder.RemoteName, "err", err)
			continue
		}
		messages := []map[string]any{}
		attachmentRecords := []map[string]any{}
		for start := 0; start < len(uids); start += externalFetchBatchSize {
			end := start + externalFetchBatchSize
			if end > len(uids) {
				end = len(uids)
			}
			fetched, _, fetchErr := fetchIMAPMessages(conn, uids[start:end], appFolder, status.UidValidity)
			if fetchErr != nil {
				s.logger.Warn("external IMAP fetch failed", "account", account.Email, "folder", folder.RemoteName, "err", fetchErr)
				break
			}
			for _, fetchedItem := range fetched {
				if len(fetchedItem.CalendarParts) > 0 {
					calendarFetched = append(calendarFetched, fetchedItem)
				}
				message, attachments := s.externalFetchedMessage(ctx, account, user, fetchedItem, appFolder, folder.RemoteName)
				messages = append(messages, message)
				attachmentRecords = append(attachmentRecords, attachments...)
			}
		}
		if len(messages) > 0 {
			if persistErr := s.persistExternalFetched(ctx, account, user, messages, attachmentRecords); persistErr != nil {
				s.logger.Warn("external message persist failed", "account", account.Email, "folder", folder.RemoteName, "err", persistErr)
				continue
			}
			imported += len(messages)
		}
		// Reconcile flags for cached messages that were not downloaded this pass.
		flagUIDs := make([]uint32, 0, len(known))
		for uid := range known {
			flagUIDs = append(flagUIDs, uid)
		}
		sort.Slice(flagUIDs, func(i, j int) bool { return flagUIDs[i] < flagUIDs[j] })
		if len(flagUIDs) > externalFlagReconcileSize {
			flagUIDs = flagUIDs[len(flagUIDs)-externalFlagReconcileSize:]
		}
		if len(flagUIDs) > 0 {
			if updates, flagErr := fetchIMAPFlagUpdates(conn, flagUIDs, appFolder, status.UidValidity); flagErr == nil {
				s.applyExternalFlagUpdates(ctx, account.ID, folder.RemoteName, int64(status.UidValidity), updates)
			}
		}
		// A message removed in another mail client must leave the local cache
		// too. Only UIDs we have actually cached are checked, so an older remote
		// message outside the sync window is never affected.
		s.reconcileExternalFolder(ctx, conn, account, folder.RemoteName, int64(status.UidValidity), known)
	}
	s.pruneExternalAccountCache(ctx, account)
	if len(calendarFetched) > 0 {
		if applied := s.processInboundCalendarReplies(ctx, user, calendarFetched); applied > 0 {
			s.logger.Info("connected calendar replies applied", "account", account.Email, "count", applied)
		}
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.mail_accounts
		SET status='active', last_error='', last_sync_at=now(), updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, account.ID, account.UserID)
	if imported > 0 {
		s.publishRealtimeTo(account.UserID, "mail")
	}
	s.logger.Info("external mail sync complete", "account", account.Email, "imported", imported)
	return nil
}

func (s *Server) applyExternalFlagUpdates(ctx context.Context, accountID, remoteName string, uidValidity int64, updates []imapFlagUpdate) {
	for _, update := range updates {
		unread := !hasIMAPFlag(update.Flags, `\Seen`)
		starred := hasIMAPFlag(update.Flags, `\Flagged`)
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE public.mail_messages SET unread=$5, starred=$6, flagged=$6, updated_at=now()
			WHERE account_id=$1 AND imap_folder=$2 AND uid=$3 AND uid_validity=$4
		`, accountID, remoteName, update.UID, uidValidity, unread, starred)
	}
}

// reconcileExternalFolder removes cached messages that the provider reports as
// gone. UID SEARCH is restricted to the cached UID set, so messages outside the
// sync window are never touched.
func (s *Server) reconcileExternalFolder(ctx context.Context, conn *imapclient.Client, account mailAccountRecord, remoteName string, uidValidity int64, known map[uint32]bool) {
	if len(known) == 0 {
		return
	}
	set := new(imap.SeqSet)
	for uid := range known {
		set.AddNum(uid)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = set
	presentUIDs, err := conn.UidSearch(criteria)
	if err != nil {
		s.logger.Warn("external deletion reconcile search failed", "account", account.Email, "folder", remoteName, "err", err)
		return
	}
	present := make(map[uint32]bool, len(presentUIDs))
	for _, uid := range presentUIDs {
		present[uid] = true
	}
	removed := 0
	for uid := range known {
		if present[uid] {
			continue
		}
		if _, err := s.store.DB.Exec(ctx, `
			DELETE FROM public.mail_messages
			WHERE account_id=$1 AND imap_folder=$2 AND uid_validity=$3 AND uid=$4
		`, account.ID, remoteName, uidValidity, uid); err == nil {
			removed++
		}
	}
	if removed > 0 {
		if s.logger != nil {
			s.logger.Info("external mail cache reconciled", "account", account.Email, "folder", remoteName, "removed", removed)
		}
	}
}

// pruneExternalAccountCache keeps the connected-account cache bounded without
// touching the provider. Starred, pinned and draft messages stay available.
func (s *Server) pruneExternalAccountCache(ctx context.Context, account mailAccountRecord) {
	result, err := s.store.DB.Exec(ctx, `
		DELETE FROM public.mail_messages
		WHERE account_id=$1 AND NOT draft AND NOT starred AND NOT pinned
		  AND id IN (
			SELECT id FROM public.mail_messages
			WHERE account_id=$1 AND NOT draft AND NOT starred AND NOT pinned
			ORDER BY sent_at DESC OFFSET $2
		  )
	`, account.ID, externalCacheMessages)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("external cache count prune failed", "account", account.Email, "err", err)
		}
	} else if removed := result.RowsAffected(); removed > 0 && s.logger != nil {
		s.logger.Info("external cache pruned", "account", account.Email, "removed", removed, "reason", "count")
	}
	result, err = s.store.DB.Exec(ctx, `
		DELETE FROM public.mail_messages
		WHERE account_id=$1 AND NOT draft AND NOT starred AND NOT pinned
		  AND sent_at < now() - make_interval(days => $2)
	`, account.ID, externalCacheDays)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("external cache age prune failed", "account", account.Email, "err", err)
		}
	} else if removed := result.RowsAffected(); removed > 0 && s.logger != nil {
		s.logger.Info("external cache pruned", "account", account.Email, "removed", removed, "reason", "age")
	}
}

func (s *Server) recordExternalSyncFailure(ctx context.Context, account mailAccountRecord, syncErr error) {
	status := "active"
	message := truncate(syncErr.Error(), 500)
	lower := strings.ToLower(message)
	if strings.Contains(lower, "login failed") || strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "invalid credentials") || strings.Contains(lower, "authenticate") {
		status = "reauth"
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.mail_accounts SET status=$2, last_error=$3, updated_at=now()
		WHERE id=$1 AND user_id=$4
	`, account.ID, status, message, account.UserID)
}

func (s *Server) persistExternalFetched(ctx context.Context, account mailAccountRecord, user userRecord, messages []map[string]any, attachments []map[string]any) error {
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, record := range attachments {
		id := stateString(record, "id")
		if id == "" {
			continue
		}
		storedName := stateString(record, "storedName")
		if _, err := tx.Exec(ctx, `
			INSERT INTO attachments (id, tenant_id, user_id, name, mime, size_bytes, sha256, stored_path, previewable)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (id) DO NOTHING
		`, id, user.TenantID, user.ID,
			truncate(sanitizeMailText(firstNonEmpty(stateString(record, "name"), "attachment")), 240),
			sanitizeMailText(firstNonEmpty(stateString(record, "mime"), "application/octet-stream")),
			stateInt(record, "sizeBytes"), sanitizeMailText(stateString(record, "checksum")),
			sanitizeMailText(storedName), stateBool(record, "previewable")); err != nil {
			return err
		}
	}
	for _, message := range messages {
		if err := s.persistMailMessageTx(ctx, tx, user, message); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) externalFetchedMessage(ctx context.Context, account mailAccountRecord, user userRecord, item fetchedMail, appFolder, remoteName string) (map[string]any, []map[string]any) {
	domain := domainOf(account.Email)
	messageID := item.MessageID
	if strings.TrimSpace(messageID) == "" {
		messageID = fmt.Sprintf("<imap-%d-%d@%s>", item.UIDValidity, item.UID, domain)
	}
	from := map[string]any{"name": "Unknown sender", "email": "unknown@" + domain, "initials": "?", "color": "#64748b"}
	if len(item.From) > 0 {
		from = item.From[0]
	}
	fromEmail := stateString(from, "email")
	attachments := []any{}
	attachmentRecords := []map[string]any{}
	for _, attachment := range item.Attachments {
		record, err := storeImportedAttachment(s.cfg.UploadDir, attachment, user)
		if err != nil {
			continue
		}
		attachmentRecords = append(attachmentRecords, record)
		attachments = append(attachments, publicAttachment(record))
	}
	date := item.Date
	if date.IsZero() {
		date = item.InternalDate
	}
	if date.IsZero() {
		date = time.Now()
	}
	root := messageID
	if len(item.References) > 0 {
		root = item.References[0]
	} else if item.InReplyTo != "" {
		root = item.InReplyTo
	}
	existingID := ""
	existingThread := ""
	if s.store != nil && s.store.DB != nil {
		_ = s.store.DB.QueryRow(ctx, `
			SELECT id, thread_id FROM public.mail_messages
			WHERE account_id=$1 AND message_id=$2 AND folder=$3
			ORDER BY updated_at DESC LIMIT 1
		`, account.ID, messageID, appFolder).Scan(&existingID, &existingThread)
	}
	if existingID == "" {
		existingID = imapMessageID(account.ID, messageID, appFolder)
	}
	if existingThread == "" {
		existingThread = opaqueThreadID("external:" + account.ID + "\x00" + root)
	}
	unread := !hasIMAPFlag(item.Flags, `\Seen`)
	starred := hasIMAPFlag(item.Flags, `\Flagged`)
	external := !strings.EqualFold(domainOf(fromEmail), domain)
	classification := "Internal"
	if external {
		classification = "External"
	}
	auth := item.Auth
	if auth == nil {
		auth = map[string]any{"spf": "none", "dkim": "none", "dmarc": "none", "tls": "TLS 1.3", "ip": "—", "signer": "", "firstContact": false, "warnings": []any{}}
	}
	message := map[string]any{
		"id":        existingID,
		"threadId":  existingThread,
		"accountId": account.ID,
		"folder":    appFolder, "imapFolder": remoteName,
		"imapUid": item.UID, "imapUidValidity": item.UIDValidity,
		"from": from, "to": peopleToAny(item.To), "cc": peopleToAny(item.CC), "bcc": peopleToAny(item.BCC),
		"subject": item.Subject, "snippet": snippet(firstNonEmpty(item.BodyText, item.Subject)),
		"bodyHtml": item.BodyHTML, "bodyText": item.BodyText,
		"date": date.UTC().Format(time.RFC3339), "unread": unread, "starred": starred, "flagged": starred,
		"draft": appFolder == "drafts", "attachments": attachments, "categories": []any{},
		"classification": classification, "external": external, "auth": auth, "event": nil,
		"sizeBytes": item.Size, "messageId": messageID, "inReplyTo": nullableString(item.InReplyTo),
		"references": peopleToAnyReferences(item.References),
		"priority":   "normal", "receipt": false, "delivery": false, "saveCopy": true,
		"viaApi": false, "windowMode": "floating",
	}
	if item.Unsubscribe != nil {
		message["unsubscribe"] = item.Unsubscribe
	}
	if item.SenderUTCOffsetMinutes != nil {
		message["senderUtcOffsetMinutes"] = *item.SenderUTCOffsetMinutes
	}
	return message, attachmentRecords
}

func peopleToAnyReferences(references []string) []any {
	result := make([]any, 0, len(references))
	for _, reference := range references {
		if strings.TrimSpace(reference) != "" {
			result = append(result, reference)
		}
	}
	return result
}

func (s *Server) externalFolderViews(ctx context.Context, account mailAccountRecord) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, remote_name, display_name, role, sync_enabled
		FROM public.mail_folders
		WHERE account_id=$1 AND role IN ('inbox','sent','drafts','archive','junk','trash','custom')
		ORDER BY CASE role WHEN 'inbox' THEN 0 WHEN 'sent' THEN 1 WHEN 'drafts' THEN 2
		                   WHEN 'archive' THEN 3 WHEN 'junk' THEN 4 WHEN 'trash' THEN 5 ELSE 6 END,
		         sync_enabled DESC, lower(display_name), lower(remote_name)
	`, account.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := []map[string]any{}
	seen := map[string]bool{}
	for rows.Next() {
		var id, remoteName, displayName, role string
		var syncEnabled bool
		if rows.Scan(&id, &remoteName, &displayName, &role, &syncEnabled) != nil {
			continue
		}
		appFolder := externalAppFolder(role)
		viewID := appFolder
		if viewID == "" {
			viewID = id
		}
		if seen[viewID] {
			continue
		}
		seen[viewID] = true
		icon := map[string]string{
			"inbox": "i-inbox", "sent": "i-send", "drafts": "i-file", "archive": "i-archive",
			"spam": "i-shield-alert", "trash": "i-trash",
		}[appFolder]
		if icon == "" {
			icon = "i-folder"
		}
		label := displayName
		if strings.TrimSpace(label) == "" {
			if appFolder != "" {
				label = strings.ToUpper(appFolder[:1]) + appFolder[1:]
			} else {
				label = remoteName
			}
		}
		views = append(views, map[string]any{
			"id": viewID, "label": label, "icon": icon, "system": appFolder != "",
			"section": "folders", "order": len(views), "remoteName": remoteName,
			"connected": true, "syncEnabled": syncEnabled,
		})
	}
	return views, rows.Err()
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
