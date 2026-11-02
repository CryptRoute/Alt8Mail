package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const webPushPollInterval = 10 * time.Second

type webPushSettings struct {
	Enabled           bool
	PublicKey         string
	Subject           string
	IncludeSender     bool
	IncludeSubject    bool
	QuietHoursEnabled bool
	QuietStart        int
	QuietEnd          int
	CursorAt          *time.Time
	CursorID          string
}

type webPushSubscription struct {
	ID       string
	UserID   string
	Endpoint string
	P256dh   string
	Auth     string
}

type webPushSubscriptionInput struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Server) loadWebPushSettings(ctx context.Context) (webPushSettings, error) {
	settings := webPushSettings{}
	err := s.store.DB.QueryRow(ctx, `
		SELECT enabled,public_key,subject,include_sender,include_subject,quiet_hours_enabled,
		       quiet_start,quiet_end,cursor_at,cursor_id
		FROM public.web_push_settings WHERE id=true
	`).Scan(&settings.Enabled, &settings.PublicKey, &settings.Subject, &settings.IncludeSender,
		&settings.IncludeSubject, &settings.QuietHoursEnabled, &settings.QuietStart, &settings.QuietEnd,
		&settings.CursorAt, &settings.CursorID)
	return settings, err
}

func (s *Server) pushPublicConfig(ctx context.Context, userID string) map[string]any {
	settings, err := s.loadWebPushSettings(ctx)
	if err != nil {
		return map[string]any{"enabled": false, "publicKey": "", "subscribed": false}
	}
	enabled := settings.Enabled && strings.TrimSpace(settings.PublicKey) != ""
	if enabled {
		if _, err := os.Stat(s.cfg.PushVAPIDKeyFile); err != nil {
			enabled = false
		}
	}
	subscribed := false
	if userID != "" {
		_ = s.store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.web_push_subscriptions WHERE user_id=$1)`, userID).Scan(&subscribed)
	}
	return map[string]any{
		"enabled": enabled, "publicKey": settings.PublicKey, "subscribed": subscribed,
		"includeSender": settings.IncludeSender, "includeSubject": settings.IncludeSubject,
	}
}

func (s *Server) getPushConfig(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	writeJSON(w, http.StatusOK, s.pushPublicConfig(r.Context(), sess.User.ID))
}

func validPushEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, fmt.Errorf("invalid push endpoint")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid push endpoint")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("push endpoints must use HTTPS")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("push endpoints cannot contain credentials")
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		return nil, fmt.Errorf("push endpoints must use port 443")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("local push endpoints are not allowed")
	}
	if literal := net.ParseIP(host); literal != nil && !pushPublicIP(literal) {
		return nil, fmt.Errorf("private push endpoints are not allowed")
	}
	return parsed, nil
}

func pushPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	// Carrier-grade NAT (100.64.0.0/10) is not public address space even
	// though net.IP does not classify it as private.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

func validPushKey(value string) bool {
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		if decoded, err := encoding.DecodeString(strings.TrimSpace(value)); err == nil && len(decoded) >= 8 {
			return true
		}
	}
	return false
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body webPushSubscriptionInput
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if _, err := validPushEndpoint(body.Endpoint); err != nil {
		writeError(w, http.StatusBadRequest, "PUSH_ENDPOINT", err.Error())
		return
	}
	if !validPushKey(body.Keys.P256dh) || !validPushKey(body.Keys.Auth) {
		writeError(w, http.StatusBadRequest, "PUSH_KEYS", "The browser supplied an invalid push subscription")
		return
	}
	settings, err := s.loadWebPushSettings(r.Context())
	if err != nil || !settings.Enabled || strings.TrimSpace(settings.PublicKey) == "" {
		writeError(w, http.StatusForbidden, "PUSH_DISABLED", "Browser push is not enabled by your organization")
		return
	}
	if _, err := os.Stat(s.cfg.PushVAPIDKeyFile); err != nil {
		writeError(w, http.StatusForbidden, "PUSH_DISABLED", "The browser push signing key is not available")
		return
	}
	var existed bool
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM public.web_push_subscriptions WHERE user_id=$1 AND endpoint=$2)
	`, sess.User.ID, strings.TrimSpace(body.Endpoint)).Scan(&existed)
	tag, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.web_push_subscriptions (user_id, endpoint, p256dh, auth, user_agent)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (endpoint) DO UPDATE SET
		  p256dh=EXCLUDED.p256dh, auth=EXCLUDED.auth,
		  user_agent=EXCLUDED.user_agent, last_error='', failure_count=0
		WHERE public.web_push_subscriptions.user_id = EXCLUDED.user_id
	`, sess.User.ID, strings.TrimSpace(body.Endpoint), strings.TrimSpace(body.Keys.P256dh),
		strings.TrimSpace(body.Keys.Auth), truncate(sanitizeUserAgent(r.UserAgent()), 500))
	if err != nil {
		s.logger.Warn("could not save web push subscription", "user", sess.User.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "PUSH_STORE", "Could not save the browser subscription")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "PUSH_OWNED", "That browser subscription belongs to another account")
		return
	}
	if !existed {
		s.audit(r, sess.User, "push.subscribed", "Webmail browser", "Enabled browser push notifications")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	if strings.TrimSpace(body.Endpoint) == "" {
		writeError(w, http.StatusBadRequest, "PUSH_ENDPOINT", "The push endpoint is required")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM public.web_push_subscriptions WHERE user_id=$1 AND endpoint=$2`, sess.User.ID, strings.TrimSpace(body.Endpoint))
	s.audit(r, sess.User, "push.unsubscribed", "Webmail browser", "Disabled browser push notifications")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) pushHTTPClient() *http.Client {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, candidate := range ips {
				if !pushPublicIP(candidate.IP) {
					continue
				}
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("push endpoint did not resolve to a public address")
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("push endpoint redirects are not allowed")
		},
	}
}

