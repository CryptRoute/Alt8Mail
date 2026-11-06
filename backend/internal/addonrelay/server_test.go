package addonrelay

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/addonoauth"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	server, err := New(Config{
		PublicURL: "https://connect.alt8mail.test", Secret: "relay-secret",
		SlackClientID: "slack-client", SlackClientSecret: "slack-secret",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	return server
}

func signedState(t *testing.T, secret, callback string) (addonoauth.State, string) {
	t.Helper()
	state := addonoauth.State{
		TenantID: "tenant-1", AddonID: "slack", UserID: "user-1", Scope: "tenant",
		Mode: "managed", Callback: callback, Nonce: "nonce-1",
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix(),
	}
	raw, err := addonoauth.Sign(secret, state)
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	return state, raw
}

func TestSlackStartRedirectsToSlack(t *testing.T) {
	server := testServer(t)
	_, raw := signedState(t, "relay-secret", "https://mail.example/oauth/relay/complete")
	request := httptest.NewRequest(http.MethodGet, "/oauth/slack/start?state="+url.QueryEscape(raw), nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("start status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "slack.com/oauth/v2/authorize") || !strings.Contains(location, "state=") {
		t.Fatalf("unexpected Slack redirect: %s", location)
	}
	// The same signed request must not be accepted twice.
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("replayed start status = %d", recorder.Code)
	}
}

func TestSlackCallbackAndOneTimeExchange(t *testing.T) {
	originalTokenURL := slackTokenURL
	t.Cleanup(func() { slackTokenURL = originalTokenURL })
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "access_token": "xoxb-test",
			"team":             map[string]string{"id": "T123", "name": "Acme"},
			"incoming_webhook": map[string]string{"url": "https://hooks.slack.com/services/T123/B123/X", "channel": "#support", "channel_id": "C123"},
		})
	}))
	defer slack.Close()
	slackTokenURL = slack.URL

	server := testServer(t)
	state, raw := signedState(t, "relay-secret", "https://mail.example/oauth/relay/complete")
	request := httptest.NewRequest(http.MethodGet, "/oauth/slack/callback?code=oauth-code&state="+url.QueryEscape(raw), nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil || location.Host != "mail.example" || location.Query().Get("code") == "" {
		t.Fatalf("unexpected callback redirect: %s (%v)", recorder.Header().Get("Location"), err)
	}
	code := location.Query().Get("code")
	body, _ := json.Marshal(map[string]string{"code": code, "state": raw})
	exchange := httptest.NewRequest(http.MethodPost, "/oauth/exchange", strings.NewReader(string(body)))
	exchange.Header.Set("Authorization", "Bearer relay-secret")
	exchange.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, exchange)
	if recorder.Code != http.StatusOK {
		t.Fatalf("exchange status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Credentials       map[string]string `json:"credentials"`
		ExternalAccountID string            `json:"externalAccountId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Credentials["teamId"] != "T123" || envelope.ExternalAccountID != "T123" || envelope.Credentials["webhookUrl"] == "" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if state.TenantID != "tenant-1" {
		t.Fatalf("state mutated: %#v", state)
	}
	// A relay code is one-time.
	replay := httptest.NewRequest(http.MethodPost, "/oauth/exchange", strings.NewReader(string(body)))
	replay.Header.Set("Authorization", "Bearer relay-secret")
	replay.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, replay)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("replayed exchange status = %d", recorder.Code)
	}
}

func TestExchangeRejectsBadSecretAndState(t *testing.T) {
	server := testServer(t)
	_, raw := signedState(t, "relay-secret", "https://mail.example/callback")
	body := strings.NewReader(`{"code":"missing","state":"` + raw + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/oauth/exchange", body)
	request.Header.Set("Authorization", "Bearer wrong")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bad secret status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/oauth/slack/start?state=not-signed", nil)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad state status = %d", recorder.Code)
	}
}
