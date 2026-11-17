// Package addonrelay implements the optional managed OAuth relay used by
// Alt8Mail add-ons. It holds no tenant mailbox data or long-lived tokens; it
// exchanges an authorization code and hands the resulting provider connection
// back to the tenant over a one-time code.
package addonrelay

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/addonoauth"
)

var (
	slackAuthorizeURL = "https://slack.com/oauth/v2/authorize"
	slackTokenURL     = "https://slack.com/api/oauth.v2.access"
)

const slackScopes = "incoming-webhook,chat:write,team:read"

type Config struct {
	PublicURL         string
	Secret            string
	SlackClientID     string
	SlackClientSecret string
	Logger            *slog.Logger
	Client            *http.Client
}

type pendingConnection struct {
	State             addonoauth.State
	Credentials       map[string]string
	Config            map[string]any
	AccountLabel      string
	ExternalAccountID string
	ExpiresAt         time.Time
}

type Server struct {
	cfg     Config
	mu      sync.Mutex
	pending map[string]pendingConnection
	started map[string]int64
}

func New(cfg Config) (*Server, error) {
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
	cfg.Secret = strings.TrimSpace(cfg.Secret)
	if cfg.PublicURL == "" || cfg.Secret == "" {
		return nil, fmt.Errorf("relay public URL and secret are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Server{cfg: cfg, pending: map[string]pendingConnection{}, started: map[string]int64{}}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "alt8mail-addon-relay"})
	})
	mux.HandleFunc("GET /oauth/slack/start", s.slackStart)
	mux.HandleFunc("GET /oauth/slack/callback", s.slackCallback)
	mux.HandleFunc("POST /oauth/exchange", s.exchange)
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) slackStart(w http.ResponseWriter, r *http.Request) {
	s.cleanup()
	rawState := r.URL.Query().Get("state")
	state, err := addonoauth.Parse(s.cfg.Secret, rawState)
	if err != nil || state.AddonID != "slack" || state.Mode != "managed" || !validCallback(state.Callback) {
		writeError(w, http.StatusBadRequest, "STATE", "Invalid or expired add-on request")
		return
	}
	if s.cfg.SlackClientID == "" || s.cfg.SlackClientSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "SLACK", "Slack OAuth is not configured on this relay")
		return
	}
	s.mu.Lock()
	if expires := s.started[state.Nonce]; expires > time.Now().Unix() {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, "STATE", "This add-on request has already been used")
		return
	}
	s.started[state.Nonce] = state.ExpiresAt
	s.mu.Unlock()
	values := url.Values{}
	values.Set("client_id", s.cfg.SlackClientID)
	values.Set("scope", slackScopes)
	values.Set("redirect_uri", s.cfg.PublicURL+"/oauth/slack/callback")
	values.Set("state", rawState)
	http.Redirect(w, r, slackAuthorizeURL+"?"+values.Encode(), http.StatusFound)
}

func (s *Server) slackCallback(w http.ResponseWriter, r *http.Request) {
	s.cleanup()
	rawState := r.URL.Query().Get("state")
	state, err := addonoauth.Parse(s.cfg.Secret, rawState)
	if err != nil || state.AddonID != "slack" || state.Mode != "managed" || !validCallback(state.Callback) {
		writeError(w, http.StatusBadRequest, "STATE", "Invalid or expired add-on request")
		return
	}
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		redirectCallback(w, r, state.Callback, "", rawState, "Slack authorization was cancelled")
		return
	}
	form := url.Values{}
	form.Set("client_id", s.cfg.SlackClientID)
	form.Set("client_secret", s.cfg.SlackClientSecret)
	form.Set("code", r.URL.Query().Get("code"))
	form.Set("redirect_uri", s.cfg.PublicURL+"/oauth/slack/callback")
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, slackTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SLACK", "Could not contact Slack")
		return
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.cfg.Client.Do(request)
	if err != nil {
		s.cfg.Logger.Warn("slack relay exchange", "err", err)
		redirectCallback(w, r, state.Callback, "", rawState, "Could not reach Slack")
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	var token struct {
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
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
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(raw, &token) != nil || !token.OK {
		message := "Slack rejected the authorization"
		if token.Error != "" {
			message = "Slack: " + token.Error
		}
		redirectCallback(w, r, state.Callback, "", rawState, message)
		return
	}
	if token.IncomingWebhook.URL == "" {
		redirectCallback(w, r, state.Callback, "", rawState, "Slack did not return an incoming webhook URL")
		return
	}
	credentials := map[string]string{
		"accessToken": token.AccessToken, "webhookUrl": token.IncomingWebhook.URL,
		"teamId": token.Team.ID, "teamName": token.Team.Name,
		"channel": token.IncomingWebhook.Channel, "channelId": token.IncomingWebhook.ChannelID,
	}
	config := map[string]any{"oauth": true, "teamName": token.Team.Name, "channel": token.IncomingWebhook.Channel}
	label := strings.TrimSpace(token.Team.Name)
	if token.IncomingWebhook.Channel != "" {
		label += " · " + token.IncomingWebhook.Channel
	}
	code := randomToken()
	s.mu.Lock()
	s.pending[code] = pendingConnection{
		State: state, Credentials: credentials, Config: config,
		AccountLabel: label, ExternalAccountID: token.Team.ID,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	s.mu.Unlock()
	redirectCallback(w, r, state.Callback, code, rawState, "")
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	s.cleanup()
	if !s.validBearer(r.Header.Get("Authorization")) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid relay credentials")
		return
	}
	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", "Invalid exchange request")
		return
	}
	body.Code = strings.TrimSpace(body.Code)
	state, err := addonoauth.Parse(s.cfg.Secret, strings.TrimSpace(body.State))
	if err != nil {
		writeError(w, http.StatusBadRequest, "STATE", "Invalid or expired add-on state")
		return
	}
	s.mu.Lock()
	pending, ok := s.pending[body.Code]
	if ok {
		delete(s.pending, body.Code)
	}
	s.mu.Unlock()
	if !ok || pending.ExpiresAt.Before(time.Now()) || pending.State.Nonce != state.Nonce || pending.State.TenantID != state.TenantID || pending.State.AddonID != state.AddonID {
		writeError(w, http.StatusNotFound, "CODE", "This relay code is invalid or has already been used")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credentials": pending.Credentials, "config": pending.Config,
		"accountLabel": pending.AccountLabel, "externalAccountId": pending.ExternalAccountID,
	})
}

func (s *Server) validBearer(header string) bool {
	raw := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return raw != "" && hmac.Equal([]byte(raw), []byte(s.cfg.Secret))
}

func (s *Server) cleanup() {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, pending := range s.pending {
		if pending.ExpiresAt.Unix() <= now {
			delete(s.pending, code)
		}
	}
	for nonce, expires := range s.started {
		if expires <= now {
			delete(s.started, nonce)
		}
	}
}

func validCallback(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func redirectCallback(w http.ResponseWriter, r *http.Request, callback, code, state, message string) {
	parsed, err := url.Parse(callback)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CALLBACK", "Invalid relay callback")
		return
	}
	query := parsed.Query()
	query.Set("state", state)
	if code != "" {
		query.Set("code", code)
	}
	if message != "" {
		query.Set("error", message)
	}
	parsed.RawQuery = query.Encode()
	http.Redirect(w, r, parsed.String(), http.StatusFound)
}

func randomToken() string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
