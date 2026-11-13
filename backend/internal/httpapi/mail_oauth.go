package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const mailOAuthStateTTL = 10 * time.Minute

type mailOAuthProvider struct {
	ID        string
	Authorize string
	Token     string
	UserInfo  string
	Scope     string
	ExtraAuth map[string]string
}

func (s *Server) mailOAuthConfig(provider string) (mailOAuthProvider, string, string, bool) {
	switch provider {
	case "gmail":
		if s.cfg.GoogleMailOAuthDisabled {
			return mailOAuthProvider{}, "", "", false
		}
		clientID := firstNonEmpty(s.cfg.GoogleMailClientID, s.cfg.GoogleClientID)
		clientSecret := firstNonEmpty(s.cfg.GoogleMailClientSecret, s.cfg.GoogleClientSecret)
		if clientID == "" || clientSecret == "" {
			return mailOAuthProvider{}, "", "", false
		}
		return mailOAuthProvider{
			ID:        "gmail",
			Authorize: "https://accounts.google.com/o/oauth2/v2/auth",
			Token:     "https://oauth2.googleapis.com/token",
			UserInfo:  "https://openidconnect.googleapis.com/v1/userinfo",
			Scope:     "openid email profile https://mail.google.com/",
			ExtraAuth: map[string]string{"access_type": "offline", "prompt": "consent"},
		}, clientID, clientSecret, true
	case "microsoft":
		if s.cfg.MicrosoftMailOAuthDisabled {
			return mailOAuthProvider{}, "", "", false
		}
		clientID := firstNonEmpty(s.cfg.MicrosoftMailClientID, s.cfg.MicrosoftClientID)
		clientSecret := firstNonEmpty(s.cfg.MicrosoftMailClientSecret, s.cfg.MicrosoftClientSecret)
		if clientID == "" || clientSecret == "" {
			return mailOAuthProvider{}, "", "", false
		}
		tenant := strings.TrimSpace(s.cfg.MicrosoftMailTenant)
		if tenant == "" {
			tenant = "common"
		}
		for _, character := range tenant {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' || character == '.' {
				continue
			}
			return mailOAuthProvider{}, "", "", false
		}
		return mailOAuthProvider{
			ID:        "microsoft",
			Authorize: "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize",
			Token:     "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
			UserInfo:  "https://graph.microsoft.com/v1.0/me?$select=mail,userPrincipalName,displayName",
			Scope:     "openid email profile offline_access https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send",
			ExtraAuth: map[string]string{"prompt": "consent"},
		}, clientID, clientSecret, true
	}
	return mailOAuthProvider{}, "", "", false
}

// mailOAuthConfigured reports whether an administrator has supplied usable
// credentials for the provider. The webmail shell uses this to hide sign-in
// actions that would only fail after the user leaves Alt8Mail.
func (s *Server) mailOAuthConfigured(provider string) bool {
	_, _, _, ok := s.mailOAuthConfig(provider)
	return ok
}

func mailOAuthUnavailable(provider string) error {
	name := "This sign-in provider"
	switch provider {
	case "gmail":
		name = "Gmail sign-in"
	case "microsoft":
		name = "Microsoft sign-in"
	}
	return fmt.Errorf("%s is not configured on this server. Ask your administrator to add OAuth credentials in System Control → Mail OAuth", name)
}

func mailOAuthProviderErrorMessage(provider, code, description, redirectURI string) string {
	name := "the sign-in provider"
	switch provider {
	case "gmail":
		name = "Google"
	case "microsoft":
		name = "Microsoft"
	}
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "access_denied":
		return "Sign-in was cancelled. No account was connected."
	case "redirect_uri_mismatch":
		return fmt.Sprintf("%s rejected the redirect URI. Register %s in the OAuth application, then try again.", name, redirectURI)
	case "invalid_client", "unauthorized_client":
		return fmt.Sprintf("%s rejected the OAuth client. Ask your administrator to check the client ID and secret in System Control → Mail OAuth.", name)
	case "invalid_scope":
		return fmt.Sprintf("%s rejected the requested mail permissions. Ask your administrator to verify the app registration and consent screen.", name)
	case "consent_required", "interaction_required":
		return fmt.Sprintf("%s requires consent or an administrator approval before this mailbox can connect.", name)
	}
	if detail := truncate(strings.TrimSpace(description), 220); detail != "" {
		return fmt.Sprintf("%s rejected the sign-in: %s", name, detail)
	}
	return "Sign-in was cancelled or rejected by the provider"
}

