package httpapi

import (
	"context"
	"fmt"
	"net/textproto"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

var appToIMAPFolder = map[string]string{
	"inbox": "INBOX", "sent": "Sent", "drafts": "Drafts",
	"archive": "Archive", "spam": "Junk", "trash": "Trash",
}

type imapAction struct {
	AccountID        string
	MessageID        string
	SourceUID        int64
	SourceFolder     string
	SourceIMAPFolder string
	TargetFolder     string
	TargetIMAPFolder string
	SetSeen          *bool
	SetFlagged       *bool
	MarkDeleted      bool
	ClearDeleted     bool
}

func imapActionForMessage(message map[string]any) imapAction {
	source := stateString(message, "imapFolder")
	if source == "" {
		source = stateString(message, "folder")
	}
	return imapAction{
		AccountID:        stateString(message, "accountId"),
		MessageID:        stateString(message, "messageId"),
		SourceUID:        stateInt(message, "imapUid"),
		SourceFolder:     source,
		SourceIMAPFolder: stateString(message, "imapFolder"),
	}
}

func imapFolderFor(appFolder string) string {
	if name, ok := appToIMAPFolder[appFolder]; ok {
		return name
	}
	return ""
}

func (s *Server) applyIMAPActions(user userRecord, actions []imapAction) error {
	if len(actions) == 0 {
		return nil
	}
	ctx := context.Background()
	if !s.tenantPolicyFor(ctx, user.TenantID).Mail.IMAPAccess {
		return nil
	}
	// Older call sites only know the message id. Resolve its account in one
	// query so native and connected messages can travel through the same
	// mutation path without exposing account ids to every caller.
	messageIDs := []string{}
	for _, action := range actions {
		if action.AccountID == "" && action.MessageID != "" {
			messageIDs = append(messageIDs, action.MessageID)
		}
	}
	if len(messageIDs) > 0 {
		rows, err := s.store.DB.Query(ctx, `
			SELECT id, account_id FROM public.mail_messages
			WHERE user_id=$1 AND id = ANY($2)
		`, user.ID, messageIDs)
		if err == nil {
			accountsByMessage := map[string]string{}
			for rows.Next() {
				var messageID, accountID string
				if rows.Scan(&messageID, &accountID) == nil {
					accountsByMessage[messageID] = accountID
				}
			}
			rows.Close()
			for index := range actions {
				if actions[index].AccountID == "" {
					actions[index].AccountID = accountsByMessage[actions[index].MessageID]
				}
			}
		}
	}
	native, nativeErr := s.nativeMailAccountForUser(ctx, user.ID)
	groups := map[string][]imapAction{}
	for _, action := range actions {
		if action.AccountID == "" && nativeErr == nil {
			action.AccountID = native.ID
		}
		if action.AccountID == "" {
			continue
		}
		groups[action.AccountID] = append(groups[action.AccountID], action)
	}
	for accountID, group := range groups {
		account, err := s.resolveMailAccount(ctx, user.ID, accountID)
		if err != nil {
			s.logger.Warn("IMAP action account missing", "account", accountID, "err", err)
			continue
		}
		if account.Kind == mailAccountKindExternal && !s.externalMailAccountsAllowed(ctx, user) {
			continue
		}
		if account.Kind == mailAccountKindNative {
			if !s.mailAccountConfigured(user.Email) {
				continue
			}
			loginUser, loginPassword, loginErr := s.mailLoginForUser(user.Email)
			if loginErr != nil {
				s.logger.Warn("IMAP action credentials", "err", loginErr)
				continue
			}
			tlsConfig, tlsErr := s.mailTLSConfig(s.cfg.IMAPTLSName)
			if tlsErr != nil {
				continue
			}
			conn, connectErr := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
			if connectErr != nil {
				s.logger.Warn("IMAP action connect", "err", connectErr)
				continue
			}
			conn.Timeout = 30 * time.Second
			if loginErr := conn.Login(loginUser, loginPassword); loginErr != nil {
				_ = conn.Logout()
				s.logger.Warn("IMAP action login", "err", loginErr)
				continue
			}
			ensureIMAPFolders(conn)
			state, _ := s.loadState(ctx, user.ID)
			s.applyIMAPActionsForAccount(ctx, conn, user, account, state, group)
			_ = conn.Logout()
			continue
		}
		conn, connectErr := s.openExternalIMAP(ctx, account)
		if connectErr != nil {
			s.recordExternalSyncFailure(ctx, account, connectErr)
			s.logger.Warn("connected IMAP action login", "account", account.Email, "err", connectErr)
			continue
		}
		s.applyIMAPActionsForAccount(ctx, conn, user, account, nil, group)
		_ = conn.Logout()
	}
	return nil
}

func (s *Server) applyIMAPActionsForAccount(ctx context.Context, conn *imapclient.Client, user userRecord, account mailAccountRecord, state map[string]any, actions []imapAction) {
	for _, action := range actions {
		if account.Kind == mailAccountKindExternal && action.SetSeen != nil {
			s.propagateExternalSeen(ctx, conn, user, account, action)
		}
		source := action.SourceIMAPFolder
		if source == "" {
			source = s.actionIMAPFolder(ctx, account, state, action.SourceFolder)
		}
		if source == "" || (action.MessageID == "" && action.SourceUID == 0) {
			continue
		}
		uid, err := findIMAPUID(conn, source, action.MessageID)
		if (err != nil || uid == 0) && action.SourceUID > 0 {
			if _, selectErr := conn.Select(source, false); selectErr == nil {
				uid = uint32(action.SourceUID)
				err = nil
			}
		}
		if err != nil || uid == 0 {
			s.logger.Warn("IMAP action could not find message", "account", account.Email, "folder", source, "message", action.MessageID, "err", err)
			continue
		}
		currentFolder := source
		if action.ClearDeleted {
			if err := setIMAPFlags(conn, source, uid, nil, []string{imap.DeletedFlag}); err != nil {
				s.logger.Warn("IMAP clear deleted flag", "message", action.MessageID, "err", err)
			}
		}
		if action.TargetFolder != "" && action.TargetFolder != action.SourceFolder {
			target := action.TargetIMAPFolder
			if target == "" {
				target = s.actionIMAPFolder(ctx, account, state, action.TargetFolder)
			}
			if target == "" {
				continue
			}
			if err := moveIMAPMessage(conn, source, uid, target); err != nil {
				s.logger.Warn("IMAP move failed", "message", action.MessageID, "from", source, "to", target, "err", err)
			} else {
				currentFolder = target
				uid, err = findIMAPUID(conn, target, action.MessageID)
				if err != nil || uid == 0 {
					continue
				}
			}
		}
		add := []string{}
		remove := []string{}
		if action.SetSeen != nil {
			if *action.SetSeen {
				add = append(add, imap.SeenFlag)
			} else {
				remove = append(remove, imap.SeenFlag)
			}
		}
		if action.SetFlagged != nil {
			if *action.SetFlagged {
				add = append(add, imap.FlaggedFlag)
			} else {
				remove = append(remove, imap.FlaggedFlag)
			}
		}
		if action.MarkDeleted {
			add = append(add, imap.DeletedFlag)
		}
		if len(add) > 0 || len(remove) > 0 {
			if err := setIMAPFlags(conn, currentFolder, uid, add, remove); err != nil {
				s.logger.Warn("IMAP flag update failed", "message", action.MessageID, "err", err)
			}
		}
		if action.MarkDeleted && account.Kind == mailAccountKindExternal {
			set := new(imap.SeqSet)
			set.AddNum(uid)
			if err := uidExpunge(conn, set); err != nil {
				_ = conn.Expunge(nil)
			}
		}
	}
}

// propagateExternalSeen applies a read/unread flag to every cached copy of the
// message for a connected account. Gmail exposes one message through several
// label folders; setting \Seen on only the copy that happened to be open can
// leave the provider's Inbox copy unread, and the next flag reconcile then
// restores the local row to unread.
func (s *Server) propagateExternalSeen(ctx context.Context, conn *imapclient.Client, user userRecord, account mailAccountRecord, action imapAction) {
	if action.SetSeen == nil || action.MessageID == "" {
		return
	}
	type copyRef struct {
		folder string
		uid    uint32
	}
	copies := []copyRef{}
	if s.store != nil && s.store.DB != nil {
		rows, err := s.store.DB.Query(ctx, `
			SELECT imap_folder, uid
			FROM public.mail_messages
			WHERE user_id=$1 AND account_id=$2 AND message_id=$3 AND uid > 0
			  AND folder NOT IN ('sent', 'drafts')
			ORDER BY imap_folder
		`, user.ID, account.ID, action.MessageID)
		if err == nil {
			for rows.Next() {
				var folder string
				var uid int64
				if rows.Scan(&folder, &uid) == nil && folder != "" && uid > 0 {
					copies = append(copies, copyRef{folder: folder, uid: uint32(uid)})
				}
			}
			rows.Close()
		}
	}
	if len(copies) == 0 && action.SourceUID > 0 && action.SourceFolder != "" {
		copies = append(copies, copyRef{folder: action.SourceFolder, uid: uint32(action.SourceUID)})
	}
	seen := *action.SetSeen
	add := []string{}
	remove := []string{}
	if seen {
		add = append(add, imap.SeenFlag)
	} else {
		remove = append(remove, imap.SeenFlag)
	}
	applied := 0
	seenFolders := map[string]bool{}
	for _, copy := range copies {
		key := fmt.Sprintf("%s\x00%d", copy.folder, copy.uid)
		if seenFolders[key] {
			continue
		}
		seenFolders[key] = true
		if err := setIMAPFlags(conn, copy.folder, copy.uid, add, remove); err != nil {
			s.logger.Warn("connected IMAP copy flag update failed",
				"account", account.Email, "folder", copy.folder, "uid", copy.uid, "err", err)
			continue
		}
		applied++
	}
	if applied == 0 {
		return
	}
	_, err := s.store.DB.Exec(ctx, `
		UPDATE public.mail_messages SET unread=$4, updated_at=now()
		WHERE user_id=$1 AND account_id=$2 AND message_id=$3
		  AND folder NOT IN ('sent', 'drafts')
	`, user.ID, account.ID, action.MessageID, !seen)
	if err != nil {
		s.logger.Warn("connected IMAP local flag update failed", "account", account.Email, "message", action.MessageID, "err", err)
	}
}

func (s *Server) actionIMAPFolder(ctx context.Context, account mailAccountRecord, state map[string]any, appFolder string) string {
	if account.Kind == mailAccountKindNative {
		if folder := imapFolderFor(appFolder); folder != "" {
			return folder
		}
		if state != nil {
			return folderIMAPName(state, appFolder)
		}
		return ""
	}
	role := appFolder
	if appFolder == "spam" {
		role = "junk"
	}
	var remote string
	_ = s.store.DB.QueryRow(ctx, `
		SELECT remote_name FROM public.mail_folders
		WHERE account_id=$1 AND (id=$2 OR role=$3 OR remote_name=$2)
		ORDER BY CASE WHEN id=$2 THEN 0 WHEN remote_name=$2 THEN 1 ELSE 2 END,
		         sync_enabled DESC, remote_name
		LIMIT 1
	`, account.ID, appFolder, role).Scan(&remote)
	return remote
}

func findIMAPUID(conn *imapclient.Client, mailbox, messageID string) (uint32, error) {
	if _, err := conn.Select(mailbox, false); err != nil {
		return 0, err
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header = textproto.MIMEHeader{"Message-Id": []string{messageID}}
	uids, err := conn.UidSearch(criteria)
	if err != nil {
		return 0, err
	}
	if len(uids) == 0 {
		return 0, nil
	}
	return uids[len(uids)-1], nil
}

func setIMAPFlags(conn *imapclient.Client, mailbox string, uid uint32, add, remove []string) error {
	if _, err := conn.Select(mailbox, false); err != nil {
		return err
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	if len(add) > 0 {
		values := make([]interface{}, len(add))
		for i, flag := range add {
			values[i] = flag
		}
		if err := conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), values, nil); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		values := make([]interface{}, len(remove))
		for i, flag := range remove {
			values[i] = flag
		}
		if err := conn.UidStore(set, imap.FormatFlagsOp(imap.RemoveFlags, true), values, nil); err != nil {
			return err
		}
	}
	return nil
}

func moveIMAPMessage(conn *imapclient.Client, source string, uid uint32, target string) error {
	if _, err := conn.Select(source, false); err != nil {
		return err
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	if err := conn.UidMove(set, target); err == nil {
		return nil
	}
	if err := conn.UidCopy(set, target); err != nil {
		return err
	}
	if err := conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
		return err
	}
	return conn.Expunge(nil)
}

func (s *Server) queueExpunge(state map[string]any, messageID, folder string) {
	if messageID == "" || folder == "" {
		return
	}
	entries := append([]any{}, stateSlice(state, "pendingExpunges")...)
	entries = append(entries, map[string]any{
		"id": newID("expunge"), "messageId": messageID, "folder": folder,
		"createdAt": nowISO(), "expiresAt": time.Now().Add(undoWindow).UnixMilli(),
	})
	state["pendingExpunges"] = entries
}

func (s *Server) cancelExpunge(state map[string]any, messageID string) {
	kept := []any{}
	for _, raw := range stateSlice(state, "pendingExpunges") {
		entry, _ := raw.(map[string]any)
		if entry == nil || stateString(entry, "messageId") == messageID {
			continue
		}
		kept = append(kept, raw)
	}
	state["pendingExpunges"] = kept
}

func (s *Server) processPendingExpunges(ctx context.Context) {
	if !s.mailSyncConfigured() {
		return
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT user_id::text, tenant_id::text FROM app_states
		WHERE jsonb_typeof(state->'pendingExpunges') = 'array'
		  AND jsonb_array_length(state->'pendingExpunges') > 0
	`)
	if err != nil {
		return
	}
	defer rows.Close()
	type box struct{ userID, tenantID string }
	boxes := []box{}
	for rows.Next() {
		var value box
		if err := rows.Scan(&value.userID, &value.tenantID); err == nil {
			boxes = append(boxes, value)
		}
	}
	for _, value := range boxes {
		user, err := s.userByID(ctx, value.userID)
		if err != nil {
			continue
		}
		lock := s.userMailLock(value.userID)
		lock.Lock()
		var expired []map[string]any
		_ = s.withState(value.userID, value.tenantID, func(state map[string]any) error {
			ensureStateCollections(state)
			now := time.Now().UnixMilli()
			for _, raw := range stateSlice(state, "pendingExpunges") {
				entry, _ := raw.(map[string]any)
				if entry != nil && stateInt(entry, "expiresAt") <= now {
					expired = append(expired, entry)
				}
			}
			return nil
		})
		if len(expired) == 0 {
			lock.Unlock()
			continue
		}
		if err := s.expungeIMAPMessages(user, expired); err != nil {
			s.logger.Warn("pending IMAP expunge failed", "user", user.Email, "err", err)
			lock.Unlock()
			continue
		}
		_ = s.withState(value.userID, value.tenantID, func(state map[string]any) error {
			ids := map[string]bool{}
			for _, entry := range expired {
				ids[stateString(entry, "id")] = true
			}
			kept := []any{}
			for _, raw := range stateSlice(state, "pendingExpunges") {
				entry, _ := raw.(map[string]any)
				if entry != nil && ids[stateString(entry, "id")] {
					continue
				}
				kept = append(kept, raw)
			}
			state["pendingExpunges"] = kept
			return nil
		})
		lock.Unlock()
	}
}

func (s *Server) expungeIMAPMessages(user userRecord, entries []map[string]any) error {
	if !s.mailAccountConfigured(user.Email) {
		return nil
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
		return err
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return err
	}
	for _, entry := range entries {
		folder := imapFolderFor(stateString(entry, "folder"))
		messageID := stateString(entry, "messageId")
		if folder == "" || messageID == "" {
			continue
		}
		uid, err := findIMAPUID(conn, folder, messageID)
		if err != nil || uid == 0 {
			continue
		}
		if err := setIMAPFlags(conn, folder, uid, []string{imap.DeletedFlag}, nil); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		folder := imapFolderFor(stateString(entry, "folder"))
		messageID := stateString(entry, "messageId")
		if folder == "" || messageID == "" {
			continue
		}
		if _, err := conn.Select(folder, false); err != nil {
			continue
		}
		if err := conn.Expunge(nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) userByID(ctx context.Context, userID string) (userRecord, error) {
	var user userRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, email::text, name, initials, color, role, status,
		       COALESCE(password_hash, ''), preferences, security, quota_bytes, used_bytes, enrollment_required
		FROM users WHERE id = $1
	`, userID).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color,
		&user.Role, &user.Status, &user.PasswordHash, &user.Preferences, &user.Security,
		&user.QuotaBytes, &user.UsedBytes, &user.EnrollmentRequired,
	)
	return user, err
}
