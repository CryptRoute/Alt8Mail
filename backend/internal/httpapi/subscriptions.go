package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) registerSubscriptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/messages/{id}/unsubscribe", s.requireUser(s.unsubscribeMessage))
	mux.HandleFunc("GET /api/unsubscribe-actions", s.requireUser(s.listUnsubscribeActions))
}

func (s *Server) listUnsubscribeActions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{"actions": s.loadUnsubscribeActions(r.Context(), sess.User.ID)})
}

func (s *Server) loadUnsubscribeActions(ctx context.Context, userID string) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, message_id, sender_email::text, method, target, status, error, updated_at
		FROM public.mail_unsubscribe_actions WHERE user_id=$1 ORDER BY updated_at DESC
	`, userID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, messageID, sender, method, target, status, lastError string
		var updated time.Time
		if rows.Scan(&id, &messageID, &sender, &method, &target, &status, &lastError, &updated) == nil {
			out = append(out, map[string]any{
				"id": id, "messageId": messageID, "senderEmail": sender,
				"method": method, "target": target, "status": status,
				"error": lastError, "updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	return out
}

func unsubscribeMetadata(message map[string]any) map[string]any {
	raw, _ := message["unsubscribe"].(map[string]any)
	if raw == nil {
		return nil
	}
	return raw
}

func (s *Server) unsubscribeMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	owner, _, message, err := s.resolveReactionMessage(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.reactionSendAllowed(r.Context(), sess.User, owner) {
		writeError(w, http.StatusForbidden, "UNSUBSCRIBE_READ_ONLY", "This shared mailbox is read-only for your account")
		return
	}
	metadata := unsubscribeMetadata(message)
	if metadata == nil {
		writeError(w, http.StatusBadRequest, "UNSUBSCRIBE_UNAVAILABLE", "This message does not offer a machine-readable unsubscribe link")
		return
	}
	sender := normalizeEmailValue(stateString(message, "fromEmail"))
	if !looksLikeEmail(sender) {
		writeError(w, http.StatusBadRequest, "UNSUBSCRIBE_SENDER", "This message has no unsubscribeable sender address")
		return
	}
	targetURL := strings.TrimSpace(stateString(metadata, "url"))
	mailto := normalizeEmailValue(stateString(metadata, "mailto"))
	oneClick := stateBool(metadata, "oneClick") && targetURL != "" && strings.HasPrefix(strings.ToLower(targetURL), "https://")
	method, target, status, lastError := "", "", "", ""
	action := "manual"
	if oneClick {
		method, target = "one-click", targetURL
		if err := s.postUnsubscribe(r.Context(), targetURL); err != nil {
			status, lastError = "failed", truncate(sanitizeMailText(err.Error()), 300)
			s.recordUnsubscribeAction(r.Context(), sess.User, message, sender, method, target, status, lastError)
			s.audit(r, sess.User, "unsubscribe.failed", firstNonEmpty(stateString(message, "subject"), "(no subject)"),
				"One-click unsubscribe failed for "+sender+": "+lastError)
			writeError(w, http.StatusBadGateway, "UNSUBSCRIBE_FAILED", "The unsubscribe request could not be delivered: "+lastError)
			return
		}
		status = "sent"
		action = "sent"
	} else if targetURL != "" {
		method, target, status = "link", targetURL, "manual"
	} else if mailto != "" {
		method, target, status = "mailto", mailto, "manual"
	} else {
		writeError(w, http.StatusBadRequest, "UNSUBSCRIBE_UNAVAILABLE", "This message does not offer a usable unsubscribe action")
		return
	}
	record := s.recordUnsubscribeAction(r.Context(), sess.User, message, sender, method, target, status, "")
	detail := "Opened unsubscribe page for " + sender
	if status == "sent" {
		detail = "One-click unsubscribe sent to " + sender
	}
	s.audit(r, sess.User, "unsubscribe."+status, firstNonEmpty(stateString(message, "subject"), "(no subject)"), detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "action": action, "url": targetURL, "email": mailto,
		"actionRecord": record,
	})
}

func (s *Server) recordUnsubscribeAction(ctx context.Context, user userRecord, message map[string]any, sender, method, target, status, lastError string) map[string]any {
	messageID := stateString(message, "id")
	actionID := newID("unsub")
	var id string
	var updated time.Time
	err := s.store.DB.QueryRow(ctx, `
		INSERT INTO public.mail_unsubscribe_actions
		  (id, tenant_id, user_id, message_id, sender_email, method, target, status, error, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		ON CONFLICT (user_id, sender_email) DO UPDATE SET
		  message_id=EXCLUDED.message_id, method=EXCLUDED.method, target=EXCLUDED.target,
		  status=EXCLUDED.status, error=EXCLUDED.error, updated_at=now()
		RETURNING id, updated_at
	`, actionID, user.TenantID, user.ID, messageID, sender, method, truncate(target, 2048), status, truncate(lastError, 300)).Scan(&id, &updated)
	if err != nil {
		return map[string]any{
			"id": actionID, "messageId": messageID, "senderEmail": sender, "method": method,
			"target": target, "status": status, "error": lastError, "updatedAt": time.Now().UTC().Format(time.RFC3339),
		}
	}
	return map[string]any{
		"id": id, "messageId": messageID, "senderEmail": sender, "method": method,
		"target": target, "status": status, "error": lastError, "updatedAt": updated.UTC().Format(time.RFC3339),
	}
}

func validateUnsubscribeURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid unsubscribe URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https unsubscribe URLs are supported")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("unsubscribe URLs cannot contain credentials")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != "80" && port != "443" {
		return nil, fmt.Errorf("only unsubscribe URLs on ports 80 and 443 are supported")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("local unsubscribe URLs are not allowed")
	}
	if literal := net.ParseIP(host); literal != nil && !publicRemoteIP(literal) {
		return nil, fmt.Errorf("private unsubscribe URLs are not allowed")
	}
	return parsed, nil
}

func (s *Server) postUnsubscribe(ctx context.Context, rawURL string) error {
	parsed, err := validateUnsubscribeURL(rawURL)
	if err != nil {
		return fmt.Errorf("unsubscribe URL is not allowed: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("one-click unsubscribe requires an HTTPS URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			ips, lookupErr := resolver.LookupIPAddr(ctx, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			for _, candidate := range ips {
				if !publicRemoteIP(candidate.IP) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			}
			return nil, fmt.Errorf("unsubscribe host did not resolve to a public address")
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many unsubscribe redirects")
			}
			_, err := validateUnsubscribeURL(req.URL.String())
			return err
		},
	}
	form := url.Values{"List-Unsubscribe": {"One-Click"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build unsubscribe request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "Alt8Mail-Unsubscribe/1.0")
	request.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.5")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("unsubscribe request failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("unsubscribe host returned HTTP %d", response.StatusCode)
	}
	return nil
}