func (s *Server) mailOAuthRedirectURL(provider string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/api/mail/oauth/" + url.PathEscape(provider) + "/callback"
}

func mailOAuthPKCE() (verifier, challenge string) {
	verifier = randomToken(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func (s *Server) beginMailOAuth(ctx context.Context, sess *sessionRecord, provider, accountID string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	config, clientID, _, ok := s.mailOAuthConfig(provider)
	if !ok {
		return "", mailOAuthUnavailable(provider)
	}
	if !s.externalMailAccountsAllowed(ctx, sess.User) {
		return "", fmt.Errorf("connected mail accounts are disabled for this organization")
	}
	accountID = strings.TrimSpace(accountID)
	var loginHint string
	if accountID != "" {
		account, err := s.resolveMailAccount(ctx, sess.User.ID, accountID)
		if err != nil || account.Kind != mailAccountKindExternal || account.Provider != provider {
			return "", fmt.Errorf("mail account not found")
		}
		loginHint = account.Email
	}
	state := randomToken(32)
	verifier, challenge := mailOAuthPKCE()
	sealedVerifier, err := s.sealMailAccountSecret(verifier)
	if err != nil {
		return "", fmt.Errorf("could not protect the OAuth request")
	}
	_, _ = s.store.DB.Exec(ctx, `DELETE FROM public.mail_oauth_states WHERE user_id=$1 AND expires_at < now()`, sess.User.ID)
	var accountValue any
	if accountID != "" {
		accountValue = accountID
	}
	if _, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.mail_oauth_states (id, tenant_id, user_id, provider, verifier_sealed, account_id, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,now() + $7::interval)
	`, state, sess.User.TenantID, sess.User.ID, provider, sealedVerifier, accountValue,
		fmt.Sprintf("%d seconds", int(mailOAuthStateTTL.Seconds()))); err != nil {
		return "", fmt.Errorf("could not start the OAuth request")
	}
	values := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {s.mailOAuthRedirectURL(provider)},
		"response_type":         {"code"},
		"scope":                 {config.Scope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	for key, value := range config.ExtraAuth {
		values.Set(key, value)
	}
	if loginHint != "" {
		values.Set("login_hint", loginHint)
	}
	return config.Authorize + "?" + values.Encode(), nil
}

func (s *Server) startMailOAuth(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.mailAccountRateAllowed(sess) {
		s.mailOAuthResult(w, "error", "Too many sign-in attempts. Try again in a few minutes.")
		return
	}
	target, err := s.beginMailOAuth(r.Context(), sess, r.PathValue("provider"), r.URL.Query().Get("account"))
	if err != nil {
		s.mailOAuthResult(w, "error", err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) mailOAuthCallback(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
			_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM public.mail_oauth_states WHERE id=$1 AND provider=$2`, state, provider)
		}
		s.mailOAuthResult(w, "error", mailOAuthProviderErrorMessage(provider, providerError, r.URL.Query().Get("error_description"), s.mailOAuthRedirectURL(provider)))
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		s.mailOAuthResult(w, "error", "The provider did not return an authorization code")
		return
	}
	var tenantID, userID, verifierSealed, accountID string
	err := s.store.DB.QueryRow(r.Context(), `
		DELETE FROM public.mail_oauth_states
		WHERE id=$1 AND provider=$2 AND expires_at > now()
		RETURNING tenant_id::text, user_id::text, verifier_sealed, COALESCE(account_id, '')
	`, state, provider).Scan(&tenantID, &userID, &verifierSealed, &accountID)
	if err != nil {
		s.mailOAuthResult(w, "error", "This sign-in request is invalid or has expired")
		return
	}
	config, clientID, clientSecret, ok := s.mailOAuthConfig(provider)
	if !ok {
		s.mailOAuthResult(w, "error", mailOAuthUnavailable(provider).Error())
		return
	}
	verifier, err := s.openMailAccountSecret(verifierSealed)
	if err != nil || verifier == "" {
		s.mailOAuthResult(w, "error", "Could not read the OAuth request")
		return
	}
	token, err := s.exchangeMailOAuthCode(r.Context(), config, clientID, clientSecret, provider, code, verifier)
	if err != nil {
		s.mailOAuthResult(w, "error", err.Error())
		return
	}
	email, displayName, err := s.mailOAuthUserInfo(r.Context(), config, token)
	if err != nil {
		s.mailOAuthResult(w, "error", "Could not read the provider account")
		return
	}
	user, err := s.userByID(r.Context(), userID)
	if err != nil || user.TenantID != tenantID {
		s.mailOAuthResult(w, "error", "The signed-in user is no longer available")
		return
	}
	account, err := s.saveMailOAuthAccount(r.Context(), user, config, email, displayName, accountID, token)
	if err != nil {
		s.mailOAuthResult(w, "error", err.Error())
		return
	}
	sess := &sessionRecord{User: user}
	s.recordWorkspaceAudit(r.Context(), sess, "account.connected", account.Email, "Connected external mail account "+account.Email+" with OAuth")
	s.scheduleMailAccountSync(account.ID, user.ID)
	s.mailOAuthResult(w, "connected", account.Email, account.ID)
}

