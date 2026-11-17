package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNormalizeMailAccountScope(t *testing.T) {
	cases := map[string]string{
		"":             mailAccountKindNative,
		"   ":          mailAccountKindNative,
		"native":       mailAccountKindNative,
		" NATIVE ":     mailAccountKindNative,
		"all":          mailAccountScopeAll,
		" ALL ":        mailAccountScopeAll,
		"macct_1234":   "macct_1234",
		"macct_AbCdEf": "macct_AbCdEf",
	}
	for input, want := range cases {
		if got := normalizeMailAccountScope(input); got != want {
			t.Errorf("normalizeMailAccountScope(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMailAccountRateLimiter(t *testing.T) {
	server := &Server{accountLimit: newLoginLimiter(2, time.Minute)}
	sess := &sessionRecord{User: userRecord{ID: "user-1"}}
	for attempt := 1; attempt <= 2; attempt++ {
		recorder := httptest.NewRecorder()
		if server.mailAccountRateLimited(recorder, sess) {
			t.Fatalf("attempt %d was rate limited too early", attempt)
		}
	}
	recorder := httptest.NewRecorder()
	if !server.mailAccountRateLimited(recorder, sess) {
		t.Fatal("third attempt was not rate limited")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
}

func TestMailAccountViewOmitsCredentials(t *testing.T) {
	view := mailAccountView(mailAccountRecord{
		ID: "macct_test", Kind: mailAccountKindNative, Provider: mailAccountProviderAlt8Mail,
		Email: "owner@example.test", DisplayName: "Owner", Color: "#34557a", Status: "active",
		UnreadCount: 4,
	})
	for _, forbidden := range []string{
		"secret", "secretSealed", "secret_sealed", "oauthRefresh", "oauthAccess",
		"oauth_refresh_sealed", "oauth_access_sealed", "password", "authType",
	} {
		if _, exists := view[forbidden]; exists {
			t.Fatalf("mail account view exposes %q", forbidden)
		}
	}
	if view["isNative"] != true || view["email"] != "owner@example.test" {
		t.Fatalf("unexpected account view: %#v", view)
	}
	if view["unreadCount"] != 4 {
		t.Fatalf("unreadCount = %#v, want 4", view["unreadCount"])
	}
	unified := unifiedMailAccountView([]map[string]any{
		view,
		mailAccountView(mailAccountRecord{ID: "macct_second", Kind: mailAccountKindExternal, UnreadCount: 3}),
	})
	if unified["unreadCount"] != 7 {
		t.Fatalf("unified unreadCount = %#v, want 7", unified["unreadCount"])
	}
}

func TestRequestedMailAccountID(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/messages?account=macct_query", nil)
	request.Header.Set(mailAccountHeader, "macct_header")
	if got := requestedMailAccountID(request); got != "macct_query" {
		t.Fatalf("query account = %q, want macct_query", got)
	}
	request = httptest.NewRequest("GET", "/api/messages", nil)
	request.Header.Set(mailAccountHeader, "  macct_header  ")
	if got := requestedMailAccountID(request); got != "macct_header" {
		t.Fatalf("header account = %q, want macct_header", got)
	}
	request = httptest.NewRequest("GET", "/api/messages", nil)
	if got := requestedMailAccountID(request); got != "" {
		t.Fatalf("default account = %q, want empty", got)
	}
}

// Run with:
//
//	A8M_MAIL_ACCOUNTS_TEST_DSN='postgres://...' go test ./internal/httpapi -run MailAccountsPhase0 -count=1
//
// The test owns its tenant and users and deletes the tenant afterwards.
func TestMailAccountPersistenceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_MAIL_ACCOUNTS_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_MAIL_ACCOUNTS_TEST_DSN to run the mail account persistence test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	slug := fmt.Sprintf("mail-persist-test-%d", time.Now().UnixNano())
	var tenantID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text
	`, slug, "Mail persistence test").Scan(&tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id=$1`, tenantID)
	}()
	var userID, email string
	email = "persist-" + slug + "@example.test"
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role)
		VALUES ($1, $2, 'Persist User', 'owner') RETURNING id::text, email::text
	`, tenantID, email).Scan(&userID, &email); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var nativeID string
	if err := pool.QueryRow(ctx, `SELECT id FROM mail_accounts WHERE user_id=$1 AND kind='native'`, userID).Scan(&nativeID); err != nil {
		t.Fatalf("native account: %v", err)
	}
	externalID := "macct_ext_" + slug
	externalEmail := "external-" + slug + "@example.test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_accounts (
			id, tenant_id, user_id, kind, provider, email, display_name, status, auth_type,
			imap_host, smtp_host, username
		) VALUES ($1,$2,$3,'external','imap',$4,'External','active','password',
			'imap.example.test','smtp.example.test',$5)
	`, externalID, tenantID, userID, externalEmail, externalEmail); err != nil {
		t.Fatalf("external account: %v", err)
	}
	server := &Server{store: &store.Store{DB: pool}}
	user := userRecord{ID: userID, TenantID: tenantID, Email: email, Name: "Persist User"}
	now := time.Now().UTC().Format(time.RFC3339)
	message := map[string]any{
		"id": "msg-persist-" + slug, "accountId": externalID,
		"folder": "inbox", "imapFolder": "INBOX", "imapUid": 1, "imapUidValidity": 1,
		"threadId": "thread-persist-" + slug, "subject": "External message",
		"from": map[string]any{"name": "Sender", "email": "sender@example.test"},
		"to":   []any{}, "cc": []any{}, "bcc": []any{},
		"date": now, "bodyText": "body", "bodyHtml": "<p>body</p>",
		"unread": true, "sizeBytes": 4, "messageId": "<persist-" + slug + "@example.test>",
		"external": true, "classification": "External",
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin message transaction: %v", err)
	}
	if err := server.persistMailMessageTx(ctx, tx, user, message); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("persist external message: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit external message: %v", err)
	}
	var storedAccount, storedMailbox string
	if err := pool.QueryRow(ctx, `
		SELECT account_id, COALESCE(mailbox_id::text, '') FROM mail_messages WHERE id=$1
	`, message["id"]).Scan(&storedAccount, &storedMailbox); err != nil {
		t.Fatalf("read external message: %v", err)
	}
	if storedAccount != externalID || storedMailbox != "" {
		t.Fatalf("stored account=%q mailbox=%q", storedAccount, storedMailbox)
	}
	// A message that already exists locally (for example a Sent copy written at
	// delivery time, or a row moved by a user action) keeps its identity when
	// the next sync fetches the same message from the provider.
	fetched, _ := server.externalFetchedMessage(ctx, mailAccountRecord{
		ID: externalID, UserID: userID, TenantID: tenantID,
		Email: externalEmail, DisplayName: "External",
	}, user, fetchedMail{
		UID: 1, UIDValidity: 1, Folder: "inbox", MessageID: "<persist-" + slug + "@example.test>",
		Subject: "External message", From: []map[string]any{{"name": "Sender", "email": "sender@example.test"}},
	}, "inbox", "INBOX")
	if stateString(fetched, "id") != stateString(message, "id") ||
		stateString(fetched, "threadId") != stateString(message, "threadId") {
		t.Fatalf("sync did not reuse the existing row: id=%q thread=%q",
			stateString(fetched, "id"), stateString(fetched, "threadId"))
	}

	nativeMessage := map[string]any{
		"id": "msg-native-persist-" + slug, "folder": "inbox", "imapFolder": "INBOX",
		"imapUid": 2, "imapUidValidity": 1, "threadId": "thread-native-persist-" + slug,
		"subject": "Native message", "from": map[string]any{"name": "Owner", "email": email},
		"to": []any{}, "cc": []any{}, "bcc": []any{}, "date": now, "bodyText": "native",
		"unread": true, "sizeBytes": 6, "messageId": "<native-persist-" + slug + "@example.test>",
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin native message transaction: %v", err)
	}
	if err := server.persistMailMessageTx(ctx, tx, user, nativeMessage); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("persist native message: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit native message: %v", err)
	}
	var nativeStored string
	if err := pool.QueryRow(ctx, `SELECT account_id FROM mail_messages WHERE id=$1`, nativeMessage["id"]).Scan(&nativeStored); err != nil {
		t.Fatalf("read native message: %v", err)
	}
	if nativeStored != nativeID {
		t.Fatalf("native message account=%q, want %q", nativeStored, nativeID)
	}
	if count := server.countMailMessages(ctx, userID, "INBOX", 1); count != 1 {
		t.Fatalf("native INBOX count=%d, want 1 (external row must not leak into native sync)", count)
	}
	knownUIDs := server.loadFolderUIDs(ctx, userID, "INBOX", 1)
	if knownUIDs[1] {
		t.Fatal("external UID leaked into the native UID index")
	}
	if !knownUIDs[2] {
		t.Fatal("native UID is missing from the native UID index")
	}

	outbox := map[string]any{
		"id": "send-persist-" + slug, "accountId": externalID,
		"from":    map[string]any{"name": "External", "email": externalEmail},
		"to":      []any{map[string]any{"name": "Recipient", "email": "recipient@example.test"}},
		"subject": "External send", "bodyText": "hello", "sendAt": now, "queuedAt": now,
		"status": "queued",
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin outbox transaction: %v", err)
	}
	if err := server.persistOutboxItemTx(ctx, tx, user, outbox); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("persist external outbox: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit external outbox: %v", err)
	}
	var outboxAccount string
	if err := pool.QueryRow(ctx, `SELECT account_id FROM outbox WHERE id=$1`, outbox["id"]).Scan(&outboxAccount); err != nil {
		t.Fatalf("read external outbox: %v", err)
	}
	if outboxAccount != externalID {
		t.Fatalf("outbox account=%q, want %q", outboxAccount, externalID)
	}
}

