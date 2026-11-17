package httpapi

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

const sentReactionUndo = 8 * time.Second

func validReactionEmoji(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	semantic, ok := reactionEmojiSemantics[value]
	return value, semantic, ok
}

type reactionRecord struct {
	ID         string
	MessageID  string
	ThreadID   string
	Emoji      string
	Semantic   string
	Visibility string
	Status     string
	SendAfter  *time.Time
	SentAt     *time.Time
	Error      string
	UserID     string
	UserName   string
	RuleID     string
	CreatedAt  time.Time
}

func (r reactionRecord) public(reactorID string) map[string]any {
	item := map[string]any{
		"id": r.ID, "messageId": r.MessageID, "threadId": r.ThreadID,
		"emoji": r.Emoji, "semantic": r.Semantic, "visibility": r.Visibility,
		"status": r.Status, "error": r.Error, "own": r.UserID == reactorID,
		"userName": r.UserName, "createdAt": r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.SentAt != nil {
		item["sentAt"] = r.SentAt.UTC().Format(time.RFC3339)
	}
	if r.Status == "pending" && r.SendAfter != nil {
		item["undoUntil"] = r.SendAfter.UTC().Format(time.RFC3339)
	}
	if r.RuleID != "" {
		item["ruleId"] = r.RuleID
	}
	return item
}

func (s *Server) registerReactionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/reactions", s.requireUser(s.listReactions))
	mux.HandleFunc("GET /api/reaction-catalog", s.requireUser(s.reactionCatalog))
	mux.HandleFunc("POST /api/messages/{id}/reaction", s.requireUser(s.upsertReaction))
	mux.HandleFunc("DELETE /api/messages/{id}/reaction", s.requireUser(s.deleteReaction))
	mux.HandleFunc("POST /api/ai/reactions", s.requireUser(s.aiReactionSuggestions))
	mux.HandleFunc("GET /api/reaction-rules", s.requireUser(s.listReactionRules))
	mux.HandleFunc("POST /api/reaction-rules", s.requireUser(s.createReactionRule))
	mux.HandleFunc("PATCH /api/reaction-rules/{id}", s.requireUser(s.patchReactionRule))
	mux.HandleFunc("DELETE /api/reaction-rules/{id}", s.requireUser(s.deleteReactionRule))
}

func (s *Server) reactionsFeatureEnabled(ctx context.Context, tenantID string) bool {
	return s.tenantPolicyFor(ctx, tenantID).Features.Reactions
}

func (s *Server) reactionCatalog(w http.ResponseWriter, r *http.Request) {
	_ = s.requireSessionFromContext(r)
	writeJSON(w, http.StatusOK, reactionCatalogPayload())
}

func (s *Server) resolveReactionMessage(ctx context.Context, user userRecord, rawID string) (userRecord, string, map[string]any, error) {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return userRecord{}, "", nil, fail(http.StatusBadRequest, "MESSAGE", "Choose a message")
	}
	if ownerID, messageID, delegated := parseDelegatedMessageID(rawID); delegated {
		owner, _, err := s.collaborationOwner(ctx, user, ownerID)
		if err != nil {
			return userRecord{}, "", nil, err
		}
		message, err := s.loadMailMessage(ctx, owner.ID, messageID)
		if err != nil {
			return userRecord{}, "", nil, fail(http.StatusNotFound, "MESSAGE_NOT_FOUND", "Message not found")
		}
		return owner, messageID, message, nil
	}
	message, err := s.loadMailMessage(ctx, user.ID, rawID)
	if err != nil {
		return userRecord{}, "", nil, fail(http.StatusNotFound, "MESSAGE_NOT_FOUND", "Message not found")
	}
	return user, rawID, message, nil
}

func (s *Server) reactionSendAllowed(ctx context.Context, user, owner userRecord) bool {
	if owner.ID == user.ID {
		return true
	}
	delegation, ok := s.delegationFor(ctx, user.TenantID, owner.ID, user.ID)
	return ok && delegation.CanSend
}

