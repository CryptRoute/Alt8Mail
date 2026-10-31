package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type actionsBody struct {
	IDs        []string `json:"ids"`
	Action     string   `json:"action"`
	Folder     string   `json:"folder"`
	Reason     string   `json:"reason"`
	Note       string   `json:"note"`
	Categories []string `json:"categories"`
	Until      string   `json:"until"`
	RequestID  string   `json:"requestId"`
}

type readBody struct {
	IDs []string `json:"ids"`
}

type messagePatch struct {
	Unread  *bool `json:"unread"`
	Starred *bool `json:"starred"`
	Flagged *bool `json:"flagged"`
	Pinned  *bool `json:"pinned"`
}

type draftBody struct {
	ToText          *string `json:"toText"`
	To              any     `json:"to"`
	CC              any     `json:"cc"`
	BCC             any     `json:"bcc"`
	CCText          *string `json:"ccText"`
	BCCText         *string `json:"bccText"`
	Subject         *string `json:"subject"`
	BodyText        *string `json:"bodyText"`
	BodyHTML        *string `json:"bodyHtml"`
	Body            *string `json:"body"`
	FromEmail       string  `json:"fromEmail"`
	Attachments     []any   `json:"attachments"`
	ThreadID        string  `json:"threadId"`
	InReplyTo       *string `json:"inReplyTo"`
	WindowMode      string  `json:"windowMode"`
	Priority        string  `json:"priority"`
	Receipt         bool    `json:"receipt"`
	Delivery        bool    `json:"delivery"`
	SaveCopy        *bool   `json:"saveCopy"`
	ComposeKind     string  `json:"composeKind"`
	SourceMessageID string  `json:"sourceMessageId"`
}

func (s *Server) getFolders(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": toAnyFolders(stateFolderList(state))})
}

func (s *Server) getAliases(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	identities, err := s.mailIdentities(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ALIASES", "Could not load sending identities")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": identityAliases(identities), "identities": identities})
}

func (s *Server) getMessages(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	repairEncodedSubjects(state)
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "inbox"
	}
	if threadID := strings.TrimSpace(r.URL.Query().Get("thread")); threadID != "" {
		messages, loadErr := s.loadMailMessageThread(r.Context(), sess.User.ID, threadID)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "MESSAGES", "Could not load the conversation")
			return
		}
		filtered := make([]any, 0, len(messages))
		for _, message := range messages {
			filtered = append(filtered, publicStateMessage(state, message))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"folder": folder, "thread": threadID, "count": len(filtered), "messages": filtered,
			"threads": threadSummaries(filtered, filtered), "total": len(filtered),
			"offset": 0, "limit": len(filtered), "hasMore": false,
		})
		return
	}
	query := r.URL.Query().Get("q")
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	limit := 50
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 {
		limit = value
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if value, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && value > 0 {
		offset = value
	}
	var matched []map[string]any
	var page mailThreadPage
	var err error
	total := 0
	hasMore := false
	if query != "" {
		matched, _, err = s.searchMailMessages(r.Context(), sess.User.ID, query)
		total = len(matched)
	} else {
		page, err = s.loadMailThreadPage(r.Context(), sess.User.ID, folder, filter, limit, offset)
		matched = page.Messages
		total = page.Total
		hasMore = page.HasMore
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MESSAGES", "Could not load messages")
		return
	}
	filtered := make([]any, 0, len(matched))
	threadIDs := []string{}
	for _, message := range matched {
		if query == "" && !mailMessageMatchesScope(message, folder, filter) {
			continue
		}
		switch filter {
		case "unread":
			if !stateBool(message, "unread") {
				continue
			}
		case "starred":
			if !stateBool(message, "starred") && !stateBool(message, "flagged") {
				continue
			}
		case "attachments":
			if len(stateSlice(message, "attachments")) == 0 {
				continue
			}
		}
		filtered = append(filtered, publicStateMessage(state, message))
		threadIDs = append(threadIDs, stateString(message, "threadId"))
	}
	all := make([]any, 0, len(matched))
	if query == "" {
		for _, message := range matched {
			all = append(all, publicStateMessage(state, message))
		}
	} else {
		siblings, _, siblingErr := s.loadMailMessagesByThreads(r.Context(), sess.User.ID, uniqueStrings(threadIDs, 1000))
		if siblingErr != nil {
			siblings = nil
		}
		seen := map[string]bool{}
		for _, message := range append(append([]map[string]any{}, matched...), siblings...) {
			id := stateString(message, "id")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, publicStateMessage(state, message))
		}
	}
	filtered = sortByDateDesc(filtered)
	writeJSON(w, http.StatusOK, map[string]any{
		"folder": folder, "filter": filter, "count": len(filtered), "messages": filtered,
		"threads": threadSummaries(filtered, all), "total": total,
		"totalMessages": page.TotalMessages, "offset": offset, "limit": limit, "hasMore": hasMore,
	})
}

