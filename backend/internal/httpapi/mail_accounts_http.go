package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

const maxExternalMailAccounts = 5

var mailAccountColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type mailAccountCreateBody struct {
	Provider    string `json:"provider"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Color       string `json:"color"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	IMAPHost    string `json:"imapHost"`
	IMAPPort    int    `json:"imapPort"`
	IMAPTLS     string `json:"imapTls"`
	SMTPHost    string `json:"smtpHost"`
	SMTPPort    int    `json:"smtpPort"`
	SMTPTLS     string `json:"smtpTls"`
}

type mailAccountUpdateBody struct {
	DisplayName string `json:"displayName"`
	Color       string `json:"color"`
	Password    string `json:"password"`
	Username    string `json:"username"`
	IMAPHost    string `json:"imapHost"`
	IMAPPort    int    `json:"imapPort"`
	IMAPTLS     string `json:"imapTls"`
	SMTPHost    string `json:"smtpHost"`
	SMTPPort    int    `json:"smtpPort"`
	SMTPTLS     string `json:"smtpTls"`
}

func (s *Server) externalMailAccountsAllowed(ctx context.Context, user userRecord) bool {
	policy := s.tenantPolicyFor(ctx, user.TenantID)
	return policy.Mail.ExternalAccounts && policy.Mail.IMAPAccess
}

func (s *Server) requireExternalMailAccounts(w http.ResponseWriter, r *http.Request, user userRecord) bool {
	if s.externalMailAccountsAllowed(r.Context(), user) {
		return true
	}
	writeError(w, http.StatusForbidden, "ACCOUNT_POLICY", "Connected mail accounts are disabled for this organization")
	return false
}

func normalizeAccountProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "gmail":
		return "gmail"
	case "microsoft", "outlook", "microsoft365", "office365":
		return "microsoft"
	case "imap", "":
		return "imap"
	default:
		return ""
	}
}

func providerMailDefaults(provider string) (imapHost string, imapPort int, smtpHost string, smtpPort int) {
	switch provider {
	case "gmail":
		return "imap.gmail.com", 993, "smtp.gmail.com", 587
	case "microsoft":
		return "outlook.office365.com", 993, "smtp.office365.com", 587
	default:
		return "", 993, "", 587
	}
}

func defaultMailTLS(port int, imap bool) string {
	if imap {
		if port == 143 {
			return "starttls"
		}
		return "tls"
	}
	if port == 465 {
		return "tls"
	}
	return "starttls"
}

func normalizeAccountEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address == "" {
		return "", fmt.Errorf("enter a valid email address")
	}
	if !strings.EqualFold(parsed.Address, value) {
		return "", fmt.Errorf("enter the account email address without a display name")
	}
	return strings.ToLower(parsed.Address), nil
}

