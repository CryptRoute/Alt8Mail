package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const maxMergeRecipients = 500

var mergeVariablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]{1,64})\s*\}\}`)

type mergeRecipientInput struct {
	Email     string            `json:"email"`
	Name      string            `json:"name"`
	Variables map[string]string `json:"variables"`
}

type mergeRecipientRecord struct {
	ID        string
	Email     string
	Name      string
	Token     string
	Status    string
	Variables map[string]string
	OutboxID  string
}

func (s *Server) requireMailMergeEntitlement(w http.ResponseWriter) bool {
	if s.entitled("mail_merge") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Mail merge requires a license with the mail_merge feature")
	return false
}

func parseMergeCSV(raw string) ([]mergeRecipientInput, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
	if raw == "" {
		return []mergeRecipientInput{}, nil
	}
	reader := csv.NewReader(strings.NewReader(raw))
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("CSV needs a header row and at least one recipient")
	}
	headers := []string{}
	for _, header := range rows[0] {
		header = strings.ToLower(strings.TrimSpace(header))
		if header == "" {
			return nil, fmt.Errorf("CSV has an empty column name")
		}
		headers = append(headers, header)
	}
	emailIndex, nameIndex := -1, -1
	for index, header := range headers {
		if header == "email" || header == "e-mail" {
			emailIndex = index
		}
		if header == "name" || header == "full name" {
			nameIndex = index
		}
	}
	if emailIndex < 0 {
		return nil, fmt.Errorf("CSV needs an email column")
	}
	recipients := []mergeRecipientInput{}
	for rowNumber, row := range rows[1:] {
		if len(row) != len(headers) {
			return nil, fmt.Errorf("CSV row %d has %d columns; expected %d", rowNumber+2, len(row), len(headers))
		}
		entry := mergeRecipientInput{Email: normalizeEmailValue(row[emailIndex]), Variables: map[string]string{}}
		if nameIndex >= 0 {
			entry.Name = strings.TrimSpace(row[nameIndex])
		}
		for index, value := range row {
			if index == emailIndex || index == nameIndex {
				continue
			}
			entry.Variables[headers[index]] = strings.TrimSpace(value)
		}
		if entry.Email == "" {
			continue
		}
		recipients = append(recipients, entry)
	}
	return recipients, nil
}

func normalizeMergeRecipients(inputs []mergeRecipientInput, csvRaw string) ([]mergeRecipientInput, error) {
	if rows, err := parseMergeCSV(csvRaw); err != nil {
		return nil, err
	} else {
		inputs = append(inputs, rows...)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("add at least one recipient")
	}
	if len(inputs) > maxMergeRecipients {
		return nil, fmt.Errorf("a merge may have at most %d recipients", maxMergeRecipients)
	}
	seen := map[string]bool{}
	result := []mergeRecipientInput{}
	for _, input := range inputs {
		email := normalizeEmailValue(input.Email)
		if !looksLikeEmail(email) {
			return nil, fmt.Errorf("invalid recipient %q", input.Email)
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		input.Email = email
		input.Name = truncate(strings.TrimSpace(input.Name), 120)
		variables := map[string]string{}
		for key, value := range input.Variables {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" || key == "email" || key == "name" {
				continue
			}
			variables[truncate(key, 64)] = truncate(strings.TrimSpace(value), 500)
		}
		input.Variables = variables
		result = append(result, input)
	}
	if len(result) > maxMergeRecipients {
		return nil, fmt.Errorf("a merge may have at most %d unique recipients", maxMergeRecipients)
	}
	return result, nil
}

func mergeValues(input mergeRecipientInput) map[string]string {
	values := map[string]string{"email": input.Email, "name": input.Name}
	for key, value := range input.Variables {
		values[key] = value
	}
	return values
}

func renderMergeText(source string, values map[string]string, escape bool) string {
	return mergeVariablePattern.ReplaceAllStringFunc(source, func(match string) string {
		parts := mergeVariablePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		value, ok := values[parts[1]]
		if !ok {
			return match
		}
		if escape {
			return html.EscapeString(value)
		}
		return value
	})
}

func renderMergeMessage(input mergeRecipientInput, subject, bodyText, bodyHTML, unsubscribeURL string) (string, string, string) {
	values := mergeValues(input)
	renderedSubject := renderMergeText(subject, values, false)
	text := renderMergeText(bodyText, values, false)
	body := renderMergeText(bodyHTML, values, true)
	footer := "\n\nUnsubscribe: " + unsubscribeURL
	htmlFooter := `<p style="margin-top:24px;font-size:12px;color:#667085">Don't want these emails? <a href="` + html.EscapeString(unsubscribeURL) + `">Unsubscribe</a>.</p>`
	return renderedSubject, text + footer, body + htmlFooter
}