func (s *Server) loadMailMessageThread(ctx context.Context, userID, target string) ([]map[string]any, error) {
	var threadID string
	err := s.store.DB.QueryRow(ctx, `
		SELECT thread_id FROM mail_messages
		WHERE user_id=$1 AND (id=$2 OR thread_id=$2)
		LIMIT 1
	`, userID, target).Scan(&threadID)
	if err == pgx.ErrNoRows {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if threadID == "" {
		threadID = target
	}
	messages, _, err := s.loadMailMessagesByThreads(ctx, userID, []string{threadID})
	return messages, err
}

func mailMessageMatchesScope(message map[string]any, folder, filter string) bool {
	if stateBool(message, "ignored") {
		return false
	}
	snoozedUntil, _ := parseStateTime(stateString(message, "snoozedUntil"))
	snoozed := snoozedUntil.After(time.Now())
	if folder == "snoozed" {
		if !snoozed {
			return false
		}
	} else {
		if snoozed {
			return false
		}
		if folder == "starred" {
			if !stateBool(message, "starred") && !stateBool(message, "flagged") {
				return false
			}
			if stateString(message, "folder") == "spam" || stateString(message, "folder") == "trash" {
				return false
			}
		} else if folder != "" && folder != "all" && stateString(message, "folder") != folder {
			return false
		}
	}
	switch filter {
	case "unread":
		return stateBool(message, "unread")
	case "starred":
		return stateBool(message, "starred") || stateBool(message, "flagged")
	case "attachments":
		return len(stateSlice(message, "attachments")) > 0
	}
	return true
}

func (s *Server) patchMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body messagePatch
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	var result map[string]any
	var mutation imapAction
	hasMutation := false
	err := s.withMailState(r.Context(), sess.User, []string{id}, false, func(state map[string]any) error {
		message := findMessageIn(state, id)
		if message == nil {
			return fail(http.StatusNotFound, "NOT_FOUND", "Message not found")
		}
		mutation = imapAction{
			MessageID:    stateString(message, "messageId"),
			SourceUID:    stateInt(message, "imapUid"),
			SourceFolder: stateString(message, "imapFolder"),
		}
		if mutation.SourceFolder == "" {
			mutation.SourceFolder = stateString(message, "folder")
		}
		if body.Unread != nil {
			message["unread"] = *body.Unread
			seen := !*body.Unread
			mutation.SetSeen = &seen
			hasMutation = true
		}
		flaggedChanged := false
		flagged := stateBool(message, "starred") || stateBool(message, "flagged")
		if body.Starred != nil {
			message["starred"] = *body.Starred
			flaggedChanged = true
		}
		if body.Flagged != nil {
			message["flagged"] = *body.Flagged
			flaggedChanged = true
		}
		if flaggedChanged {
			flagged = stateBool(message, "starred") || stateBool(message, "flagged")
			mutation.SetFlagged = &flagged
			hasMutation = true
		}
		if body.Pinned != nil {
			// Pin state is a webmail preference and is not represented by an
			// IMAP system flag, so it never triggers flag propagation.
			message["pinned"] = *body.Pinned
		}
		result = map[string]any{"message": publicStateMessage(state, message)}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if result != nil {
		result["quota"] = s.mailQuotaForUser(r.Context(), sess.User.ID)
	}
	if hasMutation {
		if err := s.applyIMAPActions(sess.User, []imapAction{mutation}); err != nil {
			s.logger.Warn("IMAP flag propagation failed", "message", id, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body readBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	ids := uniqueStrings(body.IDs, 500)
	changed := 0
	mutations := []imapAction{}
	delegatedIDs := []string{}
	localIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, delegatedIDPrefix) {
			delegatedIDs = append(delegatedIDs, id)
		} else {
			localIDs = append(localIDs, id)
		}
	}
	if len(delegatedIDs) > 0 {
		delegatedChanged, err := s.applyDelegatedActions(r.Context(), sess.User, "markRead", delegatedIDs)
		if err != nil {
			writeFromError(w, err)
			return
		}
		changed += delegatedChanged
	}
	if len(localIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
		return
	}
	ids = localIDs
	err := s.withMailState(r.Context(), sess.User, ids, false, func(state map[string]any) error {
		for _, id := range ids {
			message := findMessageIn(state, id)
			if message != nil && stateBool(message, "unread") {
				message["unread"] = false
				folder := stateString(message, "imapFolder")
				if folder == "" {
					folder = stateString(message, "folder")
				}
				seen := true
				mutations = append(mutations, imapAction{
					MessageID: stateString(message, "messageId"), SourceUID: stateInt(message, "imapUid"),
					SourceFolder: folder, SetSeen: &seen,
				})
				changed++
			}
		}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(sess.User, mutations); err != nil {
			s.logger.Warn("IMAP read propagation failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

func webmailAdminRole(role string) bool {
	return role == "owner" || role == "admin"
}

// quarantineReleaseAction reports whether an action would take a message out
// of the quarantine (Junk) folder. Deleting to Trash and marking as spam are
// containment actions, not releases.
func quarantineReleaseAction(action, folder string) bool {
	switch action {
	case "notSpam", "archive":
		return true
	case "move":
		return folder != "spam" && folder != "trash"
	}
	return false
}

func (s *Server) messageActions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body actionsBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	ids := uniqueStrings(body.IDs, 500)
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "NO_MESSAGES", "Select at least one message")
		return
	}
	requestID := truncate(strings.TrimSpace(body.RequestID), 80)
	if requestID != "" {
		var cached []byte
		if err := s.store.DB.QueryRow(r.Context(), `
			SELECT response FROM mail_action_requests
			WHERE user_id=$1 AND request_id=$2 AND created_at > now() - interval '7 days'
		`, sess.User.ID, requestID).Scan(&cached); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cached)
			return
		}
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM mail_action_requests WHERE user_id=$1 AND created_at <= now() - interval '7 days'`, sess.User.ID)
	}
	allowed := map[string]bool{
		"archive": true, "move": true, "copy": true, "delete": true, "markRead": true, "markUnread": true,
		"star": true, "unstar": true, "spam": true, "notSpam": true, "report": true,
		"flag": true, "unflag": true, "pin": true, "unpin": true, "categorize": true,
		"snooze": true, "unsnooze": true, "ignore": true, "unignore": true, "block": true,
	}
	if !allowed[body.Action] {
		writeError(w, http.StatusBadRequest, "BAD_ACTION", "Unknown bulk action")
		return
	}
	if (body.Action == "move" || body.Action == "copy") && strings.TrimSpace(body.Folder) == "" {
		writeError(w, http.StatusBadRequest, "BAD_FOLDER", "Choose a valid destination folder")
		return
	}
	if body.Action == "snooze" {
		until, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(body.Until))
		if parseErr != nil || !until.After(time.Now().Add(time.Minute)) || until.After(time.Now().Add(366*24*time.Hour)) {
			writeError(w, http.StatusBadRequest, "BAD_SNOOZE", "Choose a snooze time between one minute and one year from now")
			return
		}
	}
	if body.Action == "categorize" {
		if len(body.Categories) > 12 {
			writeError(w, http.StatusBadRequest, "BAD_CATEGORIES", "Choose at most 12 categories")
			return
		}
		for _, category := range body.Categories {
			if strings.TrimSpace(category) == "" || len([]rune(category)) > 40 {
				writeError(w, http.StatusBadRequest, "BAD_CATEGORIES", "Category names must be 1–40 characters")
				return
			}
		}
	}
	delegatedIDs := []string{}
	localIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, delegatedIDPrefix) {
			delegatedIDs = append(delegatedIDs, id)
		} else {
			localIDs = append(localIDs, id)
		}
	}
	delegatedChanged := 0
	if len(delegatedIDs) > 0 {
		changed, err := s.applyDelegatedActions(r.Context(), sess.User, body.Action, delegatedIDs)
		if err != nil {
			writeFromError(w, err)
			return
		}
		delegatedChanged = changed
	}
	if len(localIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": delegatedChanged})
		return
	}
	ids = localIDs
	var response map[string]any
	mutations := []imapAction{}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	err := s.withMailState(r.Context(), sess.User, ids, false, func(state map[string]any) error {
		targets := []map[string]any{}
		for _, id := range ids {
			if message := findMessageIn(state, id); message != nil {
				targets = append(targets, message)
			}
		}
		if len(targets) == 0 {
			return fail(http.StatusNotFound, "NOT_FOUND", "No matching messages")
		}
		moveIMAPFolder := ""
		if body.Action == "move" || body.Action == "copy" {
			folder := findStateFolder(state, body.Folder)
			if folder == nil || stateBool(folder, "virtual") {
				return fail(http.StatusBadRequest, "BAD_FOLDER", "Choose a valid destination folder")
			}
			if body.Action == "move" {
				moveIMAPFolder = folderIMAPName(state, body.Folder)
			}
			if body.Action == "copy" {
				copied := 0
				for _, message := range targets {
					copyMessage := deepCopyMap(message)
					copyMessage["id"] = newID("msg")
					copyMessage["folder"] = body.Folder
					copyMessage["imapFolder"] = body.Folder
					copyMessage["imapUid"] = 0
					copyMessage["imapUidValidity"] = 0
					copyMessage["copiedFrom"] = stateString(message, "id")
					state["messages"] = append(stateMessages(state), copyMessage)
					copied++
				}
				appendStateAudit(state, "message.copy", fmt.Sprintf("%d message", copied),
					stateString(stateMap(state, "user"), "name"),
					fmt.Sprintf("Copied %d message to %s", copied, body.Folder), clientIP(r), sanitizeUserAgent(r.UserAgent()))
				response = map[string]any{"ok": true, "affected": copied, "copied": copied, "quota": s.mailQuotaForUser(r.Context(), sess.User.ID)}
				if requestID != "" {
					if encoded, marshalErr := json.Marshal(response); marshalErr == nil {
						_, _ = s.store.DB.Exec(r.Context(), `
							INSERT INTO mail_action_requests (user_id, request_id, response)
							VALUES ($1, $2, $3)
							ON CONFLICT (user_id, request_id) DO UPDATE SET response=EXCLUDED.response
						`, sess.User.ID, requestID, encoded)
					}
				}
				return nil
			}
		}
		if policy.Mail.Quarantine && !webmailAdminRole(sess.User.Role) {
			for _, message := range targets {
				if stateString(message, "folder") == "spam" && quarantineReleaseAction(body.Action, body.Folder) {
					return fail(http.StatusForbidden, "QUARANTINE_ADMIN_ONLY", "Your organization requires an administrator to release quarantined mail")
				}
			}
		}
		now := time.Now()
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM undo_actions WHERE user_id=$1 AND expires_at <= now()`, sess.User.ID)
		token := newID("undo")
		expiresAt := now.Add(undoWindow)
		snapshot := []any{}
		permanentlyDeleted := 0
		permanentIDs := []string{}
		for _, message := range targets {
			report, _ := message["spamReport"].(map[string]any)
			originalFolder := stateString(message, "folder")
			originalIMAPFolder := stateString(message, "imapFolder")
			if originalIMAPFolder == "" {
				originalIMAPFolder = originalFolder
			}
			entry := map[string]any{
				"id": stateString(message, "id"), "folder": originalFolder,
				"imapFolder": originalIMAPFolder, "imapUid": message["imapUid"],
				"messageId": stateString(message, "messageId"),
				"unread":    stateBool(message, "unread"), "starred": stateBool(message, "starred"),
				"flagged": stateBool(message, "flagged"), "pinned": stateBool(message, "pinned"),
				"spamReport": report, "categories": message["categories"],
				"snoozedUntil": stateString(message, "snoozedUntil"),
				"ignored":      stateBool(message, "ignored"), "ignoredAt": stateString(message, "ignoredAt"),
			}
			mutation := imapAction{
				MessageID:    stateString(message, "messageId"),
				SourceUID:    stateInt(message, "imapUid"),
				SourceFolder: originalIMAPFolder,
			}
			targetFolder := originalFolder
			permanent := false
			switch body.Action {
			case "archive":
				targetFolder = "archive"
			case "move":
				targetFolder = body.Folder
			case "delete":
				if originalFolder == "trash" || originalFolder == "drafts" {
					permanent = true
					entry["full"] = deepCopyMap(message)
					permanentlyDeleted++
					permanentIDs = append(permanentIDs, stateString(message, "id"))
					mutation.MarkDeleted = true
					s.queueExpunge(state, stateString(message, "messageId"), originalIMAPFolder)
				} else {
					targetFolder = "trash"
				}
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
				flagged := stateBool(message, "flagged")
				mutation.SetFlagged = &flagged
			case "flag":
				message["flagged"] = true
				flagged := true
				mutation.SetFlagged = &flagged
			case "unflag":
				message["flagged"] = false
				flagged := stateBool(message, "starred")
				mutation.SetFlagged = &flagged
			case "pin":
				message["pinned"] = true
			case "unpin":
				message["pinned"] = false
			case "categorize":
				categories := make([]any, 0, len(body.Categories))
				seen := map[string]bool{}
				for _, category := range body.Categories {
					category = truncate(strings.TrimSpace(category), 40)
					if category != "" && !seen[strings.ToLower(category)] {
						seen[strings.ToLower(category)] = true
						categories = append(categories, category)
					}
				}
				message["categories"] = categories
			case "snooze":
				message["snoozedUntil"] = strings.TrimSpace(body.Until)
				message["unread"] = false
				seen := true
				mutation.SetSeen = &seen
			case "unsnooze":
				delete(message, "snoozedUntil")
			case "ignore":
				message["ignored"] = true
				message["ignoredAt"] = nowISO()
				rememberIgnoredThread(state, stateString(message, "threadId"))
			case "unignore":
				message["ignored"] = false
				delete(message, "ignoredAt")
				forgetIgnoredThread(state, stateString(message, "threadId"))
			case "block":
				targetFolder = "spam"
				message["unread"] = false
				message["ignored"] = false
				delete(message, "ignoredAt")
				message["spamReport"] = map[string]any{
					"at": nowISO(), "reason": "blocked-sender", "note": truncate(body.Note, 500), "source": "message-menu",
				}
				seen, flagged := true, stateBool(message, "starred")
				mutation.SetSeen = &seen
				mutation.SetFlagged = &flagged
				from, _ := message["from"].(map[string]any)
				user := stateMap(state, "user")
				blocked := stateSlice(user, "blockedSenders")
				sender := strings.ToLower(stateString(from, "email"))
				found := false
				for _, raw := range blocked {
					if strings.EqualFold(fmt.Sprint(raw), sender) {
						found = true
						break
					}
				}
				if sender != "" && !found {
					blocked = append(blocked, sender)
					user["blockedSenders"] = blocked
				}
			case "spam":
				targetFolder = "spam"
				message["unread"] = false
				seen, flagged := true, stateBool(message, "starred")
				mutation.SetSeen = &seen
				mutation.SetFlagged = &flagged
			case "notSpam":
				targetFolder = "inbox"
				delete(message, "spamReport")
			case "report":
				targetFolder = "spam"
				message["unread"] = false
				message["flagged"] = true
				seen, flagged := true, true
				mutation.SetSeen = &seen
				mutation.SetFlagged = &flagged
				message["spamReport"] = map[string]any{
					"at": nowISO(), "reason": truncate(firstNonEmpty(body.Reason, "spam"), 40),
					"note": truncate(body.Note, 500), "source": "reading-pane",
				}
				if err := s.recordSecurityReport(r.Context(), sess.User, message, body.Reason, body.Note); err != nil {
					return err
				}
			}
			if !permanent && targetFolder != originalFolder {
				delete(message, "snoozedUntil")
				message["folder"] = targetFolder
				message["imapFolder"] = targetFolder
				message["imapUid"] = 0
				entry["destinationFolder"] = targetFolder
				entry["destinationIMAPFolder"] = targetFolder
				mutation.TargetFolder = targetFolder
				if body.Action == "move" {
					mutation.TargetIMAPFolder = moveIMAPFolder
				}
			}
			if mutation.MessageID != "" && mutation.SourceFolder != "" {
				mutations = append(mutations, mutation)
			}
			snapshot = append(snapshot, entry)
		}
		for _, id := range permanentIDs {
			removeMessage(state, id)
		}
		snapshotJSON, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO undo_actions (token, tenant_id, user_id, action, snapshot, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, token, sess.User.TenantID, sess.User.ID, body.Action, snapshotJSON, now, expiresAt); err != nil {
			return err
		}
		auditAction := map[string]string{
			"archive": "message.archive", "move": "message.move", "delete": "message.delete",
			"markRead": "message.markRead", "markUnread": "message.markUnread", "star": "message.star",
			"unstar": "message.unstar", "flag": "message.flag", "unflag": "message.unflag",
			"spam": "message.spam", "notSpam": "message.notSpam", "report": "message.report",
			"pin": "message.pin", "unpin": "message.unpin", "categorize": "message.categorize",
			"snooze": "message.snooze", "unsnooze": "message.unsnooze", "ignore": "message.ignore",
			"unignore": "message.unignore", "block": "message.block", "copy": "message.copy",
		}[body.Action]
		pastTense := map[string]string{
			"archive": "archived", "delete": "deleted", "markRead": "marked read", "markUnread": "marked unread",
			"star": "starred", "unstar": "unstarred", "flag": "flagged for follow-up", "unflag": "flag cleared",
			"spam": "marked as spam", "notSpam": "moved back to Inbox", "report": "reported to security",
			"pin": "pinned", "unpin": "unpinned", "categorize": "categorized", "snooze": "snoozed",
			"unsnooze": "returned to Inbox", "ignore": "ignored", "unignore": "no longer ignored",
			"block": "blocked", "copy": "copied",
		}[body.Action]
		if pastTense == "" {
			pastTense = body.Action
		}
		countLabel := fmt.Sprintf("%d message", len(targets))
		if len(targets) != 1 {
			countLabel += "s"
		}
		detail := countLabel + " " + pastTense
		switch body.Action {
		case "move":
			detail = fmt.Sprintf("Moved %s to %s", countLabel, body.Folder)
		case "delete":
			detail = countLabel + " deleted"
			if permanentlyDeleted > 0 {
				detail += fmt.Sprintf(" · %d removed permanently", permanentlyDeleted)
			} else {
				detail += " to Trash"
			}
		case "report":
			detail = fmt.Sprintf("%s reported · reason: %s", countLabel, truncate(firstNonEmpty(body.Reason, "spam"), 40))
			if body.Note != "" {
				detail += " · analyst note recorded"
			}
		case "snooze":
			detail = fmt.Sprintf("%s snoozed until %s", countLabel, body.Until)
		case "block":
			detail = countLabel + " blocked · sender added to the blocked list"
		case "categorize":
			detail = fmt.Sprintf("%s categorized as %s", countLabel, strings.Join(body.Categories, ", "))
		}
		appendStateAudit(state, auditAction, countLabel, stateString(stateMap(state, "user"), "name"), detail, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		response = map[string]any{
			"ok": true, "affected": len(targets), "permanentlyDeleted": permanentlyDeleted,
			"undoToken": token, "undoExpiresAt": expiresAt.UTC().Format(time.RFC3339),
			"outbound": stateOutbound(state),
		}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if response != nil {
		response["quota"] = s.mailQuotaForUser(r.Context(), sess.User.ID)
	}
	if requestID != "" && response != nil {
		if encoded, marshalErr := json.Marshal(response); marshalErr == nil {
			if _, insertErr := s.store.DB.Exec(r.Context(), `
				INSERT INTO mail_action_requests (user_id, request_id, response)
				VALUES ($1, $2, $3)
				ON CONFLICT (user_id, request_id) DO UPDATE SET response=EXCLUDED.response
			`, sess.User.ID, requestID, encoded); insertErr != nil {
				s.logger.Warn("message action receipt store failed", "err", insertErr)
			}
		}
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(sess.User, mutations); err != nil {
			s.logger.Warn("IMAP bulk action propagation failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) undoAction(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	token := r.PathValue("token")
	var response map[string]any
	mutations := []imapAction{}
	var action string
	var snapshotJSON []byte
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT action, snapshot FROM undo_actions
		WHERE token=$1 AND user_id=$2 AND expires_at > now()
	`, token, sess.User.ID).Scan(&action, &snapshotJSON); err != nil {
		writeError(w, http.StatusGone, "UNDO_EXPIRED", "The undo window has closed")
		return
	}
	snapshot := []any{}
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		writeError(w, http.StatusGone, "UNDO_EXPIRED", "The undo window has closed")
		return
	}
	err := s.withMailMetadataState(r.Context(), sess.User, func(state map[string]any) error {
		restored := 0
		for _, raw := range snapshot {
			item, _ := raw.(map[string]any)
			if item == nil {
				continue
			}
			id := stateString(item, "id")
			message := findMessageIn(state, id)
			permanent := false
			if message == nil {
				if full, ok := item["full"].(map[string]any); ok {
					message = full
					state["messages"] = append(stateMessages(state), message)
					permanent = true
				}
			}
			if message == nil {
				continue
			}
			originalFolder := stateString(item, "folder")
			originalIMAPFolder := stateString(item, "imapFolder")
			if originalIMAPFolder == "" {
				originalIMAPFolder = originalFolder
			}
			messageID := stateString(item, "messageId")
			if messageID == "" {
				messageID = stateString(message, "messageId")
			}
			currentFolder := stateString(message, "imapFolder")
			if currentFolder == "" {
				currentFolder = stateString(message, "folder")
			}
			seen := !stateBool(item, "unread")
			flagged := stateBool(item, "starred") || stateBool(item, "flagged")
			mutation := imapAction{
				MessageID: messageID, SourceUID: stateInt(message, "imapUid"),
				SourceFolder: currentFolder, SetSeen: &seen, SetFlagged: &flagged,
			}
			if permanent {
				mutation.SourceFolder = originalIMAPFolder
				mutation.ClearDeleted = true
				s.cancelExpunge(state, messageID)
			} else if destination := stateString(item, "destinationFolder"); destination != "" && destination != originalFolder {
				mutation.SourceFolder = destination
				mutation.TargetFolder = originalFolder
			}
			if mutation.MessageID != "" && mutation.SourceFolder != "" {
				mutations = append(mutations, mutation)
			}
			message["folder"] = originalFolder
			message["imapFolder"] = originalFolder
			message["imapUid"] = 0
			message["unread"] = stateBool(item, "unread")
			message["starred"] = stateBool(item, "starred")
			message["flagged"] = stateBool(item, "flagged")
			if pinned, ok := item["pinned"].(bool); ok {
				message["pinned"] = pinned
			}
			if report, ok := item["spamReport"].(map[string]any); ok && report != nil {
				message["spamReport"] = deepCopyMap(report)
			} else {
				delete(message, "spamReport")
			}
			if categories, ok := item["categories"].([]any); ok {
				message["categories"] = categories
			}
			if snoozed := stateString(item, "snoozedUntil"); snoozed != "" {
				message["snoozedUntil"] = snoozed
			} else {
				delete(message, "snoozedUntil")
			}
			ignored, hasIgnored := item["ignored"].(bool)
			if hasIgnored {
				message["ignored"] = ignored
			}
			if ignoredAt := stateString(item, "ignoredAt"); ignoredAt != "" {
				message["ignoredAt"] = ignoredAt
			} else {
				delete(message, "ignoredAt")
			}
			if action == "ignore" {
				forgetIgnoredThread(state, stateString(message, "threadId"))
			}
			restored++
		}
		appendStateAudit(state, "message.undo", fmt.Sprintf("%d message", restored), stateString(stateMap(state, "user"), "name"),
			"Undid "+action, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		response = map[string]any{"ok": true, "restored": restored}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM undo_actions WHERE token=$1 AND user_id=$2`, token, sess.User.ID)
	if response != nil {
		response["quota"] = s.mailQuotaForUser(r.Context(), sess.User.ID)
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(sess.User, mutations); err != nil {
			s.logger.Warn("IMAP undo propagation failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) getDrafts(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	stored, _, err := s.loadMailMessagesByFolder(r.Context(), sess.User.ID, "drafts", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DRAFTS", "Could not load drafts")
		return
	}
	drafts := make([]any, 0, len(stored))
	for _, message := range stored {
		drafts = append(drafts, publicStateMessage(state, message))
	}
	drafts = sortByDateDesc(drafts)
	writeJSON(w, http.StatusOK, map[string]any{"count": len(drafts), "drafts": drafts})
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body draftBody
	if !s.decodeBody(w, r, &body, 8*1024*1024) {
		return
	}
	var response map[string]any
	var savedDraft map[string]any
	var attachmentRecords map[string]any
	err := s.withMailState(r.Context(), sess.User, []string{id}, false, func(state map[string]any) error {
		existing := findMessageIn(state, id)
		if existing != nil && stateString(existing, "folder") != "drafts" {
			return fail(http.StatusConflict, "NOT_A_DRAFT", "That id belongs to a message, not a draft")
		}
		attachmentIDs := []string{}
		if body.Attachments == nil && existing != nil {
			attachmentIDs = attachmentIDsFromValue(existing["attachments"])
		} else {
			attachmentIDs = attachmentIDsFromValue(body.Attachments)
		}
		s.preloadAttachments(r.Context(), sess.User.ID, state, attachmentIDs)
		draft, err := drainDraft(state, body, existing)
		if err != nil {
			return err
		}
		draft["id"] = id
		if existing != nil {
			for key, value := range draft {
				existing[key] = value
			}
			draft = existing
		} else {
			state["messages"] = append(stateMessages(state), draft)
			appendStateAudit(state, "draft.created", firstNonEmpty(stateString(draft, "subject"), "(no subject)"),
				stateString(stateMap(state, "user"), "name"), "Draft started in compose", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		}
		savedDraft = deepCopyMap(draft)
		attachmentRecords, _ = state["attachments"].(map[string]any)
		response = map[string]any{"ok": true, "savedAt": nowISO(), "draft": publicStateMessage(state, draft)}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if savedDraft != nil {
		s.persistDraftToIMAP(sess.User, savedDraft, attachmentRecords)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	messageID := ""
	err := s.withMailState(r.Context(), sess.User, []string{id}, false, func(state map[string]any) error {
		existing := findMessageIn(state, id)
		if existing == nil || stateString(existing, "folder") != "drafts" {
			return fail(http.StatusNotFound, "NOT_FOUND", "Draft not found")
		}
		messageID = stateString(existing, "messageId")
		removeMessage(state, id)
		appendStateAudit(state, "draft.deleted", firstNonEmpty(stateString(existing, "subject"), "(no subject)"),
			stateString(stateMap(state, "user"), "name"), "Draft discarded", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.deleteIMAPDraft(sess.User, messageID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func drainDraft(state map[string]any, body draftBody, existing map[string]any) (map[string]any, error) {
	subject := ""
	bodyText := ""
	bodyHTML := ""
	toText := ""
	ccText := ""
	bccText := ""
	if existing != nil {
		subject = stateString(existing, "subject")
		bodyText = stateString(existing, "bodyText")
		bodyHTML = stateString(existing, "bodyHtml")
		toText = stateString(existing, "toText")
		ccText = stateString(existing, "ccText")
		bccText = stateString(existing, "bccText")
	}
	if body.Subject != nil {
		subject = truncate(*body.Subject, 500)
	}
	if body.BodyText != nil {
		bodyText = truncate(*body.BodyText, 200_000)
		if body.BodyHTML == nil {
			bodyHTML = ""
		}
	} else if body.Body != nil {
		bodyText = truncate(*body.Body, 200_000)
		if body.BodyHTML == nil {
			bodyHTML = ""
		}
	}
	if body.BodyHTML != nil {
		bodyHTML = truncate(*body.BodyHTML, 4_000_000)
	}
	bodyHTML, bodyText = normalizeComposeBody(bodyHTML, bodyText)
	if body.ToText != nil {
		toText = truncate(*body.ToText, 2000)
	} else if body.To != nil {
		if text, ok := body.To.(string); ok {
			toText = truncate(text, 2000)
		}
	}
	if body.CCText != nil {
		ccText = truncate(*body.CCText, 2000)
	} else if body.CC != nil {
		if text, ok := body.CC.(string); ok {
			ccText = truncate(text, 2000)
		}
	}
	if body.BCCText != nil {
		bccText = truncate(*body.BCCText, 2000)
	} else if body.BCC != nil {
		if text, ok := body.BCC.(string); ok {
			bccText = truncate(text, 2000)
		}
	}
	fromEmail := body.FromEmail
	if fromEmail == "" && existing != nil {
		fromEmail = stateString(stateMap(existing, "from"), "email")
	}
	if fromEmail == "" {
		fromEmail = stateString(stateMap(state, "user"), "email")
	}
	alias := aliasFor(state, fromEmail)
	if alias == nil || alias["canSend"] == false {
		return nil, fail(http.StatusForbidden, "BAD_FROM", "That From identity is not available for sending")
	}
	to, _ := parseRecipients(toText, state)
	cc, _ := parseRecipients(ccText, state)
	bcc, _ := parseRecipients(bccText, state)
	attachments := normalizeAttachments(state, body.Attachments)
	if body.Attachments == nil && existing != nil {
		attachments = normalizeAttachments(state, existing["attachments"])
	}
	now := nowISO()
	createdAt := now
	messageID := fmt.Sprintf("<%s@a8m.test>", newID("draft"))
	threadID := newID("t")
	inReplyTo := any(nil)
	priority := "normal"
	windowMode := "floating"
	saveCopy := true
	composeKind := ""
	sourceMessageID := ""
	auth := map[string]any{"spf": "pass", "dkim": "pass", "dmarc": "pass", "tls": "TLS 1.3", "ip": "127.0.0.1", "signer": "a8m.test", "firstContact": false, "warnings": []any{}}
	if existing != nil {
		createdAt = stateString(existing, "createdAt")
		messageID = stateString(existing, "messageId")
		threadID = stateString(existing, "threadId")
		inReplyTo = existing["inReplyTo"]
		priority = stateString(existing, "priority")
		windowMode = stateString(existing, "windowMode")
		composeKind = stateString(existing, "composeKind")
		sourceMessageID = stateString(existing, "sourceMessageId")
		if existing["saveCopy"] == false {
			saveCopy = false
		}
		if existingAuth, ok := existing["auth"].(map[string]any); ok {
			auth = existingAuth
		}
	}
	if body.ThreadID != "" {
		threadID = truncate(body.ThreadID, 100)
	}
	if body.InReplyTo != nil {
		inReplyTo = truncate(*body.InReplyTo, 400)
	}
	if body.Priority == "low" || body.Priority == "normal" || body.Priority == "high" {
		priority = body.Priority
	}
	if body.WindowMode == "floating" || body.WindowMode == "minimized" || body.WindowMode == "inline" || body.WindowMode == "closed" {
		windowMode = body.WindowMode
	}
	if body.SaveCopy != nil {
		saveCopy = *body.SaveCopy
	}
	if body.ComposeKind == "reply" || body.ComposeKind == "replyAll" || body.ComposeKind == "forward" {
		composeKind = body.ComposeKind
	}
	if body.SourceMessageID != "" {
		sourceMessageID = truncate(body.SourceMessageID, 120)
	}
	sizeBytes := int64(4*1024 + len(bodyText))
	for _, raw := range attachments {
		file, _ := raw.(map[string]any)
		if file != nil {
			sizeBytes += stateInt(file, "sizeBytes")
		}
	}
	return map[string]any{
		"id": nil, "folder": "drafts", "from": aliasPerson(alias), "fromEmail": stateString(alias, "email"),
		"to": to, "toText": toText, "cc": cc, "ccText": ccText, "bcc": bcc, "bccText": bccText,
		"subject": subject, "snippet": snippet(firstNonEmpty(bodyText, subject, "Draft")),
		"bodyHtml": bodyHTML, "bodyText": bodyText,
		"date": now, "updatedAt": now, "createdAt": createdAt,
		"unread": false, "starred": false, "draft": true, "attachments": attachments,
		"categories": []any{}, "classification": "Internal", "external": false, "auth": auth,
		"event": nil, "sizeBytes": sizeBytes, "messageId": messageID, "inReplyTo": inReplyTo,
		"threadId": threadID, "priority": priority, "receipt": body.Receipt, "delivery": body.Delivery,
		"saveCopy": saveCopy, "windowMode": windowMode, "viaApi": true,
		"composeKind": composeKind, "sourceMessageId": sourceMessageID,
	}, nil
}

func (s *Server) getQuota(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	writeJSON(w, http.StatusOK, s.mailQuota(r.Context(), sess.User.ID, state))
}

func (s *Server) getOutboundInfo(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateWithOutbox(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	writeJSON(w, http.StatusOK, stateOutbound(state))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	query := r.URL.Query().Get("q")
	if len([]rune(query)) > 500 {
		writeError(w, http.StatusBadRequest, "QUERY_TOO_LONG", "Search queries are limited to 500 characters")
		return
	}
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	matched, parsed, err := s.searchMailMessages(r.Context(), sess.User.ID, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SEARCH", "Could not search the mailbox")
		return
	}
	messages := make([]any, 0, len(matched))
	for _, message := range matched {
		messages = append(messages, publicStateMessage(state, message))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": query, "parsed": mailSearchParsed(query, parsed),
		"count": len(messages), "messages": messages,
	})
}

func (s *Server) getTrustProfile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	sender := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sender")))
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sender": sender, "trusted": trustedIn(state, sender)})
}

func (s *Server) messageTrust(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	message, loadErr := s.loadMailMessage(r.Context(), sess.User.ID, id)
	if loadErr != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Message not found")
		return
	}
	from, _ := message["from"].(map[string]any)
	if r.Method == http.MethodGet {
		auth, _ := message["auth"].(map[string]any)
		checks := []any{
			map[string]any{"name": "SPF", "result": firstNonEmpty(stateString(auth, "spf"), "none"), "detail": "Authorised sending host"},
			map[string]any{"name": "DKIM", "result": firstNonEmpty(stateString(auth, "dkim"), "none"), "detail": firstNonEmpty("Signature domain "+stateString(auth, "signer"), "No valid signature")},
			map[string]any{"name": "DMARC", "result": firstNonEmpty(stateString(auth, "dmarc"), "none"), "detail": map[bool]string{true: "Domain alignment policy", false: "Internal domain policy"}[stateBool(message, "external")]},
		}
		signer := stateString(auth, "signer")
		if signer == "" {
			signer = "—"
		}
		ip := stateString(auth, "ip")
		if ip == "" {
			ip = "—"
		}
		policy := "Internal sender · verified by Alt8Mail directory"
		if stateBool(message, "external") {
			policy = "External sender · verified against Alt8Mail DMARC policy"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"messageId": stateString(message, "id"), "sender": from, "external": stateBool(message, "external"),
			"trusted": messageTrusted(state, message), "firstContact": stateBool(auth, "firstContact"),
			"checks": checks, "tls": firstNonEmpty(stateString(auth, "tls"), "TLS 1.3"), "ip": ip, "signer": signer,
			"warnings": auth["warnings"], "policy": policy, "checkedAt": nowISO(),
		})
		return
	}
	if !stateBool(message, "external") {
		writeError(w, http.StatusBadRequest, "INTERNAL_SENDER", "Internal senders are already verified")
		return
	}
	var body struct {
		Trusted *bool `json:"trusted"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	trusted := body.Trusted == nil || *body.Trusted
	email := strings.ToLower(stateString(from, "email"))
	err := s.withState(sess.User.ID, sess.User.TenantID, func(current map[string]any) error {
		ensureStateCollections(current)
		user := stateMap(current, "user")
		list := stateTrusted(current)
		next := []any{}
		for _, entry := range list {
			if entry != email {
				next = append(next, entry)
			}
		}
		if trusted {
			next = append(next, email)
		}
		user["trustedSenders"] = next
		action := "trust.unmarked"
		if trusted {
			action = "trust.marked"
		}
		appendStateAudit(current, action, email, stateString(user, "name"),
			map[bool]string{true: "Trusted external sender", false: "Removed trust for external sender"}[trusted],
			clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sender": email, "trusted": trusted})
}

func messageTrusted(state map[string]any, message map[string]any) bool {
	if !stateBool(message, "external") {
		return true
	}
	from, _ := message["from"].(map[string]any)
	return trustedIn(state, stateString(from, "email"))
}

func searchTerms(query string) []string {
	terms := []string{}
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(token, ":") {
			terms = append(terms, strings.Trim(token, `"`))
		}
	}
	return terms
}

var searchOperatorPattern = regexp.MustCompile(`([A-Za-z]+):("[^"]*"|\S+)`)

func searchStateMessages(state map[string]any, raw string) []any {
	query := strings.TrimSpace(raw)
	if query == "" {
		return []any{}
	}
	filters := map[string][]string{}
	free := []string{}
	remaining := searchOperatorPattern.ReplaceAllStringFunc(query, func(match string) string {
		parts := searchOperatorPattern.FindStringSubmatch(match)
		operator := strings.ToLower(parts[1])
		value := strings.Trim(parts[2], `"`)
		switch operator {
		case "from", "to", "cc", "subject", "body", "filename", "attachment", "has", "before", "after", "is", "in", "larger", "smaller":
			filters[operator] = append(filters[operator], strings.ToLower(value))
			return ""
		}
		return match
	})
	for _, term := range strings.Fields(remaining) {
		term = strings.Trim(term, `"`)
		if term != "" {
			free = append(free, strings.ToLower(term))
		}
	}
	results := []any{}
	for _, rawMessage := range stateMessages(state) {
		message, _ := rawMessage.(map[string]any)
		if message == nil {
			continue
		}
		from, _ := message["from"].(map[string]any)
		fromText := strings.ToLower(stateString(from, "name") + " " + stateString(from, "email"))
		toText := recipientSearchText(message["to"])
		ccText := recipientSearchText(message["cc"])
		subject := strings.ToLower(stateString(message, "subject"))
		body := strings.ToLower(stateString(message, "bodyText"))
		attachmentText := ""
		for _, rawAttachment := range stateSlice(message, "attachments") {
			attachment, _ := rawAttachment.(map[string]any)
			if attachment != nil {
				attachmentText += strings.ToLower(stateString(attachment, "name")+" "+stateString(attachment, "type")) + " "
			}
		}
		if !allContain(filters["from"], fromText) || !allContain(filters["to"], toText) || !allContain(filters["cc"], ccText) ||
			!allContain(filters["subject"], subject) || !allContain(filters["body"], body) ||
			!allContain(filters["filename"], attachmentText) || !allContain(filters["attachment"], attachmentText) {
			continue
		}
		if hasValue(filters["has"], "attachment") || hasValue(filters["has"], "attachments") || hasValue(filters["has"], "file") || hasValue(filters["has"], "files") {
			if len(stateSlice(message, "attachments")) == 0 {
				continue
			}
		}
		if hasValue(filters["is"], "unread") && !stateBool(message, "unread") {
			continue
		}
		if hasValue(filters["is"], "read") && stateBool(message, "unread") {
			continue
		}
		if (hasValue(filters["is"], "starred") || hasValue(filters["is"], "flagged")) && !stateBool(message, "starred") && !stateBool(message, "flagged") {
			continue
		}
		if folder := firstValue(filters["in"]); folder != "" && folder != "all" && folder != "anywhere" && stateString(message, "folder") != folder {
			continue
		}
		date, _ := parseStateTime(stateString(message, "date"))
		if cutoff := firstValue(filters["before"]); cutoff != "" {
			if stamp, ok := parseSearchDate(cutoff); ok && !date.Before(stamp) {
				continue
			}
		}
		if cutoff := firstValue(filters["after"]); cutoff != "" {
			if stamp, ok := parseSearchDate(cutoff); ok && date.Before(stamp) {
				continue
			}
		}
		size := stateInt(message, "sizeBytes")
		if value := firstValue(filters["larger"]); value != "" {
			if limit, ok := parseSearchSize(value); ok && size <= limit {
				continue
			}
		}
		if value := firstValue(filters["smaller"]); value != "" {
			if limit, ok := parseSearchSize(value); ok && size >= limit {
				continue
			}
		}
		haystack := strings.ToLower(fromText + " " + toText + " " + ccText + " " + subject + " " + body + " " + attachmentText)
		matched := true
		for _, term := range free {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			results = append(results, publicStateMessage(state, message))
		}
	}
	return sortByDateDesc(results)
}

func recipientSearchText(value any) string {
	people, _ := value.([]any)
	parts := []string{}
	for _, raw := range people {
		person, _ := raw.(map[string]any)
		if person != nil {
			parts = append(parts, stateString(person, "name")+" "+stateString(person, "email"))
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func allContain(filters []string, haystack string) bool {
	for _, filter := range filters {
		if !strings.Contains(haystack, filter) {
			return false
		}
	}
	return true
}

func hasValue(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func parseSearchDate(value string) (time.Time, bool) {
	value = strings.ReplaceAll(value, "/", "-")
	stamp, err := time.Parse("2006-1-2", value)
	if err != nil {
		return time.Time{}, false
	}
	return stamp.UTC(), true
}

func parseSearchSize(value string) (int64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	multiplier := int64(1)
	for suffix, factor := range map[string]int64{"kb": 1024, "k": 1024, "mb": 1024 * 1024, "m": 1024 * 1024, "gb": 1024 * 1024 * 1024, "g": 1024 * 1024 * 1024, "b": 1} {
		if strings.HasSuffix(value, suffix) {
			multiplier = factor
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
			break
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return int64(number) * multiplier, true
}

func uniqueStrings(values []string, limit int) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) >= limit {
			break
		}
	}
	return result
}