func (s *Server) externalAccountRecordFromBody(ctx context.Context, user userRecord, body mailAccountCreateBody) (mailAccountRecord, string, error) {
	provider := normalizeAccountProvider(body.Provider)
	if provider == "" {
		return mailAccountRecord{}, "", fmt.Errorf("choose a supported mail provider")
	}
	email, err := normalizeAccountEmail(body.Email)
	if err != nil {
		return mailAccountRecord{}, "", err
	}
	password := body.Password
	if strings.TrimSpace(password) == "" {
		return mailAccountRecord{}, "", fmt.Errorf("enter the account password or app password")
	}
	if len(password) > 1024 {
		return mailAccountRecord{}, "", fmt.Errorf("the account password is too long")
	}
	imapHostDefault, imapPortDefault, smtpHostDefault, smtpPortDefault := providerMailDefaults(provider)
	imapHost := strings.TrimSpace(body.IMAPHost)
	if imapHost == "" {
		imapHost = imapHostDefault
	}
	smtpHost := strings.TrimSpace(body.SMTPHost)
	if smtpHost == "" {
		smtpHost = smtpHostDefault
	}
	if imapHost == "" || smtpHost == "" {
		return mailAccountRecord{}, "", fmt.Errorf("enter the IMAP and SMTP server hostnames")
	}
	imapPort := body.IMAPPort
	if imapPort == 0 {
		imapPort = imapPortDefault
	}
	smtpPort := body.SMTPPort
	if smtpPort == 0 {
		smtpPort = smtpPortDefault
	}
	imapTLS, err := validateMailTLSMode(body.IMAPTLS, imapPort == 993)
	if err != nil {
		return mailAccountRecord{}, "", err
	}
	smtpTLS, err := validateMailTLSMode(body.SMTPTLS, smtpPort == 465)
	if err != nil {
		return mailAccountRecord{}, "", err
	}
	if imapTLS == "starttls" && imapPort == 993 {
		return mailAccountRecord{}, "", fmt.Errorf("port 993 requires implicit TLS")
	}
	if smtpTLS == "tls" && smtpPort == 587 {
		return mailAccountRecord{}, "", fmt.Errorf("port 587 requires STARTTLS")
	}
	if smtpTLS == "starttls" && smtpPort == 465 {
		return mailAccountRecord{}, "", fmt.Errorf("port 465 requires implicit TLS")
	}
	imapHost, err = validateMailEndpoint(ctx, imapHost, imapPort)
	if err != nil {
		return mailAccountRecord{}, "", fmt.Errorf("IMAP: %w", err)
	}
	smtpHost, err = validateMailEndpoint(ctx, smtpHost, smtpPort)
	if err != nil {
		return mailAccountRecord{}, "", fmt.Errorf("SMTP: %w", err)
	}
	displayName := truncate(strings.TrimSpace(body.DisplayName), 120)
	if displayName == "" {
		if index := strings.Index(email, "@"); index > 0 {
			displayName = email[:index]
		} else {
			displayName = email
		}
	}
	color := strings.TrimSpace(body.Color)
	if color == "" {
		color = "#34557a"
	}
	if !mailAccountColorPattern.MatchString(color) {
		return mailAccountRecord{}, "", fmt.Errorf("choose a valid account color")
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		username = email
	}
	return mailAccountRecord{
		TenantID: user.TenantID, UserID: user.ID, Kind: mailAccountKindExternal,
		Provider: provider, Email: email, DisplayName: displayName, Color: color,
		Status: "active", AuthType: "password",
		IMAPHost: imapHost, IMAPPort: imapPort, IMAPTLS: imapTLS,
		SMTPHost: smtpHost, SMTPPort: smtpPort, SMTPTLS: smtpTLS,
		Username: username,
	}, password, nil
}

func (s *Server) mailAccountRateAllowed(sess *sessionRecord) bool {
	if s.accountLimit == nil {
		return true
	}
	if !s.accountLimit.Allow(sess.User.ID) {
		return false
	}
	s.accountLimit.Fail(sess.User.ID)
	return true
}

func (s *Server) mailAccountRateLimited(w http.ResponseWriter, sess *sessionRecord) bool {
	if s.mailAccountRateAllowed(sess) {
		return false
	}
	writeError(w, http.StatusTooManyRequests, "RATE_LIMIT", "Too many mail account changes. Try again in a few minutes.")
	return true
}