func (s *Server) mergeFromAlias(ctx context.Context, userID, requested string) (string, error) {
	email := normalizeEmailValue(requested)
	if email != "" {
		var exists bool
		if err := s.store.DB.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM public.user_aliases WHERE user_id=$1 AND lower(email::text)=$2 AND active AND can_send)
		`, userID, email).Scan(&exists); err != nil || !exists {
			return "", fmt.Errorf("fromEmail must be an active sending alias")
		}
		return email, nil
	}
	var primary string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT email::text FROM public.user_aliases WHERE user_id=$1 AND active AND can_send
		ORDER BY (kind='primary') DESC, created_at LIMIT 1
	`, userID).Scan(&primary); err != nil {
		return "", fmt.Errorf("no sending identity is available")
	}
	return primary, nil
}

type mailMergeBody struct {
	Name            string                `json:"name"`
	FromEmail       string                `json:"fromEmail"`
	Subject         string                `json:"subject"`
	BodyText        string                `json:"bodyText"`
	BodyHTML        string                `json:"bodyHtml"`
	ThrottleSeconds int                   `json:"throttleSeconds"`
	ScheduledAt     string                `json:"scheduledAt"`
	Recipients      []mergeRecipientInput `json:"recipients"`
	CSV             string                `json:"csv"`
}

func (s *Server) validateMailMergeBody(ctx context.Context, user userRecord, body mailMergeBody) (mailMergeBody, []mergeRecipientInput, error) {
	subject := truncate(strings.TrimSpace(body.Subject), 1000)
	if subject == "" {
		return body, nil, fmt.Errorf("subject is required")
	}
	if len(body.BodyText) > 200_000 || len(body.BodyHTML) > 300_000 {
		return body, nil, fmt.Errorf("merge body is too large")
	}
	recipients, err := normalizeMergeRecipients(body.Recipients, body.CSV)
	if err != nil {
		return body, nil, err
	}
	from, err := s.mergeFromAlias(ctx, user.ID, body.FromEmail)
	if err != nil {
		return body, nil, err
	}
	throttle := body.ThrottleSeconds
	if throttle == 0 {
		throttle = 5
	}
	if throttle < 1 || throttle > 60 {
		return body, nil, fmt.Errorf("throttle must be between 1 and 60 seconds")
	}
	body.Name = truncate(firstNonEmpty(strings.TrimSpace(body.Name), subject), 100)
	body.Subject = subject
	body.FromEmail = from
	body.ThrottleSeconds = throttle
	body.BodyText = truncate(body.BodyText, 200_000)
	body.BodyHTML = truncate(body.BodyHTML, 300_000)
	return body, recipients, nil
}

func (s *Server) createMailMerge(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body mailMergeBody
	if !s.decodeBody(w, r, &body, 4*1024*1024) {
		return
	}
	body, recipients, err := s.validateMailMergeBody(r.Context(), sess.User, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "MERGE", err.Error())
		return
	}
	var scheduledAt any
	if raw := strings.TrimSpace(body.ScheduledAt); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil || !parsed.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "MERGE_SCHEDULE", "scheduledAt must be a future RFC3339 timestamp")
			return
		}
		scheduledAt = parsed
	}
	id := newID("merge")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_merges (id, tenant_id, user_id, name, from_email, subject, body_text, body_html, throttle_seconds, scheduled_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, id, sess.User.TenantID, sess.User.ID, body.Name, body.FromEmail, body.Subject, body.BodyText, body.BodyHTML, body.ThrottleSeconds, scheduledAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MERGE", "Could not create the mail merge")
		return
	}
	for _, recipient := range recipients {
		variables, _ := json.Marshal(recipient.Variables)
		_, _ = s.store.DB.Exec(r.Context(), `
			INSERT INTO public.mail_merge_recipients (id, merge_id, tenant_id, user_id, email, name, variables, token)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)
		`, newID("recipient"), id, sess.User.TenantID, sess.User.ID, recipient.Email, recipient.Name, variables, newID("unsub"))
	}
	s.audit(r, sess.User, "mail_merge.created", id, fmt.Sprintf("Created merge %s with %d recipients", body.Name, len(recipients)))
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "merge": s.mailMergePayload(r.Context(), sess.User.ID, id)})
}

