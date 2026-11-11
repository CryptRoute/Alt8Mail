package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testMailOAuthServer(t *testing.T) *Server {
	t.Helper()
	box, err := newSecretBox(strings.Repeat("mail-oauth-test-secret-", 2))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{secrets: box, cfg: Config{AppEnvFile: filepath.Join(t.TempDir(), "app.env")}}
}

func TestMailOAuthAppliedPrefersDedicatedCredentials(t *testing.T) {
	definition, _ := mailOAuthDefinition(mailOAuthProviderGmail)
	applied := mailOAuthAppliedFor(definition, map[string]string{
		"ALT8MAIL_GOOGLE_CLIENT_ID":          "shared-id",
		"ALT8MAIL_GOOGLE_CLIENT_SECRET":      "shared-secret",
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_ID":     "mail-id",
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET": "mail-secret",
		"ALT8MAIL_GOOGLE_MAIL_OAUTH_ENABLED": "true",
	})
	if !applied.Enabled || applied.ClientID != "mail-id" || applied.Secret != "mail-secret" {
		t.Fatalf("dedicated credentials were not preferred: %#v", applied)
	}
	if applied.SecretSource != "mail" {
		t.Fatalf("secret source = %q", applied.SecretSource)
	}
}

func TestMailOAuthAppliedUsesSharedOIDCFallback(t *testing.T) {
	definition, _ := mailOAuthDefinition(mailOAuthProviderMicrosoft)
	applied := mailOAuthAppliedFor(definition, map[string]string{
		"ALT8MAIL_MICROSOFT_CLIENT_ID":     "shared-id",
		"ALT8MAIL_MICROSOFT_CLIENT_SECRET": "shared-secret",
	})
	if !applied.Enabled || applied.ClientID != "shared-id" || applied.SecretSource != "shared" {
		t.Fatalf("shared fallback was not used: %#v", applied)
	}
	if applied.Tenant != "common" {
		t.Fatalf("tenant = %q", applied.Tenant)
	}
}

func TestMailOAuthExplicitDisableWinsOverSharedCredentials(t *testing.T) {
	definition, _ := mailOAuthDefinition(mailOAuthProviderGmail)
	applied := mailOAuthAppliedFor(definition, map[string]string{
		"ALT8MAIL_GOOGLE_CLIENT_ID":          "shared-id",
		"ALT8MAIL_GOOGLE_CLIENT_SECRET":      "shared-secret",
		"ALT8MAIL_GOOGLE_MAIL_OAUTH_ENABLED": "false",
	})
	if applied.Enabled {
		t.Fatal("explicit disable was ignored")
	}
}

func TestMailOAuthDraftFallsBackToEnvironmentBeforeFirstSave(t *testing.T) {
	server := testMailOAuthServer(t)
	definition, _ := mailOAuthDefinition(mailOAuthProviderGmail)
	applied := mailOAuthAppliedFor(definition, map[string]string{
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_ID":     "mail-id",
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET": "mail-secret",
	})
	draft := server.mailOAuthDraftFor(definition, mailOAuthProviderRow{Provider: definition.Provider}, applied)
	if !draft.Enabled || draft.ClientID != "mail-id" || draft.Secret != "mail-secret" {
		t.Fatalf("draft did not mirror the environment: %#v", draft)
	}
	if draft.DraftSaved {
		t.Fatal("environment-derived draft must not claim to be saved")
	}
}

func TestMailOAuthDraftReadsSealedSecret(t *testing.T) {
	server := testMailOAuthServer(t)
	definition, _ := mailOAuthDefinition(mailOAuthProviderMicrosoft)
	sealed, err := server.secrets.Seal("stored-secret")
	if err != nil {
		t.Fatal(err)
	}
	row := mailOAuthProviderRow{
		Provider: definition.Provider, Enabled: true, ClientID: "client-id",
		Tenant: "organizations", SecretSealed: sealed, DraftSaved: true,
	}
	draft := server.mailOAuthDraftFor(definition, row, mailOAuthApplied{})
	if draft.Secret != "stored-secret" || draft.SecretSource != "stored" {
		t.Fatalf("sealed secret was not read: %#v", draft)
	}
	if draft.Tenant != "organizations" {
		t.Fatalf("tenant = %q", draft.Tenant)
	}
}