func (s *Server) listReactions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.reactionsFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Reactions are turned off for this organization")
		return
	}
	owner, _, err := s.collaborationOwner(r.Context(), sess.User, r.URL.Query().Get("ownerId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("threadId"))
	if threadID == "" {
		writeError(w, http.StatusBadRequest, "THREAD", "A conversation is required")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT r.id, r.message_id, r.thread_id, r.emoji, r.semantic, r.visibility, r.status,
		       r.send_after, r.sent_at, r.error, r.user_id::text, COALESCE(u.name, ''),
		       COALESCE(r.rule_id, ''), r.created_at
		FROM public.mail_reactions r
		LEFT JOIN public.users u ON u.id=r.user_id
		WHERE r.owner_user_id=$1 AND r.thread_id=$2 AND r.dismissed_at IS NULL
		  AND (r.visibility='sent' OR r.user_id=$3)
		ORDER BY r.created_at ASC
	`, owner.ID, threadID, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var item reactionRecord
		if rows.Scan(&item.ID, &item.MessageID, &item.ThreadID, &item.Emoji, &item.Semantic,
			&item.Visibility, &item.Status, &item.SendAfter, &item.SentAt, &item.Error,
			&item.UserID, &item.UserName, &item.RuleID, &item.CreatedAt) == nil {
			items = append(items, item.public(sess.User.ID))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reactions": items, "quick": quickReactionEmojis})
}

func reactionUnavailableReason(message map[string]any) string {
	if message == nil {
		return "Message not found"
	}
	if stateBool(message, "draft") {
		return "Open the sent conversation before reacting"
	}
	folder := strings.ToLower(stateString(message, "folder"))
	if folder == "sent" || folder == "drafts" || folder == "spam" {
		return "Reactions are unavailable in this folder"
	}
	if unsubscribe, ok := message["unsubscribe"].(map[string]any); ok && len(unsubscribe) > 0 {
		return "Reactions are unavailable for newsletter and unsubscribe mail"
	}
	return ""
}

func (s *Server) upsertReaction(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.reactionsFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Reactions are turned off for this organization")
		return
	}
	var body struct {
		Emoji      string `json:"emoji"`
		Semantic   string `json:"semantic"`
		Visibility string `json:"visibility"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	emoji, fallbackSemantic, ok := validReactionEmoji(body.Emoji)
	if !ok {
		writeError(w, http.StatusBadRequest, "REACTION_EMOJI", "Choose one of the available reactions")
		return
	}
	semantic := truncate(strings.TrimSpace(body.Semantic), 60)
	if semantic == "" {
		semantic = fallbackSemantic
	}
	visibility := strings.ToLower(strings.TrimSpace(body.Visibility))
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "sent" {
		writeError(w, http.StatusBadRequest, "REACTION_VISIBILITY", "Choose private or sent")
		return
	}
	owner, messageID, message, err := s.resolveReactionMessage(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if reason := reactionUnavailableReason(message); reason != "" {
		writeError(w, http.StatusBadRequest, "REACTION_UNAVAILABLE", reason)
		return
	}
	if threadID := stateString(message, "threadId"); threadID != "" {
		var subscriberThread bool
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT EXISTS(
				SELECT 1 FROM public.mail_messages
				WHERE user_id=$1 AND thread_id=$2 AND unsubscribe IS NOT NULL AND unsubscribe <> '{}'::jsonb
			)
		`, owner.ID, threadID).Scan(&subscriberThread)
		if subscriberThread {
			writeError(w, http.StatusBadRequest, "REACTION_UNAVAILABLE", "Reactions are unavailable for newsletter and unsubscribe mail")
			return
		}
	}
	originalSender := normalizeEmailValue(stateString(message, "fromEmail"))
	status := "private"
	var sendAfter any
	if visibility == "sent" {
		if !s.reactionSendAllowed(r.Context(), sess.User, owner) {
			writeError(w, http.StatusForbidden, "REACTION_READ_ONLY", "This shared mailbox is read-only for your account")
			return
		}
		if originalSender == normalizeEmailValue(sess.User.Email) {
			writeError(w, http.StatusBadRequest, "REACTION_SELF", "Send a reaction to a message from someone else, or keep it private")
			return
		}
		if !looksLikeEmail(originalSender) {
			writeError(w, http.StatusBadRequest, "REACTION_RECIPIENT", "This message has no replyable sender address")
			return
		}
		var recent int
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT count(*) FROM public.mail_reactions
			WHERE user_id=$1 AND visibility='sent' AND dismissed_at IS NULL
			  AND created_at > now() - interval '1 hour'
		`, sess.User.ID).Scan(&recent)
		if recent >= 30 {
			writeError(w, http.StatusTooManyRequests, "REACTION_RATE_LIMIT", "You have sent many reactions in the last hour. Try again later.")
			return
		}
		status = "pending"
		sendAfter = time.Now().UTC().Add(sentReactionUndo)
	}
	threadID := stateString(message, "threadId")
	if threadID == "" {
		threadID = messageID
	}
	item := reactionRecord{}
	err = s.store.DB.QueryRow(r.Context(), `
		INSERT INTO public.mail_reactions
		  (id, tenant_id, owner_user_id, user_id, message_id, thread_id, emoji, semantic,
		   visibility, status, send_after, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (user_id, message_id) DO UPDATE SET
		  emoji=EXCLUDED.emoji, semantic=EXCLUDED.semantic, visibility=EXCLUDED.visibility,
		  status=EXCLUDED.status, send_after=EXCLUDED.send_after, sent_at=NULL,
		  dismissed_at=NULL, error='', rule_id=NULL, updated_at=now()
		RETURNING id, message_id, thread_id, emoji, semantic, visibility, status,
		          send_after, sent_at, error, user_id::text, '', COALESCE(rule_id, ''), created_at
	`, newID("reaction"), owner.TenantID, owner.ID, sess.User.ID, messageID, threadID,
		emoji, semantic, visibility, status, sendAfter).Scan(
		&item.ID, &item.MessageID, &item.ThreadID, &item.Emoji, &item.Semantic,
		&item.Visibility, &item.Status, &item.SendAfter, &item.SentAt, &item.Error,
		&item.UserID, &item.UserName, &item.RuleID, &item.CreatedAt)
	if err != nil {
		writeFromError(w, err)
		return
	}
	item.UserName = sess.User.Name
	s.invalidateReactionSummaryCache(r.Context(), owner.ID, threadID)
	auditTarget := firstNonEmpty(stateString(message, "subject"), "(no subject)")
	s.audit(r, sess.User, "reaction."+visibility, auditTarget,
		fmt.Sprintf("%s reaction %s %s", visibility, emoji, firstNonEmpty(stateString(message, "fromEmail"), "sender")))
	response := map[string]any{"ok": true, "reaction": item.public(sess.User.ID)}
	if visibility == "sent" {
		response["undoUntil"] = sendAfter.(time.Time).UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deleteReaction(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	owner, messageID, message, err := s.resolveReactionMessage(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var visibility, status string
	err = s.store.DB.QueryRow(r.Context(), `
		UPDATE public.mail_reactions SET dismissed_at=now(), updated_at=now()
		WHERE owner_user_id=$1 AND message_id=$2 AND user_id=$3 AND dismissed_at IS NULL
		RETURNING visibility, status
	`, owner.ID, messageID, sess.User.ID).Scan(&visibility, &status)
	if err != nil {
		writeError(w, http.StatusNotFound, "REACTION_NOT_FOUND", "Reaction not found")
		return
	}
	if threadID := stateString(message, "threadId"); threadID != "" {
		s.invalidateReactionSummaryCache(r.Context(), owner.ID, threadID)
	}
	s.audit(r, sess.User, "reaction.removed", messageID,
		fmt.Sprintf("Removed %s reaction · previous status %s", visibility, status))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wasSent": status == "sent", "pending": status == "pending"})
}

func (s *Server) reactionCounts(ctx context.Context, ownerID, reactorID, threadID string) map[string]int {
	counts := map[string]int{}
	rows, err := s.store.DB.Query(ctx, `
		SELECT emoji, count(*) FROM public.mail_reactions
		WHERE owner_user_id=$1 AND thread_id=$2 AND dismissed_at IS NULL
		  AND (visibility='sent' OR user_id=$3)
		GROUP BY emoji ORDER BY count(*) DESC, emoji ASC
	`, ownerID, threadID, reactorID)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var emoji string
		var count int
		if rows.Scan(&emoji, &count) == nil {
			counts[emoji] = count
		}
	}
	return counts
}

func (s *Server) reactionPromptForThread(ctx context.Context, ownerID, userID, threadID string) string {
	counts := s.reactionCounts(ctx, ownerID, userID, threadID)
	if len(counts) == 0 {
		return ""
	}
	parts := []string{}
	for emoji, count := range counts {
		parts = append(parts, fmt.Sprintf("%s x%d (%s)", emoji, count, reactionEmojiSemantics[emoji]))
	}
	return "Reaction signals visible to this reader: " + strings.Join(parts, ", ") + "."
}

// ---------------------------------------------------------------------------
// Reaction rules
// ---------------------------------------------------------------------------

type reactionRuleBody struct {
	Name            string `json:"name"`
	SenderContains  string `json:"senderContains"`
	SubjectContains string `json:"subjectContains"`
	Emoji           string `json:"emoji"`
	Enabled         *bool  `json:"enabled"`
}

func sanitizeReactionRule(body reactionRuleBody) (map[string]any, error) {
	name := truncate(strings.TrimSpace(body.Name), 120)
	sender := truncate(strings.ToLower(strings.TrimSpace(body.SenderContains)), 160)
	subject := truncate(strings.ToLower(strings.TrimSpace(body.SubjectContains)), 160)
	emoji, semantic, ok := validReactionEmoji(body.Emoji)
	if !ok {
		return nil, fail(http.StatusBadRequest, "REACTION_RULE_EMOJI", "Choose one of the available reactions")
	}
	if sender == "" && subject == "" {
		return nil, fail(http.StatusBadRequest, "REACTION_RULE_MATCH", "Add a sender or subject condition")
	}
	if name == "" {
		name = "Reaction rule"
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	return map[string]any{
		"name": name, "senderContains": sender, "subjectContains": subject,
		"emoji": emoji, "semantic": semantic, "enabled": enabled,
	}, nil
}

func (s *Server) listReactionRules(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.reactionsFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Reactions are turned off for this organization")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, name, sender_contains, subject_contains, emoji, enabled, created_at, updated_at
		FROM public.mail_reaction_rules WHERE user_id=$1 ORDER BY created_at ASC
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, sender, subject, emoji string
		var enabled bool
		var created, updated time.Time
		if rows.Scan(&id, &name, &sender, &subject, &emoji, &enabled, &created, &updated) == nil {
			items = append(items, map[string]any{
				"id": id, "name": name, "senderContains": sender, "subjectContains": subject,
				"emoji": emoji, "enabled": enabled,
				"createdAt": created.UTC().Format(time.RFC3339), "updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": items, "quick": quickReactionEmojis})
}

func (s *Server) createReactionRule(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.reactionsFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Reactions are turned off for this organization")
		return
	}
	var body reactionRuleBody
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	rule, err := sanitizeReactionRule(body)
	if err != nil {
		writeFromError(w, err)
		return
	}
	ruleID := newID("rrule")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_reaction_rules
		  (id, tenant_id, user_id, name, sender_contains, subject_contains, emoji, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, ruleID, sess.User.TenantID, sess.User.ID, rule["name"], rule["senderContains"],
		rule["subjectContains"], rule["emoji"], rule["enabled"])
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.audit(r, sess.User, "reaction_rule.created", ruleID, "Created reaction rule "+stateString(rule, "name"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": ruleID, "rule": rule})
}

func (s *Server) patchReactionRule(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.reactionsFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Reactions are turned off for this organization")
		return
	}
	var body struct {
		Name            *string `json:"name"`
		SenderContains  *string `json:"senderContains"`
		SubjectContains *string `json:"subjectContains"`
		Emoji           *string `json:"emoji"`
		Enabled         *bool   `json:"enabled"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	ruleID := strings.TrimSpace(r.PathValue("id"))
	var currentName, currentSender, currentSubject, currentEmoji string
	var currentEnabled bool
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, sender_contains, subject_contains, emoji, enabled
		FROM public.mail_reaction_rules WHERE id=$1 AND user_id=$2
	`, ruleID, sess.User.ID).Scan(&currentName, &currentSender, &currentSubject, &currentEmoji, &currentEnabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "REACTION_RULE_NOT_FOUND", "Reaction rule not found")
		return
	}
	merged := reactionRuleBody{
		Name: currentName, SenderContains: currentSender,
		SubjectContains: currentSubject, Emoji: currentEmoji, Enabled: &currentEnabled,
	}
	if body.Name != nil {
		merged.Name = strings.TrimSpace(*body.Name)
	}
	if body.SenderContains != nil {
		merged.SenderContains = strings.ToLower(strings.TrimSpace(*body.SenderContains))
	}
	if body.SubjectContains != nil {
		merged.SubjectContains = strings.ToLower(strings.TrimSpace(*body.SubjectContains))
	}
	if body.Emoji != nil {
		merged.Emoji = strings.TrimSpace(*body.Emoji)
	}
	if body.Enabled != nil {
		merged.Enabled = body.Enabled
	}
	rule, err := sanitizeReactionRule(merged)
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.mail_reaction_rules SET name=$3, sender_contains=$4, subject_contains=$5,
		  emoji=$6, enabled=$7, updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, ruleID, sess.User.ID, rule["name"], rule["senderContains"], rule["subjectContains"],
		rule["emoji"], rule["enabled"])
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.audit(r, sess.User, "reaction_rule.updated", ruleID, "Updated reaction rule "+stateString(rule, "name"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
}

func (s *Server) deleteReactionRule(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.reactionsFeatureEnabled(r.Context(), sess.User.TenantID) {
		writeError(w, http.StatusForbidden, "FEATURE_DISABLED", "Reactions are turned off for this organization")
		return
	}
	ruleID := strings.TrimSpace(r.PathValue("id"))
	_, err := s.store.DB.Exec(r.Context(), `DELETE FROM public.mail_reaction_rules WHERE id=$1 AND user_id=$2`, ruleID, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.audit(r, sess.User, "reaction_rule.deleted", ruleID, "Deleted reaction rule")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) reactionRuleWorker(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.applyAllReactionRules(ctx)
		}
	}
}

func (s *Server) applyAllReactionRules(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `SELECT id FROM public.mail_reaction_rules WHERE enabled ORDER BY created_at ASC LIMIT 500`)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_, _ = s.applyReactionRule(ctx, id)
	}
}

func (s *Server) applyReactionRule(ctx context.Context, ruleID string) (int, error) {
	var tenantID string
	if err := s.store.DB.QueryRow(ctx, `SELECT tenant_id::text FROM public.mail_reaction_rules WHERE id=$1`, ruleID).Scan(&tenantID); err != nil {
		return 0, err
	}
	if !s.reactionsFeatureEnabled(ctx, tenantID) {
		return 0, nil
	}
	tag, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.mail_reactions
		  (id, tenant_id, owner_user_id, user_id, message_id, thread_id, emoji, semantic,
		   visibility, status, rule_id)
		SELECT 'rct_' || replace(gen_random_uuid()::text, '-', ''), r.tenant_id, m.user_id, r.user_id,
		       m.id, m.thread_id, r.emoji, '', 'private', 'private', r.id
		FROM public.mail_reaction_rules r
		JOIN public.mail_messages m ON m.user_id=r.user_id
		WHERE r.id=$1 AND r.enabled
		  AND m.sent_at >= r.created_at
		  AND m.folder NOT IN ('sent','drafts','trash','spam')
		  AND NOT m.draft
		  AND (r.sender_contains='' OR strpos(lower(m.from_email::text), lower(r.sender_contains)) > 0)
		  AND (r.subject_contains='' OR strpos(lower(m.subject), lower(r.subject_contains)) > 0)
		  AND NOT EXISTS (SELECT 1 FROM public.mail_reactions x WHERE x.user_id=r.user_id AND x.message_id=m.id)
		ORDER BY m.sent_at DESC
		LIMIT 50
		ON CONFLICT (user_id, message_id) DO NOTHING
	`, ruleID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ---------------------------------------------------------------------------
// Outbound private/sent reaction delivery
// ---------------------------------------------------------------------------

func (s *Server) reactionDispatchWorker(ctx context.Context) {
	s.dispatchDueReactions(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchDueReactions(ctx)
		}
	}
}

func (s *Server) dispatchDueReactions(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT r.id, r.user_id::text, r.owner_user_id::text, r.message_id, r.emoji,
		       m.from_email::text, m.from_name, m.subject, m.message_id,
		       m.body_text, m.body_html, m.sent_at, t.name
		FROM public.mail_reactions r
		JOIN public.mail_messages m ON m.id=r.message_id AND m.user_id=r.owner_user_id
		JOIN public.tenants t ON t.id=r.tenant_id
		WHERE r.status='pending' AND r.dismissed_at IS NULL AND r.send_after <= now()
		ORDER BY r.send_after ASC LIMIT 50
	`)
	if err != nil {
		return
	}
	type dueReaction struct {
		id, userID, ownerID, messageID, emoji, toEmail, toName, subject, rfcMessageID string
		bodyText, bodyHTML                                                            string
		sentAt                                                                        time.Time
		tenantName                                                                    string
	}
	due := []dueReaction{}
	for rows.Next() {
		var item dueReaction
		if rows.Scan(&item.id, &item.userID, &item.ownerID, &item.messageID, &item.emoji,
			&item.toEmail, &item.toName, &item.subject, &item.rfcMessageID,
			&item.bodyText, &item.bodyHTML, &item.sentAt, &item.tenantName) == nil {
			due = append(due, item)
		}
	}
	rows.Close()
	for _, item := range due {
		reactor, err := s.userByID(ctx, item.userID)
		if err != nil || !looksLikeEmail(item.toEmail) || normalizeEmailValue(item.toEmail) == normalizeEmailValue(reactor.Email) {
			s.failReaction(ctx, item.id, "Reaction recipient is no longer available")
			continue
		}
		if !s.reactionsFeatureEnabled(ctx, reactor.TenantID) {
			s.failReaction(ctx, item.id, "Reactions are turned off for this organization")
			continue
		}
		subject := strings.TrimSpace(item.subject)
		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + firstNonEmpty(subject, "(no subject)")
		}
		reactorName := strings.TrimSpace(reactor.Name)
		if reactorName == "" {
			reactorName = reactor.Email
		}
		tenantName := strings.TrimSpace(item.tenantName)
		if tenantName == "" {
			tenantName = "your organization"
		}
		attribution := reactorName + " from " + tenantName + " reacted via Alt8Mail"
		originalSender := firstNonEmpty(strings.TrimSpace(item.toName), item.toEmail)
		originalWhen := item.sentAt.UTC().Format("Mon, Jan 2, 2006 at 3:04 PM MST")
		quoteLead := fmt.Sprintf("On %s, %s wrote:", originalWhen, originalSender)
		originalText := truncate(strings.TrimSpace(item.bodyText), 12000)
		if originalText == "" {
			originalText = truncate(htmlToPlainText(item.bodyHTML), 12000)
		}
		originalHTML := truncate(strings.TrimSpace(item.bodyHTML), 60000)
		if originalHTML == "" && originalText != "" {
			originalHTML = "<p>" + strings.ReplaceAll(html.EscapeString(originalText), "\n", "<br>") + "</p>"
		}
		quotedText := originalText
		if quotedText != "" {
			lines := strings.Split(quotedText, "\n")
			for index, line := range lines {
				lines[index] = "> " + line
			}
			quotedText = strings.Join(lines, "\n")
		}
		bodyText := item.emoji + "\n\n" + attribution + "\n\n" + quoteLead + "\n" + quotedText
		bodyHTML := `<div style="font-family:Arial,Helvetica,sans-serif;text-align:center;padding:28px 16px;color:#334155">` +
			`<div style="font-size:48px;line-height:56px" aria-hidden="true">` + html.EscapeString(item.emoji) + `</div>` +
			`<p style="margin:14px 0 0;font-size:13px;line-height:20px;color:#64748b">` + html.EscapeString(attribution) + `</p>` +
			`<div class="compose-quote" data-compose-quote="reply" style="margin-top:24px;padding-top:16px;border-top:1px solid #e2e8f0;text-align:left">` +
			`<div class="compose-quote-inner">` +
			`<p class="compose-quote-lead" style="margin:0 0 10px;font-size:12px;line-height:18px;color:#64748b;font-style:italic">` + html.EscapeString(quoteLead) + `</p>` +
			`<blockquote class="email-quote" style="margin:0;padding:0 0 0 12px;border-left:3px solid #cbd5e1;color:#475569">` + originalHTML + `</blockquote>` +
			`</div></div></div>`
		outbound := map[string]any{
			"from": map[string]any{"name": reactor.Name, "email": reactor.Email},
			"to":   []any{map[string]any{"name": item.toName, "email": item.toEmail}},
			"cc":   []any{}, "bcc": []any{},
			"subject": truncate(subject, 500), "bodyText": bodyText,
			"bodyHtml": bodyHTML, "saveCopy": false,
			"inReplyTo": item.rfcMessageID, "reaction": item.emoji,
		}
		if _, err := s.deliverSMTP(outbound, nil); err != nil {
			s.failReaction(ctx, item.id, err.Error())
			continue
		}
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE public.mail_reactions SET status='sent', sent_at=now(), error='', updated_at=now()
			WHERE id=$1 AND status='pending'
		`, item.id)
		s.insertAudit(reactor, "reaction.sent", truncate(subject, 240),
			"Sent reaction "+item.emoji, "", "reaction-worker")
	}
}

func (s *Server) failReaction(ctx context.Context, reactionID, message string) {
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.mail_reactions SET status='failed', error=$2, updated_at=now()
		WHERE id=$1 AND status='pending'
	`, reactionID, truncate(sanitizeMailText(message), 300))
}

func (s *Server) invalidateReactionSummaryCache(ctx context.Context, ownerID, threadID string) {
	if ownerID == "" || threadID == "" {
		return
	}
	_, _ = s.store.DB.Exec(ctx, `
		DELETE FROM public.mail_ai_cache c
		USING public.mail_messages m
		WHERE c.message_id=m.id AND m.user_id=$1 AND m.thread_id=$2
		  AND c.kind='summary'
	`, ownerID, threadID)
}
