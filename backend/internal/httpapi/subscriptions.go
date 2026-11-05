package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

func (s *Server) registerSubscriptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/messages/{id}/unsubscribe", s.requireUser(s.unsubscribeMessage))
	mux.HandleFunc("POST /api/folders/subscriptions/unsubscribe-all", s.requireUser(s.unsubscribeAllSubscriptions))
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
			record := s.recordUnsubscribeAction(r.Context(), sess.User, message, sender, method, target, status, lastError)
			deleted, deleteErr := s.permanentlyDeleteMessages(r.Context(), sess.User, []string{stateString(message, "id")}, "unsubscribe failed")
			s.audit(r, sess.User, "unsubscribe.failed", firstNonEmpty(stateString(message, "subject"), "(no subject)"),
				"One-click unsubscribe failed for "+sender+": "+lastError)
			if deleteErr != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": false, "action": "failed", "deleted": deleted > 0,
					"error": lastError, "deleteError": truncate(sanitizeMailText(deleteErr.Error()), 300),
					"actionRecord": record,
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "action": "failed", "deleted": deleted > 0,
				"error": lastError, "actionRecord": record,
			})
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

type subscriptionSenderTarget struct {
	Email      string
	Name       string
	Subject    string
	URL        string
	Mailto     string
	OneClick   bool
	MessageIDs []string
}

