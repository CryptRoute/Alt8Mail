package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type apiError struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *apiError) Error() string { return e.Message }

func fail(status int, code, message string) error {
	return &apiError{Status: status, Code: code, Message: message}
}

func failDetails(status int, code, message string, details any) error {
	return &apiError{Status: status, Code: code, Message: message, Details: details}
}

func writeFromError(w http.ResponseWriter, err error) {
	var failure *apiError
	if errors.As(err, &failure) {
		payload := map[string]any{"code": failure.Code, "message": failure.Message}
		if failure.Details != nil {
			payload["details"] = failure.Details
		}
		writeJSON(w, failure.Status, map[string]any{"error": payload})
		return
	}
	writeError(w, http.StatusInternalServerError, "INTERNAL", "Something went wrong on the server")
}

func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	if err := decodeJSON(w, r, target, limit); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return false
	}
	return true
}

func deepCopyMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func aliasFor(state map[string]any, email string) map[string]any {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, raw := range stateSlice(stateMap(state, "user"), "aliases") {
		alias, _ := raw.(map[string]any)
		if alias == nil {
			continue
		}
		if strings.ToLower(stateString(alias, "email")) == email {
			return alias
		}
	}
	return nil
}

func aliasPerson(alias map[string]any) map[string]any {
	label := stateString(alias, "label")
	email := stateString(alias, "email")
	return map[string]any{
		"name": label, "email": email,
		"initials": initialsFor(label, email), "color": colorFor(email),
	}
}

func personFor(state map[string]any, email, name string) map[string]any {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	for _, raw := range stateSlice(state, "people") {
		person, _ := raw.(map[string]any)
		if person != nil && strings.EqualFold(stateString(person, "email"), email) {
			return person
		}
	}
	if strings.EqualFold(stateString(stateMap(state, "user"), "email"), email) {
		return map[string]any{
			"name": stateString(stateMap(state, "user"), "name"), "email": email,
			"initials": stateString(stateMap(state, "user"), "initials"), "color": stateString(stateMap(state, "user"), "color"),
		}
	}
	if name == "" {
		name = displayNameFromEmail(email)
	}
	return map[string]any{"name": name, "email": email, "initials": initialsFor(name, email), "color": colorFor(email)}
}

