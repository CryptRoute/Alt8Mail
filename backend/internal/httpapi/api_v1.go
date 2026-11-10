package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func apiV1Root(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "Alt8Mail API", "version": "v1", "openapi": "/api/v1/openapi.json",
		"authentication": "Bearer tenant API key", "scopes": apiSupportedScopes,
	})
}

func apiV1OpenAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title": "Alt8Mail API", "version": "1.0.0",
			"description": "Tenant-scoped mail, task and calendar API. Authenticate with a tenant API key. POST requests accept an Idempotency-Key header.",
		},
		"servers": []map[string]string{{"url": "/api/v1"}},
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}},
			"schemas": map[string]any{
				"SendMessage": map[string]any{"type": "object", "required": []string{"to", "subject", "bodyText"}, "properties": map[string]any{
					"to": map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
					"cc": map[string]any{"type": "string"}, "bcc": map[string]any{"type": "string"},
					"subject": map[string]any{"type": "string"}, "bodyText": map[string]any{"type": "string"},
				}},
				"Task": map[string]any{"type": "object", "required": []string{"title"}, "properties": map[string]any{
					"title": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"},
					"due": map[string]any{"type": "string"}, "priority": map[string]any{"type": "string"},
				}},
			},
		},
		"security": []map[string]any{{"bearerAuth": []string{}}},
		"paths": map[string]any{
			"/me":              map[string]any{"get": map[string]any{"summary": "Current API key owner", "responses": map[string]any{"200": map[string]any{"description": "OK"}}}},
			"/messages":        map[string]any{"get": map[string]any{"summary": "List mailbox messages", "parameters": []map[string]any{{"name": "folder", "in": "query", "schema": map[string]any{"type": "string"}}, {"name": "q", "in": "query", "schema": map[string]any{"type": "string"}}, {"name": "limit", "in": "query", "schema": map[string]any{"type": "integer"}}, {"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}}}, "responses": map[string]any{"200": map[string]any{"description": "OK"}}}},
			"/messages/{id}":   map[string]any{"get": map[string]any{"summary": "Read a message", "responses": map[string]any{"200": map[string]any{"description": "OK"}, "404": map[string]any{"description": "Not found"}}}},
			"/messages/send":   map[string]any{"post": map[string]any{"summary": "Queue an outbound message", "responses": map[string]any{"202": map[string]any{"description": "Queued"}, "409": map[string]any{"description": "Idempotency replay"}}}},
			"/tasks":           map[string]any{"get": map[string]any{"summary": "List tasks with cursor pagination", "parameters": []map[string]any{{"name": "status", "in": "query", "schema": map[string]any{"type": "string"}}, {"name": "limit", "in": "query", "schema": map[string]any{"type": "integer"}}, {"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}}}, "responses": map[string]any{"200": map[string]any{"description": "OK"}}}, "post": map[string]any{"summary": "Create a task", "responses": map[string]any{"201": map[string]any{"description": "Created"}}}},
			"/calendar/events": map[string]any{"get": map[string]any{"summary": "List calendar events with cursor pagination", "parameters": []map[string]any{{"name": "from", "in": "query", "schema": map[string]any{"type": "string"}}, {"name": "to", "in": "query", "schema": map[string]any{"type": "string"}}, {"name": "limit", "in": "query", "schema": map[string]any{"type": "integer"}}, {"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}}}, "responses": map[string]any{"200": map[string]any{"description": "OK"}}}},
		},
	})
}

func (s *Server) apiV1Me(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenantId": principal.TenantID, "userId": principal.UserID,
		"email": principal.User.Email, "name": principal.User.Name,
		"scopes": principal.Scopes,
	})
}

func encodeAPICursor(stamp time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(stamp.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeAPICursor(raw string) (time.Time, string, bool) {
	value, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, "", false
	}
	parts := strings.SplitN(string(value), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", false
	}
	stamp, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}
	return stamp, parts[1], true
}

func (s *Server) apiV1ListMessages(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	folder := strings.TrimSpace(r.URL.Query().Get("folder"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	var cursorTime *time.Time
	cursorID := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		stamp, id, ok := decodeAPICursor(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "API_CURSOR", "cursor is invalid")
			return
		}
		cursorTime, cursorID = &stamp, id
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, thread_id, subject, from_name, from_email::text, folder, snippet, sent_at, unread, starred, classification
		FROM public.mail_messages
		WHERE user_id=$1 AND tenant_id=$2 AND NOT draft
		  AND ($3='' OR lower(folder)=lower($3))
		  AND ($4='' OR subject ILIKE $5 ESCAPE '\' OR from_email::text ILIKE $5 ESCAPE '\' OR body_text ILIKE $5 ESCAPE '\')
		  AND ($6::timestamptz IS NULL OR (sent_at, id) < ($6::timestamptz, $7))
		ORDER BY sent_at DESC, id DESC LIMIT $8
	`, principal.UserID, principal.TenantID, folder, query, likePattern(query), cursorTime, cursorID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_MESSAGES", "Could not list messages")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var lastSent time.Time
	lastID := ""
	for rows.Next() {
		var id, threadID, subject, fromName, fromEmail, itemFolder, snippet, classification string
		var sent time.Time
		var unread, starred bool
		if rows.Scan(&id, &threadID, &subject, &fromName, &fromEmail, &itemFolder, &snippet, &sent, &unread, &starred, &classification) == nil {
			lastSent, lastID = sent, id
			items = append(items, map[string]any{
				"id": id, "threadId": threadID, "subject": subject, "from": map[string]any{"name": fromName, "email": fromEmail},
				"folder": itemFolder, "snippet": snippet, "sentAt": sent.UTC().Format(time.RFC3339),
				"unread": unread, "starred": starred, "classification": classification,
			})
		}
	}
	nextCursor := ""
	if len(items) == limit {
		nextCursor = encodeAPICursor(lastSent, lastID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "limit": limit, "nextCursor": nextCursor})
}

func (s *Server) apiV1GetMessage(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	var id, threadID, subject, fromName, fromEmail, folder, bodyText, bodyHTML, classification string
	var sent time.Time
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id, thread_id, subject, from_name, from_email::text, folder, body_text, body_html, sent_at, classification
		FROM public.mail_messages WHERE id=$1 AND user_id=$2 AND tenant_id=$3
	`, r.PathValue("id"), principal.UserID, principal.TenantID).Scan(
		&id, &threadID, &subject, &fromName, &fromEmail, &folder, &bodyText, &bodyHTML, &sent, &classification)
	if err != nil {
		writeError(w, http.StatusNotFound, "API_MESSAGE", "Message not found")
		return
	}
	attachments := []map[string]any{}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT a.id, a.name, a.mime, a.size_bytes FROM public.mail_message_attachments ma
		JOIN public.attachments a ON a.id=ma.attachment_id WHERE ma.message_id=$1
	`, id)
	if err == nil {
		for rows.Next() {
			var attachmentID, name, mime string
			var size int64
			if rows.Scan(&attachmentID, &name, &mime, &size) == nil {
				attachments = append(attachments, map[string]any{"id": attachmentID, "name": name, "mime": mime, "sizeBytes": size})
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "threadId": threadID, "subject": subject,
		"from": map[string]any{"name": fromName, "email": fromEmail}, "folder": folder,
		"bodyText": bodyText, "bodyHtml": bodyHTML, "sentAt": sent.UTC().Format(time.RFC3339),
		"classification": classification, "attachments": attachments,
	})
}

func apiRecipientList(value any) []string {
	values := []string{}
	switch typed := value.(type) {
	case string:
		values = strings.FieldsFunc(typed, func(char rune) bool { return char == ',' || char == ';' || char == '\n' })
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			} else if object, ok := item.(map[string]any); ok {
				values = append(values, stateString(object, "email"))
			}
		}
	case []string:
		values = append(values, typed...)
	}
	out := []string{}
	for _, value := range values {
		email := normalizeEmailValue(value)
		if looksLikeEmail(email) {
			out = append(out, email)
		}
	}
	return apiUniqueRecipients(out)
}

func apiUniqueRecipients(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (s *Server) apiV1SendMessage(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	var body struct {
		To       any    `json:"to"`
		CC       any    `json:"cc"`
		BCC      any    `json:"bcc"`
		Subject  string `json:"subject"`
		BodyText string `json:"bodyText"`
		BodyHTML string `json:"bodyHtml"`
	}
	if err := decodeJSON(w, r, &body, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	to := apiRecipientList(body.To)
	cc := apiRecipientList(body.CC)
	bcc := apiRecipientList(body.BCC)
	if len(to) == 0 || len(to)+len(cc)+len(bcc) > 100 {
		writeError(w, http.StatusBadRequest, "API_MESSAGE", "Use between 1 and 100 valid recipients")
		return
	}
	subject := truncate(strings.TrimSpace(body.Subject), 1000)
	bodyText := truncate(strings.TrimSpace(body.BodyText), 100000)
	if bodyText == "" {
		writeError(w, http.StatusBadRequest, "API_MESSAGE", "bodyText is required")
		return
	}
	idempotency := apiIdempotencyKey(r)
	if status, payload, found := apiIdempotencyLookup(r.Context(), s, principal, idempotency, http.MethodPost, r.URL.Path); found {
		writeJSON(w, status, payload)
		return
	}
	classification, dlpWarnings, dlpMatches, policyErr := s.validateProgrammaticSend(r.Context(), principal.User, to, cc, bcc, subject, bodyText, "Internal")
	if policyErr != nil {
		status := http.StatusForbidden
		if len(dlpMatches) > 0 || policyErr.Error() == "message blocked by organization data protection policy" {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{
			"code": "API_DLP", "message": policyErr.Error(),
			"details": map[string]any{"matches": dlpMatches, "warnings": dlpWarnings},
		}})
		return
	}
	var accountID string
	if err := s.store.DB.QueryRow(r.Context(), `SELECT id FROM public.mail_accounts WHERE user_id=$1 AND kind='native' LIMIT 1`, principal.UserID).Scan(&accountID); err != nil {
		writeError(w, http.StatusConflict, "API_MESSAGE", "The mailbox has no native sending account")
		return
	}
	toJSON, _ := json.Marshal(to)
	ccJSON, _ := json.Marshal(cc)
	bccJSON, _ := json.Marshal(bcc)
	fromJSON, _ := json.Marshal(map[string]any{"name": principal.User.Name, "email": principal.User.Email})
	outboxID := newID("api")
	html := strings.TrimSpace(body.BodyHTML)
	if html == "" {
		html = textToHTML(bodyText)
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.outbox
		  (id, tenant_id, user_id, from_json, to_json, cc_json, bcc_json, subject, body_text, body_html,
		   status, scheduled, send_at, account_id, priority, classification)
		VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6::jsonb,$7::jsonb,$8,$9,$10,'queued',true,now(),$11,'normal',$12)
	`, outboxID, principal.TenantID, principal.UserID, fromJSON, toJSON, ccJSON, bccJSON,
		subject, bodyText, truncate(html, 200000), accountID, classification)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_MESSAGE", "Could not queue the message")
		return
	}
	payload := map[string]any{
		"messageId": outboxID, "subject": subject, "to": to,
		"tenantId": principal.TenantID, "queuedAt": time.Now().UTC().Format(time.RFC3339),
	}
	eventID := s.enqueueWebhookEvent(r.Context(), principal.TenantID, "message.queued", payload)
	response := map[string]any{"id": outboxID, "status": "queued", "eventId": eventID, "classification": classification, "dlpWarnings": dlpWarnings}
	apiIdempotencyStore(r.Context(), s, principal, idempotency, http.MethodPost, r.URL.Path, http.StatusAccepted, response)
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) apiV1ListTasks(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	var cursorTime *time.Time
	cursorID := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		stamp, id, ok := decodeAPICursor(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "API_CURSOR", "cursor is invalid")
			return
		}
		cursorTime, cursorID = &stamp, id
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, title, notes, status, priority, COALESCE(to_char(due_date,'YYYY-MM-DD'),''),
		       COALESCE(linked_thread_id,''), created_at, updated_at
		FROM public.tasks
		WHERE user_id=$1 AND ($2='' OR status=$2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3::timestamptz, $4))
		ORDER BY created_at DESC, id DESC LIMIT $5
	`, principal.UserID, status, cursorTime, cursorID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_TASKS", "Could not list tasks")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var lastCreated time.Time
	lastID := ""
	for rows.Next() {
		var id, title, notes, itemStatus, priority, due, threadID string
		var created, updated time.Time
		if rows.Scan(&id, &title, &notes, &itemStatus, &priority, &due, &threadID, &created, &updated) == nil {
			lastCreated, lastID = created, id
			items = append(items, map[string]any{
				"id": id, "title": title, "notes": notes, "status": itemStatus, "priority": priority,
				"due": due, "threadId": threadID, "createdAt": created.UTC().Format(time.RFC3339),
				"updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	nextCursor := ""
	if len(items) == limit {
		nextCursor = encodeAPICursor(lastCreated, lastID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "limit": limit, "nextCursor": nextCursor})
}

func (s *Server) apiV1CreateTask(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	var body struct {
		Title    string `json:"title"`
		Notes    string `json:"notes"`
		Due      string `json:"due"`
		Priority string `json:"priority"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	title := truncate(strings.TrimSpace(body.Title), 200)
	if title == "" {
		writeError(w, http.StatusBadRequest, "API_TASK", "title is required")
		return
	}
	due := strings.TrimSpace(body.Due)
	if due != "" && !taskDatePattern.MatchString(due) {
		writeError(w, http.StatusBadRequest, "API_TASK", "due must be YYYY-MM-DD")
		return
	}
	priority := firstNonEmpty(strings.TrimSpace(body.Priority), "normal")
	if priority != "low" && priority != "normal" && priority != "high" && priority != "urgent" {
		writeError(w, http.StatusBadRequest, "API_TASK", "Invalid priority")
		return
	}
	idempotency := apiIdempotencyKey(r)
	if status, payload, found := apiIdempotencyLookup(r.Context(), s, principal, idempotency, http.MethodPost, r.URL.Path); found {
		writeJSON(w, status, payload)
		return
	}
	taskID := newID("task")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.tasks (id, tenant_id, user_id, title, notes, status, priority, due_date, source)
		VALUES ($1,$2,$3,$4,$5,'open',$6,NULLIF($7,'')::date,'api')
	`, taskID, principal.TenantID, principal.UserID, title, truncate(strings.TrimSpace(body.Notes), 5000), priority, due)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_TASK", "Could not create the task")
		return
	}
	payload := map[string]any{"taskId": taskID, "title": title, "priority": priority, "due": due,
		"tenantId": principal.TenantID, "createdAt": time.Now().UTC().Format(time.RFC3339)}
	eventID := s.enqueueWebhookEvent(r.Context(), principal.TenantID, "task.created", payload)
	response := map[string]any{"id": taskID, "status": "open", "eventId": eventID}
	apiIdempotencyStore(r.Context(), s, principal, idempotency, http.MethodPost, r.URL.Path, http.StatusCreated, response)
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) apiV1ListCalendar(w http.ResponseWriter, r *http.Request) {
	principal := apiPrincipalFrom(r)
	start := time.Now().UTC()
	end := start.AddDate(0, 0, 7)
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		start, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "API_CALENDAR", "from must be RFC3339")
			return
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		end, err = time.Parse(time.RFC3339, raw)
		if err != nil || !end.After(start) {
			writeError(w, http.StatusBadRequest, "API_CALENDAR", "to must be RFC3339 and after from")
			return
		}
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	var cursorTime *time.Time
	cursorID := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		stamp, id, ok := decodeAPICursor(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "API_CURSOR", "cursor is invalid")
			return
		}
		cursorTime, cursorID = &stamp, id
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, calendar_id, title, COALESCE(description,''), COALESCE(location,''), starts_at, ends_at, all_day, status
		FROM public.calendar_events
		WHERE user_id=$1 AND starts_at >= $2 AND starts_at < $3
		  AND ($4::timestamptz IS NULL OR (starts_at, id) > ($4::timestamptz, $5))
		ORDER BY starts_at ASC, id ASC LIMIT $6
	`, principal.UserID, start, end, cursorTime, cursorID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "API_CALENDAR", "Could not list calendar events")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var lastStart time.Time
	lastID := ""
	for rows.Next() {
		var id, calendarID, title, description, location, status string
		var starts, ends time.Time
		var allDay bool
		if rows.Scan(&id, &calendarID, &title, &description, &location, &starts, &ends, &allDay, &status) == nil {
			lastStart, lastID = starts, id
			items = append(items, map[string]any{
				"id": id, "calendarId": calendarID, "title": title, "description": description,
				"location": location, "startsAt": starts.UTC().Format(time.RFC3339),
				"endsAt": ends.UTC().Format(time.RFC3339), "allDay": allDay, "status": status,
			})
		}
	}
	nextCursor := ""
	if len(items) == limit {
		nextCursor = encodeAPICursor(lastStart, lastID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "limit": limit, "nextCursor": nextCursor,
		"from": start.UTC().Format(time.RFC3339), "to": end.UTC().Format(time.RFC3339)})
}

func (s *Server) registerAPIPlatformRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1", apiV1Root)
	mux.HandleFunc("GET /api/v1/openapi.json", apiV1OpenAPI)
	mux.HandleFunc("GET /api/v1/me", s.requireAPIKey("", s.apiV1Me))
	mux.HandleFunc("GET /api/v1/messages", s.requireAPIKey("mail.read", s.apiV1ListMessages))
	mux.HandleFunc("GET /api/v1/messages/{id}", s.requireAPIKey("mail.read", s.apiV1GetMessage))
	mux.HandleFunc("POST /api/v1/messages/send", s.requireAPIKey("mail.send", s.apiV1SendMessage))
	mux.HandleFunc("GET /api/v1/tasks", s.requireAPIKey("tasks.read", s.apiV1ListTasks))
	mux.HandleFunc("POST /api/v1/tasks", s.requireAPIKey("tasks.write", s.apiV1CreateTask))
	mux.HandleFunc("GET /api/v1/calendar/events", s.requireAPIKey("calendar.read", s.apiV1ListCalendar))
}

func (s *Server) registerAPIAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api-keys", s.adminRequirePermission("api.read", s.adminListAPIKeys))
	mux.HandleFunc("POST /api-keys", s.adminRequirePermission("api.manage", s.adminCreateAPIKey))
	mux.HandleFunc("DELETE /api-keys/{id}", s.adminRequirePermission("api.manage", s.adminRevokeAPIKey))
	mux.HandleFunc("GET /webhooks", s.adminRequirePermission("api.read", s.adminListWebhooks))
	mux.HandleFunc("POST /webhooks", s.adminRequirePermission("webhooks.manage", s.adminCreateWebhook))
	mux.HandleFunc("PATCH /webhooks/{id}", s.adminRequirePermission("webhooks.manage", s.adminUpdateWebhook))
	mux.HandleFunc("DELETE /webhooks/{id}", s.adminRequirePermission("webhooks.manage", s.adminDeleteWebhook))
	mux.HandleFunc("POST /webhooks/{id}/test", s.adminRequirePermission("webhooks.manage", s.adminTestWebhook))
	mux.HandleFunc("GET /webhooks/deliveries", s.adminRequirePermission("api.read", s.adminListWebhookDeliveries))
	mux.HandleFunc("GET /crm/state", s.adminFeature("crm.read", FeatureCRMSync, s.adminCRMState))
	mux.HandleFunc("GET /crm/records", s.adminFeature("crm.read", FeatureCRMSync, s.adminCRMRecords))
	mux.HandleFunc("POST /crm/sync", s.adminFeature("crm.manage", FeatureCRMSync, s.adminSyncCRM))

}
