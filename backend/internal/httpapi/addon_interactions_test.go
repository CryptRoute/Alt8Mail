package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAddonInteractiveActionsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the add-on interaction test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4615-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4616-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "actions-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Addon Actions')`, tenant, "addon-actions-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Actions User','owner')`, userID, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, userID, email, "actions-"+suffix, t.TempDir())
	messageID := "action-msg-" + suffix
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_name, from_email, body_text, snippet, sent_at, unread)
		SELECT $1,$2,$3,m.id,'inbox','action-thread','Action subject','Sender',$4,'Body text','Preview',now(),true
		FROM public.mailboxes m WHERE m.user_id=$3`, messageID, tenant, userID, "sender@client.example.test")
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "addon-actions-secret", PublicURL: "https://mail.example.test"}}
	if err := server.ensureAddonCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	slackSecret := "slack-signing-secret"
	sealedSecret, err := server.sealWorkspaceToken(slackSecret)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(`INSERT INTO public.tenant_addons (id, tenant_id, addon_id, enabled, webhook_secret_sealed)
		VALUES ($1,$2,'slack',true,$3)`, newID("tenantaddon"), tenant, sealedSecret)
	connID := "action-conn-" + suffix
	sealedCredential, err := server.sealWorkspaceToken(`{"webhookUrl":"https://hooks.slack.com/services/T000/B000/x"}`)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(`INSERT INTO public.addon_connections (id, tenant_id, addon_id, connection_scope, credentials_sealed, status, external_account_id)
		VALUES ($1,$2,'slack','tenant',$3,'active','T123')`, connID, tenant, sealedCredential)

	markTokens, err := server.createAddonActionTokens(ctx, tenant, userID, "slack", connID, messageID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "block_actions", "team": map[string]string{"id": "T123"},
		"actions": []map[string]string{{"action_id": "a8m_mark_read", "value": markTokens["mark_read"]}},
	})
	body := []byte("payload=" + url.QueryEscape(string(payload)))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(slackSecret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))
	request := httptest.NewRequest(http.MethodPost, "/api/addons/webhooks/slack/interactions", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Slack-Request-Timestamp", timestamp)
	request.Header.Set("X-Slack-Signature", signature)
	recorder := httptest.NewRecorder()
	server.slackInteraction(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Marked the message read") {
		t.Fatalf("slack interaction = %d %s", recorder.Code, recorder.Body.String())
	}
	var used bool
	_ = pool.QueryRow(ctx, `SELECT used_at IS NOT NULL FROM public.addon_action_tokens WHERE id=$1`, markTokens["mark_read"]).Scan(&used)
	if !used {
		t.Fatal("slack action token was not consumed")
	}
	user, err := server.userByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	var unread bool
	err = server.withMailState(ctx, user, []string{messageID}, false, func(state map[string]any) error {
		message := findMessageIn(state, messageID)
		if message == nil {
			return fmt.Errorf("message missing from state")
		}
		unread = stateBool(message, "unread")
		return nil
	})
	if err != nil || unread {
		t.Fatalf("mark read state unread=%v err=%v", unread, err)
	}

	// A second use of the same token must fail.
	request = httptest.NewRequest(http.MethodPost, "/api/addons/webhooks/slack/interactions", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Slack-Request-Timestamp", timestamp)
	request.Header.Set("X-Slack-Signature", signature)
	recorder = httptest.NewRecorder()
	server.slackInteraction(recorder, request)
	if !strings.Contains(recorder.Body.String(), "expired or was already used") {
		t.Fatalf("replayed slack interaction = %d %s", recorder.Code, recorder.Body.String())
	}

	// Teams link actions are single-use capability URLs.
	teamsTokens, err := server.createAddonActionTokens(ctx, tenant, userID, "teams", "teams-conn", messageID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	linkRequest := httptest.NewRequest(http.MethodGet, "/api/addons/actions/"+teamsTokens["create_task"], nil)
	linkRequest.SetPathValue("token", teamsTokens["create_task"])
	linkRecorder := httptest.NewRecorder()
	server.teamsInteractiveLink(linkRecorder, linkRequest)
	if linkRecorder.Code != http.StatusOK || !strings.Contains(linkRecorder.Body.String(), "Task created") {
		t.Fatalf("teams link = %d %s", linkRecorder.Code, linkRecorder.Body.String())
	}
	var tasks int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.tasks WHERE user_id=$1 AND source='addon_action'`, userID).Scan(&tasks)
	if tasks != 1 {
		t.Fatalf("addon action tasks = %d", tasks)
	}
	linkRecorder = httptest.NewRecorder()
	server.teamsInteractiveLink(linkRecorder, linkRequest)
	if linkRecorder.Code != http.StatusGone {
		t.Fatalf("replayed teams link = %d", linkRecorder.Code)
	}
}

func TestTeamsWebhookValidation(t *testing.T) {
	for _, valid := range []string{
		"https://acme.webhook.office.com/webhookb2/abc",
		"https://prod-12.westus.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke",
	} {
		if err := validateTeamsWebhook(valid); err != nil {
			t.Fatalf("expected %q valid: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"http://acme.webhook.office.com/webhookb2/abc",
		"https://evil.example.com/webhook",
		"https://acme.webhook.office.com",
	} {
		if err := validateTeamsWebhook(invalid); err == nil {
			t.Fatalf("expected %q invalid", invalid)
		}
	}
}