func (s *Server) sendWebPushBatch(ctx context.Context, client *http.Client, settings webPushSettings, subscriptions []webPushSubscription, payload map[string]any) (int, int) {
	if len(subscriptions) == 0 {
		return 0, 0
	}
	privateKey, err := os.ReadFile(s.cfg.PushVAPIDKeyFile)
	if err != nil || strings.TrimSpace(string(privateKey)) == "" {
		return 0, len(subscriptions)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, len(subscriptions)
	}
	sent, failed := 0, 0
	for _, subscription := range subscriptions {
		if _, err := validPushEndpoint(subscription.Endpoint); err != nil {
			failed++
			continue
		}
		response, sendErr := webpush.SendNotificationWithContext(ctx, raw, &webpush.Subscription{
			Endpoint: subscription.Endpoint,
			Keys:     webpush.Keys{P256dh: subscription.P256dh, Auth: subscription.Auth},
		}, &webpush.Options{
			HTTPClient: client, Subscriber: settings.Subject, TTL: 3600, Urgency: webpush.UrgencyNormal,
			VAPIDPublicKey: settings.PublicKey, VAPIDPrivateKey: strings.TrimSpace(string(privateKey)),
		})
		if sendErr != nil {
			failed++
			_, _ = s.store.DB.Exec(ctx, `UPDATE public.web_push_subscriptions SET last_error=$2, failure_count=failure_count+1 WHERE id=$1`, subscription.ID, truncate(sendErr.Error(), 300))
			continue
		}
		status := 0
		if response != nil {
			status = response.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32*1024))
			_ = response.Body.Close()
		}
		if status == http.StatusNotFound || status == http.StatusGone {
			_, _ = s.store.DB.Exec(ctx, `DELETE FROM public.web_push_subscriptions WHERE id=$1`, subscription.ID)
			failed++
			continue
		}
		if status < 200 || status >= 300 {
			failed++
			_, _ = s.store.DB.Exec(ctx, `UPDATE public.web_push_subscriptions SET last_error=$2, failure_count=failure_count+1 WHERE id=$1`, subscription.ID, fmt.Sprintf("push service returned HTTP %d", status))
			continue
		}
		sent++
		_, _ = s.store.DB.Exec(ctx, `UPDATE public.web_push_subscriptions SET last_success_at=now(), last_error='', failure_count=0 WHERE id=$1`, subscription.ID)
	}
	return sent, failed
}

