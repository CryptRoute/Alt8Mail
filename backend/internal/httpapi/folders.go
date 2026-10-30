package httpapi

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

var primaryFolderIDs = map[string]bool{
	"inbox": true, "starred": true, "sent": true, "drafts": true,
	"archive": true, "spam": true, "trash": true,
}

var folderLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _&'().+-]{0,59}$`)
var folderColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func defaultFolderIMAPName(id string) string {
	switch id {
	case "inbox":
		return "INBOX"
	case "sent":
		return "Sent"
	case "drafts":
		return "Drafts"
	case "archive":
		return "Archive"
	case "spam":
		return "Junk"
	case "trash":
		return "Trash"
	}
	return ""
}

// normalizeFolderRecords adds the metadata introduced with custom folders to
// old workspace snapshots without changing their labels or order.
func normalizeFolderRecords(folders []any) []any {
	result := make([]any, 0, len(folders))
	for index, raw := range folders {
		folder, _ := raw.(map[string]any)
		if folder == nil {
			continue
		}
		id := stateString(folder, "id")
		if id == "" {
			continue
		}
		if _, ok := folder["system"]; !ok {
			folder["system"] = primaryFolderIDs[id]
		}
		if stateString(folder, "imapFolder") == "" {
			if name := defaultFolderIMAPName(id); name != "" {
				folder["imapFolder"] = name
			} else if !stateBool(folder, "virtual") {
				folder["imapFolder"] = stateString(folder, "label")
			}
		}
		if _, ok := folder["order"]; !ok {
			folder["order"] = index
		}
		if stateString(folder, "badgeMode") == "" {
			folder["badgeMode"] = "unread"
		}
		if stateString(folder, "section") == "" {
			folder["section"] = "folders"
		}
		result = append(result, folder)
	}
	return result
}

func stateFolderList(state map[string]any) []map[string]any {
	raw := normalizeFolderRecords(stateSlice(state, "folders"))
	folders := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if folder, ok := item.(map[string]any); ok {
			folders = append(folders, folder)
		}
	}
	sort.SliceStable(folders, func(i, j int) bool {
		left, right := stateInt(folders[i], "order"), stateInt(folders[j], "order")
		if left == right {
			return strings.ToLower(stateString(folders[i], "label")) < strings.ToLower(stateString(folders[j], "label"))
		}
		return left < right
	})
	return folders
}

func findStateFolder(state map[string]any, id string) map[string]any {
	for _, folder := range stateFolderList(state) {
		if stateString(folder, "id") == id {
			return folder
		}
	}
	return nil
}

func folderIMAPName(state map[string]any, id string) string {
	if id == "" {
		return ""
	}
	if name := defaultFolderIMAPName(id); name != "" {
		return name
	}
	if folder := findStateFolder(state, id); folder != nil {
		if name := stateString(folder, "imapFolder"); name != "" {
			return name
		}
		if label := stateString(folder, "label"); label != "" {
			return label
		}
	}
	return id
}

func customFolderIMAPMap(state map[string]any) map[string]string {
	result := map[string]string{}
	for _, folder := range stateFolderList(state) {
		id := stateString(folder, "id")
		if primaryFolderIDs[id] || stateBool(folder, "virtual") {
			continue
		}
		if name := stateString(folder, "imapFolder"); name != "" {
			result[strings.ToLower(name)] = id
		}
	}
	return result
}

func reservedFolderLabel(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "inbox", "starred", "sent", "sent items", "sent messages", "drafts", "archive",
		"archives", "all mail", "spam", "junk", "junk e-mail", "trash", "deleted items", "deleted messages":
		return true
	}
	return false
}

func folderLabelTaken(folders []map[string]any, label, exceptID string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	for _, folder := range folders {
		if stateString(folder, "id") == exceptID {
			continue
		}
		if strings.ToLower(strings.TrimSpace(stateString(folder, "label"))) == label {
			return true
		}
	}
	return false
}

