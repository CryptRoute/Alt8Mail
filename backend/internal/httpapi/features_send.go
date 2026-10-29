package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type sendBody struct {
	DraftID      string   `json:"draftId"`
	FromEmail    string   `json:"fromEmail"`
	To           any      `json:"to"`
	CC           any      `json:"cc"`
	BCC          any      `json:"bcc"`
	Subject      string   `json:"subject"`
	BodyText     *string  `json:"bodyText"`
	BodyHTML     *string  `json:"bodyHtml"`
	Body         *string  `json:"body"`
	Attachments  []any    `json:"attachments"`
	ThreadID     string   `json:"threadId"`
	InReplyTo    *string  `json:"inReplyTo"`
	Priority     string   `json:"priority"`
	Receipt      *bool    `json:"receipt"`
	Delivery     *bool    `json:"delivery"`
	SaveCopy     *bool    `json:"saveCopy"`
	ScheduledFor string   `json:"scheduledFor"`
	SendAt       string   `json:"sendAt"`
	HoldSeconds  *float64 `json:"holdSeconds"`
}

type outboxPatch struct {
	FromEmail    string  `json:"fromEmail"`
	To           any     `json:"to"`
	CC           any     `json:"cc"`
	BCC          any     `json:"bcc"`
	Subject      *string `json:"subject"`
	BodyText     *string `json:"bodyText"`
	BodyHTML     *string `json:"bodyHtml"`
	Body         *string `json:"body"`
	Attachments  []any   `json:"attachments"`
	SendAt       string  `json:"sendAt"`
	ScheduledFor string  `json:"scheduledFor"`
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body sendBody
	if !s.decodeBody(w, r, &body, 12*1024*1024) {
		return
	}
	var response map[string]any
	draftMessageID := ""
	draftIDs := []string{}
	if body.DraftID != "" {
		draftIDs = append(draftIDs, body.DraftID)
	}
	err := s.withMailState(r.Context(), sess.User, draftIDs, false, func(state map[string]any) error {
		fromEmail := body.FromEmail
		if fromEmail == "" {
			fromEmail = stateString(stateMap(state, "user"), "email")
		}
		alias := aliasFor(state, fromEmail)
		if alias == nil || alias["canSend"] == false {
			return fail(http.StatusForbidden, "BAD_FROM", "That From identity is not available for sending")
		}
		to, invalid := parseRecipients(body.To, state)
		if len(invalid) > 0 {
			return fail(http.StatusBadRequest, "BAD_RECIPIENT", "Check these recipients: "+strings.Join(invalid, ", "))
		}
		if len(to) == 0 {
			return fail(http.StatusBadRequest, "NO_RECIPIENT", "Add at least one recipient")
		}
		cc, invalidCC := parseRecipients(body.CC, state)
		bcc, invalidBCC := parseRecipients(body.BCC, state)
		if len(invalidCC)+len(invalidBCC) > 0 {
			return fail(http.StatusBadRequest, "BAD_RECIPIENT", "Check these recipients: "+strings.Join(append(invalidCC, invalidBCC...), ", "))
		}
		if len(to)+len(cc)+len(bcc) > 50 {
			return fail(http.StatusBadRequest, "TOO_MANY_RECIPIENTS", "A message can have at most 50 recipients")
		}
		subject := truncate(body.Subject, 500)
		bodyText := ""
		bodyHTML := ""
		if body.BodyText != nil {
			bodyText = truncate(*body.BodyText, 200_000)
		} else if body.Body != nil {
			bodyText = truncate(*body.Body, 200_000)
		}
		if body.BodyHTML != nil {
			bodyHTML = truncate(*body.BodyHTML, 500_000)
		}
		bodyHTML, bodyText = normalizeComposeBody(bodyHTML, bodyText)
		var draft map[string]any
		if body.DraftID != "" {
			draft = findMessageIn(state, body.DraftID)
			if draft == nil || stateString(draft, "folder") != "drafts" {
				return fail(http.StatusConflict, "DRAFT_MISSING", "This draft is no longer available")
			}
			draftMessageID = stateString(draft, "messageId")
		}
		if err := s.enforceOutboundPolicy(r.Context(), sess.User.TenantID, append(append(append([]any{}, to...), cc...), bcc...), nil, s.tenantPolicyFor(r.Context(), sess.User.TenantID)); err != nil {
			return err
		}
		outbound := stateOutbound(state)
		if stateInt(outbound, "usedWithQueued") >= stateInt(outbound, "limit") {
			return failDetails(http.StatusTooManyRequests, "OUTBOUND_CAP",
				fmt.Sprintf("Daily outbound cap reached (%d/%d). Try again after the window resets.",
					stateInt(outbound, "usedWithQueued"), stateInt(outbound, "limit")),
				map[string]any{"outbound": outbound})
		}
		now := time.Now().UTC()
		var scheduledAt time.Time
		rawSchedule := firstNonEmpty(body.ScheduledFor, body.SendAt)
		if rawSchedule != "" {
			parsed, err := time.Parse(time.RFC3339Nano, rawSchedule)
			if err != nil {
				return fail(http.StatusBadRequest, "SCHEDULE_INVALID", "Choose a valid date and time")
			}
			if !parsed.After(now.Add(5 * time.Second)) {
				return fail(http.StatusBadRequest, "SCHEDULE_INVALID", "Scheduled time must be at least a few seconds in the future")
			}
			if parsed.After(now.Add(366 * 24 * time.Hour)) {
				return fail(http.StatusBadRequest, "SCHEDULE_INVALID", "Scheduled time can be at most one year ahead")
			}
			scheduledAt = parsed
		}
		holdMs := int64(8000)
		if body.HoldSeconds != nil {
			requested := int64(*body.HoldSeconds * 1000)
			if requested >= 5000 && requested <= 10000 {
				holdMs = requested
			}
		}
		threadID := body.ThreadID
		if threadID == "" && draft != nil {
			threadID = stateString(draft, "threadId")
		}
		if threadID == "" {
			threadID = newID("t")
		}
		inReplyTo := any(nil)
		if body.InReplyTo != nil {
			inReplyTo = truncate(*body.InReplyTo, 400)
		} else if draft != nil {
			inReplyTo = draft["inReplyTo"]
		}
		priority := body.Priority
		if priority != "low" && priority != "normal" && priority != "high" {
			if draft != nil && stateString(draft, "priority") != "" {
				priority = stateString(draft, "priority")
			} else {
				priority = "normal"
			}
		}
		receipt := body.Receipt != nil && *body.Receipt
		delivery := body.Delivery != nil && *body.Delivery
		saveCopy := body.SaveCopy == nil || *body.SaveCopy
		if draft != nil {
			if body.Receipt == nil && stateBool(draft, "receipt") {
				receipt = true
			}
			if body.Delivery == nil && stateBool(draft, "delivery") {
				delivery = true
			}
			if body.SaveCopy == nil && draft["saveCopy"] == false {
				saveCopy = false
			}
		}
		attachmentIDs := []string{}
		if body.Attachments == nil && draft != nil {
			attachmentIDs = attachmentIDsFromValue(draft["attachments"])
		} else {
			attachmentIDs = attachmentIDsFromValue(body.Attachments)
		}
		s.preloadAttachments(r.Context(), sess.User.ID, state, attachmentIDs)
		attachments := normalizeAttachments(state, body.Attachments)
		if body.Attachments == nil && draft != nil {
			attachments = normalizeAttachments(state, draft["attachments"])
		}
		if err := s.enforceOutboundPolicy(r.Context(), sess.User.TenantID, append(append(append([]any{}, to...), cc...), bcc...), attachments, s.tenantPolicyFor(r.Context(), sess.User.TenantID)); err != nil {
			return err
		}
		sendAt := now.Add(time.Duration(holdMs) * time.Millisecond)
		if !scheduledAt.IsZero() {
			sendAt = scheduledAt
			holdMs = 0
		}
		item := map[string]any{
			"id": newID("send"), "draftId": body.DraftID, "from": aliasPerson(alias),
			"to": to, "cc": cc, "bcc": bcc, "subject": subject,
			"bodyText": bodyText, "bodyHtml": bodyHTML,
			"attachments": attachments, "threadId": threadID, "inReplyTo": inReplyTo,
			"priority": priority, "receipt": receipt, "delivery": delivery, "saveCopy": saveCopy,
			"queuedAt": now.Format(time.RFC3339), "sendAt": sendAt.Format(time.RFC3339),
			"holdMs": holdMs, "scheduled": !scheduledAt.IsZero(),
			"ip": clientIP(r), "userAgent": sanitizeUserAgent(r.UserAgent()),
		}
		if draft != nil {
			item["draftSnapshot"] = map[string]any{
				"toText": stateString(draft, "toText"), "ccText": stateString(draft, "ccText"),
				"bccText": stateString(draft, "bccText"), "createdAt": stateString(draft, "createdAt"),
				"auth": draft["auth"], "priority": stateString(draft, "priority"),
				"receipt": stateBool(draft, "receipt"), "delivery": stateBool(draft, "delivery"),
				"saveCopy": draft["saveCopy"],
			}
			removeMessage(state, body.DraftID)
		}
		state["outbox"] = append(stateSlice(state, "outbox"), item)
		auditDetail := fmt.Sprintf("Queued for %ds undo window · %d recipient · from %s", holdMs/1000, len(to), stateString(alias, "email"))
		auditAction := "send.queued"
		if !scheduledAt.IsZero() {
			auditAction = "send.scheduled"
			auditDetail = fmt.Sprintf("Scheduled for %s · %d recipient · from %s", sendAt.Format(time.RFC3339), len(to), stateString(alias, "email"))
		}
		appendStateAudit(state, auditAction, firstNonEmpty(subject, "(no subject)"), stateString(stateMap(state, "user"), "name"),
			auditDetail, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		outbound = stateOutbound(state)
		undoUntil := any(nil)
		if scheduledAt.IsZero() {
			undoUntil = sendAt.Format(time.RFC3339)
		}
		var scheduledFor any
		if !scheduledAt.IsZero() {
			scheduledFor = sendAt.Format(time.RFC3339)
		}
		response = map[string]any{
			"ok": true, "queued": publicOutboxItem(item), "outbound": outbound,
			"holdMs": holdMs, "scheduled": !scheduledAt.IsZero(),
			"scheduledFor": scheduledFor, "undoUntil": undoUntil,
		}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if draftMessageID != "" {
		s.deleteIMAPDraft(sess.User, draftMessageID)
	}
	if response != nil {
		if queued, ok := response["queued"].(map[string]any); ok {
			recipients := []any{}
			for _, key := range []string{"to", "cc", "bcc"} {
				if list, ok := queued[key].([]any); ok {
					recipients = append(recipients, list...)
				}
			}
			s.maybeAutoAddExternalContacts(r.Context(), sess.User, recipients)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) getOutbox(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateWithOutbox(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": statePendingSends(state), "outbound": stateOutbound(state)})
}

func (s *Server) cancelSend(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var response map[string]any
	var restoredDraft map[string]any
	var attachmentRecords map[string]any
	err := s.withMailState(r.Context(), sess.User, nil, true, func(state map[string]any) error {
		item := stateObjectByID(stateSlice(state, "outbox"), id)
		if item == nil {
			return fail(http.StatusConflict, "SEND_ALREADY_COMMITTED", "This message has already been sent and cannot be undone")
		}
		s.preloadAttachments(r.Context(), sess.User.ID, state, attachmentIDsFromValue(item["attachments"]))
		if stateString(item, "deliveryState") == "sending" {
			return fail(http.StatusConflict, "SEND_IN_PROGRESS", "This message is already being delivered and can no longer be cancelled")
		}
		sendAt, ok := parseStateTime(stateString(item, "sendAt"))
		if !ok || !time.Now().Before(sendAt) {
			return fail(http.StatusConflict, "SEND_ALREADY_COMMITTED", "The undo window has closed")
		}
		state["outbox"] = stateRemoveByID(stateSlice(state, "outbox"), id)
		draft := restoreDraft(state, item)
		restoredDraft = deepCopyMap(draft)
		attachmentRecords, _ = state["attachments"].(map[string]any)
		detail := "Send undone; restored to Drafts"
		if stateBool(item, "scheduled") {
			detail = "Scheduled send cancelled; restored to Drafts"
		}
		appendStateAudit(state, "send.cancelled", firstNonEmpty(stateString(item, "subject"), "(no subject)"),
			stateString(stateMap(state, "user"), "name"), detail, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		response = map[string]any{"ok": true, "draft": publicStateMessage(state, draft), "outbound": stateOutbound(state)}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if restoredDraft != nil {
		s.persistDraftToIMAP(sess.User, restoredDraft, attachmentRecords)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) patchOutbox(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body outboxPatch
	if !s.decodeBody(w, r, &body, 12*1024*1024) {
		return
	}
	var response map[string]any
	err := s.withMailState(r.Context(), sess.User, nil, false, func(state map[string]any) error {
		item := stateObjectByID(stateSlice(state, "outbox"), id)
		if item == nil {
			return fail(http.StatusConflict, "SEND_ALREADY_COMMITTED", "This message has already been sent and cannot be changed")
		}
		if !stateBool(item, "scheduled") {
			return fail(http.StatusConflict, "NOT_SCHEDULED", "Only scheduled messages can be edited here")
		}
		if stateString(item, "deliveryState") == "sending" {
			return fail(http.StatusConflict, "SEND_IN_PROGRESS", "This message is already being delivered and can no longer be edited")
		}
		if sendAt, ok := parseStateTime(stateString(item, "sendAt")); !ok || !time.Now().Before(sendAt) {
			return fail(http.StatusConflict, "SEND_ALREADY_COMMITTED", "This message is already sending and can no longer be edited")
		}
		changed := []string{}
		if body.FromEmail != "" {
			alias := aliasFor(state, body.FromEmail)
			if alias == nil || alias["canSend"] == false {
				return fail(http.StatusForbidden, "BAD_ALIAS", "Choose one of your sendable identities")
			}
			item["from"] = aliasPerson(alias)
			changed = append(changed, "from")
		}
		if body.To != nil {
			parsed, invalid := parseRecipients(body.To, state)
			if len(invalid) > 0 {
				return fail(http.StatusBadRequest, "BAD_RECIPIENT", "Check these recipients: "+strings.Join(invalid, ", "))
			}
			if len(parsed) == 0 {
				return fail(http.StatusBadRequest, "NO_RECIPIENTS", "Add at least one recipient")
			}
			item["to"] = parsed
			changed = append(changed, "recipients")
		}
		if body.CC != nil {
			parsed, invalid := parseRecipients(body.CC, state)
			if len(invalid) > 0 {
				return fail(http.StatusBadRequest, "BAD_RECIPIENT", "Check these recipients: "+strings.Join(invalid, ", "))
			}
			item["cc"] = parsed
			changed = append(changed, "cc")
		}
		if body.BCC != nil {
			parsed, invalid := parseRecipients(body.BCC, state)
			if len(invalid) > 0 {
				return fail(http.StatusBadRequest, "BAD_RECIPIENT", "Check these recipients: "+strings.Join(invalid, ", "))
			}
			item["bcc"] = parsed
			changed = append(changed, "bcc")
		}
		recipientCount := len(stateSlice(item, "to")) + len(stateSlice(item, "cc")) + len(stateSlice(item, "bcc"))
		if recipientCount == 0 {
			return fail(http.StatusBadRequest, "NO_RECIPIENTS", "Add at least one recipient")
		}
		if recipientCount > 50 {
			return fail(http.StatusBadRequest, "TOO_MANY_RECIPIENTS", "A message can have at most 50 recipients")
		}
		if body.Subject != nil {
			item["subject"] = truncate(*body.Subject, 500)
			changed = append(changed, "subject")
		}
		if body.BodyText != nil || body.BodyHTML != nil || body.Body != nil {
			text := stateString(item, "bodyText")
			htmlBody := stateString(item, "bodyHtml")
			if body.BodyText != nil {
				text = *body.BodyText
				htmlBody = ""
			} else if body.Body != nil {
				text = *body.Body
				htmlBody = ""
			}
			if body.BodyHTML != nil {
				htmlBody = *body.BodyHTML
				if body.BodyText == nil && body.Body == nil {
					text = ""
				}
			}
			htmlBody, text = normalizeComposeBody(htmlBody, text)
			item["bodyText"] = truncate(text, 200_000)
			item["bodyHtml"] = truncate(htmlBody, 500_000)
			changed = append(changed, "body")
		}
		if body.Attachments != nil {
			s.preloadAttachments(r.Context(), sess.User.ID, state, attachmentIDsFromValue(body.Attachments))
			item["attachments"] = normalizeAttachments(state, body.Attachments)
			changed = append(changed, "attachments")
		}
		if body.SendAt != "" || body.ScheduledFor != "" {
			raw := firstNonEmpty(body.SendAt, body.ScheduledFor)
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return fail(http.StatusBadRequest, "SCHEDULE_INVALID", "Choose a valid date and time")
			}
			now := time.Now()
			if !parsed.After(now.Add(5 * time.Second)) {
				return fail(http.StatusBadRequest, "SCHEDULE_INVALID", "Scheduled time must be at least a few seconds in the future")
			}
			if parsed.After(now.Add(366 * 24 * time.Hour)) {
				return fail(http.StatusBadRequest, "SCHEDULE_INVALID", "Scheduled time can be at most one year ahead")
			}
			item["sendAt"] = parsed.Format(time.RFC3339)
			item["scheduled"] = true
			changed = append(changed, "schedule")
		}
		if len(changed) == 0 {
			return fail(http.StatusBadRequest, "NOTHING_TO_UPDATE", "No scheduled-message changes were provided")
		}
		appendStateAudit(state, "send.rescheduled", firstNonEmpty(stateString(item, "subject"), "(no subject)"),
			stateString(stateMap(state, "user"), "name"),
			fmt.Sprintf("Scheduled message updated · %s · sends %s", strings.Join(changed, ", "), stateString(item, "sendAt")),
			clientIP(r), sanitizeUserAgent(r.UserAgent()))
		response = map[string]any{
			"ok": true, "item": publicOutboxItem(item), "changed": changed,
			"pending": statePendingSends(state), "outbound": stateOutbound(state),
		}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) sendNow(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	state, ok := s.loadStateWithOutbox(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	item := stateObjectByID(stateSlice(state, "outbox"), id)
	if item == nil {
		writeError(w, http.StatusConflict, "SEND_ALREADY_COMMITTED", "This message has already been sent")
		return
	}
	if !stateBool(item, "scheduled") {
		writeError(w, http.StatusConflict, "NOT_SCHEDULED", "This message is already in the undo-send window")
		return
	}
	originalSendAt := stateString(item, "sendAt")
	subject := firstNonEmpty(stateString(item, "subject"), "(no subject)")
	result, err := s.processOutboxItem(r.Context(), sess.User.ID, sess.User.TenantID, id, true)
	if err != nil {
		var failure *apiError
		if errors.As(err, &failure) {
			writeFromError(w, err)
			return
		}
		s.logger.Warn("send-now delivery failed", "user", sess.User.Email, "item", id, "err", err)
		writeError(w, http.StatusBadGateway, "SEND_DELIVERY_FAILED", "The message could not be delivered and remains queued for retry")
		return
	}
	if err := s.withState(sess.User.ID, sess.User.TenantID, func(current map[string]any) error {
		ensureStateCollections(current)
		appendStateAudit(current, "send.sent_now", subject, stateString(stateMap(current, "user"), "name"),
			"Scheduled send advanced to now · originally "+originalSendAt, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	}); err != nil {
		s.logger.Warn("send-now audit failed", "err", err)
	}
	current, _ := s.loadStateWithOutbox(r.Context(), sess.User.ID)
	response := map[string]any{
		"ok": true, "subject": subject, "sentMessageId": result["sentMessageId"],
		"pending": statePendingSends(current), "outbound": stateOutbound(current),
	}
	writeJSON(w, http.StatusOK, response)
}

func restoreDraft(state map[string]any, item map[string]any) map[string]any {
	now := nowISO()
	snapshot, _ := item["draftSnapshot"].(map[string]any)
	draftID := stateString(item, "draftId")
	if draftID == "" {
		draftID = newID("draft")
	}
	from, _ := item["from"].(map[string]any)
	text := stateString(item, "bodyText")
	htmlBody, text := normalizeComposeBody(stateString(item, "bodyHtml"), text)
	attachments, _ := item["attachments"].([]any)
	if attachments == nil {
		attachments = []any{}
	}
	sizeBytes := int64(4*1024 + len(text))
	for _, raw := range attachments {
		file, _ := raw.(map[string]any)
		if file != nil {
			sizeBytes += stateInt(file, "sizeBytes")
		}
	}
	to := stateSlice(item, "to")
	cc := stateSlice(item, "cc")
	bcc := stateSlice(item, "bcc")
	draft := map[string]any{
		"id": draftID, "folder": "drafts", "draft": true,
		"from": from, "fromEmail": stateString(from, "email"),
		"to": to, "toText": firstNonEmpty(stateString(snapshot, "toText"), recipientText(to)),
		"cc": cc, "ccText": firstNonEmpty(stateString(snapshot, "ccText"), recipientText(cc)),
		"bcc": bcc, "bccText": firstNonEmpty(stateString(snapshot, "bccText"), recipientText(bcc)),
		"subject": stateString(item, "subject"), "snippet": snippet(firstNonEmpty(text, stateString(item, "subject"), "Draft")),
		"bodyText": text, "bodyHtml": htmlBody, "attachments": attachments,
		"date": now, "updatedAt": now, "createdAt": firstNonEmpty(stateString(snapshot, "createdAt"), now),
		"unread": false, "starred": false, "sizeBytes": sizeBytes,
		"threadId":  firstNonEmpty(stateString(item, "threadId"), newID("t")),
		"inReplyTo": item["inReplyTo"], "windowMode": "floating", "external": false,
		"priority": firstNonEmpty(stateString(item, "priority"), "normal"),
		"receipt":  stateBool(item, "receipt"), "delivery": stateBool(item, "delivery"),
		"saveCopy": item["saveCopy"] != false, "categories": []any{}, "classification": "Internal",
		"messageId": fmt.Sprintf("<%s@meridian.test>", draftID),
		"viaApi":    true,
	}
	auth, _ := snapshot["auth"].(map[string]any)
	if auth == nil {
		auth = map[string]any{"spf": "pass", "dkim": "pass", "dmarc": "pass", "tls": "TLS 1.3", "ip": "127.0.0.1", "signer": "meridian.test", "firstContact": false, "warnings": []any{}}
	}
	draft["auth"] = auth
	if existing := findMessageIn(state, draftID); existing != nil && stateString(existing, "folder") == "drafts" {
		for key, value := range draft {
			existing[key] = value
		}
		return existing
	}
	state["messages"] = append(stateMessages(state), draft)
	return draft
}

func recipientText(people []any) string {
	parts := []string{}
	for _, raw := range people {
		person, _ := raw.(map[string]any)
		if person != nil {
			parts = append(parts, stateString(person, "email"))
		}
	}
	return strings.Join(parts, ", ")
}

func commitQueued(state map[string]any, item map[string]any) string {
	itemID := stateString(item, "id")
	state["outbox"] = stateRemoveByID(stateSlice(state, "outbox"), itemID)
	now := nowISO()
	sentMessageID := ""
	if item["saveCopy"] != false {
		text := stateString(item, "bodyText")
		htmlBody, text := normalizeComposeBody(stateString(item, "bodyHtml"), text)
		attachments, _ := item["attachments"].([]any)
		if attachments == nil {
			attachments = []any{}
		}
		sizeBytes := int64(4*1024 + len(text))
		for _, raw := range attachments {
			file, _ := raw.(map[string]any)
			if file != nil {
				sizeBytes += stateInt(file, "sizeBytes")
			}
		}
		threadID := stateString(item, "threadId")
		if threadID == "" {
			threadID = newID("t")
		}
		sent := map[string]any{
			"id": newID("msg"), "threadId": threadID, "folder": "sent",
			"from": item["from"], "to": item["to"], "cc": item["cc"], "bcc": item["bcc"],
			"subject": firstNonEmpty(stateString(item, "subject"), "(no subject)"),
			"snippet": snippet(firstNonEmpty(text, "(no body)")), "bodyHtml": htmlBody, "bodyText": text,
			"date": now, "unread": false, "starred": false, "draft": false,
			"attachments": attachments, "categories": []any{}, "classification": "Internal", "external": false,
			"auth":  map[string]any{"spf": "pass", "dkim": "pass", "dmarc": "pass", "tls": "TLS 1.3", "ip": "127.0.0.1", "signer": "meridian.test", "firstContact": false, "warnings": []any{}},
			"event": nil, "sizeBytes": sizeBytes,
			"messageId": firstNonEmpty(stateString(item, "mimeMessageId"), fmt.Sprintf("<%s@meridian.test>", newID("sent"))),
			"inReplyTo": item["inReplyTo"], "priority": firstNonEmpty(stateString(item, "priority"), "normal"),
			"receipt": stateBool(item, "receipt"), "delivery": stateBool(item, "delivery"),
			"saveCopy": true, "viaApi": true, "windowMode": "floating",
		}
		state["messages"] = append(stateMessages(state), sent)
		sentMessageID = stateString(sent, "id")
	}
	user := stateMap(state, "user")
	outbound := stateMap(user, "outbound")
	events, _ := outbound["events"].([]any)
	events = append(events, map[string]any{"at": now, "id": itemID, "messageId": sentMessageID})
	outbound["events"] = events
	user["outbound"] = outbound
	from, _ := item["from"].(map[string]any)
	to := stateSlice(item, "to")
	appendStateAudit(state, "send.committed", firstNonEmpty(stateString(item, "subject"), "(no subject)"),
		stateString(user, "name"),
		fmt.Sprintf("Delivered to %s · alias %s", recipientText(to), stateString(from, "email")),
		stateString(item, "ip"), stateString(item, "userAgent"))
	return sentMessageID
}

func (s *Server) StartWorkers(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.dispatchDueSends(ctx)
			}
		}
	}()
	go func() {
		s.dispatchDueSends(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				s.logger.Debug("outbox worker alive")
			}
		}
	}()
	if s.mailSyncConfigured() {
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.processPendingExpunges(ctx)
				}
			}
		}()
		s.logger.Info("mail worker pool starting", "concurrency", s.cfg.MailWorkerConcurrency, "idle", s.cfg.IMAPIdle)
		s.startMailWorkerPool(ctx, s.cfg.MailWorkerConcurrency)
		go s.retentionWorker(ctx)
	}
}

func (s *Server) dispatchDueSends(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, user_id::text, tenant_id::text
		FROM outbox
		WHERE status='queued' AND send_at <= now()
		  AND (retry_at IS NULL OR retry_at <= now())
		ORDER BY send_at ASC
		LIMIT 200
	`)
	if err != nil {
		s.logger.Warn("outbox scan failed", "err", err)
		return
	}
	defer rows.Close()
	type dueItem struct{ id, userID, tenantID string }
	items := []dueItem{}
	for rows.Next() {
		var item dueItem
		if err := rows.Scan(&item.id, &item.userID, &item.tenantID); err == nil {
			items = append(items, item)
		}
	}
	for _, item := range items {
		result, err := s.processOutboxItem(ctx, item.userID, item.tenantID, item.id, false)
		if err != nil {
			var failure *apiError
			if errors.As(err, &failure) && failure.Code == "SEND_ALREADY_COMMITTED" {
				continue
			}
			s.logger.Warn("dispatch send", "user", item.userID, "item", item.id, "err", err)
			continue
		}
		s.logger.Info("outbound send committed", "user", item.userID, "item", item.id,
			"subject", result["subject"], "message", result["sentMessageId"])
	}
}

func (s *Server) enforceOutboundPolicy(ctx context.Context, tenantID string, recipients []any, attachments []any, policy adminTenantPolicy) error {
	if !policy.Mail.SMTPAccess {
		return fail(http.StatusForbidden, "MAIL_POLICY", "Outbound SMTP is disabled by your organization's mail policy")
	}
	domainSet := map[string]bool{}
	rows, err := s.store.DB.Query(ctx, `SELECT domain::text FROM mail_domains WHERE tenant_id = $1 AND status IN ('verified','manual')`, tenantID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var domain string
			if rows.Scan(&domain) == nil {
				domainSet[strings.ToLower(domain)] = true
			}
		}
	}
	if len(policy.Mail.AllowedExternalRecipients) > 0 {
		for _, raw := range recipients {
			recipient, _ := raw.(map[string]any)
			if recipient == nil {
				continue
			}
			email := strings.ToLower(stateString(recipient, "email"))
			_, domain := splitEmail(email)
			if domain == "" || domainSet[domain] {
				continue
			}
			allowed := false
			for _, rule := range policy.Mail.AllowedExternalRecipients {
				rule = strings.ToLower(strings.TrimSpace(rule))
				if rule == "" {
					continue
				}
				if strings.HasPrefix(rule, "*.") {
					if strings.HasSuffix(domain, rule[1:]) {
						allowed = true
						break
					}
				} else if rule == email || rule == domain {
					allowed = true
					break
				}
			}
			if !allowed {
				return fail(http.StatusForbidden, "MAIL_POLICY", "External recipient "+email+" is not permitted by the organization's mail policy")
			}
		}
	}
	blocked := map[string]bool{}
	for _, extension := range policy.Mail.BlockedExtensions {
		blocked[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), "."))] = true
	}
	var totalBytes int64
	for _, raw := range attachments {
		attachment, _ := raw.(map[string]any)
		if attachment == nil {
			continue
		}
		name := stateString(attachment, "name")
		totalBytes += stateInt(attachment, "sizeBytes")
		if dot := strings.LastIndex(name, "."); dot >= 0 && dot < len(name)-1 {
			extension := strings.ToLower(name[dot+1:])
			if blocked[extension] {
				return fail(http.StatusForbidden, "MAIL_POLICY", "Attachments ending in ."+extension+" are blocked by the organization's mail policy")
			}
		}
	}
	if policy.Mail.MaxAttachmentMB > 0 && totalBytes > int64(policy.Mail.MaxAttachmentMB)*1024*1024 {
		return fail(http.StatusForbidden, "MAIL_POLICY", fmt.Sprintf("Attachments exceed the organization's %d MB limit", policy.Mail.MaxAttachmentMB))
	}
	return nil
}