func (s *Server) createMailAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if s.mailAccountRateLimited(w, sess) {
		return
	}
	if !s.requireExternalMailAccounts(w, r, sess.User) {
		return
	}
	var body mailAccountCreateBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	account, password, err := s.externalAccountRecordFromBody(r.Context(), sess.User, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", err.Error())
		return
	}
	var existing int
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FROM public.mail_accounts
		WHERE user_id=$1 AND kind='external'
	`, sess.User.ID).Scan(&existing)
	if existing >= maxExternalMailAccounts {
		writeError(w, http.StatusConflict, "ACCOUNT_LIMIT", fmt.Sprintf("You can connect up to %d external accounts", maxExternalMailAccounts))
		return
	}
	var duplicate int
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FROM public.mail_accounts
		WHERE user_id=$1 AND provider=$2 AND email=$3
	`, sess.User.ID, account.Provider, account.Email).Scan(&duplicate)
	if duplicate > 0 {
		writeError(w, http.StatusConflict, "ACCOUNT_EXISTS", "This mail account is already connected")
		return
	}
	if err := s.testExternalMailConnectionWithPassword(r.Context(), account, password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ACCOUNT_CONNECT", "Could not connect to the mail server: "+err.Error())
		return
	}
	sealed, err := s.sealMailAccountSecret(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ACCOUNT_SECRET", "Could not protect the account credential")
		return
	}
	account.ID = newID("macct")
	if _, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_accounts (
			id, tenant_id, user_id, kind, provider, email, display_name, color, status, auth_type,
			secret_sealed, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, username
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
	`, account.ID, account.TenantID, account.UserID, account.Kind, account.Provider, account.Email,
		account.DisplayName, account.Color, account.Status, account.AuthType, sealed,
		account.IMAPHost, account.IMAPPort, account.IMAPTLS, account.SMTPHost, account.SMTPPort,
		account.SMTPTLS, account.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "ACCOUNT_CREATE", "Could not save the mail account")
		return
	}
	s.audit(r, sess.User, "account.connected", account.Email, "Connected external mail account "+account.Email)
	s.scheduleMailAccountSync(account.ID, sess.User.ID)
	created, err := s.mailAccountByID(r.Context(), sess.User.ID, account.ID)
	if err != nil {
		created = account
	}
	writeJSON(w, http.StatusCreated, map[string]any{"account": mailAccountView(created), "syncing": true})
}

func (s *Server) testMailAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if s.mailAccountRateLimited(w, sess) {
		return
	}
	if !s.requireExternalMailAccounts(w, r, sess.User) {
		return
	}
	var body mailAccountCreateBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	account, password, err := s.externalAccountRecordFromBody(r.Context(), sess.User, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", err.Error())
		return
	}
	if err := s.testExternalMailConnectionWithPassword(r.Context(), account, password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ACCOUNT_CONNECT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) updateMailAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if s.mailAccountRateLimited(w, sess) {
		return
	}
	accountID := strings.TrimSpace(r.PathValue("id"))
	var body mailAccountUpdateBody
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	account, err := s.resolveMailAccount(r.Context(), sess.User.ID, accountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Mail account not found")
		return
	}
	if account.Kind != mailAccountKindExternal {
		writeError(w, http.StatusBadRequest, "ACCOUNT_NATIVE", "The native mailbox cannot be changed here")
		return
	}
	displayName := account.DisplayName
	if strings.TrimSpace(body.DisplayName) != "" {
		displayName = truncate(strings.TrimSpace(body.DisplayName), 120)
	}
	color := account.Color
	if strings.TrimSpace(body.Color) != "" {
		if !mailAccountColorPattern.MatchString(strings.TrimSpace(body.Color)) {
			writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "Choose a valid account color")
			return
		}
		color = strings.TrimSpace(body.Color)
	}
	candidate := account
	if username := strings.TrimSpace(body.Username); username != "" {
		candidate.Username = username
	}
	if host := strings.TrimSpace(body.IMAPHost); host != "" {
		candidate.IMAPHost = host
	}
	if body.IMAPPort > 0 {
		candidate.IMAPPort = body.IMAPPort
	}
	if tls := strings.TrimSpace(body.IMAPTLS); tls != "" {
		candidate.IMAPTLS = tls
	}
	if host := strings.TrimSpace(body.SMTPHost); host != "" {
		candidate.SMTPHost = host
	}
	if body.SMTPPort > 0 {
		candidate.SMTPPort = body.SMTPPort
	}
	if tls := strings.TrimSpace(body.SMTPTLS); tls != "" {
		candidate.SMTPTLS = tls
	}
	if candidate.Username == "" {
		candidate.Username = candidate.Email
	}
	imapTLS, err := validateMailTLSMode(candidate.IMAPTLS, candidate.IMAPPort == 993)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "IMAP: "+err.Error())
		return
	}
	smtpTLS, err := validateMailTLSMode(candidate.SMTPTLS, candidate.SMTPPort == 465)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "SMTP: "+err.Error())
		return
	}
	if imapTLS == "starttls" && candidate.IMAPPort == 993 {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "Port 993 requires implicit TLS")
		return
	}
	if smtpTLS == "tls" && candidate.SMTPPort == 587 {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "Port 587 requires STARTTLS")
		return
	}
	if smtpTLS == "starttls" && candidate.SMTPPort == 465 {
		writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "Port 465 requires implicit TLS")
		return
	}
	candidate.IMAPTLS, candidate.SMTPTLS = imapTLS, smtpTLS
	endpointChanged := candidate.Username != account.Username || candidate.IMAPHost != account.IMAPHost ||
		candidate.IMAPPort != account.IMAPPort || candidate.IMAPTLS != account.IMAPTLS ||
		candidate.SMTPHost != account.SMTPHost || candidate.SMTPPort != account.SMTPPort ||
		candidate.SMTPTLS != account.SMTPTLS
	passwordChanged := strings.TrimSpace(body.Password) != ""
	if endpointChanged {
		if candidate.IMAPHost, err = validateMailEndpoint(r.Context(), candidate.IMAPHost, candidate.IMAPPort); err != nil {
			writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "IMAP: "+err.Error())
			return
		}
		if candidate.SMTPHost, err = validateMailEndpoint(r.Context(), candidate.SMTPHost, candidate.SMTPPort); err != nil {
			writeError(w, http.StatusBadRequest, "ACCOUNT_INVALID", "SMTP: "+err.Error())
			return
		}
	}
	var sealed any
	authType := account.AuthType
	status := account.Status
	lastError := account.LastError
	clearOAuth := false
	if passwordChanged {
		if !s.requireExternalMailAccounts(w, r, sess.User) {
			return
		}
		auth := externalMailAuth{Username: candidate.Username, Password: body.Password}
		if err := s.testExternalMailConnectionWithAuth(r.Context(), candidate, auth); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "ACCOUNT_CONNECT", "Could not connect to the mail server: "+err.Error())
			return
		}
		value, sealErr := s.sealMailAccountSecret(body.Password)
		if sealErr != nil {
			writeError(w, http.StatusInternalServerError, "ACCOUNT_SECRET", "Could not protect the account credential")
			return
		}
		sealed = value
		authType = "password"
		status = "active"
		lastError = ""
		clearOAuth = true
	} else if endpointChanged {
		auth, authErr := s.accountMailAuth(r.Context(), account)
		if authErr != nil {
			writeError(w, http.StatusUnprocessableEntity, "ACCOUNT_CONNECT", "Reconnect the account before changing its servers")
			return
		}
		auth.Username = candidate.Username
		if err := s.testExternalMailConnectionWithAuth(r.Context(), candidate, auth); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "ACCOUNT_CONNECT", "Could not connect to the mail server: "+err.Error())
			return
		}
		status = "active"
		lastError = ""
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.mail_accounts SET
			display_name=$2, color=$3,
			secret_sealed=COALESCE($4, secret_sealed),
			auth_type=$5,
			oauth_access_sealed=CASE WHEN $6 THEN '' ELSE oauth_access_sealed END,
			oauth_refresh_sealed=CASE WHEN $6 THEN '' ELSE oauth_refresh_sealed END,
			oauth_expires_at=CASE WHEN $6 THEN NULL ELSE oauth_expires_at END,
			username=$7, imap_host=$8, imap_port=$9, imap_tls=$10,
			smtp_host=$11, smtp_port=$12, smtp_tls=$13,
			status=$14, last_error=$15, updated_at=now()
		WHERE id=$1 AND user_id=$16 AND kind='external'
	`, account.ID, displayName, color, sealed, authType, clearOAuth,
		candidate.Username, candidate.IMAPHost, candidate.IMAPPort, candidate.IMAPTLS,
		candidate.SMTPHost, candidate.SMTPPort, candidate.SMTPTLS,
		status, lastError, sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "ACCOUNT_UPDATE", "Could not update the mail account")
		return
	}
	s.audit(r, sess.User, "account.updated", account.Email, "Updated connected mail account "+account.Email)
	if passwordChanged || endpointChanged {
		s.scheduleMailAccountSync(account.ID, sess.User.ID)
	}
	updated, err := s.mailAccountByID(r.Context(), sess.User.ID, account.ID)
	if err != nil {
		updated = account
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": mailAccountView(updated)})
}