func TestMailOAuthApplyValuesWritesDedicatedEnvironment(t *testing.T) {
	server := testMailOAuthServer(t)
	gmail, _ := mailOAuthDefinition(mailOAuthProviderGmail)
	microsoft, _ := mailOAuthDefinition(mailOAuthProviderMicrosoft)
	gmailSealed, _ := server.secrets.Seal("google-secret")
	microsoftSealed, _ := server.secrets.Seal("microsoft-secret")
	rows := map[string]mailOAuthProviderRow{
		gmail.Provider: {
			Provider: gmail.Provider, Enabled: true, ClientID: "google-id",
			SecretSealed: gmailSealed, DraftSaved: true,
		},
		microsoft.Provider: {
			Provider: microsoft.Provider, Enabled: false, ClientID: "microsoft-id",
			Tenant: "contoso.onmicrosoft.com", SecretSealed: microsoftSealed, DraftSaved: true,
		},
	}
	values, err := server.mailOAuthApplyValues(rows, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if values["ALT8MAIL_GOOGLE_MAIL_CLIENT_ID"] != "google-id" ||
		values["ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET"] != "google-secret" ||
		values["ALT8MAIL_GOOGLE_MAIL_OAUTH_ENABLED"] != "true" {
		t.Fatalf("Gmail values = %#v", values)
	}
	if values["ALT8MAIL_MICROSOFT_MAIL_CLIENT_ID"] != "" ||
		values["ALT8MAIL_MICROSOFT_MAIL_CLIENT_SECRET"] != "" ||
		values["ALT8MAIL_MICROSOFT_MAIL_OAUTH_ENABLED"] != "false" {
		t.Fatalf("disabled Microsoft provider was not cleared: %#v", values)
	}
	if values["ALT8MAIL_MICROSOFT_MAIL_TENANT"] != "contoso.onmicrosoft.com" {
		t.Fatalf("Microsoft tenant = %q", values["ALT8MAIL_MICROSOFT_MAIL_TENANT"])
	}
}

func TestMailOAuthApplyValuesRequiresSecret(t *testing.T) {
	server := testMailOAuthServer(t)
	gmail, _ := mailOAuthDefinition(mailOAuthProviderGmail)
	rows := map[string]mailOAuthProviderRow{
		gmail.Provider: {Provider: gmail.Provider, Enabled: true, ClientID: "google-id", DraftSaved: true},
	}
	if _, err := server.mailOAuthApplyValues(rows, map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), "client secret") {
		t.Fatalf("missing secret error = %v", err)
	}
}

func TestMailOAuthApplyValuesImportsEnvironmentSecret(t *testing.T) {
	server := testMailOAuthServer(t)
	gmail, _ := mailOAuthDefinition(mailOAuthProviderGmail)
	applied := mailOAuthAppliedFor(gmail, map[string]string{
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_ID":     "mail-id",
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET": "mail-secret",
	})
	draft := server.mailOAuthDraftFor(gmail, mailOAuthProviderRow{Provider: gmail.Provider}, applied)
	values, err := server.mailOAuthApplyValues(
		map[string]mailOAuthProviderRow{gmail.Provider: {Provider: gmail.Provider}},
		map[string]string{
			"ALT8MAIL_GOOGLE_MAIL_CLIENT_ID":     "mail-id",
			"ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET": "mail-secret",
		})
	if err != nil {
		t.Fatal(err)
	}
	if draft.ClientID != "mail-id" || values["ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET"] != "mail-secret" {
		t.Fatalf("environment credentials were not preserved: %#v %#v", draft, values)
	}
}

