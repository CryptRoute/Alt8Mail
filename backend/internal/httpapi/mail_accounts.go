package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	mailAccountKindNative       = "native"
	mailAccountKindExternal     = "external"
	mailAccountProviderAlt8Mail = "alt8mail"
	mailAccountScopeAll         = "all"
	mailAccountHeader           = "X-A8M-Account"
)

var errMailAccountNotFound = errors.New("mail account not found")

// mailAccountRecord is the non-secret shape of a mailbox source. Credentials
// live in the sealed columns and are never selected by this model.
type mailAccountRecord struct {
	ID          string
	TenantID    string
	UserID      string
	Kind        string
	Provider    string
	Email       string
	DisplayName string
	Color       string
	Status      string
	AuthType    string
	IMAPHost    string
	IMAPPort    int
	IMAPTLS     string
	SMTPHost    string
	SMTPPort    int
	SMTPTLS     string
	Username    string
	Settings    json.RawMessage
	LastSyncAt  *time.Time
	LastError   string
	UnreadCount int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type mailAccountScanner interface {
	Scan(dest ...any) error
}

const mailAccountColumns = `
	id, tenant_id::text, user_id::text, kind, provider, email::text, display_name, color,
	status, auth_type, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls,
	username, settings, last_sync_at, last_error, created_at, updated_at
`

func scanMailAccount(row mailAccountScanner) (mailAccountRecord, error) {
	var account mailAccountRecord
	var settings []byte
	err := row.Scan(
		&account.ID, &account.TenantID, &account.UserID, &account.Kind, &account.Provider,
		&account.Email, &account.DisplayName, &account.Color, &account.Status, &account.AuthType,
		&account.IMAPHost, &account.IMAPPort, &account.IMAPTLS, &account.SMTPHost, &account.SMTPPort,
		&account.SMTPTLS, &account.Username, &settings, &account.LastSyncAt, &account.LastError,
		&account.CreatedAt, &account.UpdatedAt,
	)
	if err != nil {
		return mailAccountRecord{}, err
	}
	if len(settings) > 0 {
		account.Settings = json.RawMessage(settings)
	}
	return account, nil
}

func (s *Server) mailAccountsForUser(ctx context.Context, userID string) ([]mailAccountRecord, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT `+mailAccountColumns+`
		FROM public.mail_accounts
		WHERE user_id = $1
		ORDER BY CASE WHEN kind = 'native' THEN 0 ELSE 1 END, lower(email::text), created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []mailAccountRecord{}
	for rows.Next() {
		account, scanErr := scanMailAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (s *Server) nativeMailAccountForUser(ctx context.Context, userID string) (mailAccountRecord, error) {
	account, err := scanMailAccount(s.store.DB.QueryRow(ctx, `
		SELECT `+mailAccountColumns+`
		FROM public.mail_accounts
		WHERE user_id = $1 AND kind = 'native'
		LIMIT 1
	`, userID))
	if err != nil {
		return mailAccountRecord{}, err
	}
	return account, nil
}

func (s *Server) mailAccountByID(ctx context.Context, userID, accountID string) (mailAccountRecord, error) {
	return scanMailAccount(s.store.DB.QueryRow(ctx, `
		SELECT `+mailAccountColumns+`
		FROM public.mail_accounts
		WHERE id = $1 AND user_id = $2
		LIMIT 1
	`, accountID, userID))
}

func (s *Server) mailAccountSealedSecret(ctx context.Context, userID, accountID string) (string, error) {
	var sealed string
	err := s.store.DB.QueryRow(ctx, `
		SELECT secret_sealed FROM public.mail_accounts
		WHERE id = $1 AND user_id = $2 AND kind = 'external'
	`, accountID, userID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errMailAccountNotFound
	}
	return sealed, err
}

func (s *Server) sealMailAccountSecret(value string) (string, error) {
	return s.sealWorkspaceToken(value)
}

func (s *Server) openMailAccountSecret(value string) (string, error) {
	return s.openWorkspaceToken(value)
}

func normalizeMailAccountScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return mailAccountKindNative
	}
	if strings.EqualFold(value, "all") {
		return mailAccountScopeAll
	}
	if strings.EqualFold(value, "native") {
		return mailAccountKindNative
	}
	return value
}

// resolveMailAccount maps an empty/native scope to the user's native mailbox
// and verifies ownership for every other account id. It never reveals whether
// an account belongs to another user.
func (s *Server) resolveMailAccount(ctx context.Context, userID, accountID string) (mailAccountRecord, error) {
	scope := normalizeMailAccountScope(accountID)
	if scope == mailAccountScopeAll {
		return mailAccountRecord{}, errMailAccountNotFound
	}
	if scope == mailAccountKindNative {
		account, err := s.nativeMailAccountForUser(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return mailAccountRecord{}, errMailAccountNotFound
		}
		return account, err
	}
	if len(scope) > 120 {
		return mailAccountRecord{}, errMailAccountNotFound
	}
	account, err := s.mailAccountByID(ctx, userID, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return mailAccountRecord{}, errMailAccountNotFound
	}
	return account, err
}

// mailAccountIDsForScope returns the account ids covered by a request scope.
// "all" is the only multi-account scope; every individual id is authorized
// against the signed-in user.
func (s *Server) mailAccountIDsForScope(ctx context.Context, userID, scope string) ([]string, error) {
	scope = normalizeMailAccountScope(scope)
	if scope == mailAccountScopeAll {
		accounts, err := s.mailAccountsForUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(accounts))
		for _, account := range accounts {
			ids = append(ids, account.ID)
		}
		return ids, nil
	}
	account, err := s.resolveMailAccount(ctx, userID, scope)
	if err != nil {
		return nil, err
	}
	return []string{account.ID}, nil
}

