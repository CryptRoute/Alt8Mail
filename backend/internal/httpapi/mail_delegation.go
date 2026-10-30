package httpapi

import (
	"context"
	"sort"
	"strings"
)

const (
	delegatedMessageLimit = 300
	delegatedIDPrefix     = "dlg:"
)

type mailboxDelegation struct {
	OwnerID    string
	OwnerName  string
	OwnerEmail string
	CanRead    bool
	CanSend    bool
}

func (s *Server) delegationsFor(ctx context.Context, tenantID, delegateID string) []mailboxDelegation {
	rows, err := s.store.DB.Query(ctx, `
		SELECT d.owner_user_id::text, u.name, u.email::text, d.can_read, d.can_send
		FROM mailbox_delegates d
		JOIN users u ON u.id = d.owner_user_id
		WHERE d.delegate_user_id = $1 AND d.tenant_id = $2
		  AND d.can_read = true AND u.deleted_at IS NULL AND u.status = 'active'
		ORDER BY u.name, u.email
	`, delegateID, tenantID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := []mailboxDelegation{}
	for rows.Next() {
		var item mailboxDelegation
		if rows.Scan(&item.OwnerID, &item.OwnerName, &item.OwnerEmail, &item.CanRead, &item.CanSend) == nil {
			result = append(result, item)
		}
	}
	return result
}

func (s *Server) delegationFor(ctx context.Context, tenantID, ownerID, delegateID string) (mailboxDelegation, bool) {
	var item mailboxDelegation
	err := s.store.DB.QueryRow(ctx, `
		SELECT d.owner_user_id::text, u.name, u.email::text, d.can_read, d.can_send
		FROM mailbox_delegates d
		JOIN users u ON u.id = d.owner_user_id
		WHERE d.tenant_id = $1 AND d.owner_user_id = $2 AND d.delegate_user_id = $3
		  AND d.can_read = true AND u.deleted_at IS NULL
	`, tenantID, ownerID, delegateID).Scan(&item.OwnerID, &item.OwnerName, &item.OwnerEmail, &item.CanRead, &item.CanSend)
	if err != nil || !item.CanRead {
		return mailboxDelegation{}, false
	}
	return item, true
}

// applyDelegatedMailboxes appends each delegated Inbox to the bootstrap payload
// as a virtual folder. Messages are cloned with a stable "dlg:<owner>:<id>" ID
// and their own attachment URLs, so opening, searching and replying work
// through the normal reader without mixing the owner's state into the
// delegate's account.
func (s *Server) applyDelegatedMailboxes(ctx context.Context, user userRecord, folders, messages []any) ([]any, []any) {
	delegations := s.delegationsFor(ctx, user.TenantID, user.ID)
	if len(delegations) == 0 {
		return folders, messages
	}
	seen := map[string]bool{}
	for _, message := range messages {
		if record, ok := message.(map[string]any); ok {
			seen[stateString(record, "id")] = true
		}
	}
	nextOrder := 9000
	for _, delegation := range delegations {
		ownerState, ok := s.loadStateFor(ctx, delegation.OwnerID)
		if !ok {
			continue
		}
		folderID := "delegate_" + delegation.OwnerID
		inbox := []map[string]any{}
		for _, raw := range stateMessages(ownerState) {
			message, _ := raw.(map[string]any)
			if message == nil || stateString(message, "folder") != "inbox" {
				continue
			}
			inbox = append(inbox, message)
		}
		sort.SliceStable(inbox, func(i, j int) bool {
			return stateString(inbox[i], "date") > stateString(inbox[j], "date")
		})
		unread := 0
		for _, message := range inbox {
			if stateBool(message, "unread") {
				unread++
			}
		}
		shown := inbox
		if len(shown) > delegatedMessageLimit {
			shown = shown[:delegatedMessageLimit]
		}
		for _, message := range shown {
			originalID := stateString(message, "id")
			delegatedID := delegatedIDPrefix + delegation.OwnerID + ":" + originalID
			if seen[delegatedID] {
				continue
			}
			clone := map[string]any{}
			for key, value := range message {
				clone[key] = value
			}
			clone["id"] = delegatedID
			clone["folder"] = folderID
			clone["delegateOwnerId"] = delegation.OwnerID
			clone["delegateOwnerName"] = delegation.OwnerName
			clone["delegateOwnerEmail"] = delegation.OwnerEmail
			clone["delegateCanSend"] = delegation.CanSend
			clone["delegateOriginalFolder"] = stateString(message, "folder")
			clone["attachments"] = delegatedAttachments(message["attachments"], delegation.OwnerID)
			messages = append(messages, clone)
			seen[delegatedID] = true
		}
		folders = append(folders, map[string]any{
			"id": folderID, "label": delegation.OwnerName + " · Inbox",
			"icon": "i-people", "section": "folders", "system": false, "delegated": true,
			"delegateOwnerId": delegation.OwnerID, "delegateOwnerEmail": delegation.OwnerEmail,
			"delegateCanSend": delegation.CanSend, "unread": unread, "total": len(inbox),
			"badgeMode": "count", "order": nextOrder,
		})
		nextOrder++
	}
	return folders, messages
}

func delegatedAttachments(raw any, ownerID string) any {
	list, _ := raw.([]any)
	if list == nil {
		return raw
	}
	cloned := make([]any, 0, len(list))
	for _, item := range list {
		attachment, _ := item.(map[string]any)
		if attachment == nil {
			continue
		}
		copyRecord := map[string]any{}
		for key, value := range attachment {
			copyRecord[key] = value
		}
		if url := stateString(copyRecord, "contentUrl"); url != "" && !strings.Contains(url, "owner=") {
			separator := "?"
			if strings.Contains(url, "?") {
				separator = "&"
			}
			copyRecord["contentUrl"] = url + separator + "owner=" + ownerID
		}
		cloned = append(cloned, copyRecord)
	}
	return cloned
}

// parseDelegatedMessageID splits "dlg:<owner uuid>:<message id>".
func parseDelegatedMessageID(id string) (ownerID, messageID string, ok bool) {
	if !strings.HasPrefix(id, delegatedIDPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, delegatedIDPrefix)
	index := strings.Index(rest, ":")
	if index <= 0 || index == len(rest)-1 {
		return "", "", false
	}
	return rest[:index], rest[index+1:], true
}

func delegatedReadOnlyAction(action string) bool {
	switch action {
	case "markRead", "markUnread", "star", "unstar", "flag", "unflag":
		return true
	}
	return false
}

// applyDelegatedActions mutates flags in the owner's mailbox on behalf of a
// delegate. Only reading-state actions are accepted; deleting, moving or
// releasing quarantine always stays with the mailbox owner.
func (s *Server) applyDelegatedActions(ctx context.Context, user userRecord, action string, delegatedIDs []string) (int, error) {
	if !delegatedReadOnlyAction(action) {
		return 0, fail(403, "DELEGATE_READ_ONLY", "Delegated mailboxes are read-only for this action")
	}
	owners := map[string][]string{}
	for _, id := range delegatedIDs {
		ownerID, messageID, ok := parseDelegatedMessageID(id)
		if !ok {
			continue
		}
		owners[ownerID] = append(owners[ownerID], messageID)
	}
	changed := 0
	for ownerID, messageIDs := range owners {
		if _, allowed := s.delegationFor(ctx, user.TenantID, ownerID, user.ID); !allowed {
			return changed, fail(403, "DELEGATE_FORBIDDEN", "You do not have access to this mailbox")
		}
		owner, err := s.userByID(ctx, ownerID)
		if err != nil || owner.TenantID != user.TenantID {
			return changed, fail(404, "DELEGATE_OWNER", "The delegated mailbox is no longer available")
		}
		mutations := []imapAction{}
		err = s.withMailState(ctx, owner, messageIDs, false, func(state map[string]any) error {
			for _, messageID := range messageIDs {
				message := findMessageIn(state, messageID)
				if message == nil {
					continue
				}
				mutation := imapAction{
					MessageID:    stateString(message, "messageId"),
					SourceUID:    stateInt(message, "imapUid"),
					SourceFolder: stateString(message, "imapFolder"),
				}
				if mutation.SourceFolder == "" {
					mutation.SourceFolder = folderIMAPName(state, stateString(message, "folder"))
				}
				switch action {
				case "markRead":
					message["unread"] = false
					seen := true
					mutation.SetSeen = &seen
				case "markUnread":
					message["unread"] = true
					seen := false
					mutation.SetSeen = &seen
				case "star":
					message["starred"] = true
					flagged := true
					mutation.SetFlagged = &flagged
				case "unstar":
					message["starred"] = false
					flagged := false
					mutation.SetFlagged = &flagged
				case "flag":
					message["flagged"] = true
					flagged := true
					mutation.SetFlagged = &flagged
				case "unflag":
					message["flagged"] = false
					flagged := false
					mutation.SetFlagged = &flagged
				}
				mutations = append(mutations, mutation)
				changed++
			}
			return nil
		})
		if err != nil {
			return changed, err
		}
		if len(mutations) > 0 {
			if err := s.applyIMAPActions(owner, mutations); err != nil {
				s.logger.Warn("delegated IMAP flags failed", "owner", owner.Email, "err", err)
			}
		}
	}
	return changed, nil
}
