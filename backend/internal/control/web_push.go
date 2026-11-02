package control

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type browserPushSettings struct {
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
	UpdatedBy         string
	UpdatedAt         time.Time
}

func (s *Server) registerWebPushRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/web-push", s.require("notifications.read", s.handleWebPushGet))
	mux.HandleFunc("PUT /control/api/web-push", s.require("notifications.manage", s.handleWebPushSave))
	mux.HandleFunc("POST /control/api/web-push/test", s.require("notifications.manage", s.handleWebPushTest))
	mux.HandleFunc("POST /control/api/web-push/rotate", s.require("notifications.manage", s.handleWebPushRotate))
}

func (s *Server) loadBrowserPushSettings(ctx context.Context) (browserPushSettings, error) {
	settings := browserPushSettings{}
	err := s.db.QueryRow(ctx, `
		SELECT enabled,public_key,subject,include_sender,include_subject,
		       quiet_hours_enabled,quiet_start,quiet_end,cursor_at,cursor_id,updated_by,updated_at
		FROM public.web_push_settings WHERE id=true
	`).Scan(&settings.Enabled, &settings.PublicKey, &settings.Subject, &settings.IncludeSender,
		&settings.IncludeSubject, &settings.QuietHoursEnabled, &settings.QuietStart, &settings.QuietEnd,
		&settings.CursorAt, &settings.CursorID, &settings.UpdatedBy, &settings.UpdatedAt)
	if err == nil {
		return settings, nil
	}
	// A fresh migration always inserts the singleton row. If the row is missing
	// for any reason, create defaults rather than breaking the page.
	_, _ = s.db.Exec(ctx, `INSERT INTO public.web_push_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING`)
	err = s.db.QueryRow(ctx, `
		SELECT enabled,public_key,subject,include_sender,include_subject,
		       quiet_hours_enabled,quiet_start,quiet_end,cursor_at,cursor_id,updated_by,updated_at
		FROM public.web_push_settings WHERE id=true
	`).Scan(&settings.Enabled, &settings.PublicKey, &settings.Subject, &settings.IncludeSender,
		&settings.IncludeSubject, &settings.QuietHoursEnabled, &settings.QuietStart, &settings.QuietEnd,
		&settings.CursorAt, &settings.CursorID, &settings.UpdatedBy, &settings.UpdatedAt)
	return settings, err
}

func (s *Server) webPushDefaultSubject(ctx context.Context) string {
	var domain string
	_ = s.db.QueryRow(ctx, `SELECT domain::text FROM public.mail_domains ORDER BY is_primary DESC, domain LIMIT 1`).Scan(&domain)
	if domain == "" {
		domain = strings.TrimPrefix(strings.ToLower(s.cfg.MailHostname), "mail.")
	}
	if domain == "" {
		domain = "localhost"
	}
	return "mailto:postmaster@" + domain
}

