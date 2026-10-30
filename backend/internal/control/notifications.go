package control

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

var notificationEvents = []string{
	"auth.lockout", "auth.login_failed", "ssl.failed", "ssl.renewed", "ssl.expiring",
	"backup.completed", "backup.failed", "blocklist.listed", "service.down",
	"service.up", "user.created", "user.deleted", "domain.created",
	"domain.deleted", "outbound.ip_discovered", "config.sync",
	"system.updated", "system.update_failed", "ssl.imported", "ssl.assigned",
	"security.report", "security.report_reviewed",
}

func (s *Server) registerNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/notifications/events", s.require("notifications.read", s.handleNotificationEvents))
	mux.HandleFunc("GET /control/api/notifications/handles", s.require("notifications.read", s.handleNotificationHandles))
	mux.HandleFunc("POST /control/api/notifications/handles", s.require("notifications.manage", s.handleNotificationHandleCreate))
	mux.HandleFunc("PATCH /control/api/notifications/handles/{id}", s.require("notifications.manage", s.handleNotificationHandleUpdate))
	mux.HandleFunc("DELETE /control/api/notifications/handles/{id}", s.require("notifications.manage", s.handleNotificationHandleDelete))
	mux.HandleFunc("POST /control/api/notifications/handles/{id}/test", s.require("notifications.manage", s.handleNotificationHandleTest))
	mux.HandleFunc("GET /control/api/notifications/deliveries", s.require("notifications.read", s.handleNotificationDeliveries))

	mux.HandleFunc("GET /control/api/webhooks", s.require("notifications.read", s.handleWebhooks))
	mux.HandleFunc("POST /control/api/webhooks", s.require("notifications.manage", s.handleWebhookCreate))
	mux.HandleFunc("PATCH /control/api/webhooks/{id}", s.require("notifications.manage", s.handleWebhookUpdate))
	mux.HandleFunc("DELETE /control/api/webhooks/{id}", s.require("notifications.manage", s.handleWebhookDelete))
	mux.HandleFunc("POST /control/api/webhooks/{id}/test", s.require("notifications.manage", s.handleWebhookTest))
	mux.HandleFunc("GET /control/api/webhooks/deliveries", s.require("notifications.read", s.handleWebhookDeliveries))
}

func (s *Server) handleNotificationEvents(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	writeJSON(w, http.StatusOK, map[string]any{"events": notificationEvents})
}

func eventMatches(events []string, event string) bool {
	for _, candidate := range events {
		if candidate == "*" || candidate == event {
			return true
		}
	}
	return false
}

func parseEvents(raw string) ([]string, error) {
	events := []string{}
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		if value != "*" && !containsString(notificationEvents, value) {
			return nil, fmt.Errorf("unknown event %q", value)
		}
		seen[value] = true
		events = append(events, value)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("choose at least one event")
	}
	return events, nil
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func (s *Server) handleNotificationHandles(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,name,kind,events,enabled,created_at FROM control.notification_handles
		WHERE user_id=$1 ORDER BY created_at DESC
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, kind string
		var events []string
		var enabled bool
		var created time.Time
		if rows.Scan(&id, &name, &kind, &events, &enabled, &created) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "kind": kind,
				"events": events, "enabled": enabled, "createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"handles": items, "events": notificationEvents})
}

