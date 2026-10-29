package control

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

type apiKeyRecord struct {
	ID     string
	Name   string
	Scopes []string
}

type apiKeyContextKey struct{}

var templateVarPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]{1,64})\s*\}\}`)

func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/access/api-keys", s.require("api.read", s.handleAPIKeys))
	mux.HandleFunc("POST /control/api/access/api-keys", s.require("api.manage", s.handleAPIKeyCreate))
	mux.HandleFunc("DELETE /control/api/access/api-keys/{id}", s.require("api.manage", s.handleAPIKeyRevoke))

	mux.HandleFunc("GET /api/v1", s.apiKey("read", s.handleAPIRoot))
	mux.HandleFunc("GET /api/v1/suppressions", s.apiKey("read", s.handleAPISuppressions))
	mux.HandleFunc("POST /api/v1/suppressions", s.apiKey("admin", s.handleAPISuppressionCreate))
	mux.HandleFunc("DELETE /api/v1/suppressions/{id}", s.apiKey("admin", s.handleAPISuppressionDelete))
	mux.HandleFunc("POST /api/v1/messages", s.apiKey("write", s.handleAPIMessageSend))
	mux.HandleFunc("GET /api/v1/messages", s.apiKey("read", s.handleAPIMessageHistory))
	mux.HandleFunc("GET /api/v1/messages/{id}", s.apiKey("read", s.handleAPIMessageDetail))
	mux.HandleFunc("GET /api/v1/templates", s.apiKey("read", s.handleAPITemplates))
	mux.HandleFunc("POST /api/v1/templates", s.apiKey("write", s.handleAPITemplateCreate))
	mux.HandleFunc("PATCH /api/v1/templates/{id}", s.apiKey("write", s.handleAPITemplateUpdate))
	mux.HandleFunc("DELETE /api/v1/templates/{id}", s.apiKey("write", s.handleAPITemplateDelete))
}

func (s *Server) apiKey(required string, next func(http.ResponseWriter, *http.Request, apiKeyRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Use a Bearer API key")
			return
		}
		raw := strings.TrimSpace(header[7:])
		var key apiKeyRecord
		var expiresAt *time.Time
		var revokedAt *time.Time
		err := s.db.QueryRow(r.Context(), `
			SELECT id::text,name,scopes,expires_at,revoked_at
			FROM control.api_keys WHERE key_hash=$1
		`, hashToken(raw)).Scan(&key.ID, &key.Name, &key.Scopes, &expiresAt, &revokedAt)
		if err != nil || revokedAt != nil || (expiresAt != nil && expiresAt.Before(time.Now())) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid or expired API key")
			return
		}
		allowed := required == "read" && (containsString(key.Scopes, "read") || containsString(key.Scopes, "write") || containsString(key.Scopes, "admin"))
		if required == "write" && (containsString(key.Scopes, "write") || containsString(key.Scopes, "admin")) {
			allowed = true
		}
		if required == "admin" && containsString(key.Scopes, "admin") {
			allowed = true
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "SCOPE", "API key does not have the "+required+" scope")
			return
		}
		_, _ = s.db.Exec(r.Context(), `UPDATE control.api_keys SET last_used_at=now() WHERE id=$1`, key.ID)
		next(w, r.WithContext(context.WithValue(r.Context(), apiKeyContextKey{}, key)), key)
	}
}

func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,name,prefix,scopes,created_at,last_used_at,expires_at,revoked_at
		FROM control.api_keys ORDER BY created_at DESC
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, prefix string
		var scopes []string
		var created time.Time
		var lastUsed, expires, revoked *time.Time
		if rows.Scan(&id, &name, &prefix, &scopes, &created, &lastUsed, &expires, &revoked) == nil {
			status := "active"
			if revoked != nil {
				status = "revoked"
			} else if expires != nil && expires.Before(time.Now()) {
				status = "expired"
			}
			item := map[string]any{"id": id, "name": name, "prefix": prefix, "scopes": scopes,
				"createdAt": created.UTC().Format(time.RFC3339), "status": status}
			if lastUsed != nil {
				item["lastUsedAt"] = lastUsed.UTC().Format(time.RFC3339)
			}
			if expires != nil {
				item["expiresAt"] = expires.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apiKeys": items})
}

func (s *Server) handleAPIKeyCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	scopes := []string{}
	seen := map[string]bool{}
	for _, scope := range body.Scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope != "read" && scope != "write" && scope != "admin" {
			writeError(w, http.StatusBadRequest, "SCOPE", "Scopes must be read, write or admin")
			return
		}
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		scopes = []string{"read"}
	}
	if body.ExpiresInDays < 0 || body.ExpiresInDays > 730 {
		writeError(w, http.StatusBadRequest, "EXPIRY", "Expiry must be between 0 and 730 days")
		return
	}
	secret, err := randomToken(36)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_KEY", "Could not generate a key")
		return
	}
	raw := "a8m_" + secret
	prefix := raw[:16]
	var expires any
	if body.ExpiresInDays > 0 {
		expires = time.Now().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour)
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.api_keys (name,prefix,key_hash,scopes,created_by,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text
	`, strings.TrimSpace(body.Name), prefix, hashToken(raw), scopes, sess.User.ID, expires).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "api_key.created", prefix, "Created an API key", map[string]any{"scopes": scopes})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "key": raw, "prefix": prefix,
		"message": "Copy the API key now. Only its SHA-256 digest is stored."})
}