func displayNameFromEmail(email string) string {
	local := strings.Split(email, "@")[0]
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func parseRecipients(value any, state map[string]any) ([]any, []string) {
	raw := []string{}
	switch input := value.(type) {
	case string:
		raw = splitAddresses(input)
	case []any:
		for _, item := range input {
			switch typed := item.(type) {
			case string:
				raw = append(raw, splitAddresses(typed)...)
			case map[string]any:
				raw = append(raw, splitAddresses(stateString(typed, "email"))...)
			}
		}
	}
	people := []any{}
	invalid := []string{}
	seen := map[string]bool{}
	for _, text := range raw {
		name := ""
		email := strings.TrimSpace(text)
		if open := strings.LastIndex(email, "<"); open >= 0 && strings.HasSuffix(strings.TrimSpace(email), ">") {
			name = strings.TrimSpace(email[:open])
			email = strings.TrimSpace(strings.TrimSuffix(email[open+1:], ">"))
		}
		email = strings.ToLower(email)
		if !looksLikeEmail(email) {
			invalid = append(invalid, text)
			continue
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		people = append(people, personFor(state, email, name))
	}
	return people, invalid
}

func splitAddresses(value string) []string {
	parts := []string{}
	for _, chunk := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
		if text := strings.TrimSpace(chunk); text != "" {
			parts = append(parts, text)
		}
	}
	return parts
}

var recipientPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func looksLikeEmail(value string) bool { return recipientPattern.MatchString(value) }

func normalizeAttachments(state map[string]any, value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	if len(items) > 20 {
		items = items[:20]
	}
	result := []any{}
	known, _ := state["attachments"].(map[string]any)
	for _, raw := range items {
		file, _ := raw.(map[string]any)
		if file == nil {
			continue
		}
		if id := stateString(file, "id"); id != "" && known != nil {
			if record, ok := known[id].(map[string]any); ok {
				result = append(result, publicAttachment(record))
				continue
			}
		}
		name := safeFileName(stateString(file, "name"))
		sizeBytes := stateInt(file, "sizeBytes")
		if sizeBytes < 0 {
			sizeBytes = 0
		}
		if sizeBytes > 100*1024*1024 {
			sizeBytes = 100 * 1024 * 1024
		}
		mime := stateString(file, "mime")
		if mime == "" {
			mime = mimeForExtension(name)
		}
		contentURL := ""
		if sample := stateString(file, "contentUrl"); strings.HasPrefix(sample, "/samples/") && !strings.Contains(sample, "..") {
			contentURL = sample
		}
		attachmentID := stateString(file, "id")
		if attachmentID == "" || !strings.HasPrefix(attachmentID, "att_") {
			attachmentID = newID("att")
		}
		previewable := contentURL != "" || strings.HasPrefix(mime, "image/")
		badge := attachmentBadgeType(mime, name)
		result = append(result, map[string]any{
			"id": attachmentID, "name": name, "size": formatBytes(sizeBytes), "sizeBytes": sizeBytes,
			"type": badge, "mime": mime, "previewable": previewable,
			"contentUrl": contentURL, "downloadUrl": contentURL,
		})
	}
	return result
}

func safeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	replacer := strings.NewReplacer("\\", "_", "/", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", "\r", "_", "\n", "_", "\t", "_")
	name = replacer.Replace(name)
	if len([]rune(name)) > 180 {
		name = string([]rune(name)[:180])
	}
	return name
}

func attachmentBadgeType(mime, name string) string {
	switch {
	case mime == "application/pdf":
		return "PDF"
	case strings.HasPrefix(mime, "image/"):
		return "IMG"
	case strings.Contains(mime, "spreadsheet") || strings.Contains(mime, "ms-excel"):
		return "XLS"
	case strings.Contains(mime, "word"):
		return "DOC"
	case strings.Contains(mime, "presentation") || strings.Contains(mime, "powerpoint"):
		return "PPT"
	case strings.Contains(mime, "zip"):
		return "ZIP"
	case strings.HasPrefix(mime, "text/"):
		return "TXT"
	}
	parts := strings.Split(name, ".")
	if len(parts) > 1 {
		extension := strings.ToUpper(parts[len(parts)-1])
		if len(extension) > 4 {
			extension = extension[:4]
		}
		return extension
	}
	return "FILE"
}

func mimeForExtension(name string) string {
	parts := strings.Split(strings.ToLower(name), ".")
	extension := ""
	if len(parts) > 1 {
		extension = parts[len(parts)-1]
	}
	switch extension {
	case "pdf":
		return "application/pdf"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "txt", "csv":
		return "text/plain"
	case "zip":
		return "application/zip"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	return "application/octet-stream"
}

func publicAttachment(record map[string]any) map[string]any {
	id := stateString(record, "id")
	return map[string]any{
		"id": id, "name": stateString(record, "name"), "type": stateString(record, "type"),
		"mime": stateString(record, "mime"), "size": stateString(record, "size"),
		"sizeBytes": stateInt(record, "sizeBytes"), "previewable": stateBool(record, "previewable"),
		"contentId": stateString(record, "contentId"), "disposition": stateString(record, "disposition"),
		"contentUrl":  "/api/attachments/" + id + "/content",
		"downloadUrl": "/api/attachments/" + id + "/content",
	}
}

func findMessageIn(state map[string]any, id string) map[string]any {
	return stateObjectByID(stateMessages(state), id)
}

func removeMessage(state map[string]any, id string) {
	state["messages"] = stateRemoveByID(stateMessages(state), id)
}

func messagePublic(state map[string]any, message map[string]any) map[string]any {
	return publicStateMessage(state, message)
}

func sortByDateDesc(items []any) []any {
	result := append([]any{}, items...)
	sort.SliceStable(result, func(i, j int) bool {
		left, _ := result[i].(map[string]any)
		right, _ := result[j].(map[string]any)
		return stateString(left, "date") > stateString(right, "date")
	})
	return result
}

func threadSummaries(scope, all []any) []any {
	type bucket struct {
		id       string
		messages []map[string]any
	}
	byThread := map[string]*bucket{}
	for _, raw := range all {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		id := stateString(message, "threadId")
		if id == "" {
			id = stateString(message, "id")
		}
		if byThread[id] == nil {
			byThread[id] = &bucket{id: id}
		}
		byThread[id].messages = append(byThread[id].messages, message)
	}
	seen := map[string]bool{}
	result := []any{}
	for _, raw := range scope {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		id := stateString(message, "threadId")
		if id == "" {
			id = stateString(message, "id")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		members := byThread[id].messages
		sort.SliceStable(members, func(i, j int) bool {
			return stateString(members[i], "date") < stateString(members[j], "date")
		})
		latest := members[len(members)-1]
		unread := 0
		for _, member := range members {
			if stateBool(member, "unread") {
				unread++
			}
		}
		result = append(result, map[string]any{
			"id": id, "subject": firstNonEmpty(stateString(latest, "subject"), "(no subject)"),
			"count": len(members), "unreadCount": unread, "date": stateString(latest, "date"),
			"latestId": stateString(latest, "id"),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, _ := result[i].(map[string]any)
		right, _ := result[j].(map[string]any)
		return stateString(left, "date") > stateString(right, "date")
	})
	return result
}

func numericString(value any) string {
	switch typed := value.(type) {
	case float64:
		return fmt.Sprintf("%.0f", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case json.Number:
		return typed.String()
	case int:
		return fmt.Sprintf("%d", typed)
	case string:
		return typed
	}
	return ""
}