func (s *Server) verifyMailOAuthIMAP(ctx context.Context, account mailAccountRecord, email string, token oauthToken) error {
	username := firstNonEmpty(email, account.Username)
	// The token endpoint returns the scopes that were actually granted, which
	// is the only reliable way to tell a consent-screen problem from a mailbox
	// policy problem. A token can be valid for userinfo while still lacking the
	// mail scope, and Gmail then answers the IMAP XOAUTH2 login with the
	// generic "Invalid credentials" failure.
	if scopes := strings.TrimSpace(token.Scope); scopes != "" {
		switch account.Provider {
		case "gmail":
			if !mailOAuthTokenHasScope(scopes, "https://mail.google.com/") {
				return fmt.Errorf("Google signed in but did not grant the Gmail IMAP and SMTP permission (https://mail.google.com/). In Google Cloud Console → APIs & Services → OAuth consent screen, add that scope, then remove Alt8Mail from myaccount.google.com/permissions and reconnect the mailbox")
			}
		case "microsoft":
			if !mailOAuthTokenHasScope(scopes, "https://outlook.office.com/IMAP.AccessAsUser.All") {
				return fmt.Errorf("Microsoft signed in but did not grant the IMAP permission. In the app registration, add the delegated Office 365 Exchange Online permission IMAP.AccessAsUser.All, grant admin consent, then reconnect the mailbox")
			}
		}
	}
	auth := externalMailAuth{Username: username, AccessToken: token.AccessToken}
	conn, err := openExternalIMAPWithAuth(ctx, account.IMAPHost, account.IMAPPort, account.IMAPTLS, auth)
	if err != nil {
		if transientExternalMailError(err) {
			return fmt.Errorf("the mail provider's IMAP server is temporarily unavailable: %w. Try the connection again in a minute", err)
		}
		switch account.Provider {
		case "gmail":
			return fmt.Errorf("the provider account could not open IMAP: %w. Gmail accepted the sign-in. Personal Gmail always has IMAP on, so check that Gmail is enabled for this account and that the OAuth consent screen includes https://mail.google.com/. For Google Workspace, an administrator must allow IMAP in Admin console → Apps → Google Workspace → Gmail → End User Access → POP and IMAP access", err)
		case "microsoft":
			return fmt.Errorf("the provider account could not open IMAP: %w. Microsoft accepted the sign-in, so check that IMAP is enabled for the mailbox and that the app has delegated IMAP.AccessAsUser.All permission with admin consent", err)
		}
		return fmt.Errorf("the provider account could not open IMAP: %w", err)
	}
	_, listErr := listIMAPMailboxes(conn)
	_ = conn.Logout()
	if listErr != nil {
		return fmt.Errorf("the provider account could not list mail folders")
	}
	return nil
}