func (s *Server) withUserIMAP(user userRecord, fn func(*imapclient.Client) error) error {
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
		return fmt.Errorf("connect to IMAP: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return fmt.Errorf("IMAP login: %w", err)
	}
	return fn(conn)
}

func (s *Server) createIMAPFolder(user userRecord, name string) error {
	return s.withUserIMAP(user, func(conn *imapclient.Client) error {
		if err := conn.Create(name); err != nil && !strings.Contains(strings.ToLower(err.Error()), "exist") {
			return err
		}
		_ = conn.Subscribe(name)
		return nil
	})
}

func (s *Server) renameIMAPFolder(user userRecord, oldName, newName string) error {
	if oldName == "" || oldName == newName {
		return nil
	}
	return s.withUserIMAP(user, func(conn *imapclient.Client) error {
		return conn.Rename(oldName, newName)
	})
}

func (s *Server) deleteIMAPFolder(user userRecord, name string) error {
	if name == "" {
		return nil
	}
	return s.withUserIMAP(user, func(conn *imapclient.Client) error {
		if err := conn.Delete(name); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return err
		}
		return nil
	})
}

func (s *Server) expungeIMAPFolder(user userRecord, name string) error {
	if name == "" {
		return nil
	}
	return s.withUserIMAP(user, func(conn *imapclient.Client) error {
		if _, err := conn.Select(name, false); err != nil {
			return err
		}
		return conn.Expunge(nil)
	})
}