type subscriptionSendResult struct {
	SenderEmail string `json:"senderEmail"`
	SenderName  string `json:"senderName"`
	Method      string `json:"method"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Messages    int    `json:"messages"`
	Deleted     int    `json:"deleted"`
}

func (s *Server) unsubscribeAllSubscriptions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	targetsByIdentity := map[string]*subscriptionSenderTarget{}
	err := s.withMailMetadataState(r.Context(), sess.User, func(state map[string]any) error {
		for _, raw := range stateMessages(state) {
			message, _ := raw.(map[string]any)
			if message == nil || stateString(message, "folder") != "subscriptions" {
				continue
			}
			metadata := unsubscribeMetadata(message)
			if metadata == nil {
				continue
			}
			sender := normalizeEmailValue(stateString(message, "fromEmail"))
			if !looksLikeEmail(sender) {
				from, _ := message["from"].(map[string]any)
				sender = normalizeEmailValue(stateString(from, "email"))
			}
			if !looksLikeEmail(sender) {
				continue
			}
			target := targetsByIdentity[sender]
			if target == nil {
				from, _ := message["from"].(map[string]any)
				target = &subscriptionSenderTarget{
					Email: sender, Name: stateString(from, "name"),
					URL:      strings.TrimSpace(stateString(metadata, "url")),
					Mailto:   normalizeEmailValue(stateString(metadata, "mailto")),
					OneClick: stateBool(metadata, "oneClick"),
					Subject:  stateString(message, "subject"),
				}
				targetsByIdentity[sender] = target
			}
			target.MessageIDs = append(target.MessageIDs, stateString(message, "id"))
		}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	targets := make([]subscriptionSenderTarget, 0, len(targetsByIdentity))
	for _, target := range targetsByIdentity {
		targets = append(targets, *target)
	}
	sort.Slice(targets, func(i, j int) bool {
		return strings.ToLower(firstNonEmpty(targets[i].Name, targets[i].Email)) <
			strings.ToLower(firstNonEmpty(targets[j].Name, targets[j].Email))
	})
	skipped := 0
	if len(targets) > 200 {
		skipped = len(targets) - 200
		targets = targets[:200]
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "processed": 0, "unsubscribed": 0, "failed": 0, "manual": 0,
			"deleted": 0, "skipped": 0, "results": []subscriptionSendResult{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	results := make([]subscriptionSendResult, 0, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for _, target := range targets {
		wg.Add(1)
		go func(item subscriptionSenderTarget) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			result := subscriptionSendResult{
				SenderEmail: item.Email, SenderName: item.Name, Messages: len(item.MessageIDs),
			}
			method, targetAddr := "", ""
			switch {
			case item.OneClick && item.URL != "":
				method, targetAddr = "one-click", item.URL
			case item.URL != "":
				method, targetAddr = "link", item.URL
			case item.Mailto != "":
				method, targetAddr = "mailto", item.Mailto
			}
			result.Method = method
			if method == "" {
				result.Status = "manual"
			} else {
				var requestErr error
				switch method {
				case "one-click":
					requestErr = s.postUnsubscribe(ctx, targetAddr)
				case "link":
					requestErr = s.getUnsubscribe(ctx, targetAddr)
				case "mailto":
					requestErr = s.sendUnsubscribeMailto(ctx, sess.User, targetAddr)
				}
				result.Status = "sent"
				if requestErr != nil {
					result.Status = "failed"
					result.Error = truncate(sanitizeMailText(requestErr.Error()), 300)
				}
				firstMessageID := ""
				if len(item.MessageIDs) > 0 {
					firstMessageID = item.MessageIDs[0]
				}
				s.recordUnsubscribeAction(ctx, sess.User, map[string]any{
					"id": firstMessageID, "subject": item.Subject,
					"fromEmail": item.Email,
				}, item.Email, method, targetAddr, result.Status, result.Error)
			}
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(target)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		return strings.ToLower(firstNonEmpty(results[i].SenderName, results[i].SenderEmail)) <
			strings.ToLower(firstNonEmpty(results[j].SenderName, results[j].SenderEmail))
	})

	deleteIDs := []string{}
	for index := range results {
		if results[index].Method == "" {
			continue
		}
		for _, target := range targets {
			if !strings.EqualFold(target.Email, results[index].SenderEmail) {
				continue
			}
			deleteIDs = append(deleteIDs, target.MessageIDs...)
			break
		}
	}
	deleted, deleteErr := s.permanentlyDeleteMessages(ctx, sess.User, deleteIDs, "Unsubscribed from all subscriptions")
	if ctx.Err() != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		moreDeleted, retryErr := s.permanentlyDeleteMessages(cleanupCtx, sess.User, deleteIDs, "Unsubscribed from all subscriptions")
		cleanupCancel()
		deleted += moreDeleted
		deleteErr = retryErr
	}
	if deleteErr == nil {
		for index := range results {
			if results[index].Method != "" {
				results[index].Deleted = results[index].Messages
			}
		}
	}
	unsubscribed, failed, manual := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case "sent":
			unsubscribed++
		case "failed":
			failed++
		default:
			manual++
		}
	}
	s.audit(r, sess.User, "unsubscribe.bulk", "Subscriptions",
		fmt.Sprintf("Processed %d senders · %d unsubscribed · %d failed · %d messages removed",
			len(results), unsubscribed, failed, deleted))
	payload := map[string]any{
		"ok": deleteErr == nil, "processed": len(results), "unsubscribed": unsubscribed,
		"failed": failed, "manual": manual, "deleted": deleted, "skipped": skipped,
		"results": results,
	}
	if deleteErr != nil {
		payload["deleteError"] = truncate(sanitizeMailText(deleteErr.Error()), 300)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) sendUnsubscribeMailto(ctx context.Context, user userRecord, address string) error {
	if !looksLikeEmail(normalizeEmailValue(address)) {
		return fmt.Errorf("invalid unsubscribe address")
	}
	item := map[string]any{
		"from":     map[string]any{"name": user.Name, "email": user.Email},
		"to":       []any{map[string]any{"name": "", "email": normalizeEmailValue(address)}},
		"subject":  "Unsubscribe request",
		"bodyText": "Please unsubscribe this address from the mailing list.\n\nThank you.",
	}
	raw, err := buildMIMEMessage(item, fmt.Sprintf("<%s@%s>", newID("unsub"), s.cfg.MailDomain), nil)
	if err != nil {
		return err
	}
	return s.submitSMTPMessage(user.Email, []string{normalizeEmailValue(address)}, raw)
}

// permanentlyDeleteMessages removes subscription noise immediately. It never
// creates a Trash copy and never adds an undo token; the physical IMAP message
// is flagged deleted and expunged.
func (s *Server) permanentlyDeleteMessages(ctx context.Context, user userRecord, ids []string, reason string) (int, error) {
	held, err := s.userOnLegalHold(ctx, user.TenantID, user.ID)
	if err != nil {
		return 0, err
	}
	if held {
		return 0, fail(http.StatusConflict, "LEGAL_HOLD", "A legal hold preserves this mailbox. Subscription messages cannot be deleted while the hold is active.")
	}
	seen := map[string]bool{}
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return 0, nil
	}
	mutations := []imapAction{}
	folders := map[string]bool{}
	removed := 0
	err = s.withMailMetadataState(ctx, user, func(state map[string]any) error {
		for _, id := range unique {
			message := findMessageIn(state, id)
			if message == nil {
				continue
			}
			physicalFolder := stateString(message, "imapFolder")
			if physicalFolder == "" {
				physicalFolder = folderIMAPName(state, stateString(message, "folder"))
			}
			messageID := stateString(message, "messageId")
			uid := stateInt(message, "imapUid")
			if physicalFolder != "" && (messageID != "" || uid > 0) {
				mutations = append(mutations, imapAction{
					MessageID: messageID, SourceUID: uid,
					SourceFolder: physicalFolder, SourceIMAPFolder: physicalFolder, MarkDeleted: true,
				})
				folders[physicalFolder] = true
			}
			removeMessage(state, id)
			removed++
		}
		if removed > 0 {
			appendStateAudit(state, "message.subscription_delete", fmt.Sprintf("%d message", removed),
				user.Name, reason, "127.0.0.1", "subscriptions")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(user, mutations); err != nil {
			s.logger.Warn("subscription delete IMAP propagation failed", "err", err)
		}
	}
	for folder := range folders {
		if err := s.expungeIMAPFolder(user, folder); err != nil {
			s.logger.Warn("subscription delete expunge failed", "folder", folder, "err", err)
		}
	}
	return removed, nil
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
	return s.requestUnsubscribe(ctx, rawURL, http.MethodPost)
}

func (s *Server) getUnsubscribe(ctx context.Context, rawURL string) error {
	return s.requestUnsubscribe(ctx, rawURL, http.MethodGet)
}

func (s *Server) requestUnsubscribe(ctx context.Context, rawURL, method string) error {
	parsed, err := validateUnsubscribeURL(rawURL)
	if err != nil {
		return fmt.Errorf("unsubscribe URL is not allowed: %w", err)
	}
	if method == http.MethodPost && parsed.Scheme != "https" {
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
	var body io.Reader
	if method == http.MethodPost {
		form := url.Values{"List-Unsubscribe": {"One-Click"}}
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return fmt.Errorf("build unsubscribe request: %w", err)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
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
