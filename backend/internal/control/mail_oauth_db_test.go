package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMailOAuthDraftRoundTrip is opt-in and destructive only to the OAuth
// draft table. Point CONTROL_MAIL_OAUTH_TEST_DSN at a disposable database
// whose name contains "test".
func TestMailOAuthDraftRoundTrip(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CONTROL_MAIL_OAUTH_TEST_DSN"))
	if dsn == "" {
		t.Skip("set CONTROL_MAIL_OAUTH_TEST_DSN to run the Mail OAuth database test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("refusing to rewrite non-test database %q", database)
	}
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS control;
		DROP TABLE IF EXISTS control.mail_oauth_providers;
		CREATE TABLE control.mail_oauth_providers (
			provider text PRIMARY KEY,
			enabled boolean NOT NULL DEFAULT false,
			client_id text NOT NULL DEFAULT '',
			tenant text NOT NULL DEFAULT '',
			secret_sealed text NOT NULL DEFAULT '',
			draft_saved boolean NOT NULL DEFAULT false,
			updated_by uuid,
			updated_at timestamptz NOT NULL DEFAULT now(),
			applied_at timestamptz
		);
		INSERT INTO control.mail_oauth_providers (provider) VALUES ('gmail'), ('microsoft');
	`); err != nil {
		t.Fatal(err)
	}
	box, err := newSecretBox(strings.Repeat("mail-oauth-round-trip-", 2))
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "app.env")
	if err := os.WriteFile(envFile, []byte("ALT8MAIL_PUBLIC_URL=https://mail.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		db: pool, secrets: box, cfg: Config{AppEnvFile: envFile},
		auditSink: func(context.Context, *adminUser, string, string, string, net.IP, string, any) {},
	}
	sess := &sessionRecord{User: adminUser{ID: "11111111-1111-1111-1111-111111111111", Email: "operator@example.test"}}

	body, _ := json.Marshal(map[string]any{"providers": []map[string]any{
		{"provider": "gmail", "enabled": true, "clientId": "google-id.apps.googleusercontent.com", "clientSecret": "google-secret"},
		{"provider": "microsoft", "enabled": false, "clientId": "6f1c6a3b-3f31-4f55-9c4e-1a2b3c4d5e6f", "tenant": "organizations"},
	}})
	save := httptest.NewRecorder()
	server.handleMailOAuthSave(save, httptest.NewRequest(http.MethodPut, "/control/api/mail-oauth", bytes.NewReader(body)), sess)
	if save.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", save.Code, save.Body.String())
	}
	var sealed, clientID, tenant string
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled,client_id,tenant,secret_sealed FROM control.mail_oauth_providers WHERE provider='gmail'`).Scan(&enabled, &clientID, &tenant, &sealed); err != nil {
		t.Fatal(err)
	}
	if !enabled || clientID == "" || sealed == "" {
		t.Fatalf("gmail row = enabled %v client %q sealed %v", enabled, clientID, sealed != "")
	}
	if plain, err := box.Open(sealed); err != nil || plain != "google-secret" {
		t.Fatalf("sealed secret = %q, %v", plain, err)
	}

	get := httptest.NewRecorder()
	server.handleMailOAuth(get, httptest.NewRequest(http.MethodGet, "/control/api/mail-oauth", nil), sess)
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", get.Code, get.Body.String())
	}
	var payload struct {
		Providers []struct {
			Provider    string `json:"provider"`
			HasSecret   bool   `json:"hasSecret"`
			ClientID    string `json:"clientId"`
			Pending     bool   `json:"pending"`
			RedirectURI string `json:"redirectUri"`
		} `json:"providers"`
		PublicURL string `json:"publicUrl"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PublicURL != "https://mail.example.test" {
		t.Fatalf("public URL = %q", payload.PublicURL)
	}
	if len(payload.Providers) != 2 {
		t.Fatalf("providers = %#v", payload.Providers)
	}
	for _, provider := range payload.Providers {
		if provider.Provider == "gmail" {
			if !provider.HasSecret || provider.ClientID == "" || !provider.Pending {
				t.Fatalf("gmail view = %#v", provider)
			}
			if provider.RedirectURI != "https://mail.example.test/api/mail/oauth/gmail/callback" {
				t.Fatalf("redirect URI = %q", provider.RedirectURI)
			}
		}
	}

	rows, err := server.mailOAuthRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env := server.readMailOAuthEnv()
	values, err := server.mailOAuthApplyValues(rows, env.Values)
	if err != nil {
		t.Fatal(err)
	}
	if values["ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET"] != "google-secret" ||
		values["ALT8MAIL_GOOGLE_MAIL_OAUTH_ENABLED"] != "true" ||
		values["ALT8MAIL_MICROSOFT_MAIL_OAUTH_ENABLED"] != "false" {
		t.Fatalf("apply values = %#v", values)
	}
	if tenant != "" {
		t.Fatalf("gmail tenant = %q", tenant)
	}
	var microsoftTenant string
	if err := pool.QueryRow(ctx, `SELECT tenant FROM control.mail_oauth_providers WHERE provider='microsoft'`).Scan(&microsoftTenant); err != nil {
		t.Fatal(err)
	}
	if microsoftTenant != "organizations" {
		t.Fatalf("microsoft tenant = %q", microsoftTenant)
	}
	if values["ALT8MAIL_MICROSOFT_MAIL_TENANT"] != "organizations" {
		t.Fatalf("applied microsoft tenant = %q", values["ALT8MAIL_MICROSOFT_MAIL_TENANT"])
	}
}