func (s *Server) deleteMailAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if s.mailAccountRateLimited(w, sess) {
		return
	}
	accountID := strings.TrimSpace(r.PathValue("id"))
	account, err := s.resolveMailAccount(r.Context(), sess.User.ID, accountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Mail account not found")
		return
	}
	if account.Kind != mailAccountKindExternal {
		writeError(w, http.StatusBadRequest, "ACCOUNT_NATIVE", "The native mailbox cannot be removed")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ACCOUNT_DELETE", "Could not remove the mail account")
		return
	}
	defer tx.Rollback(r.Context())
	attachmentIDs := []string{}
	if rows, queryErr := tx.Query(r.Context(), `
		SELECT DISTINCT ma.attachment_id
		FROM public.mail_message_attachments ma
		JOIN public.mail_messages m ON m.id = ma.message_id
		WHERE m.account_id=$1 AND m.user_id=$2
	`, account.ID, sess.User.ID); queryErr == nil {
		for rows.Next() {
			var attachmentID string
			if rows.Scan(&attachmentID) == nil {
				attachmentIDs = append(attachmentIDs, attachmentID)
			}
		}
		rows.Close()
	}
	for _, statement := range []string{
		`DELETE FROM public.mail_messages WHERE account_id=$1 AND user_id=$2`,
		`DELETE FROM public.outbox WHERE account_id=$1 AND user_id=$2`,
		`DELETE FROM public.mail_identities WHERE account_id=$1 AND user_id=$2`,
	} {
		if _, err := tx.Exec(r.Context(), statement, account.ID, sess.User.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "ACCOUNT_DELETE", "Could not remove the cached mail data")
			return
		}
	}
	if len(attachmentIDs) > 0 {
		_, _ = tx.Exec(r.Context(), `
			DELETE FROM public.attachments a
			WHERE a.user_id=$1 AND a.id = ANY($2)
			  AND NOT EXISTS (SELECT 1 FROM public.mail_message_attachments ma WHERE ma.attachment_id=a.id)
		`, sess.User.ID, attachmentIDs)
	}
	result, err := tx.Exec(r.Context(), `
		DELETE FROM public.mail_accounts WHERE id=$1 AND user_id=$2 AND kind='external'
	`, account.ID, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusInternalServerError, "ACCOUNT_DELETE", "Could not remove the mail account")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ACCOUNT_DELETE", "Could not remove the mail account")
		return
	}
	s.audit(r, sess.User, "account.removed", account.Email, "Removed connected mail account "+account.Email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) syncMailAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if s.mailAccountRateLimited(w, sess) {
		return
	}
	account, err := s.resolveMailAccount(r.Context(), sess.User.ID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Mail account not found")
		return
	}
	if account.Kind == mailAccountKindExternal && !s.requireExternalMailAccounts(w, r, sess.User) {
		return
	}
	s.scheduleMailAccountSync(account.ID, sess.User.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "syncing": true})
}

func (s *Server) scheduleMailAccountSync(accountID, userID string) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(userID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		account, err := s.mailAccountByID(ctx, userID, accountID)
		if err != nil {
			return
		}
		user, err := s.userByID(ctx, userID)
		if err != nil {
			return
		}
		if account.Kind == mailAccountKindNative {
			_, _ = s.syncUserMailbox(ctx, user)
			return
		}
		_ = s.syncExternalAccount(ctx, account, user)
	}()
}
