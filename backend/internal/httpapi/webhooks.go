package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) validateWebhookURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, fmt.Errorf("enter a webhook URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" {
		return nil, fmt.Errorf("webhook URLs must use HTTPS")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("webhook URLs cannot contain credentials or fragments")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("webhook URLs must use port 443")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return nil, fmt.Errorf("enter a valid webhook host")
	}
	if !s.webhookAllowPrivate {
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return nil, fmt.Errorf("local webhook endpoints are not allowed")
		}
		if literal := net.ParseIP(host); literal != nil && !publicRemoteIP(literal) {
			return nil, fmt.Errorf("private webhook endpoints are not allowed")
		}
		resolved, err := net.LookupIP(host)
		if err != nil || len(resolved) == 0 {
			return nil, fmt.Errorf("webhook host could not be resolved")
		}
		for _, address := range resolved {
			if !publicRemoteIP(address) {
				return nil, fmt.Errorf("private webhook endpoints are not allowed")
			}
		}
	}
	return parsed, nil
}

func signWebhookPayload(secret string, timestamp int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("\n"))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) postWebhook(ctx context.Context, endpoint string, headers map[string]string, payload []byte) (int, error) {
	if s.webhookPost != nil {
		return s.webhookPost(ctx, endpoint, headers, payload)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return 0, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Alt8Mail-Webhook/1.0")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	return response.StatusCode, nil
}

func (s *Server) enqueueWebhookEvent(ctx context.Context, tenantID, event string, payload map[string]any) string {
	if !apiWebhookEventSupported(event) {
		return ""
	}
	eventID := newID("event")
	raw, _ := json.Marshal(payload)
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.webhook_deliveries (id, tenant_id, endpoint_id, event_id, event, payload)
		SELECT $1, e.tenant_id, e.id, $2, $3, $4::jsonb
		FROM public.webhook_endpoints e
		WHERE e.tenant_id=$5 AND e.enabled AND $3 = ANY(e.events)
		ON CONFLICT (endpoint_id, event_id) DO NOTHING
	`, newID("delivery"), eventID, event, raw, tenantID)
	return eventID
}

func (s *Server) runWebhookDeliveries(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT d.id, d.endpoint_id, d.event_id, d.event, d.payload, d.attempts,
		       e.url, e.secret_sealed
		FROM public.webhook_deliveries d
		JOIN public.webhook_endpoints e ON e.id=d.endpoint_id
		WHERE d.status='pending' AND d.next_attempt_at <= now() AND e.enabled
		ORDER BY d.next_attempt_at LIMIT 50
	`)
	if err != nil {
		return
	}
	type dueDelivery struct {
		id, endpointID, eventID, event, endpoint, sealed string
		payload                                          []byte
		attempts                                         int
	}
	deliveries := []dueDelivery{}
	for rows.Next() {
		var item dueDelivery
		if rows.Scan(&item.id, &item.endpointID, &item.eventID, &item.event, &item.payload,
			&item.attempts, &item.endpoint, &item.sealed) == nil {
			deliveries = append(deliveries, item)
		}
	}
	rows.Close()
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return
		}
		secret, err := s.openWorkspaceToken(delivery.sealed)
		if err != nil {
			continue
		}
		timestamp := time.Now().Unix()
		headers := map[string]string{
			"X-A8M-Event": delivery.event, "X-A8M-Delivery": delivery.eventID,
			"X-A8M-Timestamp": strconv.FormatInt(timestamp, 10),
			"X-A8M-Signature": signWebhookPayload(secret, timestamp, delivery.payload),
		}
		status, err := s.postWebhook(ctx, delivery.endpoint, headers, delivery.payload)
		attempts := delivery.attempts + 1
		if err == nil && status >= 200 && status < 300 {
			_, _ = s.store.DB.Exec(ctx, `
				UPDATE public.webhook_deliveries SET status='delivered', attempts=$2, delivered_at=now(), last_error=''
				WHERE id=$1
			`, delivery.id, attempts)
			continue
		}
		lastError := fmt.Sprintf("HTTP %d", status)
		if err != nil {
			lastError = truncate(err.Error(), 500)
		}
		if attempts >= 5 {
			_, _ = s.store.DB.Exec(ctx, `
				UPDATE public.webhook_deliveries SET status='failed', attempts=$2, last_error=$3 WHERE id=$1
			`, delivery.id, attempts, lastError)
			continue
		}
		backoff := time.Duration(1<<uint(attempts)) * time.Minute
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE public.webhook_deliveries SET attempts=$2, next_attempt_at=now()+$3::interval, last_error=$4
			WHERE id=$1
		`, delivery.id, attempts, fmt.Sprintf("%d seconds", int(backoff.Seconds())), lastError)
	}
}

func (s *Server) webhookWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWebhookDeliveries(ctx)
		}
	}
}

func (s *Server) adminListWebhooks(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, url, events, enabled, created_at, updated_at
		FROM public.webhook_endpoints WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 200
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not load webhooks")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, endpoint string
		var events []string
		var enabled bool
		var created, updated time.Time
		if rows.Scan(&id, &endpoint, &events, &enabled, &created, &updated) == nil {
			items = append(items, map[string]any{
				"id": id, "url": endpoint, "events": events, "enabled": enabled,
				"createdAt": created.UTC().Format(time.RFC3339), "updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": items, "events": apiSupportedWebhookEvents})
}

func (s *Server) adminCreateWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
		Secret string   `json:"secret"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if _, err := s.validateWebhookURL(body.URL); err != nil {
		writeError(w, http.StatusBadRequest, "WEBHOOK", err.Error())
		return
	}
	events := []string{}
	seen := map[string]bool{}
	for _, raw := range body.Events {
		event := strings.TrimSpace(raw)
		if !apiWebhookEventSupported(event) || seen[event] {
			continue
		}
		seen[event] = true
		events = append(events, event)
	}
	if len(events) == 0 {
		writeError(w, http.StatusBadRequest, "WEBHOOK", "Choose at least one event")
		return
	}
	secret := strings.TrimSpace(body.Secret)
	if secret == "" {
		secret = randomToken(32)
	}
	sealed, err := s.sealWorkspaceToken(secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not protect the webhook secret")
		return
	}
	id := newID("webhook")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.webhook_endpoints (id, tenant_id, url, secret_sealed, events, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, id, sess.ActiveTenantID, strings.TrimSpace(body.URL), sealed, events, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not create the webhook")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.webhook_created", id, "Created a signed webhook", map[string]any{"events": events})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "secret": secret, "events": events})
}

