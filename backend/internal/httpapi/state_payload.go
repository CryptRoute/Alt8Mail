package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

const undoWindow = 30 * time.Second

func (s *Server) loadStateFor(ctx context.Context, userID string) (map[string]any, bool) {
	state, err := s.loadState(ctx, userID)
	if err != nil {
		s.logger.Warn("app state unavailable", "user", userID, "err", err)
		return map[string]any{}, false
	}
	return state, true
}

func publicStateUser(state map[string]any, user userRecord, prefs map[string]any, security map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range stateMap(state, "user") {
		if key == "password" || key == "security" {
			continue
		}
		out[key] = value
	}
	out["id"] = user.ID
	out["tenantId"] = user.TenantID
	out["email"] = user.Email
	out["name"] = user.Name
	out["initials"] = user.Initials
	out["color"] = user.Color
	out["role"] = user.Role
	out["preferences"] = prefs
	out["security"] = security
	out["forcePasswordChange"] = user.ForcePasswordChange
	out["passwordChangeRequired"] = passwordChangeRequired(user)
	out["passwordExpiresAt"] = nullableTime(user.PasswordExpiresAt)
	return out
}

func (s *Server) publicUserWithState(ctx context.Context, user userRecord) (map[string]any, map[string]any) {
	fallback := preferencesFor(user)
	security, _, _, _ := s.securityState(ctx, user)
	state, ok := s.loadStateFor(ctx, user.ID)
	if !ok {
		return publicUser(user), fallback
	}
	prefs := statePreferences(state, fallback)
	return publicStateUser(state, user, prefs, security), prefs
}

func statePreferences(state map[string]any, fallback map[string]any) map[string]any {
	prefs := map[string]any{}
	for key, value := range fallback {
		prefs[key] = value
	}
	for key, value := range stateMap(stateMap(state, "user"), "preferences") {
		prefs[key] = value
	}
	return prefs
}

func stateTrusted(state map[string]any) []string {
	result := []string{}
	user := stateMap(state, "user")
	if list, ok := user["trustedSenders"].([]any); ok {
		for _, raw := range list {
			if text, ok := raw.(string); ok {
				result = append(result, strings.ToLower(text))
			}
		}
	}
	return result
}

func trustedIn(state map[string]any, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, value := range stateTrusted(state) {
		if value == email {
			return true
		}
	}
	return false
}

func publicStateMessage(state map[string]any, message map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range message {
		out[key] = value
	}
	from, _ := message["from"].(map[string]any)
	external := stateBool(message, "external")
	out["trusted"] = !external || trustedIn(state, stateString(from, "email"))
	return out
}

func stateMessages(state map[string]any) []any {
	return stateSlice(state, "messages")
}

func stateQuota(state map[string]any) map[string]any {
	user := stateMap(state, "user")
	quota := stateMap(user, "quota")
	limit := stateInt(quota, "limitBytes")
	baseline := stateInt(quota, "baselineUsedBytes")
	dynamic := int64(0)
	for _, raw := range stateMessages(state) {
		message, _ := raw.(map[string]any)
		if message != nil {
			dynamic += stateInt(message, "sizeBytes")
		}
	}
	used := baseline + dynamic
	if used > limit {
		used = limit
	}
	folders := []any{map[string]any{"id": "_baseline", "label": "Provider baseline", "bytes": baseline}}
	perFolder := map[string]int64{}
	for _, raw := range stateMessages(state) {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		perFolder[stateString(message, "folder")] += stateInt(message, "sizeBytes")
	}
	for _, raw := range stateSlice(state, "folders") {
		folder, _ := raw.(map[string]any)
		if folder == nil || stateBool(folder, "virtual") {
			continue
		}
		id := stateString(folder, "id")
		folders = append(folders, map[string]any{"id": id, "label": stateString(folder, "label"), "bytes": perFolder[id]})
	}
	percentage := 0.0
	if limit > 0 {
		percentage = math.Round(float64(used)/float64(limit)*1000) / 10
	}
	return map[string]any{
		"source": "dovecot", "limitBytes": limit, "usedBytes": used,
		"dynamicBytes": dynamic, "percent": percentage,
		"label":   fmt.Sprintf("%s of %s", formatBytes(used), formatBytes(limit)),
		"folders": folders, "refreshedAt": nowISO(),
	}
}