func validWebPushSubject(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "mailto:") {
		_, err := mail.ParseAddress(strings.TrimSpace(value[len("mailto:"):]))
		return err == nil
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func (s *Server) webPushPublicState(ctx context.Context) (map[string]any, error) {
	settings, err := s.loadBrowserPushSettings(ctx)
	if err != nil {
		return nil, err
	}
	var subscriptions, users int
	_ = s.db.QueryRow(ctx, `SELECT count(*), count(DISTINCT user_id) FROM public.web_push_subscriptions`).Scan(&subscriptions, &users)
	return map[string]any{
		"enabled": settings.Enabled, "publicKey": settings.PublicKey, "subject": settings.Subject,
		"includeSender": settings.IncludeSender, "includeSubject": settings.IncludeSubject,
		"quietHoursEnabled": settings.QuietHoursEnabled, "quietStart": settings.QuietStart,
		"quietEnd": settings.QuietEnd, "keyPresent": pathExists(s.cfg.WebPushKeyFile),
		"subscriptionCount": subscriptions, "subscribedUsers": users,
		"updatedBy": settings.UpdatedBy, "updatedAt": settings.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *Server) handleWebPushGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	payload, err := s.webPushPublicState(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) writeWebPushPrivateKey(privateKey string) error {
	group, err := user.LookupGroup("a8m")
	if err != nil {
		return fmt.Errorf("the a8m service group is not present: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("invalid a8m group id")
	}
	_, err = writeManagedFile(s.cfg.WebPushKeyFile, strings.TrimSpace(privateKey)+"\n", 0o640, 0, gid)
	return err
}

func (s *Server) generateWebPushKeys(ctx context.Context, clearSubscriptions bool) (string, error) {
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", err
	}
	if err := s.writeWebPushPrivateKey(privateKey); err != nil {
		return "", err
	}
	if clearSubscriptions {
		_, _ = s.db.Exec(ctx, `DELETE FROM public.web_push_subscriptions`)
	}
	return publicKey, nil
}

func (s *Server) handleWebPushSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Enabled           *bool   `json:"enabled"`
		Subject           *string `json:"subject"`
		IncludeSender     *bool   `json:"includeSender"`
		IncludeSubject    *bool   `json:"includeSubject"`
		QuietHoursEnabled *bool   `json:"quietHoursEnabled"`
		QuietStart        *int    `json:"quietStart"`
		QuietEnd          *int    `json:"quietEnd"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current, err := s.loadBrowserPushSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	enabled := current.Enabled
	subject := current.Subject
	includeSender := current.IncludeSender
	includeSubject := current.IncludeSubject
	quietEnabled := current.QuietHoursEnabled
	quietStart := current.QuietStart
	quietEnd := current.QuietEnd
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.Subject != nil {
		subject = strings.TrimSpace(*body.Subject)
	}
	if body.IncludeSender != nil {
		includeSender = *body.IncludeSender
	}
	if body.IncludeSubject != nil {
		includeSubject = *body.IncludeSubject
	}
	if body.QuietHoursEnabled != nil {
		quietEnabled = *body.QuietHoursEnabled
	}
	if body.QuietStart != nil {
		quietStart = *body.QuietStart
	}
	if body.QuietEnd != nil {
		quietEnd = *body.QuietEnd
	}
	if subject == "" {
		subject = s.webPushDefaultSubject(r.Context())
	}
	if !validWebPushSubject(subject) {
		writeError(w, http.StatusBadRequest, "PUSH_SUBJECT", "VAPID subject must be a mailto: address or an https URL")
		return
	}
	if quietStart < 0 || quietStart > 23 || quietEnd < 0 || quietEnd > 23 {
		writeError(w, http.StatusBadRequest, "QUIET_HOURS", "Quiet hours must be between 0 and 23")
		return
	}
	publicKey := current.PublicKey
	keyFileMissing := !pathExists(s.cfg.WebPushKeyFile)
	clearSubscriptions := false
	if enabled && (publicKey == "" || keyFileMissing) {
		publicKey, err = s.generateWebPushKeys(r.Context(), publicKey != "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PUSH_KEY", "Could not create the VAPID key pair: "+err.Error())
			return
		}
		clearSubscriptions = true
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE public.web_push_settings SET
		  enabled=$1, public_key=$2, subject=$3, include_sender=$4, include_subject=$5,
		  quiet_hours_enabled=$6, quiet_start=$7, quiet_end=$8,
		  cursor_at=CASE WHEN $1 AND NOT enabled THEN now() ELSE cursor_at END,
		  cursor_id=CASE WHEN $1 AND NOT enabled THEN '' ELSE cursor_id END,
		  updated_by=$9, updated_at=now()
		WHERE id=true
	`, enabled, publicKey, subject, includeSender, includeSubject, quietEnabled, quietStart, quietEnd, sess.User.Email)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if clearSubscriptions {
		_, _ = s.db.Exec(r.Context(), `DELETE FROM public.web_push_subscriptions`)
	}
	s.actorAudit(r, sess, "webpush.settings_updated", "Webmail browser push", "Updated browser push settings",
		map[string]any{"enabled": enabled, "quietHours": quietEnabled, "includeSender": includeSender, "includeSubject": includeSubject})
	payload, _ := s.webPushPublicState(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": payload})
}

func validWebPushEndpoint(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4096 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && !webPushPublicIP(ip) {
		return false
	}
	return true
}

func webPushPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

func webPushSafeClient() *http.Client {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, candidate := range addresses {
				if !webPushPublicIP(candidate.IP) {
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

func (s *Server) webPushTestPayload(ctx context.Context) map[string]any {
	webmail := strings.TrimRight(s.settingString(ctx, "server.webmail_url", s.cfg.WebmailURL), "/")
	if webmail == "" {
		webmail = strings.TrimRight(s.cfg.WebmailURL, "/")
	}
	return map[string]any{
		"title": "Alt8Mail test notification",
		"body":  "Browser push is working. New mail will arrive in this notification style.",
		"tag":   "a8m-test-notification",
		"url":   webmail + "/#/Inbox",
		"icon":  "/icons/icon-192.png",
		"badge": "/icons/icon-192.png",
	}
}

func (s *Server) handleWebPushTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	settings, err := s.loadBrowserPushSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !settings.Enabled {
		writeError(w, http.StatusBadRequest, "PUSH_DISABLED", "Enable browser push before sending a test")
		return
	}
	privateKey, err := os.ReadFile(s.cfg.WebPushKeyFile)
	if err != nil || strings.TrimSpace(settings.PublicKey) == "" {
		writeError(w, http.StatusBadRequest, "PUSH_KEY", "Enable browser push first so the VAPID key can be generated")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body, 16<<10); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
	}
	targetEmail := strings.ToLower(strings.TrimSpace(body.Email))
	if targetEmail == "" {
		targetEmail = strings.ToLower(sess.User.Email)
	}
	var userID string
	err = s.db.QueryRow(r.Context(), `
		SELECT id::text FROM public.users
		WHERE lower(email::text)=lower($1) AND status='active' AND deleted_at IS NULL
	`, targetEmail).Scan(&userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "PUSH_TARGET", "No active webmail account matches "+targetEmail)
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT s.endpoint,s.p256dh,s.auth
		FROM public.web_push_subscriptions s
		JOIN public.users u ON u.id=s.user_id AND u.status='active' AND u.deleted_at IS NULL
		WHERE s.user_id=$1
		ORDER BY s.created_at DESC LIMIT 20
	`, userID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	type target struct{ endpoint, p256dh, auth string }
	targets := []target{}
	for rows.Next() {
		var item target
		if rows.Scan(&item.endpoint, &item.p256dh, &item.auth) == nil {
			targets = append(targets, item)
		}
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "PUSH_TARGET", targetEmail+" has not enabled browser notifications")
		return
	}
	payload, _ := json.Marshal(s.webPushTestPayload(r.Context()))
	client := webPushSafeClient()
	sent, failed := 0, 0
	for _, item := range targets {
		if !validWebPushEndpoint(item.endpoint) {
			failed++
			continue
		}
		resp, sendErr := webpush.SendNotificationWithContext(r.Context(), payload, &webpush.Subscription{
			Endpoint: item.endpoint, Keys: webpush.Keys{P256dh: item.p256dh, Auth: item.auth},
		}, &webpush.Options{
			HTTPClient: client, Subscriber: settings.Subject, TTL: 300,
			VAPIDPublicKey: settings.PublicKey, VAPIDPrivateKey: strings.TrimSpace(string(privateKey)),
		})
		if sendErr != nil {
			failed++
			continue
		}
		if resp == nil {
			failed++
			continue
		}
		status := resp.StatusCode
		resp.Body.Close()
		if status == http.StatusNotFound || status == http.StatusGone {
			_, _ = s.db.Exec(r.Context(), `DELETE FROM public.web_push_subscriptions WHERE endpoint=$1`, item.endpoint)
			failed++
			continue
		}
		if status < 200 || status >= 300 {
			failed++
			continue
		}
		sent++
	}
	s.actorAudit(r, sess, "webpush.test_sent", "Webmail browser push", "Sent a test browser notification",
		map[string]any{"email": targetEmail, "sent": sent, "failed": failed})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": targetEmail, "sent": sent, "failed": failed})
}

func (s *Server) handleWebPushRotate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	publicKey, err := s.generateWebPushKeys(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PUSH_KEY", "Could not rotate the VAPID key pair: "+err.Error())
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE public.web_push_settings SET public_key=$2, cursor_at=now(), cursor_id='', updated_at=now()
		WHERE id=true
	`, publicKey)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "webpush.keys_rotated", "Webmail browser push", "Rotated VAPID keys and reset browser subscriptions", nil)
	s.notifyEvent(r.Context(), "webpush.keys_rotated", map[string]any{})
	payload, _ := s.webPushPublicState(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": payload})
}
