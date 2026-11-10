package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	out["onboardingRequired"] = user.OnboardingRequired
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
	return publicUserFromState(state, user, fallback, security), statePreferences(state, fallback)
}

// publicUserFromState reuses an already-loaded workspace document. Bootstrap
// used to parse the same app_states row twice per request.
func publicUserFromState(state map[string]any, user userRecord, fallback, security map[string]any) map[string]any {
	return publicStateUser(state, user, statePreferences(state, fallback), security)
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

func ignoredThreadIn(state map[string]any, threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	for _, raw := range stateSlice(stateMap(state, "user"), "ignoredThreads") {
		if strings.TrimSpace(fmt.Sprint(raw)) == threadID {
			return true
		}
	}
	return false
}

func rememberIgnoredThread(state map[string]any, threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	user := stateMap(state, "user")
	threads := stateSlice(user, "ignoredThreads")
	for _, raw := range threads {
		if strings.TrimSpace(fmt.Sprint(raw)) == threadID {
			return
		}
	}
	user["ignoredThreads"] = append(threads, threadID)
}

func forgetIgnoredThread(state map[string]any, threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	user := stateMap(state, "user")
	threads := stateSlice(user, "ignoredThreads")
	kept := make([]any, 0, len(threads))
	for _, raw := range threads {
		if strings.TrimSpace(fmt.Sprint(raw)) != threadID {
			kept = append(kept, raw)
		}
	}
	user["ignoredThreads"] = kept
}

func blockedSenderIn(state map[string]any, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, raw := range stateSlice(stateMap(state, "user"), "blockedSenders") {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(raw)), email) {
			return true
		}
	}
	return false
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
	// Rows imported before the MIME decoder learned legacy charsets still hold
	// the raw encoded word in mail_messages. Decode on read so every API surface
	// (bootstrap, search, thread and folder views) presents the real subject.
	if subject := stateString(out, "subject"); subject != "" && strings.Contains(subject, "=?") {
		if decoded := decodeHeader(subject); decoded != subject {
			out["subject"] = decoded
			if stateString(out, "snippet") == subject {
				out["snippet"] = decoded
			}
		}
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
	committed := int64(0)
	if events, ok := outbound["events"].([]any); ok {
		for _, raw := range events {
			event, _ := raw.(map[string]any)
			if event == nil {
				continue
			}
			stamp, err := time.Parse(time.RFC3339Nano, stateString(event, "at"))
			if err == nil && stamp.After(cutoff) {
				// Each accepted recipient consumes one unit. Events written
				// before recipient counting existed fall back to one.
				units := stateInt(event, "recipients")
				if units <= 0 {
					units = 1
				}
				committed += units
			}
		}
	}
	queued, scheduled := 0, 0
	queuedRecipients := int64(0)
	for _, raw := range stateSlice(state, "outbox") {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		queued++
		queuedRecipients += outboxRecipientUnits(item)
		if stateBool(item, "scheduled") {
			scheduled++
		}
	}
	used := baseline + committed
	usedWithQueued := used + queuedRecipients
	percentage := 0.0
	if limit > 0 {
		percentage = math.Round(float64(usedWithQueued)/float64(limit)*1000) / 10
	}
	return map[string]any{
		"limit": limit, "windowHours": windowHours, "baseline": baseline,
		"committed": committed, "used": used, "queued": queued, "queuedRecipients": queuedRecipients,
		"undoQueued": queued - scheduled, "scheduled": scheduled,
		"usedWithQueued": usedWithQueued,
		"remaining":      max(int64(0), limit-usedWithQueued),
		"percent":        percentage, "source": "alt8mail+control-plane",
		"refreshedAt": nowISO(),
	}
}

// outboxRecipientUnits counts the recipients a queued message will consume.
// A queued item with no parsed recipient array still consumes one unit so old
// state cannot make the cap appear disabled.
func outboxRecipientUnits(item map[string]any) int64 {
	total := int64(0)
	for _, key := range []string{"to", "cc", "bcc"} {
		total += int64(len(stateSlice(item, key)))
	}
	if total <= 0 {
		return 1
	}
	return total
}

