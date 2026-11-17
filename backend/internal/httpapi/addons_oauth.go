package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/addonoauth"
)

const (
	slackAuthorizeURL = "https://slack.com/oauth/v2/authorize"
	slackTokenURL     = "https://slack.com/api/oauth.v2.access"
	slackOAuthScopes  = "incoming-webhook,chat:write,team:read"
)

func (s *Server) addonOAuthMode(addonID string) string {
	if addonID != "slack" {
		return ""
	}
	if s.cfg.AddonRelayURL != "" && s.cfg.AddonRelaySecret != "" {
		return "managed"
	}
	if s.cfg.SessionSecret != "" {
		return "byo"
	}
	return "disabled"
}

func (s *Server) addonOAuthSecret() string {
	if s.cfg.AddonRelayURL != "" {
		return s.cfg.AddonRelaySecret
	}
	return s.cfg.SessionSecret
}

func (s *Server) addonGatewayBase(r *http.Request) string {
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.AddonGatewayURL), "/"); base != "" {
		return base
	}
	if parsed, err := url.Parse(strings.TrimSpace(s.cfg.PublicURL)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host + strings.TrimRight(parsed.Path, "/")
	}
	scheme := "https"
	if r != nil && r.TLS == nil && strings.HasPrefix(strings.ToLower(r.Header.Get("X-Forwarded-Proto")), "http") {
		scheme = "http"
	} else if r != nil && r.TLS == nil && (strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1")) {
		scheme = "http"
	}
	host := "localhost"
	if r != nil && r.Host != "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func (s *Server) startAddonOAuth(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok || definition.ID != "slack" {
		writeError(w, http.StatusNotFound, "ADDON_OAUTH", "OAuth is not available for this add-on")
		return
	}
	if definition.ConnectionScope == "tenant" && !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "ADDON_ADMIN", "Only an administrator can connect this add-on")
		return
	}
	mode := s.addonOAuthMode(definition.ID)
	var body struct {
		ClientID      string `json:"clientId"`
		ClientSecret  string `json:"clientSecret"`
		SigningSecret string `json:"signingSecret"`
		Mode          string `json:"mode"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body, 64*1024); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
	}
	// A managed server may still let an administrator choose their own Slack
	// app so this workspace can receive signed Slack Events webhooks, which a
	// centrally managed app cannot verify for individual tenants.
	switch strings.ToLower(strings.TrimSpace(body.Mode)) {
	case "managed":
		if s.cfg.AddonRelayURL == "" || s.cfg.AddonRelaySecret == "" {
			writeError(w, http.StatusServiceUnavailable, "ADDON_OAUTH", "The managed OAuth relay is not configured for this server")
			return
		}
		mode = "managed"
	case "byo":
		if s.cfg.SessionSecret == "" {
			writeError(w, http.StatusServiceUnavailable, "ADDON_OAUTH", "OAuth is not configured for this server")
			return
		}
		mode = "byo"
	}
	if mode == "disabled" {
		writeError(w, http.StatusServiceUnavailable, "ADDON_OAUTH", "OAuth relaying is not configured for this server")
		return
	}
	state := addonoauth.State{
		TenantID: sess.User.TenantID, AddonID: definition.ID, UserID: sess.User.ID,
		Scope: definition.ConnectionScope, Mode: mode, Nonce: randomToken(18),
		ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
	}
	if mode == "managed" {
		state.Callback = s.addonGatewayBase(r) + "/oauth/relay/complete"
		rawState, err := addonoauth.Sign(s.cfg.AddonRelaySecret, state)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "ADDON_OAUTH", "Could not create the OAuth request")
			return
		}
		oauthURL := s.cfg.AddonRelayURL + "/oauth/slack/start?state=" + url.QueryEscape(rawState)
		s.audit(r, sess.User, "addon.oauth_started", definition.Name, "Started managed Slack OAuth")
		writeJSON(w, http.StatusOK, map[string]any{"oauthUrl": oauthURL, "mode": "managed"})
		return
	}
	tenant := s.addonTenantRecord(r.Context(), sess.User.TenantID, definition.ID)
	clientID := strings.TrimSpace(body.ClientID)
	if clientID == "" {
		clientID = tenant.ClientID
	}
	clientSecret := strings.TrimSpace(body.ClientSecret)
	if clientSecret == "" && tenant.ClientSecretSealed != "" {
		clientSecret, _ = s.openWorkspaceToken(tenant.ClientSecretSealed)
	}
	signingSecret := strings.TrimSpace(body.SigningSecret)
	if signingSecret == "" && tenant.WebhookSecretSealed != "" {
		signingSecret, _ = s.openWorkspaceToken(tenant.WebhookSecretSealed)
	}
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusBadRequest, "ADDON_OAUTH_APP", "Enter the Slack app client ID and client secret")
		return
	}
	clientSealed, err := s.sealWorkspaceToken(clientSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_OAUTH_APP", "Could not protect the Slack app secret")
		return
	}
	signingSealed := ""
	if signingSecret != "" {
		signingSealed, err = s.sealWorkspaceToken(signingSecret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ADDON_OAUTH_APP", "Could not protect the Slack signing secret")
			return
		}
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO tenant_addons (id, tenant_id, addon_id, enabled, client_id, client_secret_sealed, webhook_secret_sealed, created_by, updated_by)
		VALUES ($1,$2,$3,true,$4,$5,$6,$7,$7)
		ON CONFLICT (tenant_id, addon_id) DO UPDATE
		SET enabled=true, client_id=EXCLUDED.client_id, client_secret_sealed=EXCLUDED.client_secret_sealed,
		    webhook_secret_sealed=EXCLUDED.webhook_secret_sealed, updated_at=now(), updated_by=EXCLUDED.updated_by
	`, newID("tadd"), sess.User.TenantID, definition.ID, clientID, clientSealed, signingSealed, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_OAUTH_APP", "Could not save the Slack app configuration")
		return
	}
	state.Callback = ""
	rawState, err := addonoauth.Sign(s.cfg.SessionSecret, state)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ADDON_OAUTH", "Could not create the OAuth request")
		return
	}
	redirectURI := s.addonGatewayBase(r) + "/oauth/slack/callback"
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("scope", slackOAuthScopes)
	values.Set("redirect_uri", redirectURI)
	values.Set("state", rawState)
	s.audit(r, sess.User, "addon.oauth_started", definition.Name, "Started Slack OAuth")
	writeJSON(w, http.StatusOK, map[string]any{
		"oauthUrl": slackAuthorizeURL + "?" + values.Encode(), "mode": "byo",
	})
}

type slackOAuthResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	IncomingWebhook struct {
		URL       string `json:"url"`
		Channel   string `json:"channel"`
		ChannelID string `json:"channel_id"`
	} `json:"incoming_webhook"`
}

func (s *Server) slackAddonOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		s.redirectAddonOAuth(w, r, "slack", "error", "Slack authorization was cancelled")
		return
	}
	state, err := addonoauth.Parse(s.cfg.SessionSecret, r.URL.Query().Get("state"))
	if err != nil || state.AddonID != "slack" || state.Mode != "byo" {
		s.redirectAddonOAuth(w, r, "slack", "error", "This Slack connection request has expired")
		return
	}
	tenant := s.addonTenantRecord(r.Context(), state.TenantID, "slack")
	if tenant.ClientID == "" || tenant.ClientSecretSealed == "" {
		s.redirectAddonOAuth(w, r, "slack", "error", "The Slack app is not configured")
		return
	}
	clientSecret, err := s.openWorkspaceToken(tenant.ClientSecretSealed)
	if err != nil {
		s.redirectAddonOAuth(w, r, "slack", "error", "The Slack app secret is unavailable")
		return
	}
	redirectURI := s.addonGatewayBase(r) + "/oauth/slack/callback"
	form := url.Values{}
	form.Set("client_id", tenant.ClientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", r.URL.Query().Get("code"))
	form.Set("redirect_uri", redirectURI)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, slackTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		s.redirectAddonOAuth(w, r, "slack", "error", "Could not contact Slack")
		return
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := addonDo(r.Context(), "slack", request)
	if err != nil {
		s.logger.Warn("slack oauth exchange", "err", err)
		s.redirectAddonOAuth(w, r, "slack", "error", "Could not reach Slack to complete the connection")
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	var token slackOAuthResponse
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(raw, &token) != nil || !token.OK {
		message := "Slack rejected the authorization"
		if token.Error != "" {
			message = "Slack: " + token.Error
		}
		s.redirectAddonOAuth(w, r, "slack", "error", message)
		return
	}
	if token.IncomingWebhook.URL == "" {
		s.redirectAddonOAuth(w, r, "slack", "error", "Slack did not return an incoming webhook URL. Enable the incoming-webhook scope on the Slack app.")
		return
	}
	credentials := map[string]string{
		"accessToken": token.AccessToken, "webhookUrl": token.IncomingWebhook.URL,
		"teamId": token.Team.ID, "teamName": token.Team.Name,
		"channel": token.IncomingWebhook.Channel, "channelId": token.IncomingWebhook.ChannelID,
	}
	config := map[string]any{
		"oauth": true, "teamName": token.Team.Name, "channel": token.IncomingWebhook.Channel,
	}
	label := strings.TrimSpace(token.Team.Name)
	if token.IncomingWebhook.Channel != "" {
		label += " · " + token.IncomingWebhook.Channel
	}
	if err := s.storeOAuthConnection(r.Context(), state, credentials, config, label, token.Team.ID); err != nil {
		s.logger.Error("slack oauth store", "err", err)
		s.redirectAddonOAuth(w, r, "slack", "error", "The Slack connection could not be saved")
		return
	}
	if user, userErr := s.userByID(r.Context(), state.UserID); userErr == nil {
		s.audit(r, user, "addon.oauth_connected", "Slack", "Connected Slack workspace "+token.Team.Name)
	}
	s.redirectAddonOAuth(w, r, "slack", "connected", "")
}

func (s *Server) storeOAuthConnection(ctx context.Context, state addonoauth.State, credentials map[string]string, config map[string]any, accountLabel, externalAccountID string) error {
	definition, ok := addonDefinitionByID(state.AddonID)
	if !ok {
		return errors.New("add-on not found")
	}
	rawCredentials, _ := json.Marshal(credentials)
	sealed, err := s.sealWorkspaceToken(string(rawCredentials))
	if err != nil {
		return err
	}
	configJSON, _ := json.Marshal(config)
	connectionID := newID("acon")
	scope := definition.ConnectionScope
	if scope == "tenant" {
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE addon_connections SET status='revoked', revoked_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND addon_id=$2 AND connection_scope='tenant' AND status='active'
		`, state.TenantID, definition.ID)
		_, err = s.store.DB.Exec(ctx, `
			INSERT INTO addon_connections (
				id, tenant_id, addon_id, connection_scope, account_label, external_account_id,
				credentials_sealed, config, status, created_by
			) VALUES ($1,$2,$3,'tenant',$4,$5,$6,$7,'active',$8)
		`, connectionID, state.TenantID, definition.ID, accountLabel, externalAccountID, sealed, configJSON, state.UserID)
	} else {
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE addon_connections SET status='revoked', revoked_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND addon_id=$2 AND connection_scope='user' AND owner_user_id=$3 AND status='active'
		`, state.TenantID, definition.ID, state.UserID)
		_, err = s.store.DB.Exec(ctx, `
			INSERT INTO addon_connections (
				id, tenant_id, addon_id, connection_scope, owner_user_id, account_label, external_account_id,
				credentials_sealed, config, status, created_by
			) VALUES ($1,$2,$3,'user',$4,$5,$6,$7,$8,'active',$4)
		`, connectionID, state.TenantID, definition.ID, state.UserID, accountLabel, externalAccountID, sealed, configJSON)
	}
	if err != nil {
		return err
	}
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO tenant_addons (id, tenant_id, addon_id, enabled, config, created_by, updated_by)
		VALUES ($1,$2,$3,true,$4,$5,$5)
		ON CONFLICT (tenant_id, addon_id) DO UPDATE
		SET enabled=true, config=EXCLUDED.config, updated_at=now(), updated_by=EXCLUDED.updated_by
	`, newID("tadd"), state.TenantID, definition.ID, configJSON, state.UserID)
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE addon_requests SET status='approved', resolved_by=$3, resolved_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND addon_id=$2 AND status='pending'
	`, state.TenantID, definition.ID, state.UserID)
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE addon_installs SET connection_id=$3, updated_at=now()
		WHERE tenant_id=$1 AND addon_id=$2
	`, state.TenantID, definition.ID, connectionID)
	return nil
}

func (s *Server) addonRelayComplete(w http.ResponseWriter, r *http.Request) {
	stateRaw := r.URL.Query().Get("state")
	state, err := addonoauth.Parse(s.cfg.AddonRelaySecret, stateRaw)
	if err != nil || state.Mode != "managed" || state.AddonID != "slack" {
		s.redirectAddonOAuth(w, r, "slack", "error", "This Slack connection request has expired")
		return
	}
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		message := "Slack authorization failed"
		if providerError == "access_denied" {
			message = "Slack authorization was cancelled"
		}
		s.redirectAddonOAuth(w, r, "slack", "error", message)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || s.cfg.AddonRelayURL == "" || s.cfg.AddonRelaySecret == "" {
		s.redirectAddonOAuth(w, r, "slack", "error", "The add-on relay did not return a connection")
		return
	}
	payload, _ := json.Marshal(map[string]string{"code": code, "state": stateRaw})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.AddonRelayURL+"/oauth/exchange", strings.NewReader(string(payload)))
	if err != nil {
		s.redirectAddonOAuth(w, r, "slack", "error", "Could not contact the add-on relay")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.cfg.AddonRelaySecret)
	response, err := addonDo(r.Context(), "alt8mail-addon-relay", request)
	if err != nil {
		s.logger.Warn("addon relay exchange", "err", err)
		s.redirectAddonOAuth(w, r, "slack", "error", "Could not complete the relayed Slack connection")
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.redirectAddonOAuth(w, r, "slack", "error", "The add-on relay rejected the connection")
		return
	}
	var envelope struct {
		Credentials       map[string]string `json:"credentials"`
		Config            map[string]any    `json:"config"`
		AccountLabel      string            `json:"accountLabel"`
		ExternalAccountID string            `json:"externalAccountId"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Credentials == nil {
		s.redirectAddonOAuth(w, r, "slack", "error", "The relay returned an invalid connection")
		return
	}
	if err := s.storeOAuthConnection(r.Context(), state, envelope.Credentials, envelope.Config, envelope.AccountLabel, envelope.ExternalAccountID); err != nil {
		s.logger.Error("relay oauth store", "err", err)
		s.redirectAddonOAuth(w, r, "slack", "error", "The Slack connection could not be saved")
		return
	}
	if user, userErr := s.userByID(r.Context(), state.UserID); userErr == nil {
		s.audit(r, user, "addon.oauth_connected", "Slack", "Connected Slack workspace through the managed relay")
	}
	s.redirectAddonOAuth(w, r, "slack", "connected", "")
}