func mailOAuthTokenHasScope(raw, wanted string) bool {
	wanted = strings.TrimSuffix(strings.TrimSpace(wanted), "/")
	if wanted == "" {
		return false
	}
	for _, scope := range strings.Fields(raw) {
		if strings.EqualFold(strings.TrimSuffix(scope, "/"), wanted) {
			return true
		}
	}
	return false
}

func (s *Server) saveMailOAuthAccount(ctx context.Context, user userRecord, config mailOAuthProvider, email, displayName, reconnectID string, token oauthToken) (mailAccountRecord, error) {
	email, err := normalizeAccountEmail(email)
	if err != nil {
		return mailAccountRecord{}, fmt.Errorf("the provider returned an invalid account email")
	}
	provider := config.ID
	imapHost, imapPort := "imap.gmail.com", 993
	smtpHost, smtpPort := "smtp.gmail.com", 587
	if provider == "microsoft" {
		imapHost, imapPort = "outlook.office365.com", 993
		smtpHost, smtpPort = "smtp.office365.com", 587
	}
	sealedAccess, err := s.sealMailAccountSecret(token.AccessToken)
	if err != nil {
		return mailAccountRecord{}, fmt.Errorf("could not protect the OAuth token")
	}
	sealedRefresh := ""
	if strings.TrimSpace(token.RefreshToken) != "" {
		sealedRefresh, err = s.sealMailAccountSecret(token.RefreshToken)
		if err != nil {
			return mailAccountRecord{}, fmt.Errorf("could not protect the OAuth token")
		}
	}
	var expiresAt any
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	if displayName == "" {
		displayName = strings.Split(email, "@")[0]
	}
	reconnectID = strings.TrimSpace(reconnectID)
	if reconnectID != "" {
		existing, err := s.mailAccountByID(ctx, user.ID, reconnectID)
		if err != nil || existing.Provider != provider {
			return mailAccountRecord{}, fmt.Errorf("mail account not found")
		}
		if !strings.EqualFold(existing.Email, email) {
			return mailAccountRecord{}, fmt.Errorf("sign in with %s to reconnect this account", existing.Email)
		}
		if err := s.verifyMailOAuthIMAP(ctx, existing, email, token); err != nil {
			return mailAccountRecord{}, err
		}
		if _, err := s.store.DB.Exec(ctx, `
			UPDATE public.mail_accounts SET
				auth_type='oauth2', secret_sealed='', oauth_access_sealed=$2,
				oauth_refresh_sealed=CASE WHEN $3 <> '' THEN $3 ELSE oauth_refresh_sealed END,
				oauth_expires_at=$4, status='active', last_error='', username=$5, updated_at=now()
			WHERE id=$1 AND user_id=$6 AND kind='external'
		`, existing.ID, sealedAccess, sealedRefresh, expiresAt, email, user.ID); err != nil {
			return mailAccountRecord{}, fmt.Errorf("could not save the OAuth tokens")
		}
		return s.mailAccountByID(ctx, user.ID, existing.ID)
	}
	var existingID string
	_ = s.store.DB.QueryRow(ctx, `
		SELECT id FROM public.mail_accounts
		WHERE user_id=$1 AND provider=$2 AND email=$3 AND kind='external'
	`, user.ID, provider, email).Scan(&existingID)
	if existingID != "" {
		existing, existingErr := s.mailAccountByID(ctx, user.ID, existingID)
		if existingErr != nil {
			return mailAccountRecord{}, fmt.Errorf("mail account not found")
		}
		if err := s.verifyMailOAuthIMAP(ctx, existing, email, token); err != nil {
			return mailAccountRecord{}, err
		}
		if _, err := s.store.DB.Exec(ctx, `
			UPDATE public.mail_accounts SET
				auth_type='oauth2', secret_sealed='', oauth_access_sealed=$2,
				oauth_refresh_sealed=CASE WHEN $3 <> '' THEN $3 ELSE oauth_refresh_sealed END,
				oauth_expires_at=$4, status='active', last_error='', username=$5, updated_at=now()
			WHERE id=$1 AND user_id=$6
		`, existingID, sealedAccess, sealedRefresh, expiresAt, email, user.ID); err != nil {
			return mailAccountRecord{}, fmt.Errorf("could not save the OAuth tokens")
		}
		return s.mailAccountByID(ctx, user.ID, existingID)
	}
	var externalCount int
	if err := s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_accounts WHERE user_id=$1 AND kind='external'
	`, user.ID).Scan(&externalCount); err != nil {
		return mailAccountRecord{}, fmt.Errorf("could not check the account limit")
	}
	if externalCount >= maxExternalMailAccounts {
		return mailAccountRecord{}, fmt.Errorf("you can connect up to %d external accounts", maxExternalMailAccounts)
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return mailAccountRecord{}, fmt.Errorf("the provider did not grant offline access; try again and allow offline access")
	}
	account := mailAccountRecord{
		ID: newID("macct"), TenantID: user.TenantID, UserID: user.ID, Kind: mailAccountKindExternal,
		Provider: provider, Email: email, DisplayName: truncate(displayName, 120), Color: "#34557a",
		Status: "active", AuthType: "oauth2",
		IMAPHost: imapHost, IMAPPort: imapPort, IMAPTLS: "tls",
		SMTPHost: smtpHost, SMTPPort: smtpPort, SMTPTLS: "starttls", Username: email,
	}
	// Verify the fresh token against IMAP before creating the account. SMTP
	// availability is provider-tenant controlled and is reported when sending.
	if err := s.verifyMailOAuthIMAP(ctx, account, email, token); err != nil {
		return mailAccountRecord{}, err
	}
	if _, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.mail_accounts (
			id, tenant_id, user_id, kind, provider, email, display_name, color, status, auth_type,
			oauth_access_sealed, oauth_refresh_sealed, oauth_expires_at,
			imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, username
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, account.ID, account.TenantID, account.UserID, account.Kind, account.Provider, account.Email,
		account.DisplayName, account.Color, account.Status, account.AuthType,
		sealedAccess, sealedRefresh, expiresAt,
		account.IMAPHost, account.IMAPPort, account.IMAPTLS, account.SMTPHost, account.SMTPPort,
		account.SMTPTLS, account.Username); err != nil {
		return mailAccountRecord{}, fmt.Errorf("could not save the mail account")
	}
	return s.mailAccountByID(ctx, user.ID, account.ID)
}

func (s *Server) exchangeMailOAuthCode(ctx context.Context, config mailOAuthProvider, clientID, clientSecret, provider, code, verifier string) (oauthToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {s.mailOAuthRedirectURL(provider)},
		"code_verifier": {verifier},
	}
	if config.ID == "microsoft" {
		// Microsoft expects the granted scopes repeated at the token endpoint.
		// Google's authorization-code exchange does not take a scope here.
		form.Set("scope", config.Scope)
	}
	return s.mailOAuthTokenRequest(ctx, config.Token, form)
}

func (s *Server) refreshMailOAuthToken(ctx context.Context, config mailOAuthProvider, clientID, clientSecret, refreshToken string) (oauthToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	return s.mailOAuthTokenRequest(ctx, config.Token, form)
}

func (s *Server) mailOAuthTokenRequest(ctx context.Context, endpoint string, form url.Values) (oauthToken, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := storageHTTPClient().Do(request)
	if err != nil {
		return oauthToken{}, fmt.Errorf("could not reach the sign-in provider")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if response.StatusCode >= 300 {
		var payload struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &payload)
		code := strings.TrimSpace(payload.Error)
		if code == "" {
			code = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		switch strings.ToLower(code) {
		case "invalid_client", "unauthorized_client":
			return oauthToken{}, fmt.Errorf("the sign-in provider rejected the OAuth client (%s); check the client ID and secret in System Control → Mail OAuth", code)
		}
		if detail := strings.TrimSpace(payload.ErrorDescription); detail != "" {
			return oauthToken{}, fmt.Errorf("the sign-in provider rejected the request (%s: %s)", truncate(code, 80), truncate(detail, 200))
		}
		return oauthToken{}, fmt.Errorf("the sign-in provider rejected the request (%s)", truncate(code, 120))
	}
	var token oauthToken
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return oauthToken{}, fmt.Errorf("the sign-in provider returned no access token")
	}
	return token, nil
}

func (s *Server) mailOAuthUserInfo(ctx context.Context, config mailOAuthProvider, token oauthToken) (string, string, error) {
	if config.ID == "microsoft" {
		// Outlook resource tokens cannot be used against Microsoft Graph. The
		// ID token is returned directly by the token endpoint over TLS and
		// carries the same account identity without a second resource token.
		email, name, err := mailOAuthIDTokenIdentity(token.IDToken)
		if err == nil {
			return email, name, nil
		}
		return "", "", err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, config.UserInfo, nil)
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Accept", "application/json")
	response, err := storageHTTPClient().Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if response.StatusCode >= 300 {
		return "", "", fmt.Errorf("provider account lookup failed (%d)", response.StatusCode)
	}
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	email := ""
	for _, key := range []string{"email", "mail", "userPrincipalName"} {
		if value := strings.TrimSpace(stateString(payload, key)); value != "" {
			email = strings.ToLower(value)
			break
		}
	}
	if email == "" {
		return "", "", fmt.Errorf("provider returned no account email")
	}
	name := firstNonEmpty(stateString(payload, "name"), stateString(payload, "displayName"))
	return email, name, nil
}

func mailOAuthIDTokenIdentity(idToken string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("the provider returned no identity token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("the provider returned an unreadable identity token")
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", "", fmt.Errorf("the provider returned an unreadable identity token")
	}
	email := ""
	for _, key := range []string{"email", "preferred_username", "upn", "unique_name"} {
		if value := strings.TrimSpace(stateString(claims, key)); value != "" && strings.Contains(value, "@") {
			email = strings.ToLower(value)
			break
		}
	}
	if email == "" {
		return "", "", fmt.Errorf("the provider returned no account email")
	}
	return email, stateString(claims, "name"), nil
}

func (s *Server) mailOAuthAccessToken(ctx context.Context, account mailAccountRecord) (string, error) {
	lock := s.userMailLock("mail-oauth:" + account.ID)
	lock.Lock()
	defer lock.Unlock()
	var accessSealed, refreshSealed string
	var expiresAt *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT oauth_access_sealed, oauth_refresh_sealed, oauth_expires_at
		FROM public.mail_accounts
		WHERE id=$1 AND user_id=$2 AND kind='external' AND auth_type='oauth2'
	`, account.ID, account.UserID).Scan(&accessSealed, &refreshSealed, &expiresAt)
	if err != nil {
		return "", fmt.Errorf("this account needs to be reconnected")
	}
	if strings.TrimSpace(accessSealed) != "" && (expiresAt == nil || expiresAt.After(time.Now().Add(60*time.Second))) {
		token, err := s.openMailAccountSecret(accessSealed)
		if err == nil && token != "" {
			return token, nil
		}
	}
	refresh, err := s.openMailAccountSecret(refreshSealed)
	if err != nil || strings.TrimSpace(refresh) == "" {
		s.markMailAccountReauth(ctx, account, "authorization expired")
		return "", fmt.Errorf("this account needs to be reconnected")
	}
	config, clientID, clientSecret, ok := s.mailOAuthConfig(account.Provider)
	if !ok {
		return "", mailOAuthUnavailable(account.Provider)
	}
	token, err := s.refreshMailOAuthToken(ctx, config, clientID, clientSecret, refresh)
	if err != nil {
		// A provider timeout or rate limit is not a revoked authorization. Keep
		// the account in the retry pool and let the next pass refresh again.
		if transientExternalMailError(err) {
			return "", fmt.Errorf("could not refresh the OAuth token: %w", err)
		}
		s.markMailAccountReauth(ctx, account, err.Error())
		return "", fmt.Errorf("this account needs to be reconnected")
	}
	accessSealed, sealErr := s.sealMailAccountSecret(token.AccessToken)
	if sealErr != nil {
		return "", fmt.Errorf("could not protect the OAuth token")
	}
	refreshSealed = ""
	if strings.TrimSpace(token.RefreshToken) != "" {
		refreshSealed, _ = s.sealMailAccountSecret(token.RefreshToken)
	}
	var newExpiry any
	if token.ExpiresIn > 0 {
		newExpiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.mail_accounts SET
			oauth_access_sealed=$3,
			oauth_refresh_sealed=CASE WHEN $4 <> '' THEN $4 ELSE oauth_refresh_sealed END,
			oauth_expires_at=$5, status='active', last_error='', updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, account.ID, account.UserID, accessSealed, refreshSealed, newExpiry)
	return token.AccessToken, nil
}