// outboundPayload starts from the webmail's rolling counter and, when the
// domain has a Control Plane per-mailbox cap, makes that cap authoritative.
// The Postfix policy daemon enforces the same cap for every SMTP client; this
// keeps the webmail's displayed limit and pre-send check in sync with it.
func (s *Server) outboundPayload(ctx context.Context, user userRecord, state map[string]any) map[string]any {
	outbound := stateOutbound(state)
	if outbound == nil || s.store == nil || s.store.DB == nil {
		return outbound
	}
	domain := domainOf(user.Email)
	if domain == "" {
		return outbound
	}
	var controlCap int
	if err := s.store.DB.QueryRow(ctx, `
		SELECT COALESCE(cs.user_daily_cap, 200)
		FROM public.mail_domains d
		LEFT JOIN control.domain_settings cs ON cs.domain_id=d.id
		WHERE d.domain=$1
	`, domain).Scan(&controlCap); err != nil {
		// A missing grant or table must not silently masquerade as the default
		// 200-message cap; leave the fallback meter in place and make the
		// configuration failure visible in the server log.
		if s.logger != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("control outbound cap lookup failed", "user", user.ID, "domain", domain, "err", err)
		}
		return outbound
	}
	outbound["controlDomain"] = domain
	return applyControlOutboundCap(outbound, controlCap)
}

// applyControlOutboundCap makes the Control Plane per-mailbox daily cap the
// authoritative webmail meter. A cap of zero means no per-mailbox limit, which
// must also stop the webmail pre-send check from blocking at the old default.
func applyControlOutboundCap(outbound map[string]any, controlCap int) map[string]any {
	outbound["controlUserDailyCap"] = controlCap
	used := stateInt(outbound, "usedWithQueued")
	if controlCap <= 0 {
		outbound["unlimited"] = true
		outbound["limit"] = 0
		outbound["remaining"] = nil
		outbound["percent"] = 0.0
		outbound["source"] = "control-plane"
		return outbound
	}
	outbound["unlimited"] = false
	outbound["limit"] = controlCap
	outbound["remaining"] = max(int64(0), int64(controlCap)-used)
	outbound["percent"] = math.Round(float64(used)/float64(controlCap)*1000) / 10
	outbound["source"] = "control-plane"
	return outbound
}

func outboundCapReached(outbound map[string]any, recipientUnits int) bool {
	limit := stateInt(outbound, "limit")
	return limit > 0 && stateInt(outbound, "usedWithQueued")+int64(recipientUnits) > limit
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
		"kind":           map[bool]string{true: "scheduled", false: "undo"}[stateBool(item, "scheduled")],
		"classification": firstNonEmpty(stateString(item, "classification"), "Internal"),
		"deliveryState":  stateString(item, "deliveryState"), "lastError": stateString(item, "lastError"),
		"dlpWarnings": stateSlice(item, "dlpWarnings"), "accountId": stateString(item, "accountId"),
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
	if len(stateSlice(state, "folders")) == 0 {
		folders := []any{}
		for _, folder := range defaultFolders() {
			folders = append(folders, folder)
		}
		state["folders"] = folders
	}
	folders := normalizeFolderRecords(stateSlice(state, "folders"))
	hasSnoozed := false
	for _, raw := range folders {
		if folder, _ := raw.(map[string]any); stateString(folder, "id") == "snoozed" {
			hasSnoozed = true
			break
		}
	}
	if !hasSnoozed {
		folders = append(folders, map[string]any{
			"id": "snoozed", "label": "Snoozed", "icon": "i-clock",
			"section": "favorites", "virtual": true, "system": true,
			"order": 1, "badgeMode": "none",
		})
	}
	state["folders"] = folders
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
	detail = delegatedAuditDetail(sess, detail)
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
	// Audit writes are synchronous: a security-relevant action must not be
	// acknowledged to the caller before its audit row is durable. The timeout
	// keeps an unhealthy database from stalling the request indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, user_id, actor, action, target, detail, ip, user_agent)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, NULLIF($7, '')::inet, $8)
	`, user.TenantID, user.ID, user.Name, action, target, detail, ip, userAgent)
	if err != nil {
		s.logger.Warn("audit insert failed", "err", err)
	}
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
		case "office":
			// Office events live in workspace_audit, not app-state audit.
			return false
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