func (s *Server) redirectAddonOAuth(w http.ResponseWriter, r *http.Request, addonID, status, message string) {
	target := strings.TrimRight(s.cfg.PublicURL, "/")
	if target == "" {
		target = s.addonGatewayBase(r)
	}
	query := url.Values{}
	query.Set("addon", addonID)
	query.Set("addon_status", status)
	if message != "" {
		query.Set("addon_message", message)
	}
	target += "/?" + query.Encode() + "#/Settings/addons"
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) slackAddonWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var envelope struct {
		Type      string `json:"type"`
		TeamID    string `json:"team_id"`
		Challenge string `json:"challenge"`
		EventID   string `json:"event_id"`
		Event     struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if envelope.Type == "url_verification" {
		// Slack's initial URL check usually has no team_id, so match the
		// signature against every active workspace Slack app instead.
		if !s.slackVerifyAnyTenantSignature(r.Context(), r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"challenge": envelope.Challenge})
		return
	}
	if envelope.TeamID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var tenantID, connectionID, webhookSecretSealed string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT c.tenant_id::text, c.id, COALESCE(t.webhook_secret_sealed, '')
		FROM addon_connections c
		LEFT JOIN tenant_addons t ON t.tenant_id = c.tenant_id AND t.addon_id = c.addon_id
		WHERE c.addon_id='slack' AND c.connection_scope='tenant'
		  AND c.external_account_id=$1 AND c.status='active'
		ORDER BY c.updated_at DESC LIMIT 1
	`, envelope.TeamID).Scan(&tenantID, &connectionID, &webhookSecretSealed)
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
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if envelope.Type != "event_callback" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	eventType := envelope.Event.Type
	if eventType == "" {
		eventType = envelope.Type
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO addon_events (id, tenant_id, addon_id, connection_id, provider_event_id, event_type, payload)
		VALUES ($1,$2,'slack',$3,$4,$5,$6)
		ON CONFLICT DO NOTHING
	`, newID("aev"), tenantID, connectionID, envelope.EventID, eventType, body)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) slackVerifyAnyTenantSignature(ctx context.Context, timestamp, signature string, body []byte) bool {
	rows, err := s.store.DB.Query(ctx, `
		SELECT DISTINCT t.webhook_secret_sealed
		FROM tenant_addons t
		JOIN addon_connections c
		  ON c.tenant_id = t.tenant_id AND c.addon_id = t.addon_id
		WHERE t.addon_id = 'slack' AND c.connection_scope = 'tenant'
		  AND c.status = 'active' AND t.webhook_secret_sealed <> ''
		LIMIT 50
	`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var sealed string
		if rows.Scan(&sealed) != nil {
			continue
		}
		secret, err := s.openWorkspaceToken(sealed)
		if err != nil || secret == "" {
			continue
		}
		if verifySlackSignature(secret, timestamp, signature, body) {
			return true
		}
	}
	return false
}

func verifySlackSignature(secret, timestamp, signature string, body []byte) bool {
	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if seconds < now-300 || seconds > now+60 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + string(body)))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature)))
}