func (s *Server) previewMailMerge(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body mailMergeBody
	if !s.decodeBody(w, r, &body, 4*1024*1024) {
		return
	}
	body, recipients, err := s.validateMailMergeBody(r.Context(), sess.User, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "MERGE", err.Error())
		return
	}
	preview := []map[string]any{}
	for index, recipient := range recipients {
		if index >= 20 {
			break
		}
		subject, text, htmlBody := renderMergeMessage(recipient, body.Subject, body.BodyText, body.BodyHTML, "https://example.test/u/token")
		preview = append(preview, map[string]any{"email": recipient.Email, "name": recipient.Name, "subject": subject, "bodyText": text, "bodyHtml": htmlBody})
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": preview, "count": len(recipients), "fromEmail": body.FromEmail, "throttleSeconds": body.ThrottleSeconds})
}

func (s *Server) mailMergePayload(ctx context.Context, userID, mergeID string) map[string]any {
	var id, name, fromEmail, subject, status string
	var throttle int
	var scheduled *time.Time
	var created time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, name, from_email::text, subject, status, throttle_seconds, scheduled_at, created_at
		FROM public.mail_merges WHERE id=$1 AND user_id=$2
	`, mergeID, userID).Scan(&id, &name, &fromEmail, &subject, &status, &throttle, &scheduled, &created)
	if err != nil {
		return nil
	}
	payload := map[string]any{
		"id": id, "name": name, "fromEmail": fromEmail, "subject": subject,
		"status": status, "throttleSeconds": throttle, "createdAt": created.UTC().Format(time.RFC3339),
	}
	if scheduled != nil {
		payload["scheduledAt"] = scheduled.UTC().Format(time.RFC3339)
	}
	rows, _ := s.store.DB.Query(ctx, `
		SELECT r.id, r.email::text, r.name, r.status, COALESCE(r.outbox_id,''), COALESCE(o.status,''), r.created_at
		FROM public.mail_merge_recipients r
		LEFT JOIN public.outbox o ON o.id=r.outbox_id
		WHERE r.merge_id=$1 ORDER BY r.created_at LIMIT $2
	`, id, maxMergeRecipients)
	if rows != nil {
		defer rows.Close()
		recipients := []map[string]any{}
		counts := map[string]int{}
		for rows.Next() {
			var recipientID, email, recipientName, recipientStatus, outboxID, outboxStatus string
			var recipientCreated time.Time
			if rows.Scan(&recipientID, &email, &recipientName, &recipientStatus, &outboxID, &outboxStatus, &recipientCreated) == nil {
				effective := recipientStatus
				if outboxStatus == "sent" {
					effective = "sent"
				} else if outboxStatus == "failed" {
					effective = "failed"
				}
				counts[effective]++
				recipients = append(recipients, map[string]any{
					"id": recipientID, "email": email, "name": recipientName,
					"status": effective, "outboxId": outboxID,
				})
			}
		}
		payload["recipients"] = recipients
		payload["report"] = map[string]any{
			"queued": counts["queued"], "sent": counts["sent"], "failed": counts["failed"],
			"pending": counts["pending"], "suppressed": counts["suppressed"], "skipped": counts["skipped"],
		}
	}
	return payload
}

func (s *Server) listMailMerges(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id FROM public.mail_merges WHERE user_id=$1 ORDER BY created_at DESC LIMIT 100
	`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MERGE", "Could not load mail merges")
		return
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	items := []map[string]any{}
	for _, id := range ids {
		if payload := s.mailMergePayload(r.Context(), sess.User.ID, id); payload != nil {
			items = append(items, payload)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"merges": items, "count": len(items)})
}

