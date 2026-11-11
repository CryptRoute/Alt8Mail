package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mail OAuth is configured here because the control plane owns the host
// environment file that both webmail services read at startup. The webmail
// keeps its existing environment contract, so a configuration written by this
// page is applied by atomically updating /etc/a8m/app.env and restarting the
// two application services.

const (
	mailOAuthProviderGmail     = "gmail"
	mailOAuthProviderMicrosoft = "microsoft"
)

var mailOAuthTenantPattern = regexp.MustCompile(`^[A-Za-z0-9.-]{1,128}$`)
var mailOAuthClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{6,512}$`)

type mailOAuthProviderDefinition struct {
	Provider           string
	Label              string
	Description        string
	ClientIDEnv        string
	ClientSecretEnv    string
	TenantEnv          string
	EnabledEnv         string
	SharedClientIDEnv  string
	SharedSecretEnv    string
	Scopes             []string
	DiscoveryURL       func(tenant string) string
	ProviderConsoleURL string
	ProviderDocsURL    string
}

var mailOAuthDefinitions = []mailOAuthProviderDefinition{
	{
		Provider:          mailOAuthProviderGmail,
		Label:             "Google · Gmail",
		Description:       "Google Workspace and personal Gmail mailboxes.",
		ClientIDEnv:       "ALT8MAIL_GOOGLE_MAIL_CLIENT_ID",
		ClientSecretEnv:   "ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET",
		EnabledEnv:        "ALT8MAIL_GOOGLE_MAIL_OAUTH_ENABLED",
		SharedClientIDEnv: "ALT8MAIL_GOOGLE_CLIENT_ID",
		SharedSecretEnv:   "ALT8MAIL_GOOGLE_CLIENT_SECRET",
		Scopes: []string{
			"openid", "email", "profile", "https://mail.google.com/",
		},
		DiscoveryURL: func(string) string {
			return "https://accounts.google.com/.well-known/openid-configuration"
		},
		ProviderConsoleURL: "https://console.cloud.google.com/apis/credentials",
		ProviderDocsURL:    "https://developers.google.com/identity/protocols/oauth2/web-server",
	},
	{
		Provider:          mailOAuthProviderMicrosoft,
		Label:             "Microsoft · Outlook",
		Description:       "Outlook.com, Hotmail, Live and Microsoft 365 mailboxes.",
		ClientIDEnv:       "ALT8MAIL_MICROSOFT_MAIL_CLIENT_ID",
		ClientSecretEnv:   "ALT8MAIL_MICROSOFT_MAIL_CLIENT_SECRET",
		TenantEnv:         "ALT8MAIL_MICROSOFT_MAIL_TENANT",
		EnabledEnv:        "ALT8MAIL_MICROSOFT_MAIL_OAUTH_ENABLED",
		SharedClientIDEnv: "ALT8MAIL_MICROSOFT_CLIENT_ID",
		SharedSecretEnv:   "ALT8MAIL_MICROSOFT_CLIENT_SECRET",
		Scopes: []string{
			"openid", "email", "profile", "offline_access",
			"https://outlook.office.com/IMAP.AccessAsUser.All",
			"https://outlook.office.com/SMTP.Send",
		},
		DiscoveryURL: func(tenant string) string {
			return "https://login.microsoftonline.com/" + url.PathEscape(normalizeMailOAuthTenant(tenant)) + "/v2.0/.well-known/openid-configuration"
		},
		ProviderConsoleURL: "https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade",
		ProviderDocsURL:    "https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow",
	},
}

type mailOAuthProviderRow struct {
	Provider     string
	Enabled      bool
	ClientID     string
	Tenant       string
	SecretSealed string
	DraftSaved   bool
	UpdatedAt    time.Time
	AppliedAt    *time.Time
}

type mailOAuthApplied struct {
	Enabled      bool
	ClientID     string
	Tenant       string
	Secret       string
	SecretSource string
}

type mailOAuthDraft struct {
	Enabled      bool
	ClientID     string
	Tenant       string
	Secret       string
	SecretStored bool
	SecretSource string
	SecretError  string
	DraftSaved   bool
	UpdatedAt    *time.Time
	AppliedAt    *time.Time
}

func mailOAuthDefinition(provider string) (mailOAuthProviderDefinition, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, definition := range mailOAuthDefinitions {
		if definition.Provider == provider {
			return definition, true
		}
	}
	return mailOAuthProviderDefinition{}, false
}

func normalizeMailOAuthTenant(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "common"
	}
	return value
}

func validMailOAuthTenant(value string) bool {
	return mailOAuthTenantPattern.MatchString(strings.TrimSpace(value))
}

func validMailOAuthClientID(value string) bool {
	return mailOAuthClientIDPattern.MatchString(strings.TrimSpace(value))
}

func validateMailOAuthClientSecret(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a client secret is required")
	}
	if len(value) > 2048 {
		return fmt.Errorf("the client secret is longer than 2048 characters")
	}
	if strings.ContainsAny(value, " \t\r\n\x00\"'\\") {
		return fmt.Errorf("the client secret must not contain spaces, quotes, backslashes or line breaks")
	}
	return nil
}

func (s *Server) registerMailOAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/mail-oauth", s.require("server.read", s.handleMailOAuth))
	mux.HandleFunc("PUT /control/api/mail-oauth", s.require("server.manage", s.handleMailOAuthSave))
	mux.HandleFunc("POST /control/api/mail-oauth/apply", s.require("server.manage", s.handleMailOAuthApply))
	mux.HandleFunc("POST /control/api/mail-oauth/test", s.require("server.manage", s.handleMailOAuthTest))
}

func (s *Server) mailOAuthRows(ctx context.Context) (map[string]mailOAuthProviderRow, error) {
	rowsByProvider := map[string]mailOAuthProviderRow{}
	for _, definition := range mailOAuthDefinitions {
		rowsByProvider[definition.Provider] = mailOAuthProviderRow{Provider: definition.Provider}
	}
	rows, err := s.db.Query(ctx, `
		SELECT provider,enabled,client_id,tenant,secret_sealed,draft_saved,updated_at,applied_at
		FROM control.mail_oauth_providers
		WHERE provider = ANY($1)
	`, []string{mailOAuthProviderGmail, mailOAuthProviderMicrosoft})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row mailOAuthProviderRow
		if err := rows.Scan(&row.Provider, &row.Enabled, &row.ClientID, &row.Tenant, &row.SecretSealed,
			&row.DraftSaved, &row.UpdatedAt, &row.AppliedAt); err != nil {
			return nil, err
		}
		rowsByProvider[row.Provider] = row
	}
	return rowsByProvider, rows.Err()
}

type mailOAuthEnv struct {
	Values   map[string]string
	Readable bool
	Error    string
}

func (s *Server) readMailOAuthEnv() mailOAuthEnv {
	values := map[string]string{}
	path := strings.TrimSpace(s.cfg.AppEnvFile)
	if path == "" {
		return mailOAuthEnv{Values: values, Error: "The control plane has no app environment file configured."}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return mailOAuthEnv{Values: values, Error: fmt.Sprintf("Could not read %s: %v", path, err)}
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key != "" {
			values[key] = value
		}
	}
	return mailOAuthEnv{Values: values, Readable: true}
}

func mailOAuthAppliedFor(definition mailOAuthProviderDefinition, envValues map[string]string) mailOAuthApplied {
	clientID := strings.TrimSpace(envValues[definition.ClientIDEnv])
	secret := strings.TrimSpace(envValues[definition.ClientSecretEnv])
	secretSource := "environment"
	if secret == "" {
		secret = strings.TrimSpace(envValues[definition.SharedSecretEnv])
		if secret != "" {
			secretSource = "shared"
		} else {
			secretSource = ""
		}
	}
	if clientID == "" {
		clientID = strings.TrimSpace(envValues[definition.SharedClientIDEnv])
	}
	enabled := clientID != "" && secret != ""
	if raw := strings.TrimSpace(envValues[definition.EnabledEnv]); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			enabled = parsed
		}
	}
	tenant := ""
	if definition.TenantEnv != "" {
		tenant = normalizeMailOAuthTenant(envValues[definition.TenantEnv])
	}
	return mailOAuthApplied{
		Enabled: enabled, ClientID: clientID, Tenant: tenant,
		Secret: secret, SecretSource: secretSource,
	}
}

func (s *Server) mailOAuthDraftFor(definition mailOAuthProviderDefinition, row mailOAuthProviderRow, applied mailOAuthApplied) mailOAuthDraft {
	if !row.DraftSaved {
		draft := mailOAuthDraft{
			Enabled: applied.Enabled, ClientID: applied.ClientID, Tenant: applied.Tenant,
			Secret: applied.Secret, SecretSource: applied.SecretSource, AppliedAt: row.AppliedAt,
		}
		if definition.TenantEnv == "" {
			draft.Tenant = ""
		}
		return draft
	}
	draft := mailOAuthDraft{
		Enabled: row.Enabled, ClientID: strings.TrimSpace(row.ClientID), Tenant: row.Tenant,
		SecretStored: strings.TrimSpace(row.SecretSealed) != "", DraftSaved: true,
		UpdatedAt: &row.UpdatedAt, AppliedAt: row.AppliedAt,
	}
	if definition.TenantEnv == "" {
		draft.Tenant = ""
	} else {
		draft.Tenant = normalizeMailOAuthTenant(draft.Tenant)
	}
	if draft.SecretStored {
		secret, err := s.secrets.Open(row.SecretSealed)
		if err == nil && secret != "" {
			draft.Secret = secret
			draft.SecretSource = "stored"
		} else {
			draft.SecretError = "The stored client secret could not be decrypted. Enter the secret again."
			draft.SecretSource = "stored"
		}
	}
	if draft.Secret == "" && applied.Secret != "" && draft.SecretError == "" {
		draft.Secret = applied.Secret
		draft.SecretSource = applied.SecretSource
	}
	return draft
}

func (s *Server) mailOAuthPublicURL(ctx context.Context, envValues map[string]string) (string, string) {
	if value := strings.TrimRight(strings.TrimSpace(envValues["ALT8MAIL_PUBLIC_URL"]), "/"); value != "" {
		return value, "app.env"
	}
	if value := strings.TrimRight(strings.TrimSpace(s.settingString(ctx, "server.webmail_url", s.cfg.WebmailURL)), "/"); value != "" {
		return value, "Web Net"
	}
	return "", ""
}

func mailOAuthRedirectURI(base, provider string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/api/mail/oauth/" + url.PathEscape(provider) + "/callback"
}

func validMailOAuthRedirectURI(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := strings.ToLower(parsed.Hostname())
	return parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}

func (s *Server) handleMailOAuth(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	ctx := r.Context()
	rowsByProvider, err := s.mailOAuthRows(ctx)
	if err != nil {
		writeFromError(w, err)
		return
	}
	env := s.readMailOAuthEnv()
	publicURL, publicSource := s.mailOAuthPublicURL(ctx, env.Values)
	views := make([]map[string]any, 0, len(mailOAuthDefinitions))
	warnings := []string{}
	pendingAny := false
	for _, definition := range mailOAuthDefinitions {
		row := rowsByProvider[definition.Provider]
		applied := mailOAuthAppliedFor(definition, env.Values)
		draft := s.mailOAuthDraftFor(definition, row, applied)
		pending := draft.Enabled != applied.Enabled ||
			strings.TrimSpace(draft.ClientID) != applied.ClientID ||
			normalizeDraftTenant(definition, draft.Tenant) != applied.Tenant ||
			draft.Secret != applied.Secret
		if pending {
			pendingAny = true
		}
		if applied.ClientID != "" && !validMailOAuthClientID(applied.ClientID) {
			warnings = append(warnings, fmt.Sprintf("%s: the client ID in app.env contains unsupported characters, so the webmail refuses this sign-in method.", definition.Label))
		}
		if definition.TenantEnv != "" && !validMailOAuthTenant(applied.Tenant) {
			warnings = append(warnings, fmt.Sprintf("%s: the tenant in app.env contains unsupported characters, so the webmail refuses this sign-in method.", definition.Label))
		}
		if applied.Enabled && (applied.ClientID == "" || applied.Secret == "") {
			warnings = append(warnings, fmt.Sprintf("%s: app.env marks sign-in enabled but its client ID or secret is missing, so users are not offered this provider.", definition.Label))
		}
		view := map[string]any{
			"provider":     definition.Provider,
			"label":        definition.Label,
			"description":  definition.Description,
			"enabled":      draft.Enabled,
			"clientId":     draft.ClientID,
			"tenant":       draft.Tenant,
			"hasSecret":    draft.Secret != "",
			"secretSource": draft.SecretSource,
			"secretError":  draft.SecretError,
			"redirectUri":  mailOAuthRedirectURI(publicURL, definition.Provider),
			"scopes":       definition.Scopes,
			"consoleUrl":   definition.ProviderConsoleURL,
			"docsUrl":      definition.ProviderDocsURL,
			"pending":      pending,
			"applied": map[string]any{
				"enabled":      applied.Enabled,
				"clientId":     applied.ClientID,
				"tenant":       applied.Tenant,
				"hasSecret":    applied.Secret != "",
				"secretSource": applied.SecretSource,
			},
		}
		if draft.DraftSaved && draft.UpdatedAt != nil {
			view["updatedAt"] = draft.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if draft.AppliedAt != nil {
			view["appliedAt"] = draft.AppliedAt.UTC().Format(time.RFC3339)
		}
		views = append(views, view)
	}
	if env.Error != "" {
		warnings = append(warnings, env.Error)
	}
	if publicURL == "" {
		warnings = append(warnings, "No public webmail URL is configured. Set a webmail hostname in Web Net before enabling provider sign-in.")
	} else if !validMailOAuthRedirectURI(mailOAuthRedirectURI(publicURL, mailOAuthProviderGmail)) {
		warnings = append(warnings, "The public webmail URL must use HTTPS for provider sign-in. Plain HTTP is accepted only for localhost testing.")
	}
	envURL := strings.TrimRight(strings.TrimSpace(env.Values["ALT8MAIL_PUBLIC_URL"]), "/")
	settingURL := strings.TrimRight(strings.TrimSpace(s.settingString(ctx, "server.webmail_url", s.cfg.WebmailURL)), "/")
	if envURL != "" && settingURL != "" && envURL != settingURL {
		warnings = append(warnings, fmt.Sprintf("Web Net's webmail URL (%s) differs from ALT8MAIL_PUBLIC_URL in app.env (%s). Sign-in uses app.env, so the redirect URI below may not match the address users open.", settingURL, envURL))
	}
	if envURL == "" && settingURL != "" {
		warnings = append(warnings, "app.env has no ALT8MAIL_PUBLIC_URL. The redirect URI below comes from Web Net settings, but the webmail keeps using its default until Web Net is applied.")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":       views,
		"publicUrl":       publicURL,
		"publicUrlSource": publicSource,
		"webmailUrl":      settingURL,
		"appEnvPath":      s.cfg.AppEnvFile,
		"appEnvReadable":  env.Readable,
		"pending":         pendingAny,
		"warnings":        warnings,
	})
}

func normalizeDraftTenant(definition mailOAuthProviderDefinition, value string) string {
	if definition.TenantEnv == "" {
		return ""
	}
	return normalizeMailOAuthTenant(value)
}

func (s *Server) handleMailOAuthSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Providers []struct {
			Provider     string `json:"provider"`
			Enabled      bool   `json:"enabled"`
			ClientID     string `json:"clientId"`
			Tenant       string `json:"tenant"`
			ClientSecret string `json:"clientSecret"`
		} `json:"providers"`
	}
	if err := decodeJSON(w, r, &body, 128<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if len(body.Providers) == 0 {
		writeError(w, http.StatusBadRequest, "PROVIDERS", "Provide at least one provider configuration")
		return
	}
	ctx := r.Context()
	rowsByProvider, err := s.mailOAuthRows(ctx)
	if err != nil {
		writeFromError(w, err)
		return
	}
	env := s.readMailOAuthEnv()
	seen := map[string]bool{}
	saved := []string{}
	for _, input := range body.Providers {
		definition, ok := mailOAuthDefinition(input.Provider)
		if !ok {
			writeError(w, http.StatusBadRequest, "PROVIDER", "Unknown OAuth provider: "+strings.TrimSpace(input.Provider))
			return
		}
		if seen[definition.Provider] {
			writeError(w, http.StatusBadRequest, "DUPLICATE", "Each provider may appear once")
			return
		}
		seen[definition.Provider] = true
		row := rowsByProvider[definition.Provider]
		applied := mailOAuthAppliedFor(definition, env.Values)
		clientID := strings.TrimSpace(input.ClientID)
		secret := strings.TrimSpace(input.ClientSecret)
		tenant := ""
		if definition.TenantEnv != "" {
			tenant = normalizeMailOAuthTenant(input.Tenant)
			if !validMailOAuthTenant(tenant) {
				writeError(w, http.StatusBadRequest, "TENANT", fmt.Sprintf("%s: the tenant must contain only letters, digits, dots and hyphens", definition.Label))
				return
			}
		}
		if clientID != "" && !validMailOAuthClientID(clientID) {
			writeError(w, http.StatusBadRequest, "CLIENT_ID", fmt.Sprintf("%s: the client ID contains unsupported characters or has an unexpected length", definition.Label))
			return
		}
		storedSecret := false
		if strings.TrimSpace(row.SecretSealed) != "" {
			if opened, openErr := s.secrets.Open(row.SecretSealed); openErr == nil && opened != "" {
				storedSecret = true
			}
		}
		if input.Enabled {
			if clientID == "" {
				writeError(w, http.StatusBadRequest, "CLIENT_ID", fmt.Sprintf("%s: a client ID is required when sign-in is enabled", definition.Label))
				return
			}
			if secret == "" && !storedSecret && applied.Secret == "" {
				writeError(w, http.StatusBadRequest, "CLIENT_SECRET", fmt.Sprintf("%s: a client secret is required when sign-in is enabled", definition.Label))
				return
			}
		}
		sealed := ""
		if secret != "" {
			if err := validateMailOAuthClientSecret(secret); err != nil {
				writeError(w, http.StatusBadRequest, "CLIENT_SECRET", definition.Label+": "+err.Error())
				return
			}
			sealed, err = s.secrets.Seal(secret)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "SECRET", "Could not protect the client secret")
				return
			}
		}
		if _, err := s.db.Exec(ctx, `
			INSERT INTO control.mail_oauth_providers
				(provider,enabled,client_id,tenant,secret_sealed,draft_saved,updated_by,updated_at)
			VALUES ($1,$2,$3,$4,$5,true,$6,now())
			ON CONFLICT (provider) DO UPDATE SET
				enabled=EXCLUDED.enabled,
				client_id=EXCLUDED.client_id,
				tenant=EXCLUDED.tenant,
				secret_sealed=CASE WHEN EXCLUDED.secret_sealed <> '' THEN EXCLUDED.secret_sealed
					ELSE control.mail_oauth_providers.secret_sealed END,
				draft_saved=true,
				updated_by=EXCLUDED.updated_by,
				updated_at=now()
		`, definition.Provider, input.Enabled, clientID, tenant, sealed, sess.User.ID); err != nil {
			writeFromError(w, err)
			return
		}
		saved = append(saved, definition.Provider)
	}
	s.actorAudit(r, sess, "mail_oauth.draft_saved", "Mail OAuth", "Saved Gmail and Microsoft sign-in application settings",
		map[string]any{"providers": saved})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": saved})
}

func (s *Server) mailOAuthApplyValues(rowsByProvider map[string]mailOAuthProviderRow, envValues map[string]string) (map[string]string, error) {
	values := map[string]string{}
	for _, definition := range mailOAuthDefinitions {
		row := rowsByProvider[definition.Provider]
		applied := mailOAuthAppliedFor(definition, envValues)
		draft := s.mailOAuthDraftFor(definition, row, applied)
		if draft.SecretError != "" {
			return nil, fmt.Errorf("%s: %s", definition.Label, draft.SecretError)
		}
		if draft.Enabled {
			clientID := strings.TrimSpace(draft.ClientID)
			if clientID == "" {
				return nil, fmt.Errorf("%s: a client ID is required when sign-in is enabled", definition.Label)
			}
			if !validMailOAuthClientID(clientID) {
				return nil, fmt.Errorf("%s: the client ID contains unsupported characters", definition.Label)
			}
			secret := draft.Secret
			if secret == "" {
				secret = applied.Secret
			}
			if secret == "" {
				return nil, fmt.Errorf("%s: a client secret is required when sign-in is enabled", definition.Label)
			}
			if err := validateMailOAuthClientSecret(secret); err != nil {
				return nil, fmt.Errorf("%s: %s", definition.Label, err)
			}
			values[definition.EnabledEnv] = "true"
			values[definition.ClientIDEnv] = clientID
			values[definition.ClientSecretEnv] = secret
		} else {
			values[definition.EnabledEnv] = "false"
			values[definition.ClientIDEnv] = ""
			values[definition.ClientSecretEnv] = ""
		}
		if definition.TenantEnv != "" {
			tenant := normalizeMailOAuthTenant(draft.Tenant)
			if !validMailOAuthTenant(tenant) {
				return nil, fmt.Errorf("%s: the tenant must contain only letters, digits, dots and hyphens", definition.Label)
			}
			values[definition.TenantEnv] = tenant
		}
	}
	return values, nil
}

func (s *Server) handleMailOAuthApply(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	ctx := r.Context()
	if !s.mailOAuthEnvWritable() {
		writeError(w, http.StatusConflict, "APP_ENV", "The webmail environment file is not readable by System Control. Set CONTROL_APP_ENV to /etc/a8m/app.env and make sure the file exists.")
		return
	}
	rowsByProvider, err := s.mailOAuthRows(ctx)
	if err != nil {
		writeFromError(w, err)
		return
	}
	env := s.readMailOAuthEnv()
	if !env.Readable {
		writeError(w, http.StatusConflict, "APP_ENV", env.Error)
		return
	}
	// The redirect URI is built by the webmail from ALT8MAIL_PUBLIC_URL. Refuse
	// to apply credentials that could never round-trip through the provider.
	webmailPublicURL := strings.TrimRight(strings.TrimSpace(env.Values["ALT8MAIL_PUBLIC_URL"]), "/")
	if webmailPublicURL == "" {
		writeError(w, http.StatusConflict, "PUBLIC_URL", "app.env has no ALT8MAIL_PUBLIC_URL. Apply Web Net first, then apply the sign-in credentials.")
		return
	}
	if !validMailOAuthRedirectURI(mailOAuthRedirectURI(webmailPublicURL, mailOAuthProviderGmail)) {
		writeError(w, http.StatusConflict, "PUBLIC_URL", "ALT8MAIL_PUBLIC_URL must be a public HTTPS address (or localhost for testing) before sign-in can be applied.")
		return
	}
	values, err := s.mailOAuthApplyValues(rowsByProvider, env.Values)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OAUTH_CONFIG", err.Error())
		return
	}
	restore, err := s.applyWebNetEnv(values)
	if err != nil {
		writeError(w, http.StatusBadGateway, "APP_ENV", err.Error())
		return
	}
	if err := s.restartWebmailServices(ctx); err != nil {
		_ = restore()
		_ = s.restartWebmailServices(context.Background())
		writeError(w, http.StatusBadGateway, "RESTART", err.Error())
		return
	}
	appliedAt := time.Now().UTC()
	if _, err := s.db.Exec(ctx, `
		UPDATE control.mail_oauth_providers SET applied_at=$1 WHERE provider = ANY($2)
	`, appliedAt, []string{mailOAuthProviderGmail, mailOAuthProviderMicrosoft}); err != nil {
		s.logger.Warn("mail oauth applied but timestamps were not saved", "err", err)
	}
	publicURL, _ := s.mailOAuthPublicURL(ctx, s.readMailOAuthEnv().Values)
	s.actorAudit(r, sess, "mail_oauth.applied", "Mail OAuth", "Applied mail sign-in applications to the webmail services", nil)
	s.notifyEvent(ctx, "mail_oauth.applied", map[string]any{"appliedAt": appliedAt.Format(time.RFC3339)})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"appliedAt": appliedAt.Format(time.RFC3339),
		"redirectUris": map[string]string{
			mailOAuthProviderGmail:     mailOAuthRedirectURI(publicURL, mailOAuthProviderGmail),
			mailOAuthProviderMicrosoft: mailOAuthRedirectURI(publicURL, mailOAuthProviderMicrosoft),
		},
		"message": "Sign-in credentials applied and the webmail services restarted.",
	})
}

func (s *Server) mailOAuthEnvWritable() bool {
	path := strings.TrimSpace(s.cfg.AppEnvFile)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Server) handleMailOAuthTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rowsByProvider, err := s.mailOAuthRows(ctx)
	if err != nil {
		writeFromError(w, err)
		return
	}
	env := s.readMailOAuthEnv()
	publicURL, _ := s.mailOAuthPublicURL(ctx, env.Values)
	checks := []map[string]any{}
	allOK := true
	tested := 0
	for _, definition := range mailOAuthDefinitions {
		row := rowsByProvider[definition.Provider]
		draft := s.mailOAuthDraftFor(definition, row, mailOAuthAppliedFor(definition, env.Values))
		check := func(ok bool, skipped bool, message string) {
			if !ok && !skipped {
				allOK = false
			}
			checks = append(checks, map[string]any{
				"provider": definition.Provider, "label": definition.Label,
				"ok": ok, "skipped": skipped, "message": message,
			})
		}
		if !draft.Enabled {
			check(true, true, "Disabled — users are not offered this sign-in method.")
			continue
		}
		tested++
		if draft.SecretError != "" {
			check(false, false, draft.SecretError)
			continue
		}
		if strings.TrimSpace(draft.ClientID) == "" {
			check(false, false, "A client ID is required.")
			continue
		}
		if !validMailOAuthClientID(draft.ClientID) {
			check(false, false, "The client ID contains unsupported characters.")
			continue
		}
		if draft.Secret == "" {
			check(false, false, "A client secret is required.")
			continue
		}
		if definition.TenantEnv != "" && !validMailOAuthTenant(normalizeMailOAuthTenant(draft.Tenant)) {
			check(false, false, "The tenant must contain only letters, digits, dots and hyphens.")
			continue
		}
		redirectURI := mailOAuthRedirectURI(publicURL, definition.Provider)
		if !validMailOAuthRedirectURI(redirectURI) {
			check(false, false, "The webmail public URL must be HTTPS so the provider can redirect back to this server.")
			continue
		}
		authorize, token, probeErr := probeMailOAuthDiscovery(ctx, definition.DiscoveryURL(draft.Tenant))
		if probeErr != nil {
			check(false, false, "Could not reach the provider's sign-in metadata: "+probeErr.Error())
			continue
		}
		check(true, false, fmt.Sprintf("Provider reachable. Redirect URI: %s · Authorize endpoint: %s · Token endpoint: %s", redirectURI, authorize, token))
	}
	s.actorAudit(r, sess, "mail_oauth.tested", "Mail OAuth", "Tested mail sign-in applications", map[string]any{"ok": allOK})
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "tested": tested, "checks": checks})
}

func probeMailOAuthDiscovery(ctx context.Context, discoveryURL string) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid discovery URL")
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 12 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("network error")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if response.StatusCode >= 300 {
		return "", "", fmt.Errorf("the provider answered HTTP %d", response.StatusCode)
	}
	var payload struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", fmt.Errorf("the provider returned unreadable metadata")
	}
	if strings.TrimSpace(payload.AuthorizationEndpoint) == "" || strings.TrimSpace(payload.TokenEndpoint) == "" {
		return "", "", fmt.Errorf("the provider metadata is missing its OAuth endpoints")
	}
	return payload.AuthorizationEndpoint, payload.TokenEndpoint, nil
}