func stateOutbound(state map[string]any) map[string]any {
	user := stateMap(state, "user")
	outbound := stateMap(user, "outbound")
	windowHours := stateInt(outbound, "windowHours")
	if windowHours <= 0 {
		windowHours = 24
	}
	limit := stateInt(outbound, "limit")
	if limit <= 0 {
		limit = 200
	}
	baseline := stateInt(outbound, "baselineUsed")
	cutoff := time.Now().Add(-time.Duration(windowHours) * time.Hour)
	committed := 0
	if events, ok := outbound["events"].([]any); ok {
		for _, raw := range events {
			event, _ := raw.(map[string]any)
			if event == nil {
				continue
			}
			stamp, err := time.Parse(time.RFC3339Nano, stateString(event, "at"))
			if err == nil && stamp.After(cutoff) {
				committed++
			}
		}
	}
	queued, scheduled := 0, 0
	for _, raw := range stateSlice(state, "outbox") {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		queued++
		if stateBool(item, "scheduled") {
			scheduled++
		}
	}
	used := baseline + int64(committed)
	usedWithQueued := used + int64(queued)
	percentage := 0.0
	if limit > 0 {
		percentage = math.Round(float64(usedWithQueued)/float64(limit)*1000) / 10
	}
	return map[string]any{
		"limit": limit, "windowHours": windowHours, "baseline": baseline,
		"committed": committed, "used": used, "queued": queued,
		"undoQueued": queued - scheduled, "scheduled": scheduled,
		"usedWithQueued": usedWithQueued,
		"remaining":      max(int64(0), limit-usedWithQueued),
		"percent":        percentage, "source": "postfwd",
		"refreshedAt": nowISO(),
	}
}

func publicOutboxItem(item map[string]any) map[string]any {
	sendAt := stateString(item, "sendAt")
	canCancel := false
	if stamp, err := time.Parse(time.RFC3339Nano, sendAt); err == nil {
		canCancel = time.Now().Before(stamp)
	}
	attachments, _ := item["attachments"].([]any)
	if attachments == nil {
		attachments = []any{}
	}
	to, _ := item["to"].([]any)
	cc, _ := item["cc"].([]any)
	bcc, _ := item["bcc"].([]any)
	body := ""
	if stateBool(item, "scheduled") {
		body = stateString(item, "bodyText")
	}
	return map[string]any{
		"id": stateString(item, "id"), "subject": firstNonEmpty(stateString(item, "subject"), "(no subject)"),
		"from": item["from"], "to": to, "cc": cc, "bcc": bcc,
		"bodyText": body, "bodyHtml": stateString(item, "bodyHtml"), "attachments": attachments,
		"queuedAt": stateString(item, "queuedAt"), "sendAt": sendAt,
		"holdMs": stateInt(item, "holdMs"), "canCancel": canCancel,
		"attachmentCount": len(attachments), "saveCopy": item["saveCopy"] != false,
		"threadId": stateString(item, "threadId"), "scheduled": stateBool(item, "scheduled"),
		"kind": map[bool]string{true: "scheduled", false: "undo"}[stateBool(item, "scheduled")],
	}
}

func statePendingSends(state map[string]any) []any {
	items := append([]any{}, stateSlice(state, "outbox")...)
	sort.SliceStable(items, func(i, j int) bool {
		left, _ := items[i].(map[string]any)
		right, _ := items[j].(map[string]any)
		return stateString(left, "sendAt") < stateString(right, "sendAt")
	})
	result := make([]any, 0, len(items))
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			result = append(result, publicOutboxItem(item))
		}
	}
	return result
}

