package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"strings"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func configForOAuthTest() config.Config {
	return config.Config{
		GoogleMailClientID:        "google-mail-id",
		GoogleMailClientSecret:    "google-mail-secret",
		MicrosoftMailClientID:     "microsoft-mail-id",
		MicrosoftMailClientSecret: "microsoft-mail-secret",
	}
}

func TestMailOAuthPKCE(t *testing.T) {
	firstVerifier, firstChallenge := mailOAuthPKCE()
	secondVerifier, secondChallenge := mailOAuthPKCE()
	if firstVerifier == secondVerifier || firstChallenge == secondChallenge {
		t.Fatal("PKCE values must be unique per request")
	}
	if len(firstVerifier) < 43 || len(firstChallenge) < 43 {
		t.Fatalf("PKCE values are too short: verifier=%d challenge=%d", len(firstVerifier), len(firstChallenge))
	}
	sum := sha256.Sum256([]byte(firstVerifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if firstChallenge != want {
		t.Fatalf("challenge = %q, want %q", firstChallenge, want)
	}
}

func TestXOAUTH2IMAPClient(t *testing.T) {
	client := &xoauth2IMAPClient{username: "person@example.test", token: "token-123"}
	mechanism, initial, err := client.Start()
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != "XOAUTH2" {
		t.Fatalf("mechanism = %q", mechanism)
	}
	want := "user=person@example.test\x01auth=Bearer token-123\x01\x01"
	if string(initial) != want {
		t.Fatalf("initial response = %q, want %q", initial, want)
	}
	if response, err := client.Next([]byte("error")); err != nil || len(response) != 0 {
		t.Fatalf("first challenge response = %q, %v", response, err)
	}
	if _, err := client.Next([]byte("error")); err == nil {
		t.Fatal("a second challenge must abort authentication")
	}
}

func TestXOAUTH2SMTPAuth(t *testing.T) {
	auth := &xoauth2SMTPAuth{username: "person@example.test", token: "token-123"}
	mechanism, initial, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.test", TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != "XOAUTH2" || !strings.Contains(string(initial), "auth=Bearer token-123") {
		t.Fatalf("unexpected SMTP auth: %q %q", mechanism, initial)
	}
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.test", TLS: false}); err == nil {
		t.Fatal("XOAUTH2 must not run without TLS")
	}
	if response, err := auth.Next(nil, true); err != nil || len(response) != 0 {
		t.Fatalf("challenge response = %q, %v", response, err)
	}
	if _, err := auth.Next(nil, true); err == nil {
		t.Fatal("a second challenge must abort authentication")
	}
	if _, err := auth.Next(nil, false); err != nil {
		t.Fatalf("final Next = %v", err)
	}
}

func TestMailOAuthProviderConfig(t *testing.T) {
	server := &Server{cfg: configForOAuthTest()}
	google, clientID, secret, ok := server.mailOAuthConfig("gmail")
	if !ok || clientID != "google-mail-id" || secret != "google-mail-secret" {
		t.Fatalf("gmail OAuth config = %#v %q %q %v", google, clientID, secret, ok)
	}
	if !strings.Contains(google.Scope, "mail.google.com") || !strings.Contains(google.Authorize, "accounts.google.com") {
		t.Fatalf("unexpected Gmail config: %#v", google)
	}
	microsoft, clientID, secret, ok := server.mailOAuthConfig("microsoft")
	if !ok || clientID != "microsoft-mail-id" || secret != "microsoft-mail-secret" {
		t.Fatalf("microsoft OAuth config = %#v %q %q %v", microsoft, clientID, secret, ok)
	}
	if !strings.Contains(microsoft.Scope, "IMAP.AccessAsUser.All") || !strings.Contains(microsoft.Scope, "SMTP.Send") {
		t.Fatalf("unexpected Microsoft scope: %q", microsoft.Scope)
	}
	if _, _, _, ok := server.mailOAuthConfig("unknown"); ok {
		t.Fatal("unknown provider must not be configured")
	}
}