func TestMailPhase2Integration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_MAIL_ACCOUNTS_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_MAIL_ACCOUNTS_TEST_DSN to run the mail Phase 2 integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	var stateTable string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.mail_oauth_states')::text`).Scan(&stateTable); err != nil || stateTable == "" {
		t.Fatalf("mail_oauth_states is missing; apply migration 0058 first: %v", err)
	}
	var syncColumn string
	if err := pool.QueryRow(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema='public' AND table_name='mail_folders' AND column_name='sync_enabled'
	`).Scan(&syncColumn); err != nil || syncColumn == "" {
		t.Fatalf("mail_folders.sync_enabled is missing: %v", err)
	}
	slug := fmt.Sprintf("mail-phase2-test-%d", time.Now().UnixNano())
	var tenantID string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ($1,$2) RETURNING id::text`,
		slug, "Mail Phase 2 test").Scan(&tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id=$1`, tenantID)
	}()
	var userID, email string
	email = "phase2-" + slug + "@example.test"
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role)
		VALUES ($1,$2,'Phase 2 User','owner') RETURNING id::text, email::text
	`, tenantID, email).Scan(&userID, &email); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var nativeID string
	if err := pool.QueryRow(ctx, `SELECT id FROM mail_accounts WHERE user_id=$1 AND kind='native'`, userID).Scan(&nativeID); err != nil {
		t.Fatalf("native account: %v", err)
	}
	customID := "mfld_custom_" + slug
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_folders (id, tenant_id, user_id, account_id, remote_name, display_name, role, sync_enabled)
		VALUES ($1,$2,$3,$4,'Labels/Project','Project','custom',true)
	`, customID, tenantID, userID, nativeID); err != nil {
		t.Fatalf("insert custom folder: %v", err)
	}
	server := &Server{store: &store.Store{DB: pool}, cfg: config.Config{UploadDir: t.TempDir()}}
	views, err := server.externalFolderViews(ctx, mailAccountRecord{ID: nativeID, TenantID: tenantID, UserID: userID})
	if err != nil {
		t.Fatalf("folder views: %v", err)
	}
	foundCustom := false
	for _, view := range views {
		if stateString(view, "id") == customID {
			foundCustom = true
			if stateBool(view, "syncEnabled") != true || stateBool(view, "system") != false {
				t.Fatalf("unexpected custom folder view: %#v", view)
			}
		}
	}
	if !foundCustom {
		t.Fatalf("custom folder missing from views: %#v", views)
	}
	externalID := "macct_phase2_" + slug
	externalEmail := "external-" + slug + "@example.test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_accounts (
			id, tenant_id, user_id, kind, provider, email, display_name, status, auth_type,
			imap_host, smtp_host, username
		) VALUES ($1,$2,$3,'external','imap',$4,'External','active','password',
			'imap.example.test','smtp.example.test',$5)
	`, externalID, tenantID, userID, externalEmail, externalEmail); err != nil {
		t.Fatalf("external account: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE tenants SET settings = jsonb_set(
			COALESCE(settings, '{}'::jsonb), '{mail}',
			COALESCE(settings->'mail', '{}'::jsonb) || '{"externalAccounts":true,"imapAccess":true}'::jsonb,
			true
		) WHERE id=$1
	`, tenantID); err != nil {
		t.Fatalf("enable external accounts: %v", err)
	}
	user := userRecord{ID: userID, TenantID: tenantID, Email: email}
	session := &sessionRecord{User: user}
	if !server.localMailIdentity(ctx, user, externalEmail) || server.localMailIdentity(ctx, user, "stranger@example.test") {
		t.Fatal("connected account identity was not recognized for calendar/AI routing")
	}
	identityEmail, _, identityAccount := server.mailIdentityForMessage(ctx, user, map[string]any{"accountId": externalID})
	if identityEmail != externalEmail || identityAccount != externalID {
		t.Fatalf("message identity = %q %q", identityEmail, identityAccount)
	}
	scope, err := server.resolveMailReadScope(ctx, session, mailAccountScopeAll)
	if err != nil {
		t.Fatalf("resolve all scope: %v", err)
	}
	if !scope.Unified || scope.AccountID != mailAccountScopeAll {
		t.Fatalf("unified scope = %#v", scope)
	}
	scope, err = server.resolveMailReadScope(ctx, session, externalID)
	if err != nil || scope.Unified || scope.AccountID != externalID {
		t.Fatalf("external scope = %#v, %v", scope, err)
	}
	oauthServer := &Server{
		store: server.store,
		cfg: config.Config{
			PublicURL: "https://mail.example.test", SessionSecret: "phase2-session-secret",
			GoogleMailClientID: "google-mail-id", GoogleMailClientSecret: "google-mail-secret",
		},
	}
	oauthTarget, err := oauthServer.beginMailOAuth(ctx, session, "gmail", "")
	if err != nil {
		t.Fatalf("begin Gmail OAuth: %v", err)
	}
	parsedTarget, err := url.Parse(oauthTarget)
	if err != nil {
		t.Fatalf("parse OAuth URL: %v", err)
	}
	if parsedTarget.Query().Get("redirect_uri") != "https://mail.example.test/api/mail/oauth/gmail/callback" ||
		parsedTarget.Query().Get("code_challenge_method") != "S256" ||
		!strings.Contains(parsedTarget.Query().Get("scope"), "mail.google.com") {
		t.Fatalf("unexpected OAuth URL: %s", oauthTarget)
	}
	var verifierSealed string
	if err := pool.QueryRow(ctx, `
		SELECT verifier_sealed FROM mail_oauth_states
		WHERE id=$1 AND user_id=$2 AND provider='gmail'
	`, parsedTarget.Query().Get("state"), userID).Scan(&verifierSealed); err != nil {
		t.Fatalf("OAuth state row: %v", err)
	}
	verifier, err := oauthServer.openMailAccountSecret(verifierSealed)
	if err != nil || verifier == "" {
		t.Fatalf("OAuth verifier did not decrypt: %v", err)
	}
	challenge := sha256.Sum256([]byte(verifier))
	if parsedTarget.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) {
		t.Fatal("OAuth PKCE challenge does not match the stored verifier")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	persistMessage := func(id, accountID, subject string) {
		t.Helper()
		message := map[string]any{
			"id": id, "accountId": accountID, "folder": "inbox", "imapFolder": "INBOX",
			"imapUid": 1, "imapUidValidity": 1, "threadId": "thread-" + id,
			"subject": subject, "from": map[string]any{"name": "Sender", "email": "sender@example.test"},
			"to": []any{map[string]any{"name": "Phase 2 User", "email": email}},
			"cc": []any{}, "bcc": []any{}, "date": now, "bodyText": "body",
			"unread": true, "sizeBytes": 4, "messageId": "<" + id + "@example.test>",
		}
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatalf("begin message: %v", beginErr)
		}
		if persistErr := server.persistMailMessageTx(ctx, tx, user, message); persistErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("persist message: %v", persistErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			t.Fatalf("commit message: %v", commitErr)
		}
	}
	persistMessage("msg-phase2-native-"+slug, nativeID, "Native")
	persistMessage("msg-phase2-external-"+slug, externalID, "External")
	// A Gmail label copy of the same message must not double-count in Pulse.
	labelCopy := map[string]any{
		"id": "msg-phase2-label-" + slug, "accountId": externalID,
		"folder": "mfld_label", "imapFolder": "Labels/Project",
		"imapUid": 2, "imapUidValidity": 1, "threadId": "thread-msg-phase2-external-" + slug,
		"subject": "External", "from": map[string]any{"name": "Sender", "email": "sender@example.test"},
		"to": []any{map[string]any{"name": "Phase 2 User", "email": email}},
		"cc": []any{}, "bcc": []any{}, "date": now, "bodyText": "body",
		"unread": true, "sizeBytes": 4, "messageId": "<msg-phase2-external-" + slug + "@example.test>",
	}
	labelTx, labelErr := pool.Begin(ctx)
	if labelErr != nil {
		t.Fatalf("begin label copy: %v", labelErr)
	}
	if persistErr := server.persistMailMessageTx(ctx, labelTx, user, labelCopy); persistErr != nil {
		_ = labelTx.Rollback(ctx)
		t.Fatalf("persist label copy: %v", persistErr)
	}
	if commitErr := labelTx.Commit(ctx); commitErr != nil {
		t.Fatalf("commit label copy: %v", commitErr)
	}
	taskID := "task-phase2-" + slug
	if _, err := pool.Exec(ctx, `
		INSERT INTO tasks (id, tenant_id, user_id, title, linked_thread_id, linked_subject, source)
		VALUES ($1,$2,$3,'Follow up','thread-msg-phase2-external-' || $4,'External','email')
	`, taskID, tenantID, userID, slug); err != nil {
		t.Fatalf("insert linked task: %v", err)
	}
	tasks, err := server.loadTasks(ctx, userID)
	if err != nil {
		t.Fatalf("load tasks: %v", err)
	}
	taskAccount := ""
	for _, task := range tasks {
		if stateString(task, "id") == taskID {
			taskAccount = stateString(task, "linkedAccountId")
		}
	}
	if taskAccount != externalID {
		t.Fatalf("linked task account = %q, want %q", taskAccount, externalID)
	}
	pushMessages, err := server.webPushMessagesSince(ctx, time.Now().Add(-time.Hour), "")
	if err != nil {
		t.Fatalf("web push message query: %v", err)
	}
	foundPushAccount := false
	for _, message := range pushMessages {
		if message.ID == "msg-phase2-external-"+slug {
			foundPushAccount = message.AccountID == externalID && message.AccountEmail == externalEmail && message.AccountLabel == "External"
		}
	}
	if !foundPushAccount {
		t.Fatalf("connected message missing account metadata from push query: %#v", pushMessages)
	}
	inviteUID := "invite-" + slug + "@example.test"
	inviteICS := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
		"UID:" + inviteUID + "\r\nDTSTAMP:20261101T120000Z\r\nDTSTART:20261110T090000Z\r\nDTEND:20261110T100000Z\r\n" +
		"SUMMARY:External invite\r\nORGANIZER;CN=Other:mailto:other@example.test\r\n" +
		"ATTENDEE;CN=Phase 2 User:mailto:" + externalEmail + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	inviteFile := "invite-" + slug + ".ics"
	if err := os.WriteFile(filepath.Join(server.cfg.UploadDir, inviteFile), []byte(inviteICS), 0o600); err != nil {
		t.Fatalf("write invitation: %v", err)
	}
	inviteAttachmentID := "att-invite-" + slug
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachments (id, tenant_id, user_id, name, mime, size_bytes, sha256, stored_path, previewable)
		VALUES ($1,$2,$3,'invite.ics','text/calendar',$4,'checksum',$5,false)
	`, inviteAttachmentID, tenantID, userID, len(inviteICS), inviteFile); err != nil {
		t.Fatalf("insert invitation attachment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_message_attachments (message_id, attachment_id, position, content_id, disposition)
		VALUES ($1,$2,0,'','inline')
	`, "msg-phase2-external-"+slug, inviteAttachmentID); err != nil {
		t.Fatalf("link invitation attachment: %v", err)
	}
	calendarRequest := httptest.NewRequest("POST", "/api/messages/msg-phase2-external-"+slug+"/calendar", nil)
	calendarRequest.SetPathValue("id", "msg-phase2-external-"+slug)
	calendarRequest = calendarRequest.WithContext(context.WithValue(calendarRequest.Context(), sessionContextKey{}, session))
	calendarRecorder := httptest.NewRecorder()
	server.importMessageCalendar(calendarRecorder, calendarRequest)
	if calendarRecorder.Code != http.StatusOK && calendarRecorder.Code != http.StatusCreated {
		t.Fatalf("calendar import status=%d body=%s", calendarRecorder.Code, calendarRecorder.Body.String())
	}
	var eventOrganizer, eventTitle string
	if err := pool.QueryRow(ctx, `
		SELECT organizer_email::text, title FROM calendar_events WHERE user_id=$1 AND uid=$2
	`, userID, inviteUID).Scan(&eventOrganizer, &eventTitle); err != nil {
		t.Fatalf("imported calendar event: %v", err)
	}
	if eventOrganizer != "other@example.test" || eventTitle != "External invite" {
		t.Fatalf("calendar event organizer=%q title=%q", eventOrganizer, eventTitle)
	}
	var inviteAttendee int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM calendar_attendees a
		JOIN calendar_events e ON e.id=a.event_id
		WHERE e.user_id=$1 AND e.uid=$2 AND lower(a.email)=lower($3) AND a.user_id=$1
	`, userID, inviteUID, externalEmail).Scan(&inviteAttendee); err != nil {
		t.Fatalf("calendar invite attendee: %v", err)
	}
	if inviteAttendee != 1 {
		t.Fatalf("connected account attendee count = %d", inviteAttendee)
	}
	reviewRequest := httptest.NewRequest("GET", "/api/yearly-review", nil)
	reviewRequest.Header.Set(mailAccountHeader, mailAccountScopeAll)
	reviewRequest = reviewRequest.WithContext(context.WithValue(reviewRequest.Context(), sessionContextKey{}, session))
	reviewRecorder := httptest.NewRecorder()
	server.yearlyReview(reviewRecorder, reviewRequest)
	if reviewRecorder.Code != http.StatusOK {
		t.Fatalf("unified yearly review status=%d body=%s", reviewRecorder.Code, reviewRecorder.Body.String())
	}
	if !strings.Contains(reviewRecorder.Body.String(), `"messages":2`) {
		t.Fatalf("unified yearly review did not include both accounts: %s", reviewRecorder.Body.String())
	}
	oldDate := time.Now().AddDate(-2, 0, 0).UTC().Format(time.RFC3339)
	persistOld := func(id string, starred bool) {
		t.Helper()
		uid := 7
		if starred {
			uid = 8
		}
		message := map[string]any{
			"id": id, "accountId": externalID, "folder": "inbox", "imapFolder": "INBOX",
			"imapUid": uid, "imapUidValidity": 1, "threadId": "thread-" + id,
			"subject": "Old cached mail", "from": map[string]any{"name": "Sender", "email": "sender@example.test"},
			"to": []any{}, "cc": []any{}, "bcc": []any{}, "date": oldDate, "bodyText": "old",
			"unread": false, "starred": starred, "sizeBytes": 3, "messageId": "<" + id + "@example.test>",
		}
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatalf("begin old message: %v", beginErr)
		}
		if persistErr := server.persistMailMessageTx(ctx, tx, user, message); persistErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("persist old message: %v", persistErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			t.Fatalf("commit old message: %v", commitErr)
		}
	}
	persistOld("msg-phase2-old-"+slug, false)
	persistOld("msg-phase2-old-starred-"+slug, true)
	externalAccount, err := server.mailAccountByID(ctx, userID, externalID)
	if err != nil {
		t.Fatalf("external account record: %v", err)
	}
	server.pruneExternalAccountCache(ctx, externalAccount)
	var oldRemaining, starredRemaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_messages WHERE id=$1`, "msg-phase2-old-"+slug).Scan(&oldRemaining); err != nil {
		t.Fatalf("count old message: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_messages WHERE id=$1`, "msg-phase2-old-starred-"+slug).Scan(&starredRemaining); err != nil {
		t.Fatalf("count starred old message: %v", err)
	}
	if oldRemaining != 0 || starredRemaining != 1 {
		t.Fatalf("cache prune old=%d starred=%d", oldRemaining, starredRemaining)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE tenants SET settings = jsonb_set(settings, '{mail,externalAccounts}', 'false'::jsonb, true) WHERE id=$1
	`, tenantID); err != nil {
		t.Fatalf("disable external accounts: %v", err)
	}
	scope, err = server.resolveMailReadScope(ctx, session, mailAccountScopeAll)
	if err != nil || scope.Unified || scope.AccountID != nativeID {
		t.Fatalf("policy fallback scope = %#v, %v", scope, err)
	}
	stateID := "state-" + slug
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_oauth_states (id, tenant_id, user_id, provider, verifier_sealed, expires_at)
		VALUES ($1,$2,$3,'gmail','sealed',now() + interval '10 minutes')
	`, stateID, tenantID, userID); err != nil {
		t.Fatalf("insert OAuth state: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail_oauth_states WHERE id=$1`, stateID).Scan(&remaining); err != nil {
		t.Fatalf("count OAuth state: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("OAuth state survived user deletion: %d", remaining)
	}
}

func TestMailAccountsPhase0Integration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_MAIL_ACCOUNTS_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_MAIL_ACCOUNTS_TEST_DSN to run the mail accounts integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	var table string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.mail_accounts')::text`).Scan(&table); err != nil || table == "" {
		t.Fatalf("mail_accounts is missing; apply migration 0057 first: %v", err)
	}

	slug := fmt.Sprintf("mail-accounts-test-%d", time.Now().UnixNano())
	var tenantID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text
	`, slug, "Mail accounts test").Scan(&tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id=$1`, tenantID)
	}()

	createUser := func(label string) (string, string) {
		email := fmt.Sprintf("%s-%s@example.test", label, slug)
		var id, storedEmail string
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, name, role)
			VALUES ($1, $2, $3, 'owner') RETURNING id::text, email::text
		`, tenantID, email, "Mail "+label).Scan(&id, &storedEmail); err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		return id, storedEmail
	}
	ownerID, ownerEmail := createUser("owner")
	otherID, otherEmail := createUser("other")

	var nativeID, nativeKind, nativeProvider, nativeEmail string
	if err := pool.QueryRow(ctx, `
		SELECT id, kind, provider, email::text FROM public.mail_accounts
		WHERE user_id=$1 AND kind='native'
	`, ownerID).Scan(&nativeID, &nativeKind, &nativeProvider, &nativeEmail); err != nil {
		t.Fatalf("native account was not created by the trigger: %v", err)
	}
	if nativeKind != mailAccountKindNative || nativeProvider != mailAccountProviderAlt8Mail ||
		!strings.EqualFold(nativeEmail, ownerEmail) {
		t.Fatalf("unexpected native account: id=%q kind=%q provider=%q email=%q", nativeID, nativeKind, nativeProvider, nativeEmail)
	}
	var folderCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_folders
		WHERE account_id=$1 AND role IN ('inbox','sent','drafts','archive','junk','trash')
	`, nativeID).Scan(&folderCount); err != nil {
		t.Fatalf("count native folders: %v", err)
	}
	if folderCount != 6 {
		t.Fatalf("native folder count = %d, want 6", folderCount)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET name='Renamed Owner', color='#112233' WHERE id=$1`, ownerID); err != nil {
		t.Fatalf("update user: %v", err)
	}
	var displayName, color string
	if err := pool.QueryRow(ctx, `
		SELECT display_name, color FROM public.mail_accounts WHERE id=$1
	`, nativeID).Scan(&displayName, &color); err != nil {
		t.Fatalf("read synced native account: %v", err)
	}
	if displayName != "Renamed Owner" || color != "#112233" {
		t.Fatalf("native account was not synced: display_name=%q color=%q", displayName, color)
	}

	messageID := "msg-" + slug
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_messages (id, tenant_id, user_id, folder, thread_id, subject, from_name, from_email)
		VALUES ($1, $2, $3, 'inbox', $4, 'Phase 0', 'Owner', $5)
	`, messageID, tenantID, ownerID, "thread-"+slug, ownerEmail); err != nil {
		t.Fatalf("insert message without account id: %v", err)
	}
	var messageAccount string
	if err := pool.QueryRow(ctx, `SELECT account_id FROM public.mail_messages WHERE id=$1`, messageID).Scan(&messageAccount); err != nil {
		t.Fatalf("read message account: %v", err)
	}
	if messageAccount != nativeID {
		t.Fatalf("message account = %q, want native %q", messageAccount, nativeID)
	}

	var otherAccountID string
	if err := pool.QueryRow(ctx, `
		SELECT id FROM public.mail_accounts WHERE user_id=$1 AND kind='native'
	`, otherID).Scan(&otherAccountID); err != nil {
		t.Fatalf("other native account: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_messages (id, tenant_id, user_id, folder, thread_id, subject, from_name, from_email, account_id)
		VALUES ($1, $2, $3, 'inbox', $4, 'Cross account', 'Owner', $5, $6)
	`, "msg-cross-"+slug, tenantID, ownerID, "thread-cross-"+slug, ownerEmail, otherAccountID); err == nil {
		t.Fatal("cross-account message insert was accepted")
	}

	var outboxAccount string
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.outbox (id, tenant_id, user_id) VALUES ($1, $2, $3)
	`, "outbox-"+slug, tenantID, ownerID); err != nil {
		t.Fatalf("insert outbox without account id: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT account_id FROM public.outbox WHERE id=$1`, "outbox-"+slug).Scan(&outboxAccount); err != nil {
		t.Fatalf("read outbox account: %v", err)
	}
	if outboxAccount != nativeID {
		t.Fatalf("outbox account = %q, want native %q", outboxAccount, nativeID)
	}

	externalID := "macct_ext_" + slug
	externalEmail := "external-" + slug + "@example.test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mail_accounts (
			id, tenant_id, user_id, kind, provider, email, display_name, status, auth_type,
			imap_host, smtp_host, username
		) VALUES ($1, $2, $3, 'external', 'imap', $4, 'External', 'active', 'password',
			'imap.example.test', 'smtp.example.test', $5)
	`, externalID, tenantID, ownerID, externalEmail, externalEmail); err != nil {
		t.Fatalf("insert external account: %v", err)
	}

	server := &Server{store: &store.Store{DB: pool}}
	accounts, err := server.mailAccountsForUser(ctx, ownerID)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 2 || accounts[0].ID != nativeID {
		t.Fatalf("unexpected account list: %#v", accounts)
	}
	resolved, err := server.resolveMailAccount(ctx, ownerID, "")
	if err != nil || resolved.ID != nativeID {
		t.Fatalf("resolve default account = %#v, %v", resolved, err)
	}
	resolved, err = server.resolveMailAccount(ctx, ownerID, externalID)
	if err != nil || resolved.ID != externalID {
		t.Fatalf("resolve external account = %#v, %v", resolved, err)
	}
	if _, err := server.resolveMailAccount(ctx, otherID, externalID); err != errMailAccountNotFound {
		t.Fatalf("cross-user resolve error = %v, want errMailAccountNotFound", err)
	}
	ids, err := server.mailAccountIDsForScope(ctx, ownerID, "all")
	if err != nil || len(ids) != 2 {
		t.Fatalf("all account ids = %#v, %v", ids, err)
	}
	ids, err = server.mailAccountIDsForScope(ctx, ownerID, nativeID)
	if err != nil || len(ids) != 1 || ids[0] != nativeID {
		t.Fatalf("native account ids = %#v, %v", ids, err)
	}

	delegated, err := server.mailAccountsForSession(ctx, &sessionRecord{
		User: userRecord{ID: ownerID, Email: ownerEmail}, IsDelegated: true,
	})
	if err != nil || len(delegated) != 1 || delegated[0].ID != nativeID {
		t.Fatalf("delegated account list = %#v, %v", delegated, err)
	}

	// Keep the second user referenced so the fixture is obviously exercising
	// two distinct account owners.
	if otherEmail == "" {
		t.Fatal("second user is missing")
	}
}