func (s *Server) testWebPush(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	settings, err := s.loadWebPushSettings(r.Context())
	if err != nil || !settings.Enabled {
		writeError(w, http.StatusForbidden, "PUSH_DISABLED", "Browser push is not enabled by your organization")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id::text,user_id::text,endpoint,p256dh,auth
		FROM public.web_push_subscriptions WHERE user_id=$1 ORDER BY created_at DESC
	`, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	subscriptions := []webPushSubscription{}
	for rows.Next() {
		var item webPushSubscription
		if rows.Scan(&item.ID, &item.UserID, &item.Endpoint, &item.P256dh, &item.Auth) == nil {
			subscriptions = append(subscriptions, item)
		}
	}
	payload := map[string]any{
		"title": "Alt8Mail test notification",
		"body":  "Browser notifications are working on this device.",
		"tag":   "a8m-test-notification",
		"url":   strings.TrimRight(s.cfg.PublicURL, "/") + "/#/Inbox",
		"icon":  "/icons/icon-192.png", "badge": "/icons/icon-192.png",
	}
	sent, failed := s.sendWebPushBatch(r.Context(), s.pushHTTPClient(), settings, subscriptions, payload)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": sent, "failed": failed})
}

type webPushMessage struct {
	ID        string
	UserID    string
	Subject   string
	FromName  string
	FromEmail string
	Received  time.Time
}

func (s *Server) webPushWorker(ctx context.Context) {
	ticker := time.NewTicker(webPushPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.deliverWebPush(ctx); err != nil && ctx.Err() == nil {
				s.logger.Debug("web push delivery cycle skipped", "err", err)
			}
		}
	}
}

func quietHourNow(settings webPushSettings, now time.Time) bool {
	if !settings.QuietHoursEnabled || settings.QuietStart == settings.QuietEnd {
		return false
	}
	hour := now.Hour()
	if settings.QuietStart < settings.QuietEnd {
		return hour >= settings.QuietStart && hour < settings.QuietEnd
	}
	return hour >= settings.QuietStart || hour < settings.QuietEnd
}

func (s *Server) deliverWebPush(ctx context.Context) error {
	settings, err := s.loadWebPushSettings(ctx)
	if err != nil || !settings.Enabled || settings.PublicKey == "" {
		return nil
	}
	if _, err := os.Stat(s.cfg.PushVAPIDKeyFile); err != nil {
		return nil
	}
	if settings.CursorAt == nil {
		_, _ = s.store.DB.Exec(ctx, `UPDATE public.web_push_settings SET cursor_at=now(), cursor_id='' WHERE id=true`)
		return nil
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT m.id, m.user_id::text, COALESCE(m.subject,''), COALESCE(m.from_name,''),
		       m.from_email::text, m.received_at
		FROM mail_messages m
		WHERE m.folder='inbox' AND NOT m.draft AND NOT m.ignored AND m.unread
		  AND (m.snoozed_until IS NULL OR m.snoozed_until <= now())
		  AND (m.received_at > $1 OR (m.received_at = $1 AND m.id > $2))
		ORDER BY m.received_at, m.id LIMIT 200
	`, *settings.CursorAt, settings.CursorID)
	if err != nil {
		return err
	}
	defer rows.Close()
	byUser := map[string][]webPushMessage{}
	lastAt := *settings.CursorAt
	lastID := settings.CursorID
	for rows.Next() {
		var message webPushMessage
		if rows.Scan(&message.ID, &message.UserID, &message.Subject, &message.FromName, &message.FromEmail, &message.Received) != nil {
			continue
		}
		byUser[message.UserID] = append(byUser[message.UserID], message)
		lastAt = message.Received
		lastID = message.ID
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(byUser) == 0 {
		return nil
	}
	userIDs := make([]string, 0, len(byUser))
	for userID := range byUser {
		userIDs = append(userIDs, userID)
	}
	subscriptionRows, err := s.store.DB.Query(ctx, `
		SELECT s.id::text,s.user_id::text,s.endpoint,s.p256dh,s.auth
		FROM public.web_push_subscriptions s
		JOIN users u ON u.id=s.user_id AND u.status='active' AND u.deleted_at IS NULL
		WHERE s.user_id = ANY($1::uuid[])
	`, userIDs)
	if err != nil {
		return err
	}
	defer subscriptionRows.Close()
	bySubscriptionUser := map[string][]webPushSubscription{}
	for subscriptionRows.Next() {
		var item webPushSubscription
		if subscriptionRows.Scan(&item.ID, &item.UserID, &item.Endpoint, &item.P256dh, &item.Auth) == nil {
			bySubscriptionUser[item.UserID] = append(bySubscriptionUser[item.UserID], item)
		}
	}
	sendNotifications := !quietHourNow(settings, time.Now())
	if sendNotifications {
		client := s.pushHTTPClient()
		for userID, messages := range byUser {
			subscriptions := bySubscriptionUser[userID]
			if len(subscriptions) == 0 || len(messages) == 0 {
				continue
			}
			latest := messages[len(messages)-1]
			title := "New Alt8Mail message"
			bodyParts := []string{}
			if len(messages) > 1 {
				bodyParts = append(bodyParts, fmt.Sprintf("%d new messages", len(messages)))
			}
			if settings.IncludeSender {
				sender := strings.TrimSpace(latest.FromName)
				if sender == "" {
					sender = latest.FromEmail
				}
				if sender != "" {
					title = sender
				}
			}
			if settings.IncludeSubject && strings.TrimSpace(latest.Subject) != "" {
				bodyParts = append(bodyParts, latest.Subject)
			}
			if len(bodyParts) == 0 {
				bodyParts = append(bodyParts, "New message")
			}
			title = truncate(strings.Join(strings.Fields(title), " "), 120)
			body := strings.Join(strings.Fields(strings.Join(bodyParts, " · ")), " ")
			payload := map[string]any{
				"title": title, "body": truncate(body, 400), "tag": "a8m-mail-" + userID,
				"url":  strings.TrimRight(s.cfg.PublicURL, "/") + "/#/Inbox",
				"icon": "/icons/icon-192.png", "badge": "/icons/icon-192.png",
				"timestamp": latest.Received.UTC().UnixMilli(),
			}
			_, _ = s.sendWebPushBatch(ctx, client, settings, subscriptions, payload)
		}
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE public.web_push_settings SET cursor_at=$1, cursor_id=$2 WHERE id=true`, lastAt, lastID)
	return nil
}