func TestUnifiedMailAccountView(t *testing.T) {
	view := unifiedMailAccountView([]map[string]any{{"id": "a"}, {"id": "b"}})
	if view["id"] != "all" || view["isUnified"] != true || view["accountCount"] != 2 {
		t.Fatalf("unexpected unified account view: %#v", view)
	}
}

func TestMailOAuthTokenRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-1" {
			t.Errorf("unexpected form: %#v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600,"scope":"mail"}`))
	}))
	defer server.Close()
	token, err := (&Server{}).mailOAuthTokenRequest(context.Background(), server.URL, url.Values{
		"grant_type": {"authorization_code"}, "code": {"code-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access-1" || token.RefreshToken != "refresh-1" || token.ExpiresIn != 3600 {
		t.Fatalf("unexpected token: %#v", token)
	}
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer reject.Close()
	if _, err := (&Server{}).mailOAuthTokenRequest(context.Background(), reject.URL, url.Values{}); err == nil ||
		!strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("rejected token error = %v", err)
	}
}

func TestMailOAuthUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-1" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"email":"Person@Example.test","name":"Person"}`))
	}))
	defer server.Close()
	email, name, err := (&Server{}).mailOAuthUserInfo(context.Background(), mailOAuthProvider{ID: "gmail", UserInfo: server.URL}, oauthToken{AccessToken: "access-1"})
	if err != nil {
		t.Fatal(err)
	}
	if email != "person@example.test" || name != "Person" {
		t.Fatalf("user info = %q %q", email, name)
	}
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"Work@Example.test","name":"Work User"}`))
	email, name, err = (&Server{}).mailOAuthUserInfo(context.Background(), mailOAuthProvider{ID: "microsoft"},
		oauthToken{IDToken: "header." + claims + ".signature"})
	if err != nil || email != "work@example.test" || name != "Work User" {
		t.Fatalf("ID token identity = %q %q %v", email, name, err)
	}
	if _, _, err := (&Server{}).mailOAuthUserInfo(context.Background(), mailOAuthProvider{ID: "microsoft"}, oauthToken{}); err == nil {
		t.Fatal("missing Microsoft ID token must fail")
	}
}

func TestMailOAuthResultPage(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).mailOAuthResult(recorder, "connected", "", "macct_1")
	body := recorder.Body.String()
	if !strings.Contains(body, `accountId:"macct_1"`) || !strings.Contains(body, "a8m-mail-oauth") {
		t.Fatalf("result page is missing the account acknowledgement: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", recorder.Header().Get("Cache-Control"))
	}
}

func TestWorkspaceTokenRotation(t *testing.T) {
	oldServer := &Server{cfg: config.Config{SessionSecret: "old-session-secret"}}
	sealed, err := oldServer.sealWorkspaceToken("account-password")
	if err != nil {
		t.Fatal(err)
	}
	wrong := &Server{cfg: config.Config{SessionSecret: "new-session-secret", SessionSecretPrevious: "new-session-secret"}}
	if _, err := wrong.openWorkspaceToken(sealed); err == nil {
		t.Fatal("a different session secret decrypted the token")
	}
	rotated := &Server{cfg: config.Config{SessionSecret: "new-session-secret", SessionSecretPrevious: "old-session-secret"}}
	plain, err := rotated.openWorkspaceToken(sealed)
	if err != nil || plain != "account-password" {
		t.Fatalf("rotation fallback = %q, %v", plain, err)
	}
}

func TestUnifiedFolderConditionSkipsAccountFilter(t *testing.T) {
	where, args := mailFolderConditionForAccount("user-1", mailAccountScopeAll, "inbox", "all")
	if strings.Contains(where, "account_id") {
		t.Fatalf("unified condition still filters by account: %s", where)
	}
	if len(args) != 2 || args[0] != "user-1" || args[1] != "inbox" {
		t.Fatalf("unexpected unified args: %#v", args)
	}
	where, args = mailFolderConditionForAccount("user-1", "macct_1", "inbox", "all")
	if !strings.Contains(where, "account_id=$2") || len(args) != 3 {
		t.Fatalf("single-account condition = %q %#v", where, args)
	}
}