func (s *Server) getMailMerge(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	payload := s.mailMergePayload(r.Context(), sess.User.ID, r.PathValue("id"))
	if payload == nil {
		writeError(w, http.StatusNotFound, "MERGE_NOT_FOUND", "Mail merge not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"merge": payload})
}

func (s *Server) queueMailMerge(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	mergeID := r.PathValue("id")
	var fromEmail, subject, bodyText, bodyHTML, status string
	var throttle int
	var scheduled *time.Time
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT from_email::text, subject, body_text, body_html, status, throttle_seconds, scheduled_at
		FROM public.mail_merges WHERE id=$1 AND user_id=$2
	`, mergeID, sess.User.ID).Scan(&fromEmail, &subject, &bodyText, &bodyHTML, &status, &throttle, &scheduled); err != nil {
		writeError(w, http.StatusNotFound, "MERGE_NOT_FOUND", "Mail merge not found")
		return
	}
	if status != "draft" {
		writeError(w, http.StatusConflict, "MERGE_STATE", "Only a draft merge can be queued")
		return
	}
	var accountID string
	_ = s.store.DB.QueryRow(r.Context(), `SELECT id FROM public.mail_accounts WHERE user_id=$1 AND kind='native' LIMIT 1`, sess.User.ID).Scan(&accountID)
	if accountID == "" {
		writeError(w, http.StatusConflict, "MERGE_ACCOUNT", "A native mailbox is required to send a merge")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, email::text, name, variables, token FROM public.mail_merge_recipients
		WHERE merge_id=$1 AND status='pending' ORDER BY created_at
	`, mergeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MERGE", "Could not load merge recipients")
		return
	}
	recipients := []mergeRecipientRecord{}
	for rows.Next() {
		var record mergeRecipientRecord
		var variables []byte
		if rows.Scan(&record.ID, &record.Email, &record.Name, &variables, &record.Token) == nil {
			_ = json.Unmarshal(variables, &record.Variables)
			recipients = append(recipients, record)
		}
	}
	rows.Close()
	suppressed := map[string]bool{}
	suppRows, _ := s.store.DB.Query(r.Context(), `SELECT email::text FROM public.mail_suppressions WHERE user_id=$1`, sess.User.ID)
	if suppRows != nil {
		for suppRows.Next() {
			var email string
			if suppRows.Scan(&email) == nil {
				suppressed[normalizeEmailValue(email)] = true
			}
		}
		suppRows.Close()
	}
	base := time.Now().Add(5 * time.Second)
	if scheduled != nil && scheduled.After(base) {
		base = *scheduled
	}
	queued, skipped := 0, 0
	for index, recipient := range recipients {
		if suppressed[normalizeEmailValue(recipient.Email)] {
			_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mail_merge_recipients SET status='suppressed', updated_at=now() WHERE id=$1`, recipient.ID)
			skipped++
			continue
		}
		unsubscribeURL := s.publicBaseURL(r) + "/u/" + recipient.Token
		renderedSubject, renderedText, renderedHTML := renderMergeMessage(mergeRecipientInput{
			Email: recipient.Email, Name: recipient.Name, Variables: recipient.Variables,
		}, subject, bodyText, bodyHTML, unsubscribeURL)
		outboxID := newID("send")
		toJSON, _ := json.Marshal([]map[string]any{{"email": recipient.Email, "name": recipient.Name}})
		fromJSON, _ := json.Marshal(map[string]any{"name": sess.User.Name, "email": fromEmail})
		sendAt := base.Add(time.Duration(index*throttle) * time.Second)
		_, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO public.outbox
			  (id, tenant_id, user_id, from_json, to_json, subject, body_text, body_html, status,
			   scheduled, send_at, hold_ms, classification, account_id, merge_id, merge_recipient_id)
			VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,'queued',true,$9,0,'Internal',$10,$11,$12)
		`, outboxID, sess.User.TenantID, sess.User.ID, fromJSON, toJSON, renderedSubject, renderedText,
			renderedHTML, sendAt, accountID, mergeID, recipient.ID)
		if err != nil {
			_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mail_merge_recipients SET status='failed', error=$2, updated_at=now() WHERE id=$1`, recipient.ID, truncate(err.Error(), 300))
			continue
		}
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mail_merge_recipients SET status='queued', outbox_id=$2, updated_at=now() WHERE id=$1`, recipient.ID, outboxID)
		queued++
	}
	mergeStatus := "queued"
	if queued == 0 {
		mergeStatus = "completed"
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mail_merges SET status=$2, scheduled_at=$3, updated_at=now() WHERE id=$1`, mergeID, mergeStatus, base)
	s.audit(r, sess.User, "mail_merge.queued", mergeID, fmt.Sprintf("Queued %d merge recipients; skipped %d suppressed", queued, skipped))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": queued, "suppressed": skipped, "merge": s.mailMergePayload(r.Context(), sess.User.ID, mergeID)})
}

func (s *Server) cancelMailMerge(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	tag, err := s.store.DB.Exec(r.Context(), `UPDATE public.mail_merges SET status='cancelled', updated_at=now() WHERE id=$1 AND user_id=$2 AND status IN ('draft','queued','running')`, r.PathValue("id"), sess.User.ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "MERGE_NOT_FOUND", "Mail merge not found")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE public.outbox SET status='cancelled', updated_at=now()
		WHERE merge_id=$1 AND status='queued'
	`, r.PathValue("id"))
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mail_merge_recipients SET status='skipped', updated_at=now() WHERE merge_id=$1 AND status='queued'`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) publicBaseURL(r *http.Request) string {
	if configured := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/"); configured != "" {
		return configured
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) unsubscribeMerge(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	var recipientID, mergeID, email, userID, tenantID string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT r.id, r.merge_id, r.email::text, r.user_id::text, r.tenant_id::text
		FROM public.mail_merge_recipients r WHERE r.token=$1
	`, token).Scan(&recipientID, &mergeID, &email, &userID, &tenantID); err != nil {
		http.Error(w, "This unsubscribe link is no longer valid.", http.StatusNotFound)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mail_merge_recipients SET status='suppressed', updated_at=now() WHERE id=$1`, recipientID)
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_suppressions (tenant_id, user_id, email, reason) VALUES ($1,$2,$3,'unsubscribed')
		ON CONFLICT (user_id, email) DO NOTHING
	`, tenantID, userID, email)
	s.audit(r, userRecord{ID: userID, TenantID: tenantID, Email: email, Name: email}, "mail_merge.unsubscribed", mergeID, "Recipient unsubscribed from a mail merge")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Unsubscribed</title><style>body{font-family:system-ui,sans-serif;background:#f4f7fb;color:#172230;margin:0;padding:40px}main{max-width:520px;margin:0 auto;background:#fff;border:1px solid #dbe3ee;border-radius:14px;padding:28px}h1{font-size:20px;margin:0 0 8px}p{color:#4b5b70;line-height:1.5}</style></head><body><main><h1>You're unsubscribed</h1><p>This address will not receive further mail-merge messages from this sender.</p></main></body></html>`))
}

func (s *Server) listMailSuppressions(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `SELECT email::text, reason, created_at FROM public.mail_suppressions WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1000`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MERGE", "Could not load suppressions")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var email, reason string
		var created time.Time
		if rows.Scan(&email, &reason, &created) == nil {
			items = append(items, map[string]any{"email": email, "reason": reason, "createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressions": items})
}

func (s *Server) addMailSuppression(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body struct {
		Email string `json:"email"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	email := normalizeEmailValue(body.Email)
	if !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "MERGE", "Enter a valid email")
		return
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_suppressions (tenant_id, user_id, email, reason) VALUES ($1,$2,$3,'manual')
		ON CONFLICT (user_id, email) DO NOTHING
	`, sess.User.TenantID, sess.User.ID, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MERGE", "Could not add the suppression")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) removeMailSuppression(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailMergeEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM public.mail_suppressions WHERE user_id=$1 AND email=$2`, sess.User.ID, normalizeEmailValue(r.PathValue("email")))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) registerMailMergeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail-merges", s.requireUser(s.listMailMerges))
	mux.HandleFunc("POST /api/mail-merges", s.requireUser(s.createMailMerge))
	mux.HandleFunc("POST /api/mail-merges/preview", s.requireUser(s.previewMailMerge))
	mux.HandleFunc("GET /api/mail-merges/{id}", s.requireUser(s.getMailMerge))
	mux.HandleFunc("POST /api/mail-merges/{id}/queue", s.requireUser(s.queueMailMerge))
	mux.HandleFunc("POST /api/mail-merges/{id}/cancel", s.requireUser(s.cancelMailMerge))
	mux.HandleFunc("GET /api/mail-suppressions", s.requireUser(s.listMailSuppressions))
	mux.HandleFunc("POST /api/mail-suppressions", s.requireUser(s.addMailSuppression))
	mux.HandleFunc("DELETE /api/mail-suppressions/{email}", s.requireUser(s.removeMailSuppression))
	mux.HandleFunc("GET /u/{token}", s.unsubscribeMerge)
}