func (s *Server) handleNotificationHandleCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Events     string `json:"events"`
		WebhookURL string `json:"webhookUrl"`
		Email      string `json:"email"`
		BotToken   string `json:"botToken"`
		ChatID     string `json:"chatId"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	events, err := parseEvents(body.Events)
	if err != nil {
		writeError(w, http.StatusBadRequest, "EVENTS", err.Error())
		return
	}
	config := map[string]any{}
	switch body.Kind {
	case "slack", "discord", "webhook":
		if !strings.HasPrefix(body.WebhookURL, "https://") && !strings.HasPrefix(body.WebhookURL, "http://") {
			writeError(w, http.StatusBadRequest, "URL", "Webhook URL must be http(s)")
			return
		}
		config["url"] = body.WebhookURL
	case "telegram":
		if body.BotToken == "" || body.ChatID == "" {
			writeError(w, http.StatusBadRequest, "TELEGRAM", "Bot token and chat id are required")
			return
		}
		config["botToken"] = body.BotToken
		config["chatId"] = body.ChatID
	case "email":
		if _, err := mail.ParseAddress(body.Email); err != nil {
			writeError(w, http.StatusBadRequest, "EMAIL", "Enter a valid email address")
			return
		}
		config["email"] = body.Email
	default:
		writeError(w, http.StatusBadRequest, "KIND", "Kind must be slack, discord, telegram, webhook or email")
		return
	}
	raw, _ := json.Marshal(config)
	sealed, err := s.secrets.Seal(string(raw))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS", "Could not protect notification credentials")
		return
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.notification_handles (user_id,name,kind,config_sealed,events)
		VALUES ($1,$2,$3,$4,$5) RETURNING id::text
	`, sess.User.ID, strings.TrimSpace(body.Name), body.Kind, sealed, events).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "notifications.handle_created", body.Kind, "Created a notification handle", map[string]any{"events": events})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleNotificationHandleUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Name    *string `json:"name"`
		Events  *string `json:"events"`
		Enabled *bool   `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var events []string
	var err error
	if body.Events != nil {
		events, err = parseEvents(*body.Events)
		if err != nil {
			writeError(w, http.StatusBadRequest, "EVENTS", err.Error())
			return
		}
	}
	var eventsArg any
	if events != nil {
		eventsArg = events
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.notification_handles SET
		  name=COALESCE($3,name), events=COALESCE($4,events), enabled=COALESCE($5,enabled), updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID, body.Name, eventsArg, body.Enabled)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Notification handle not found")
		return
	}
	s.actorAudit(r, sess, "notifications.handle_updated", id, "Updated a notification handle", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleNotificationHandleDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.notification_handles WHERE id=$1 AND user_id=$2`, id, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Notification handle not found")
		return
	}
	s.actorAudit(r, sess, "notifications.handle_deleted", id, "Deleted a notification handle", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleNotificationHandleTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var kind string
	var sealed string
	err := s.db.QueryRow(r.Context(), `SELECT kind,config_sealed FROM control.notification_handles WHERE id=$1 AND user_id=$2`, id, sess.User.ID).Scan(&kind, &sealed)
	if err != nil {
		writeFromError(w, err)
		return
	}
	configText, _ := s.secrets.Open(sealed)
	config := map[string]any{}
	_ = json.Unmarshal([]byte(configText), &config)
	payload := map[string]any{"event": "notification.test", "timestamp": nowRFC3339(),
		"data": map[string]any{"message": "This is an Alt8Mail System Control notification test."}}
	testCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = s.sendHandle(testCtx, id, kind, config, "notification.test", payload)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.actorAudit(r, sess, "notifications.handle_test", id, "Tested a notification handle", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleNotificationDeliveries(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT d.id,d.handle_id::text,h.name,d.event,d.status,d.detail,d.attempted_at
		FROM control.notification_deliveries d
		LEFT JOIN control.notification_handles h ON h.id=d.handle_id
		WHERE h.user_id=$1 OR h.user_id IS NULL
		ORDER BY d.attempted_at DESC LIMIT 200
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var handleID, name, event, status, detail string
		var at time.Time
		if rows.Scan(&id, &handleID, &name, &event, &status, &detail, &at) == nil {
			items = append(items, map[string]any{"id": id, "handleId": handleID, "name": name,
				"event": event, "status": status, "detail": detail, "attemptedAt": at.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}

func (s *Server) notifyEvent(ctx context.Context, event string, data map[string]any) {
	go s.dispatchEvent(event, data)
}

func (s *Server) dispatchEvent(event string, data map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT id::text,kind,config_sealed,events FROM control.notification_handles WHERE enabled
	`)
	if err == nil {
		handles := []struct {
			id, kind, sealed string
			events           []string
		}{}
		for rows.Next() {
			var id, kind, sealed string
			var events []string
			if rows.Scan(&id, &kind, &sealed, &events) == nil {
				handles = append(handles, struct {
					id, kind, sealed string
					events           []string
				}{id, kind, sealed, events})
			}
		}
		rows.Close()
		payload := map[string]any{"event": event, "timestamp": nowRFC3339(), "data": data}
		for _, handle := range handles {
			if !eventMatches(handle.events, event) {
				continue
			}
			configText, _ := s.secrets.Open(handle.sealed)
			config := map[string]any{}
			_ = json.Unmarshal([]byte(configText), &config)
			go func(id, kind string, config map[string]any) {
				hctx, hcancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer hcancel()
				_ = s.sendHandle(hctx, id, kind, config, event, payload)
			}(handle.id, handle.kind, config)
		}
	}
	webhookRows, err := s.db.Query(ctx, `SELECT id::text,url,secret_sealed,events FROM control.webhooks WHERE enabled`)
	if err == nil {
		defer webhookRows.Close()
		for webhookRows.Next() {
			var id, target, sealed string
			var events []string
			if webhookRows.Scan(&id, &target, &sealed, &events) != nil || !eventMatches(events, event) {
				continue
			}
			secret, _ := s.secrets.Open(sealed)
			go s.sendWebhook(context.Background(), id, target, secret, event, data)
		}
	}
}

func (s *Server) recordDelivery(ctx context.Context, handleID, event, status, detail string) {
	var id any
	if handleID != "" {
		id = handleID
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO control.notification_deliveries (handle_id,event,status,detail) VALUES ($1,$2,$3,$4)`,
		id, event, status, truncateText(detail, 1000))
}

func (s *Server) sendHandle(ctx context.Context, id, kind string, config map[string]any, event string, payload map[string]any) error {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	text := notificationText(event, payload)
	var err error
	switch kind {
	case "slack", "discord":
		body := map[string]any{"text": text}
		if kind == "discord" {
			body = map[string]any{"content": text}
		}
		err = postJSON(ctx, client, fmt.Sprint(config["url"]), body, nil)
	case "telegram":
		url := "https://api.telegram.org/bot" + fmt.Sprint(config["botToken"]) + "/sendMessage"
		err = postJSON(ctx, client, url, map[string]any{"chat_id": config["chatId"], "text": text}, nil)
	case "webhook":
		err = postJSON(ctx, client, fmt.Sprint(config["url"]), payload, nil)
	case "email":
		result := s.runSMTPTransaction(ctx, s.defaultMailFrom(ctx), fmt.Sprint(config["email"]),
			"Alt8Mail System Control: "+event, text)
		err = result.Err
	default:
		err = fmt.Errorf("unsupported handle kind")
	}
	status := "sent"
	detail := ""
	if err != nil {
		status, detail = "failed", err.Error()
	}
	s.recordDelivery(ctx, id, event, status, detail)
	return err
}

func notificationText(event string, payload map[string]any) string {
	raw, _ := json.Marshal(payload["data"])
	return "Alt8Mail System Control · " + event + "\n" + string(raw)
}

func postJSON(ctx context.Context, client *http.Client, url string, payload any, headers map[string]string) error {
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, truncateText(strings.TrimSpace(string(raw)), 300))
	}
	return nil
}

func (s *Server) sendWebhook(ctx context.Context, id, target, secret, event string, data map[string]any) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	timestamp := nowRFC3339()
	body, _ := json.Marshal(map[string]any{"event": event, "timestamp": timestamp, "data": data})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		s.recordWebhookDelivery(ctx, id, event, string(body), "failed", 0, err.Error())
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Alt8Mail-Event", event)
	request.Header.Set("X-Alt8Mail-Signature", signature)
	response, err := client.Do(request)
	if err != nil {
		s.recordWebhookDelivery(ctx, id, event, string(body), "failed", 0, err.Error())
		return
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	status := "sent"
	if response.StatusCode/100 != 2 {
		status = "failed"
	}
	s.recordWebhookDelivery(ctx, id, event, string(body), status, response.StatusCode, string(raw))
}

func (s *Server) recordWebhookDelivery(ctx context.Context, id, event, payload, status string, code int, response string) {
	_, _ = s.db.Exec(ctx, `
		INSERT INTO control.webhook_deliveries (webhook_id,event,payload,status,response_code,response_body)
		VALUES ($1,$2,$3::jsonb,$4,$5,$6)
	`, id, event, payload, status, code, truncateText(response, 2000))
}

func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,name,url,events,enabled,created_at FROM control.webhooks ORDER BY created_at DESC
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, url string
		var events []string
		var enabled bool
		var created time.Time
		if rows.Scan(&id, &name, &url, &events, &enabled, &created) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "url": url, "events": events,
				"enabled": enabled, "createdAt": created.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": items, "events": notificationEvents})
}

func (s *Server) handleWebhookCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Secret string `json:"secret"`
		Events string `json:"events"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !strings.HasPrefix(body.URL, "https://") && !strings.HasPrefix(body.URL, "http://") {
		writeError(w, http.StatusBadRequest, "URL", "Webhook URL must be http(s)")
		return
	}
	events, err := parseEvents(body.Events)
	if err != nil {
		writeError(w, http.StatusBadRequest, "EVENTS", err.Error())
		return
	}
	generated := false
	if body.Secret == "" {
		body.Secret, _ = randomToken(32)
		generated = true
	}
	sealed, err := s.secrets.Seal(body.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not protect the signing secret")
		return
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.webhooks (name,url,secret_sealed,events,created_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id::text
	`, strings.TrimSpace(body.Name), body.URL, sealed, events, sess.User.ID).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	response := map[string]any{"ok": true, "id": id}
	if generated {
		response["secret"] = body.Secret
		response["message"] = "Copy the signing secret now. It is not shown again."
	}
	s.actorAudit(r, sess, "webhook.created", body.URL, "Created an outbound webhook", map[string]any{"events": events})
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleWebhookUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Name    *string `json:"name"`
		URL     *string `json:"url"`
		Events  *string `json:"events"`
		Enabled *bool   `json:"enabled"`
		Secret  *string `json:"secret"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var events []string
	var err error
	if body.Events != nil {
		events, err = parseEvents(*body.Events)
		if err != nil {
			writeError(w, http.StatusBadRequest, "EVENTS", err.Error())
			return
		}
	}
	sealed := ""
	if body.Secret != nil && *body.Secret != "" {
		sealed, err = s.secrets.Seal(*body.Secret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not protect the signing secret")
			return
		}
	}
	var eventsArg, sealedArg any
	if events != nil {
		eventsArg = events
	}
	if sealed != "" {
		sealedArg = sealed
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.webhooks SET name=COALESCE($2,name),url=COALESCE($3,url),
		  events=COALESCE($4,events),enabled=COALESCE($5,enabled),
		  secret_sealed=COALESCE($6,secret_sealed),updated_at=now()
		WHERE id=$1
	`, id, body.Name, body.URL, eventsArg, body.Enabled, sealedArg)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Webhook not found")
		return
	}
	s.actorAudit(r, sess, "webhook.updated", id, "Updated an outbound webhook", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleWebhookDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.webhooks WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Webhook not found")
		return
	}
	s.actorAudit(r, sess, "webhook.deleted", id, "Deleted an outbound webhook", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var target, sealed string
	if err := s.db.QueryRow(r.Context(), `SELECT url,secret_sealed FROM control.webhooks WHERE id=$1`, id).Scan(&target, &sealed); err != nil {
		writeFromError(w, err)
		return
	}
	secret, _ := s.secrets.Open(sealed)
	s.sendWebhook(r.Context(), id, target, secret, "webhook.test", map[string]any{"message": "Alt8Mail webhook test"})
	s.actorAudit(r, sess, "webhook.test", target, "Sent a webhook test", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleWebhookDeliveries(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT d.id,d.webhook_id::text,w.name,d.event,d.status,d.response_code,left(d.response_body,500),d.attempted_at
		FROM control.webhook_deliveries d LEFT JOIN control.webhooks w ON w.id=d.webhook_id
		ORDER BY d.attempted_at DESC LIMIT 200
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var webhookID, name, event, status, response string
		var code int
		var at time.Time
		if rows.Scan(&id, &webhookID, &name, &event, &status, &code, &response, &at) == nil {
			items = append(items, map[string]any{"id": id, "webhookId": webhookID, "name": name,
				"event": event, "status": status, "responseCode": code, "response": response,
				"attemptedAt": at.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}