func (s *Server) handleAPIKeyRevoke(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `UPDATE control.api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Active API key not found")
		return
	}
	s.actorAudit(r, sess, "api_key.revoked", id, "Revoked an API key", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIRoot(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "alt8mail-control", "version": s.version, "api": "v1",
		"key":       map[string]any{"id": key.ID, "name": key.Name, "scopes": key.Scopes},
		"endpoints": []string{"/api/v1/suppressions", "/api/v1/messages", "/api/v1/templates"},
	})
}

func (s *Server) handleAPISuppressions(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,direction,pattern::text,kind,reason,expires_at,created_at
		FROM control.suppressions ORDER BY direction,pattern
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, direction, pattern, kind, reason string
		var expires *time.Time
		var created time.Time
		if rows.Scan(&id, &direction, &pattern, &kind, &reason, &expires, &created) == nil {
			items = append(items, map[string]any{"id": id, "direction": direction, "pattern": pattern,
				"kind": kind, "reason": reason, "expiresAt": expires, "createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressions": items})
}

func (s *Server) handleAPISuppressionCreate(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	var body struct {
		Direction string `json:"direction"`
		Pattern   string `json:"pattern"`
		Kind      string `json:"kind"`
		Reason    string `json:"reason"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Direction == "" {
		body.Direction = "outbound"
	}
	if body.Direction != "outbound" && body.Direction != "inbound" {
		writeError(w, http.StatusBadRequest, "DIRECTION", "Direction must be outbound or inbound")
		return
	}
	pattern, kind, err := normalizeSuppressionPattern(body.Pattern, body.Kind)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PATTERN", err.Error())
		return
	}
	expires, err := parseOptionalExpiry(body.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "EXPIRY", err.Error())
		return
	}
	reason := truncateText(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(body.Reason, "\r", " "), "\n", " ")), 300)
	if reason == "" {
		reason = "Suppressed by API client " + key.Name
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.suppressions (direction,pattern,kind,reason,expires_at)
		VALUES ($1,$2,$3,$4,$5) RETURNING id::text
	`, body.Direction, pattern, kind, reason, expires).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "DUPLICATE", "That suppression already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	changed, _ := s.writeSuppressionMaps(r.Context())
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.audit(r.Context(), nil, "api.suppression_created", pattern, "Created a suppression through the API",
		s.requestIP(r), userAgent(r), map[string]any{"key": key.ID, "direction": body.Direction})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleAPISuppressionDelete(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	id := pathID(r)
	var pattern string
	_ = s.db.QueryRow(r.Context(), `SELECT pattern::text FROM control.suppressions WHERE id=$1`, id).Scan(&pattern)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.suppressions WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Suppression not found")
		return
	}
	changed, _ := s.writeSuppressionMaps(r.Context())
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.audit(r.Context(), nil, "api.suppression_deleted", pattern, "Deleted a suppression through the API",
		s.requestIP(r), userAgent(r), map[string]any{"key": key.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func renderTemplate(templateText string, data map[string]any) string {
	return templateVarPattern.ReplaceAllStringFunc(templateText, func(match string) string {
		groups := templateVarPattern.FindStringSubmatch(match)
		if len(groups) != 2 {
			return match
		}
		value, ok := data[groups[1]]
		if !ok {
			return match
		}
		return fmt.Sprint(value)
	})
}

func (s *Server) handleAPIMessageSend(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	var body struct {
		From      string            `json:"from"`
		To        any               `json:"to"`
		Cc        any               `json:"cc"`
		Subject   string            `json:"subject"`
		Text      string            `json:"text"`
		HTML      string            `json:"html"`
		Template  string            `json:"template"`
		Variables map[string]any    `json:"variables"`
		Headers   map[string]string `json:"headers"`
	}
	if err := decodeJSON(w, r, &body, 2<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	recipients := normalizeRecipientList(body.To)
	cc := normalizeRecipientList(body.Cc)
	all := append(append([]string{}, recipients...), cc...)
	if len(all) == 0 || len(all) > 50 {
		writeError(w, http.StatusBadRequest, "RECIPIENTS", "Provide between 1 and 50 recipients")
		return
	}
	if body.From == "" {
		body.From = s.defaultMailFrom(r.Context())
	}
	fromAddress, err := mail.ParseAddress(body.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FROM", "From address is invalid")
		return
	}
	fromDomain := emailDomain(fromAddress.Address)
	var managed int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.mail_domains WHERE domain=$1`, fromDomain).Scan(&managed)
	if managed == 0 {
		writeError(w, http.StatusBadRequest, "FROM", "From address domain is not managed by this control panel")
		return
	}
	if body.Template != "" {
		var subject, textTemplate, htmlTemplate string
		err := s.db.QueryRow(r.Context(), `SELECT subject,body_text,body_html FROM control.message_templates WHERE slug=$1`, body.Template).Scan(&subject, &textTemplate, &htmlTemplate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "TEMPLATE", "Unknown template")
			return
		}
		body.Subject = renderTemplate(subject, body.Variables)
		body.Text = renderTemplate(textTemplate, body.Variables)
		body.HTML = renderTemplate(htmlTemplate, body.Variables)
	}
	if strings.TrimSpace(body.Subject) == "" || (strings.TrimSpace(body.Text) == "" && strings.TrimSpace(body.HTML) == "") {
		writeError(w, http.StatusBadRequest, "MESSAGE", "Subject and text or HTML content are required")
		return
	}
	headers := map[string]string{}
	for name, value := range body.Headers {
		if !regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`).MatchString(name) {
			continue
		}
		switch strings.ToLower(name) {
		case "from", "to", "cc", "bcc", "subject", "date", "message-id", "mime-version":
			continue
		}
		headers[name] = value
	}
	messageID, transcript, err := s.sendSMTPMessage(r.Context(), fromAddress.Address, all, body.Subject, body.Text, body.HTML, headers)
	status := "sent"
	detail := ""
	if err != nil {
		status = "failed"
		detail = err.Error()
	}
	var historyID string
	_ = s.db.QueryRow(r.Context(), `
		INSERT INTO control.api_send_history (api_key_id,message_id,sender,recipient,subject,status,detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text
	`, key.ID, messageID, fromAddress.Address, strings.Join(all, ", "), body.Subject, status, detail).Scan(&historyID)
	s.audit(r.Context(), nil, "api.message_sent", strings.Join(all, ", "), "Sent a transactional message via API",
		s.requestIP(r), userAgent(r), map[string]any{"key": key.ID, "status": status, "messageId": messageID})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "SMTP", "message": err.Error()},
			"id": historyID, "messageId": messageID, "transcript": transcript})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": historyID, "messageId": messageID,
		"recipients": all, "status": "sent", "transcript": transcript})
}

func normalizeRecipientList(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case string:
		for _, item := range strings.Split(typed, ",") {
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
	case []any:
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

func (s *Server) handleAPIMessageHistory(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,message_id,sender::text,recipient::text,subject,status,detail,created_at
		FROM control.api_send_history ORDER BY created_at DESC LIMIT 200
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, messageID, sender, recipient, subject, status, detail string
		var created time.Time
		if rows.Scan(&id, &messageID, &sender, &recipient, &subject, &status, &detail, &created) == nil {
			items = append(items, map[string]any{"id": id, "messageId": messageID, "from": sender,
				"to": recipient, "subject": subject, "status": status, "detail": detail,
				"createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": items})
}

func (s *Server) handleAPIMessageDetail(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	var id, messageID, sender, recipient, subject, status, detail string
	var created time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT id::text,message_id,sender::text,recipient::text,subject,status,detail,created_at
		FROM control.api_send_history WHERE id=$1
	`, pathID(r)).Scan(&id, &messageID, &sender, &recipient, &subject, &status, &detail, &created)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "messageId": messageID, "from": sender,
		"to": recipient, "subject": subject, "status": status, "detail": detail,
		"createdAt": created.UTC().Format(time.RFC3339)})
}