func (s *Server) markMailAccountReauth(ctx context.Context, account mailAccountRecord, reason string) {
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.mail_accounts SET status='reauth', last_error=$3, updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, account.ID, account.UserID, truncate(reason, 500))
}

func (s *Server) mailOAuthResult(w http.ResponseWriter, status, message string, accountIDs ...string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'none'; frame-ancestors 'none'")
	origin := ""
	if parsed, err := url.Parse(s.cfg.PublicURL); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	statusJSON, _ := json.Marshal(status)
	messageJSON, _ := json.Marshal(message)
	originJSON, _ := json.Marshal(origin)
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	accountJSON, _ := json.Marshal(accountID)
	// Prevent a provider-supplied message from closing the script element.
	messageJSON = bytes.ReplaceAll(messageJSON, []byte("</"), []byte("<\\/"))
	statusJSON = bytes.ReplaceAll(statusJSON, []byte("</"), []byte("<\\/"))
	accountEmail := ""
	if status == "connected" {
		accountEmail = strings.TrimSpace(message)
	}
	title := "Mail account connected · Alt8Mail"
	eyebrow := "Alt8Mail · Connected account"
	heading := "Mail account connected"
	body := "The mailbox is now available in your multi-mailbox inbox. The main Alt8Mail window has been updated."
	markClass := "is-good"
	markIcon := `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m20 6-11 11-5-5"/></svg>`
	if status != "connected" {
		title = "Mail account not connected · Alt8Mail"
		eyebrow = "Alt8Mail · Sign-in result"
		heading = "Mail account not connected"
		body = message
		markClass = "is-bad"
		markIcon = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>`
	}
	accountChip := ""
	if accountEmail != "" {
		accountChip = `<div class="account"><span class="account-label">Mailbox</span><strong>` + html.EscapeString(accountEmail) + `</strong></div>`
	}
	bodyHTML := html.EscapeString(body)
	// A provider error can be a long diagnostic; keep it readable and selectable.
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title><style>
:root{color-scheme:light dark;--bg:#f3f6fa;--card:#fff;--ink:#101828;--copy:#526174;--muted:#7b8798;--line:#e1e7ef;--accent:#0b6bcb;--accent-hover:#0959ab;--good:#067647;--good-bg:#ecfdf3;--good-line:#abefc6;--bad:#b42318;--bad-bg:#fef3f2;--bad-line:#fecdca;--chip:#f6f8fb}
@media (prefers-color-scheme:dark){:root{--bg:#0a0f16;--card:#111923;--ink:#eef4fb;--copy:#b7c4d3;--muted:#92a3b6;--line:#223142;--accent:#4d9df0;--accent-hover:#70b2f5;--good:#75e0a7;--good-bg:#0b2a1c;--good-line:#1a4d35;--bad:#fda29b;--bad-bg:#2b1212;--bad-line:#5a2422;--chip:#151f2b}}
*{box-sizing:border-box}html,body{min-height:100%%}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--bg);background:radial-gradient(120%% 90%% at 50%% 0%%,color-mix(in srgb,var(--accent) 7%%,var(--bg)),var(--bg) 60%%);color:var(--ink);font:15px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
.card{width:min(480px,100%%);background:var(--card);border:1px solid var(--line);border-radius:20px;padding:34px 32px 28px;box-shadow:0 24px 70px rgba(9,16,32,.12);text-align:center}
.mark{width:58px;height:58px;margin:0 auto 18px;border-radius:50%%;display:grid;place-items:center;border:1px solid var(--line);background:var(--chip);color:var(--muted)}
.mark svg{width:26px;height:26px}.mark.is-good{background:var(--good-bg);border-color:var(--good-line);color:var(--good)}.mark.is-bad{background:var(--bad-bg);border-color:var(--bad-line);color:var(--bad)}
.eyebrow{font-size:10.5px;font-weight:700;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);margin:0 0 8px}
h1{font-size:22px;line-height:1.25;letter-spacing:-.025em;margin:0 0 10px;color:var(--ink)}
.copy{margin:0 auto 20px;max-width:38ch;color:var(--copy);font-size:13.5px;overflow-wrap:anywhere}
.account{display:flex;flex-direction:column;gap:2px;margin:0 0 22px;padding:11px 14px;border:1px solid var(--line);border-radius:10px;background:var(--chip);text-align:left}
.account-label{font-size:9.5px;font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:var(--muted)}
.account strong{font-size:13.5px;color:var(--ink);overflow-wrap:anywhere}
.btn{display:inline-flex;align-items:center;justify-content:center;width:100%%;height:40px;border-radius:9px;border:1px solid transparent;background:var(--accent);color:#fff;font:600 13px/1 ui-sans-serif,system-ui,sans-serif;cursor:pointer;transition:background .15s ease}
.btn:hover{background:var(--accent-hover)}.btn:focus-visible{outline:3px solid color-mix(in srgb,var(--accent) 35%%,transparent);outline-offset:2px}
.btn.secondary{margin-top:8px;background:transparent;border-color:var(--line);color:var(--copy)}.btn.secondary:hover{background:var(--chip);color:var(--ink)}
.hint{margin:14px 0 0;color:var(--muted);font-size:11px;line-height:1.5}
</style></head>
<body><main class="card">
  <div class="mark %s">%s</div>
  <div class="eyebrow">%s</div>
  <h1>%s</h1>
  <p class="copy">%s</p>
  %s
  <button type="button" class="btn" id="closeBtn">Close window</button>
  <button type="button" class="btn secondary" id="focusBtn" hidden>Return to Alt8Mail</button>
  <p class="hint" id="resultHint">Alt8Mail is already open in the main window. You can close this window.</p>
</main>
<script>
(function () {
  var status = %s, message = %s, accountId = %s;
  var opener = null;
  try { opener = window.opener; } catch (e) {}
  if (opener) {
    try { opener.postMessage({type:"a8m-mail-oauth", status:status, message:message, accountId:accountId}, %s || "*"); } catch (e) {}
    try { opener.focus(); } catch (e) {}
    var focusBtn = document.getElementById("focusBtn");
    if (focusBtn) {
      focusBtn.hidden = false;
      focusBtn.addEventListener("click", function () {
        try { opener.focus(); } catch (e) {}
        try { window.close(); } catch (e) {}
      });
    }
  } else {
    var hint = document.getElementById("resultHint");
    if (hint) hint.textContent = "Return to the Alt8Mail tab; it has already been updated.";
  }
  document.getElementById("closeBtn").addEventListener("click", function () {
    try { window.close(); } catch (e) {}
  });
})();
</script></body></html>`, html.EscapeString(title), markClass, markIcon, html.EscapeString(eyebrow), html.EscapeString(heading), bodyHTML, accountChip, statusJSON, messageJSON, accountJSON, originJSON)
}
