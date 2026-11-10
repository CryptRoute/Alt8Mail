package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const addonActionTokenTTL = 7 * 24 * time.Hour

var addonInteractiveActions = []struct {
	Key   string
	Label string
}{
	{Key: "mark_read", Label: "Mark read"},
	{Key: "archive", Label: "Archive"},
	{Key: "create_task", Label: "Create task"},
}

// createAddonActionTokens issues one single-use token per interactive action
// for a message. Tokens are unguessable capabilities; Slack callbacks and
// Teams links never carry a session cookie.
func (s *Server) createAddonActionTokens(ctx context.Context, tenantID, userID, addonID, connectionID, messageID string) (map[string]string, error) {
	tokens := map[string]string{}
	for _, action := range addonInteractiveActions {
		token := newID("act")
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.addon_action_tokens
			  (id, tenant_id, user_id, addon_id, connection_id, message_id, action, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,now() + interval '7 days')
		`, token, tenantID, userID, addonID, connectionID, messageID, action.Key)
		if err != nil {
			return nil, err
		}
		tokens[action.Key] = token
	}
	return tokens, nil
}

func (s *Server) applyAddonInteractiveAction(ctx context.Context, r *http.Request, user userRecord, messageID, action string) (string, error) {
	switch action {
	case "create_task":
		var subject, fromName, fromEmail, snippet string
		err := s.store.DB.QueryRow(ctx, `
			SELECT subject, from_name, from_email::text, snippet
			FROM public.mail_messages WHERE id=$1 AND user_id=$2 AND tenant_id=$3
		`, messageID, user.ID, user.TenantID).Scan(&subject, &fromName, &fromEmail, &snippet)
		if err != nil {
			return "", fmt.Errorf("message not found")
		}
		if strings.TrimSpace(subject) == "" {
			subject = "(no subject)"
		}
		title := truncate("Email: "+subject, 200)
		notes := truncate(fmt.Sprintf("From: %s <%s>\n\n%s", firstNonEmpty(fromName, fromEmail), fromEmail, snippet), 5000)
		if _, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.tasks (id, tenant_id, user_id, title, notes, status, priority, source)
			VALUES ($1,$2,$3,$4,$5,'open','normal','addon_action')
		`, newID("task"), user.TenantID, user.ID, title, notes); err != nil {
			return "", err
		}
		s.audit(r, user, "addon.action_task_created", messageID, "Created a task from an add-on action")
		return "Task created", nil
	case "mark_read", "archive":
	default:
		return "", fmt.Errorf("unsupported action")
	}
	var mutations []imapAction
	err := s.withMailState(ctx, user, []string{messageID}, false, func(state map[string]any) error {
		message := findMessageIn(state, messageID)
		if message == nil {
			return fmt.Errorf("message not found")
		}
		mutation := imapActionForMessage(message)
		switch action {
		case "mark_read":
			message["unread"] = false
			clearReturnedSnooze(message)
			seen := true
			mutation.SetSeen = &seen
		case "archive":
			if stateString(message, "folder") != "archive" {
				delete(message, "snoozedUntil")
				message["folder"] = "archive"
				message["imapFolder"] = "archive"
				message["imapUid"] = 0
				mutation.TargetFolder = "archive"
			}
		}
		if mutation.MessageID != "" && mutation.SourceFolder != "" {
			mutations = append(mutations, mutation)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(mutations) > 0 {
		if err := s.applyIMAPActions(user, mutations); err != nil {
			s.logger.Warn("add-on action IMAP propagation failed", "err", err)
		}
	}
	auditAction := "addon.action_mark_read"
	label := "Marked the message read"
	if action == "archive" {
		auditAction = "addon.action_archived"
		label = "Archived the message"
	}
	s.audit(r, user, auditAction, messageID, label)
	return label, nil
}

func (s *Server) consumeAddonActionToken(ctx context.Context, r *http.Request, token, addonID, tenantID string) (string, error) {
	var tenant, userID, storedAddon, messageID, action string
	err := s.store.DB.QueryRow(ctx, `
		UPDATE public.addon_action_tokens SET used_at=now()
		WHERE id=$1 AND expires_at > now() AND used_at IS NULL
		RETURNING tenant_id::text, user_id::text, addon_id, message_id, action
	`, token).Scan(&tenant, &userID, &storedAddon, &messageID, &action)
	if err != nil {
		return "", fmt.Errorf("This action link has expired or was already used")
	}
	if storedAddon != addonID || (tenantID != "" && tenant != tenantID) {
		return "", fmt.Errorf("This action link is not valid for this workspace")
	}
	user, err := s.userByID(ctx, userID)
	if err != nil || user.TenantID != tenant {
		return "", fmt.Errorf("The account for this action is unavailable")
	}
	result, err := s.applyAddonInteractiveAction(ctx, r, user, messageID, action)
	if err != nil {
		s.logger.Warn("add-on interactive action failed", "err", err, "action", action)
		return "", fmt.Errorf("The action could not be applied")
	}
	return result, nil
}

func slackPayloadMessage(raw []byte) (tenantID string, payload struct {
	Type string `json:"type"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}, err error) {
	content := raw
	contentType := http.DetectContentType(raw)
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") || strings.HasPrefix(string(raw), "payload=") {
		values, parseErr := url.ParseQuery(string(raw))
		if parseErr != nil {
			return "", payload, parseErr
		}
		content = []byte(values.Get("payload"))
	}
	if len(content) == 0 || content[0] != '{' {
		return "", payload, fmt.Errorf("invalid Slack interaction payload")
	}
	err = json.Unmarshal(content, &payload)
	return payload.Team.ID, payload, err
}

// slackInteraction handles Block Kit button presses. Slack signs the raw
// request body with the workspace signing secret configured on the add-on.
func (s *Server) slackInteraction(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	teamID, payload, err := slackPayloadMessage(body)
	if err != nil || teamID == "" || payload.Type != "block_actions" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var tenantID, webhookSecretSealed string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT c.tenant_id::text, COALESCE(t.webhook_secret_sealed, '')
		FROM addon_connections c
		JOIN tenant_addons t ON t.tenant_id = c.tenant_id AND t.addon_id = c.addon_id
		WHERE c.addon_id='slack' AND c.connection_scope='tenant'
		  AND c.external_account_id=$1 AND c.status='active'
		ORDER BY c.updated_at DESC LIMIT 1
	`, teamID).Scan(&tenantID, &webhookSecretSealed)
	if err != nil || webhookSecretSealed == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	secret, err := s.openWorkspaceToken(webhookSecretSealed)
	if err != nil || secret == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !verifySlackSignature(secret, r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"text":"This action could not be verified."}`)
		return
	}
	// Only act on the first button in a Slack action batch. Tokens are
	// single-use, so processing several is unnecessary and could double-apply.
	if len(payload.Actions) == 0 || strings.TrimSpace(payload.Actions[0].Value) == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	message, err := s.consumeAddonActionToken(r.Context(), r, strings.TrimSpace(payload.Actions[0].Value), "slack", tenantID)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_, _ = io.WriteString(w, `{"response_type":"ephemeral","text":"`+jsonEscape(err.Error())+`"}`)
		return
	}
	_, _ = io.WriteString(w, `{"response_type":"ephemeral","text":"`+jsonEscape(message)+`"}`)
}

// teamsInteractiveLink is the landing page behind a Teams Adaptive Card
// button. The token is consumed before the action runs and is never reused.
func (s *Server) teamsInteractiveLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	message, err := s.consumeAddonActionToken(r.Context(), r, strings.TrimSpace(r.PathValue("token")), "teams", "")
	status := http.StatusOK
	title := "Alt8Mail action complete"
	if err != nil {
		status = http.StatusGone
		title = "Alt8Mail action unavailable"
		message = err.Error()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+
		html.EscapeString(title)+`</title><style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;min-height:100vh;display:grid;place-items:center;background:#f6f7f9;color:#18212b}main{max-width:420px;padding:32px;border:1px solid #dfe4ea;border-radius:16px;background:#fff;box-shadow:0 12px 40px rgba(20,30,45,.08)}h1{font-size:19px;margin:0 0 10px}p{margin:0;color:#4c5a68;line-height:1.5}</style></head><body><main><h1>`+
		html.EscapeString(title)+`</h1><p>`+html.EscapeString(message)+`</p></main></body></html>`)
}

// teamsActionBase resolves the public origin used in Adaptive Card links.
func (s *Server) teamsActionBase(r *http.Request) string {
	return strings.TrimRight(s.addonGatewayBase(r), "/")
}

func jsonEscape(value string) string {
	raw, _ := json.Marshal(value)
	return strings.TrimSuffix(strings.TrimPrefix(string(raw), `"`), `"`)
}