func (s *Server) handleAPITemplates(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	rows, err := s.db.Query(r.Context(), `SELECT id::text,name,slug,subject,body_text,body_html,updated_at FROM control.message_templates ORDER BY name`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, slug, subject, textBody, htmlBody string
		var updated time.Time
		if rows.Scan(&id, &name, &slug, &subject, &textBody, &htmlBody, &updated) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "slug": slug, "subject": subject,
				"text": textBody, "html": htmlBody, "updatedAt": updated.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": items})
}

func (s *Server) handleAPITemplateCreate(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	var body struct {
		Name    string `json:"name"`
		Slug    string `json:"slug"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
		HTML    string `json:"html"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`).MatchString(strings.ToLower(body.Slug)) {
		writeError(w, http.StatusBadRequest, "SLUG", "Slug must be lowercase letters, digits and hyphens")
		return
	}
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.message_templates (name,slug,subject,body_text,body_html)
		VALUES ($1,$2,$3,$4,$5) RETURNING id::text
	`, strings.TrimSpace(body.Name), strings.ToLower(body.Slug), body.Subject, body.Text, body.HTML).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "SLUG", "A template with that slug already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	s.audit(r.Context(), nil, "api.template_created", body.Slug, "Created a message template through the API",
		s.requestIP(r), userAgent(r), map[string]any{"key": key.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "slug": strings.ToLower(body.Slug)})
}

func (s *Server) handleAPITemplateUpdate(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	var body struct {
		Name    *string `json:"name"`
		Subject *string `json:"subject"`
		Text    *string `json:"text"`
		HTML    *string `json:"html"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.message_templates SET name=COALESCE($2,name),subject=COALESCE($3,subject),
		  body_text=COALESCE($4,body_text),body_html=COALESCE($5,body_html),updated_at=now()
		WHERE id=$1
	`, pathID(r), body.Name, body.Subject, body.Text, body.HTML)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Template not found")
		return
	}
	s.audit(r.Context(), nil, "api.template_updated", pathID(r), "Updated a message template through the API",
		s.requestIP(r), userAgent(r), map[string]any{"key": key.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPITemplateDelete(w http.ResponseWriter, r *http.Request, key apiKeyRecord) {
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.message_templates WHERE id=$1`, pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Template not found")
		return
	}
	s.audit(r.Context(), nil, "api.template_deleted", pathID(r), "Deleted a message template through the API",
		s.requestIP(r), userAgent(r), map[string]any{"key": key.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