func folderAuditTarget(label string) string { return truncate(strings.TrimSpace(label), 120) }

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Label    string `json:"label"`
		ParentID string `json:"parentId"`
		Color    string `json:"color"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if !folderLabelPattern.MatchString(label) || reservedFolderLabel(label) {
		writeError(w, http.StatusBadRequest, "FOLDER_NAME", "Use 1–60 letters, numbers, spaces or common punctuation")
		return
	}
	color := strings.TrimSpace(body.Color)
	if color != "" && !folderColorPattern.MatchString(color) {
		writeError(w, http.StatusBadRequest, "FOLDER_COLOR", "Folder color must be a six-digit hex color")
		return
	}
	var created map[string]any
	err := s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		folders := stateFolderList(state)
		if folderLabelTaken(folders, label, "") {
			return fail(http.StatusConflict, "FOLDER_EXISTS", "A folder with that name already exists")
		}
		parentID := strings.TrimSpace(body.ParentID)
		if parentID != "" {
			parent := findStateFolder(state, parentID)
			if parent == nil || stateBool(parent, "virtual") {
				return fail(http.StatusBadRequest, "FOLDER_PARENT", "Parent folder not found")
			}
		}
		maxOrder := int64(0)
		for _, folder := range folders {
			if value := stateInt(folder, "order"); value > maxOrder {
				maxOrder = value
			}
		}
		id := newID("folder")
		created = map[string]any{
			"id": id, "label": label, "icon": "i-folder", "section": "folders",
			"system": false, "virtual": false, "imapFolder": label,
			"order": maxOrder + 1, "badgeMode": "unread", "color": color,
			"parentId": parentID, "createdAt": nowISO(),
		}
		if parentID != "" {
			created["parentId"] = parentID
		}
		state["folders"] = append(stateSlice(state, "folders"), created)
		appendStateAudit(state, "folder.created", label, sess.User.Name,
			"Created folder "+label, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := s.createIMAPFolder(sess.User, label); err != nil {
		_ = s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
			state["folders"] = stateRemoveFolder(stateFolderList(state), stateString(created, "id"))
			return nil
		})
		writeError(w, http.StatusBadGateway, "FOLDER_IMAP", "Could not create the mailbox folder: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "folder": created})
}

func stateRemoveFolder(folders []map[string]any, id string) []any {
	out := make([]any, 0, len(folders))
	for _, folder := range folders {
		if stateString(folder, "id") == id {
			continue
		}
		out = append(out, folder)
	}
	return out
}

func (s *Server) updateFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body struct {
		Label     *string `json:"label"`
		Color     *string `json:"color"`
		ParentID  *string `json:"parentId"`
		Section   *string `json:"section"`
		BadgeMode *string `json:"badgeMode"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	var updated map[string]any
	var oldIMAPName, newIMAPName string
	var scriptToPush string
	err := s.withMailState(r.Context(), sess.User, nil, true, func(state map[string]any) error {
		folder := findStateFolder(state, id)
		if folder == nil {
			return fail(http.StatusNotFound, "FOLDER_NOT_FOUND", "Folder not found")
		}
		if stateBool(folder, "virtual") {
			return fail(http.StatusBadRequest, "FOLDER_SYSTEM", "This virtual folder cannot be edited")
		}
		system := primaryFolderIDs[id] || stateBool(folder, "system")
		oldLabel := stateString(folder, "label")
		if body.Label != nil {
			if system {
				return fail(http.StatusBadRequest, "FOLDER_SYSTEM", "Primary folders cannot be renamed")
			}
			label := strings.TrimSpace(*body.Label)
			if !folderLabelPattern.MatchString(label) || reservedFolderLabel(label) {
				return fail(http.StatusBadRequest, "FOLDER_NAME", "Use 1–60 letters, numbers, spaces or common punctuation")
			}
			if folderLabelTaken(stateFolderList(state), label, id) {
				return fail(http.StatusConflict, "FOLDER_EXISTS", "A folder with that name already exists")
			}
			folder["label"] = label
			oldIMAPName = stateString(folder, "imapFolder")
			if oldIMAPName == "" {
				oldIMAPName = oldLabel
			}
			newIMAPName = label
			folder["imapFolder"] = label
		}
		if body.Color != nil {
			color := strings.TrimSpace(*body.Color)
			if color != "" && !folderColorPattern.MatchString(color) {
				return fail(http.StatusBadRequest, "FOLDER_COLOR", "Folder color must be a six-digit hex color")
			}
			folder["color"] = color
		}
		if body.ParentID != nil {
			if system {
				return fail(http.StatusBadRequest, "FOLDER_SYSTEM", "Primary folders cannot be moved")
			}
			parentID := strings.TrimSpace(*body.ParentID)
			if parentID == id {
				return fail(http.StatusBadRequest, "FOLDER_PARENT", "A folder cannot contain itself")
			}
			if parentID != "" && findStateFolder(state, parentID) == nil {
				return fail(http.StatusBadRequest, "FOLDER_PARENT", "Parent folder not found")
			}
			folder["parentId"] = parentID
		}
		if body.Section != nil {
			section := strings.TrimSpace(*body.Section)
			if section != "folders" && section != "favorites" {
				return fail(http.StatusBadRequest, "FOLDER_SECTION", "Folder section must be folders or favorites")
			}
			folder["section"] = section
		}
		if body.BadgeMode != nil {
			mode := strings.TrimSpace(*body.BadgeMode)
			if mode != "unread" && mode != "total" && mode != "none" {
				return fail(http.StatusBadRequest, "FOLDER_BADGE", "Folder count mode must be unread, total or none")
			}
			folder["badgeMode"] = mode
		}
		updated = deepCopyMap(folder)
		if newIMAPName != "" {
			if script, changed := regenerateSieveScript(state); changed {
				scriptToPush = script
			}
		}
		appendStateAudit(state, "folder.updated", stateString(folder, "label"), sess.User.Name,
			"Updated folder settings", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if oldIMAPName != "" && newIMAPName != "" && oldIMAPName != newIMAPName {
		if err := s.renameIMAPFolder(sess.User, oldIMAPName, newIMAPName); err != nil {
			// Put the old label back so the webmail and Dovecot do not drift.
			_ = s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
				if folder := findStateFolder(state, id); folder != nil {
					folder["label"] = oldIMAPName
					folder["imapFolder"] = oldIMAPName
				}
				return nil
			})
			writeError(w, http.StatusBadGateway, "FOLDER_IMAP", "Could not rename the mailbox folder: "+err.Error())
			return
		}
	}
	if scriptToPush != "" {
		if err := s.pushSieveScript(r.Context(), sess.User, scriptToPush); err != nil {
			s.logger.Warn("sieve folder rename update failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "folder": updated})
}

func (s *Server) reorderFolders(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		IDs []string `json:"ids"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "FOLDER_ORDER", "Provide at least one folder")
		return
	}
	err := s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		folders := stateFolderList(state)
		byID := map[string]map[string]any{}
		for _, folder := range folders {
			byID[stateString(folder, "id")] = folder
		}
		order := 0
		for _, id := range body.IDs {
			if folder := byID[id]; folder != nil {
				folder["order"] = order
				order++
			}
		}
		for _, folder := range folders {
			if _, listed := byID[stateString(folder, "id")]; !listed {
				folder["order"] = order
				order++
			}
		}
		state["folders"] = toAnyFolders(folders)
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func toAnyFolders(folders []map[string]any) []any {
	out := make([]any, 0, len(folders))
	for _, folder := range folders {
		out = append(out, folder)
	}
	return out
}

func (s *Server) markFolderRead(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	mutations := []imapAction{}
	affected := 0
	err := s.withMailState(r.Context(), sess.User, nil, true, func(state map[string]any) error {
		folder := findStateFolder(state, id)
		if folder == nil {
			return fail(http.StatusNotFound, "FOLDER_NOT_FOUND", "Folder not found")
		}
		imapName := folderIMAPName(state, id)
		for _, raw := range stateMessages(state) {
			message, _ := raw.(map[string]any)
			if message == nil || stateString(message, "folder") != id || !stateBool(message, "unread") {
				continue
			}
			message["unread"] = false
			seen := true
			mutations = append(mutations, imapAction{
				MessageID: stateString(message, "messageId"), SourceUID: stateInt(message, "imapUid"),
				SourceFolder: id, SourceIMAPFolder: imapName, SetSeen: &seen,
			})
			affected++
		}
		appendStateAudit(state, "folder.mark_read", stateString(folder, "label"), sess.User.Name,
			fmt.Sprintf("Marked %d messages read in %s", affected, stateString(folder, "label")), clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(sess.User, mutations); err != nil {
			s.logger.Warn("folder mark-read IMAP propagation failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
}

func (s *Server) emptyFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if primaryFolderIDs[id] && id != "spam" && id != "trash" {
		writeError(w, http.StatusBadRequest, "FOLDER_SYSTEM", "Inbox, Sent, Drafts and Archive cannot be emptied")
		return
	}
	mutations := []imapAction{}
	removed := 0
	imapName := ""
	err := s.withMailState(r.Context(), sess.User, nil, true, func(state map[string]any) error {
		folder := findStateFolder(state, id)
		if folder == nil {
			return fail(http.StatusNotFound, "FOLDER_NOT_FOUND", "Folder not found")
		}
		imapName = folderIMAPName(state, id)
		remaining := []any{}
		for _, raw := range stateMessages(state) {
			message, _ := raw.(map[string]any)
			if message == nil || stateString(message, "folder") != id {
				remaining = append(remaining, raw)
				continue
			}
			mutations = append(mutations, imapAction{
				MessageID: stateString(message, "messageId"), SourceUID: stateInt(message, "imapUid"),
				SourceFolder: id, SourceIMAPFolder: imapName, MarkDeleted: true,
			})
			removed++
		}
		state["messages"] = remaining
		appendStateAudit(state, "folder.empty", stateString(folder, "label"), sess.User.Name,
			fmt.Sprintf("Emptied %d messages from %s", removed, stateString(folder, "label")), clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(sess.User, mutations); err != nil {
			s.logger.Warn("folder empty IMAP propagation failed", "err", err)
		}
	}
	if imapName != "" {
		_ = s.expungeIMAPFolder(sess.User, imapName)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if primaryFolderIDs[id] {
		writeError(w, http.StatusBadRequest, "FOLDER_SYSTEM", "Primary folders cannot be deleted")
		return
	}
	mutations := []imapAction{}
	imapName := ""
	scriptToPush := ""
	label := ""
	err := s.withMailState(r.Context(), sess.User, nil, true, func(state map[string]any) error {
		folder := findStateFolder(state, id)
		if folder == nil {
			return fail(http.StatusNotFound, "FOLDER_NOT_FOUND", "Folder not found")
		}
		if stateBool(folder, "virtual") || stateBool(folder, "system") {
			return fail(http.StatusBadRequest, "FOLDER_SYSTEM", "Primary folders cannot be deleted")
		}
		label = stateString(folder, "label")
		imapName = folderIMAPName(state, id)
		for _, raw := range stateMessages(state) {
			message, _ := raw.(map[string]any)
			if message == nil || stateString(message, "folder") != id {
				continue
			}
			mutations = append(mutations, imapAction{
				MessageID: stateString(message, "messageId"), SourceUID: stateInt(message, "imapUid"),
				SourceFolder: id, SourceIMAPFolder: imapName, TargetFolder: "trash", TargetIMAPFolder: "Trash",
			})
			message["folder"] = "trash"
			message["imapFolder"] = "trash"
			message["imapUid"] = 0
		}
		state["folders"] = stateRemoveFolder(stateFolderList(state), id)
		if script, changed := s.rewriteSieveForFolder(state, id); changed {
			scriptToPush = script
		}
		appendStateAudit(state, "folder.deleted", label, sess.User.Name,
			"Moved folder mail to Trash and deleted the folder", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(sess.User, mutations); err != nil {
			s.logger.Warn("folder delete message move failed", "err", err)
		}
	}
	if err := s.deleteIMAPFolder(sess.User, imapName); err != nil {
		s.logger.Warn("folder delete IMAP failed", "err", err)
	}
	if scriptToPush != "" {
		if err := s.pushSieveScript(r.Context(), sess.User, scriptToPush); err != nil {
			s.logger.Warn("sieve folder delete update failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": label})
}

func regenerateSieveScript(state map[string]any) (string, bool) {
	sieve := stateMap(state, "sieve")
	if len(sieve) == 0 {
		return "", false
	}
	next := map[string]any{
		"revision": stateInt(sieve, "revision") + 1, "updatedAt": nowISO(),
		"rules": sieve["rules"], "vacation": sieve["vacation"], "forwarding": sieve["forwarding"],
	}
	script := renderSieveWithFolders(next, stateString(stateMap(state, "user"), "email"), folderMapForSieve(state))
	next["script"] = script
	state["sieve"] = next
	return script, true
}

func (s *Server) rewriteSieveForFolder(state map[string]any, folderID string) (string, bool) {
	sieve := stateMap(state, "sieve")
	if len(sieve) == 0 {
		return "", false
	}
	rules := stateSlice(sieve, "rules")
	nextRules := make([]any, 0, len(rules))
	changed := false
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		action, _ := rule["action"].(map[string]any)
		if stateString(action, "type") == "move" && stateString(action, "value") == folderID {
			changed = true
			continue
		}
		nextRules = append(nextRules, rule)
	}
	if !changed {
		return "", false
	}
	next := map[string]any{
		"revision": stateInt(sieve, "revision") + 1, "updatedAt": nowISO(),
		"rules": nextRules, "vacation": sieve["vacation"], "forwarding": sieve["forwarding"],
	}
	script := renderSieveWithFolders(next, stateString(stateMap(state, "user"), "email"), folderMapForSieve(state))
	next["script"] = script
	state["sieve"] = next
	return script, true
}

func folderMapForSieve(state map[string]any) map[string]string {
	result := map[string]string{}
	for _, folder := range stateFolderList(state) {
		id := stateString(folder, "id")
		name := stateString(folder, "imapFolder")
		if name == "" {
			name = defaultFolderIMAPName(id)
		}
		if name != "" {
			result[id] = name
		}
	}
	return result
}

func stateFolderByLabel(state map[string]any, label string) map[string]any {
	for _, folder := range stateFolderList(state) {
		if strings.EqualFold(stateString(folder, "label"), label) {
			return folder
		}
	}
	return nil
}