func (s *Server) adminUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Enabled *bool    `json:"enabled"`
		Events  []string `json:"events"`
		URL     *string  `json:"url"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if body.URL != nil {
		if _, err := s.validateWebhookURL(*body.URL); err != nil {
			writeError(w, http.StatusBadRequest, "WEBHOOK", err.Error())
			return
		}
	}
	events := []string{}
	for _, raw := range body.Events {
		event := strings.TrimSpace(raw)
		if apiWebhookEventSupported(event) {
			events = append(events, event)
		}
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.webhook_endpoints SET
		  enabled=COALESCE($3, enabled),
		  events=CASE WHEN $4::text[] IS NULL OR cardinality($4::text[]) = 0 THEN events ELSE $4::text[] END,
		  url=COALESCE($5, url), updated_at=now()
		WHERE id=$1 AND tenant_id=$2
	`, r.PathValue("id"), sess.ActiveTenantID, body.Enabled, events, body.URL); err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not update the webhook")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.webhook_updated", r.PathValue("id"), "Updated a signed webhook", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tag, err := s.store.DB.Exec(r.Context(), `DELETE FROM public.webhook_endpoints WHERE id=$1 AND tenant_id=$2`, r.PathValue("id"), sess.ActiveTenantID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "WEBHOOK", "Webhook not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.webhook_deleted", r.PathValue("id"), "Deleted a signed webhook", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminTestWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var endpoint, sealed string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT url, secret_sealed FROM public.webhook_endpoints WHERE id=$1 AND tenant_id=$2
	`, r.PathValue("id"), sess.ActiveTenantID).Scan(&endpoint, &sealed); err != nil {
		writeError(w, http.StatusNotFound, "WEBHOOK", "Webhook not found")
		return
	}
	secret, err := s.openWorkspaceToken(sealed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Webhook secret is unavailable")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"event": "webhook.test", "tenantId": sess.ActiveTenantID,
		"sentAt": time.Now().UTC().Format(time.RFC3339),
	})
	timestamp := time.Now().Unix()
	headers := map[string]string{
		"X-A8M-Event": "webhook.test", "X-A8M-Delivery": newID("test"),
		"X-A8M-Timestamp": strconv.FormatInt(timestamp, 10),
		"X-A8M-Signature": signWebhookPayload(secret, timestamp, payload),
	}
	status, err := s.postWebhook(r.Context(), endpoint, headers, payload)
	if err != nil {
		writeError(w, http.StatusBadGateway, "WEBHOOK", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": status >= 200 && status < 300, "status": status})
}

func (s *Server) adminListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT d.id, d.event, d.status, d.attempts, d.last_error, d.created_at, d.delivered_at, e.url
		FROM public.webhook_deliveries d JOIN public.webhook_endpoints e ON e.id=d.endpoint_id
		WHERE d.tenant_id=$1 ORDER BY d.created_at DESC LIMIT 200
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBHOOK", "Could not load deliveries")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, event, status, lastError, endpoint string
		var attempts int
		var created time.Time
		var delivered *time.Time
		if rows.Scan(&id, &event, &status, &attempts, &lastError, &created, &delivered, &endpoint) == nil {
			item := map[string]any{
				"id": id, "event": event, "status": status, "attempts": attempts,
				"lastError": lastError, "createdAt": created.UTC().Format(time.RFC3339), "url": endpoint,
			}
			if delivered != nil {
				item["deliveredAt"] = delivered.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}