func TestReadMailOAuthEnv(t *testing.T) {
	server := testMailOAuthServer(t)
	content := strings.Join([]string{
		"# Alt8Mail environment",
		`ALT8MAIL_PUBLIC_URL="https://mail.example.com"`,
		"export ALT8MAIL_GOOGLE_MAIL_CLIENT_ID=client-id",
		"ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET='secret-value'",
		"EMPTY=",
	}, "\n")
	if err := os.WriteFile(server.cfg.AppEnvFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env := server.readMailOAuthEnv()
	if !env.Readable {
		t.Fatalf("environment was not readable: %s", env.Error)
	}
	if env.Values["ALT8MAIL_PUBLIC_URL"] != "https://mail.example.com" ||
		env.Values["ALT8MAIL_GOOGLE_MAIL_CLIENT_ID"] != "client-id" ||
		env.Values["ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET"] != "secret-value" {
		t.Fatalf("parsed values = %#v", env.Values)
	}
}

func TestMailOAuthRedirectURI(t *testing.T) {
	if got := mailOAuthRedirectURI("https://mail.example.com/", "gmail"); got != "https://mail.example.com/api/mail/oauth/gmail/callback" {
		t.Fatalf("redirect URI = %q", got)
	}
	if !validMailOAuthRedirectURI("https://mail.example.com/api/mail/oauth/microsoft/callback") {
		t.Fatal("HTTPS redirect URI was rejected")
	}
	if !validMailOAuthRedirectURI("http://localhost:8080/api/mail/oauth/gmail/callback") {
		t.Fatal("localhost redirect URI was rejected")
	}
	if validMailOAuthRedirectURI("http://mail.example.com/api/mail/oauth/gmail/callback") {
		t.Fatal("plain HTTP on a public host was accepted")
	}
	if mailOAuthRedirectURI("", "gmail") != "" {
		t.Fatal("empty base must not produce a redirect URI")
	}
}

func TestMailOAuthValidation(t *testing.T) {
	for _, value := range []string{"1234567890-abcdef.apps.googleusercontent.com", "6f1c6a3b-3f31-4f55-9c4e-1a2b3c4d5e6f"} {
		if !validMailOAuthClientID(value) {
			t.Fatalf("valid client ID rejected: %s", value)
		}
	}
	for _, value := range []string{"", "bad client id", "line\nbreak", "abc"} {
		if validMailOAuthClientID(value) {
			t.Fatalf("invalid client ID accepted: %q", value)
		}
	}
	for _, value := range []string{"common", "organizations", "consumers", "contoso.onmicrosoft.com", "6f1c6a3b-3f31-4f55-9c4e-1a2b3c4d5e6f"} {
		if !validMailOAuthTenant(value) {
			t.Fatalf("valid tenant rejected: %s", value)
		}
	}
	if validMailOAuthTenant("bad tenant") {
		t.Fatal("invalid tenant accepted")
	}
	if normalizeMailOAuthTenant("") != "common" {
		t.Fatal("empty tenant must default to common")
	}
	if err := validateMailOAuthClientSecret("has space"); err == nil {
		t.Fatal("secret with a space was accepted")
	}
	if err := validateMailOAuthClientSecret("GOCSPX-abc_123-xyz"); err != nil {
		t.Fatalf("normal secret rejected: %v", err)
	}
}

func TestProbeMailOAuthDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://provider.example/authorize","token_endpoint":"https://provider.example/token"}`))
	}))
	defer server.Close()
	authorize, token, err := probeMailOAuthDiscovery(context.Background(), server.URL)
	if err != nil || authorize == "" || token == "" {
		t.Fatalf("discovery = %q %q %v", authorize, token, err)
	}
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer reject.Close()
	if _, _, err := probeMailOAuthDiscovery(context.Background(), reject.URL); err == nil {
		t.Fatal("non-200 discovery response was accepted")
	}
}