func ensureStateCollections(state map[string]any) {
	defaults := map[string]any{
		"people": []any{}, "folders": []any{},
		"audit": []any{}, "pendingExpunges": []any{},
	}
	for key, value := range defaults {
		if _, exists := state[key]; !exists {
			state[key] = value
		}
	}
	user := stateMap(state, "user")
	if _, exists := user["trustedSenders"]; !exists {
		user["trustedSenders"] = []any{}
	}
	if _, exists := user["outbound"]; !exists {
		user["outbound"] = map[string]any{"limit": 200, "windowHours": 24, "baselineUsed": 0, "events": []any{}}
	}
	if _, exists := user["quota"]; !exists {
		user["quota"] = map[string]any{"limitBytes": 16 * 1024 * 1024 * 1024, "baselineUsedBytes": 0}
	}
	state["user"] = user
}

func appendStateAudit(state map[string]any, action, target, actor, detail, ip, userAgent string) {
	entry := map[string]any{
		"id": newID("aud"), "ts": nowISO(), "action": action,
		"target": truncate(target, 240), "actor": actor, "detail": truncate(detail, 500),
		"ip": ip, "userAgent": truncate(userAgent, 240),
	}
	list := append([]any{entry}, stateSlice(state, "audit")...)
	if len(list) > 1000 {
		list = list[:1000]
	}
	state["audit"] = list
}

func (s *Server) auditRequest(r *http.Request, sess *sessionRecord, action, target, detail string) {
	user := sess.User
	ip := clientIP(r)
	ua := sanitizeUserAgent(r.UserAgent())
	if user.ID != "" {
		if err := s.withState(user.ID, user.TenantID, func(state map[string]any) error {
			ensureStateCollections(state)
			appendStateAudit(state, action, target, user.Name, detail, ip, ua)
			return nil
		}); err != nil {
			s.logger.Warn("state audit failed", "err", err)
		}
	}
	s.insertAudit(user, action, target, detail, ip, ua)
}

func (s *Server) insertAudit(user userRecord, action, target, detail, ip, userAgent string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO audit_log (tenant_id, user_id, actor, action, target, detail, ip, user_agent)
			VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, NULLIF($7, '')::inet, $8)
		`, user.TenantID, user.ID, user.Name, action, target, detail, ip, userAgent)
		if err != nil {
			s.logger.Warn("audit insert failed", "err", err)
		}
	}()
}

func filterStateAudit(state map[string]any, category string) []any {
	entries := append([]any{}, stateSlice(state, "audit")...)
	sort.SliceStable(entries, func(i, j int) bool {
		left, _ := entries[i].(map[string]any)
		right, _ := entries[j].(map[string]any)
		return stateString(left, "ts") > stateString(right, "ts")
	})
	if category == "" {
		return entries
	}
	match := func(action string) bool {
		switch category {
		case "login":
			return strings.HasPrefix(action, "auth.")
		case "send":
			return strings.HasPrefix(action, "send.")
		case "message":
			return strings.HasPrefix(action, "message.") || strings.HasPrefix(action, "trust.")
		case "rule":
			return strings.HasPrefix(action, "rule.") || strings.HasPrefix(action, "vacation.")
		case "draft":
			return strings.HasPrefix(action, "draft.")
		case "task":
			return strings.HasPrefix(action, "task.")
		case "files":
			return strings.HasPrefix(action, "file.") || strings.HasPrefix(action, "storage.") || strings.HasPrefix(action, "attachment.")
		case "preferences":
			return strings.HasPrefix(action, "preferences.")
		case "security":
			return strings.HasPrefix(action, "security.") || strings.HasPrefix(action, "auth.")
		case "tasks":
			return strings.HasPrefix(action, "task.")
		}
		return true
	}
	result := []any{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry != nil && match(stateString(entry, "action")) {
			result = append(result, entry)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func formatBytes(bytes int64) string {
	value := float64(bytes)
	switch {
	case value >= 1024*1024*1024:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value/(1024*1024*1024)), "0"), ".") + " GB"
	case value >= 1024*1024:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", value/(1024*1024)), "0"), ".") + " MB"
	case value >= 1024:
		return fmt.Sprintf("%.0f KB", math.Round(value/1024))
	default:
		return fmt.Sprintf("%.0f B", value)
	}
}

func parseStateTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if stamp, err := time.Parse(layout, value); err == nil {
			return stamp, true
		}
	}
	return time.Time{}, false
}
