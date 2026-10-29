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
	MessageID    string
	SourceUID    int64
	SourceFolder string
	TargetFolder string
	SetSeen      *bool
	SetFlagged   *bool
	MarkDeleted  bool
	ClearDeleted bool
}

func imapFolderFor(appFolder string) string {
	if name, ok := appToIMAPFolder[appFolder]; ok {
		return name
	}
	return ""
}

func (s *Server) applyIMAPActions(user userRecord, actions []imapAction) error {
	if !s.mailAccountConfigured(user.Email) || len(actions) == 0 {
		return nil
	}
	if !s.tenantPolicyFor(context.Background(), user.TenantID).Mail.IMAPAccess {
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
		return fmt.Errorf("connect to IMAP: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return fmt.Errorf("IMAP login: %w", err)
	}
	ensureIMAPFolders(conn)
	for _, action := range actions {
		source := imapFolderFor(action.SourceFolder)
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
			s.logger.Warn("IMAP action could not find message", "folder", source, "message", action.MessageID, "err", err)
			continue
		}
		currentFolder := source
		if action.ClearDeleted {
			if err := setIMAPFlags(conn, source, uid, nil, []string{imap.DeletedFlag}); err != nil {
				s.logger.Warn("IMAP clear deleted flag", "message", action.MessageID, "err", err)
			}
		}
		if action.TargetFolder != "" && action.TargetFolder != action.SourceFolder {
			target := imapFolderFor(action.TargetFolder)
			if target == "" {
				continue
			}
			if err := moveIMAPMessage(conn, source, uid, target); err != nil {
				s.logger.Warn("IMAP move failed", "message", action.MessageID, "from", source, "to", target, "err", err)
				// Fall through so flag changes still apply in the source folder.
			} else {
				currentFolder = target
				// The UID changed in the destination mailbox.
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
	}
	return nil
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