func requestedMailAccountID(r *http.Request) string {
	if value := strings.TrimSpace(r.URL.Query().Get("account")); value != "" {
		return value
	}
	return strings.TrimSpace(r.Header.Get(mailAccountHeader))
}

func mailAccountView(account mailAccountRecord) map[string]any {
	return map[string]any{
		"id":          account.ID,
		"kind":        account.Kind,
		"provider":    account.Provider,
		"email":       account.Email,
		"displayName": account.DisplayName,
		"color":       account.Color,
		"oauth":       account.AuthType == "oauth2",
		"status":      account.Status,
		"isNative":    account.Kind == mailAccountKindNative,
		"isUnified":   false,
		"imapHost":    account.IMAPHost,
		"imapPort":    account.IMAPPort,
		"imapTls":     account.IMAPTLS,
		"smtpHost":    account.SMTPHost,
		"smtpPort":    account.SMTPPort,
		"smtpTls":     account.SMTPTLS,
		"username":    account.Username,
		"lastSyncAt":  nullableTime(account.LastSyncAt),
		"lastError":   nullableString(account.LastError),
		"unreadCount": account.UnreadCount,
	}
}

func (s *Server) mailIdentityForMessage(ctx context.Context, user userRecord, message map[string]any) (email, name, accountID string) {
	accountID = strings.TrimSpace(stateString(message, "accountId"))
	if accountID != "" {
		if account, err := s.mailAccountByID(ctx, user.ID, accountID); err == nil && account.Kind == mailAccountKindExternal {
			return account.Email, firstNonEmpty(account.DisplayName, user.Name), account.ID
		}
	}
	return user.Email, user.Name, accountID
}

func unifiedMailAccountView(accounts []map[string]any) map[string]any {
	unread := 0
	for _, account := range accounts {
		if count, ok := account["unreadCount"].(int); ok {
			unread += count
		}
	}
	return map[string]any{
		"id": "all", "kind": "unified", "provider": "alt8mail", "email": "",
		"displayName": "All accounts", "color": "#34557a", "oauth": false,
		"status": "active", "isNative": false, "isUnified": true,
		"accountCount": len(accounts), "unreadCount": unread,
	}
}

// mailReadScope resolves a read request to one account id or to the unified
// scope. Connected accounts are silently omitted when the tenant policy turns
// them off, matching bootstrap's fallback to the native mailbox.
type mailReadScope struct {
	AccountID string
	Account   mailAccountRecord
	Kind      string
	Unified   bool
}

func (s *Server) resolveMailReadScope(ctx context.Context, sess *sessionRecord, requested string) (mailReadScope, error) {
	if sess.IsDelegated {
		requested = ""
	}
	user := sess.User
	scope := normalizeMailAccountScope(requested)
	native := func() (mailReadScope, error) {
		account, err := s.nativeMailAccountForUser(ctx, user.ID)
		if err != nil {
			return mailReadScope{}, err
		}
		return mailReadScope{AccountID: account.ID, Account: account, Kind: account.Kind}, nil
	}
	if scope == mailAccountScopeAll {
		if !s.externalMailAccountsAllowed(ctx, user) {
			return native()
		}
		return mailReadScope{AccountID: mailAccountScopeAll, Kind: "unified", Unified: true}, nil
	}
	account, err := s.resolveMailAccount(ctx, user.ID, scope)
	if err != nil {
		return native()
	}
	if account.Kind == mailAccountKindExternal && !s.externalMailAccountsAllowed(ctx, user) {
		return native()
	}
	return mailReadScope{AccountID: account.ID, Account: account, Kind: account.Kind}, nil
}

// mailAccountsForSession hides external personal accounts from delegated
// mailbox windows. Delegation exposes the owner's Alt8Mail mailbox, not the
// owner's connected personal accounts.
func (s *Server) mailAccountsForSession(ctx context.Context, sess *sessionRecord) ([]mailAccountRecord, error) {
	accounts, err := s.mailAccountsForUser(ctx, sess.User.ID)
	if err != nil {
		return nil, err
	}
	if !sess.IsDelegated {
		return accounts, nil
	}
	native := make([]mailAccountRecord, 0, 1)
	for _, account := range accounts {
		if account.Kind == mailAccountKindNative {
			native = append(native, account)
		}
	}
	return native, nil
}

func (s *Server) getMailAccounts(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	accounts, err := s.mailAccountsForSession(r.Context(), sess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_ACCOUNTS", "Could not load mail accounts")
		return
	}
	summaries, summaryErr := s.mailAccountUnreadSummaries(r.Context(), sess.User.ID)
	if summaryErr != nil {
		s.logger.Warn("mail unread summary failed", "user", sess.User.ID, "err", summaryErr)
		summaries = map[string]mailAccountUnreadSummary{}
	}
	views := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		account.UnreadCount = summaries[account.ID].UnreadCount
		views = append(views, mailAccountView(account))
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": views})
}
