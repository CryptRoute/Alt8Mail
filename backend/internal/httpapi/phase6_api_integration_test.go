package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase6APIPlatformIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 6 API test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4600-8000-%012d", time.Now().UnixNano()%1000000000000)
	otherTenant := fmt.Sprintf("00000000-0000-4601-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	user := fmt.Sprintf("00000000-0000-4602-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	otherUser := fmt.Sprintf("00000000-0000-4603-8000-%012d", (time.Now().UnixNano()+3)%1000000000000)
	email := "api-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id IN ($1,$2)`, tenant, otherTenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	for _, pair := range [][2]string{{tenant, "phase6-api-" + suffix}, {otherTenant, "phase6-other-" + suffix}} {
		mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 6 API')`, pair[0], pair[1])
	}
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'API User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','owner')`, otherUser, otherTenant, "other-"+suffix+"@gov.example.test")
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, user, email, "api-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		otherTenant, otherUser, "other-"+suffix+"@gov.example.test", "other-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','api-thread','API contract','client@example.test','The API contract body',now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "api-msg-"+suffix, tenant, user)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','other-thread','Other tenant secret','client@example.test','Should not leak',now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "api-other-"+suffix, otherTenant, otherUser)

	var webhookCalls []map[string]string
	var webhookBodies [][]byte
	webhookFailures := 1
	var webhookMu sync.Mutex
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "phase6-api-secret", PublicURL: "https://mail.test"},
		webhookAllowPrivate: true}
	server.webhookPost = func(ctx context.Context, endpoint string, headers map[string]string, body []byte) (int, error) {
		webhookMu.Lock()
		defer webhookMu.Unlock()
		webhookCalls = append(webhookCalls, headers)
		webhookBodies = append(webhookBodies, append([]byte{}, body...))
		if webhookFailures > 0 {
			webhookFailures--
			return http.StatusBadGateway, nil
		}
		return http.StatusOK, nil
	}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "API User", Role: "owner"}}
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	}
	callAdmin := func(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = bytes.NewBufferString(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler(recorder, withAdmin(request))
		return recorder
	}
	keyBody := `{"name":"CI key","scopes":["mail.read","mail.send","tasks.read","tasks.write","calendar.read"],"rateLimitPerMinute":120}`
	recorder := callAdmin(server.adminCreateAPIKey, http.MethodPost, "/admin/api/api-keys", keyBody)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", recorder.Code, recorder.Body.String())
	}
	var keyResponse map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &keyResponse)
	apiKey := stateString(keyResponse, "token")
	keyID := stateString(keyResponse, "id")
	if !strings.HasPrefix(apiKey, "a8m_") || keyID == "" {
		t.Fatalf("key response = %s", recorder.Body.String())
	}
	withKey := func(request *http.Request) *http.Request {
		request.Header.Set("Authorization", "Bearer "+apiKey)
		return request
	}
	callAPI := func(handler http.HandlerFunc, scope, method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = bytes.NewBufferString(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		request = withKey(request)
		recorder := httptest.NewRecorder()
		server.requireAPIKey(scope, handler)(recorder, request)
		return recorder
	}
	recorder = callAPI(server.apiV1Me, "", http.MethodGet, "/api/v1/me", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), email) {
		t.Fatalf("me = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = callAPI(server.apiV1ListMessages, "mail.read", http.MethodGet, "/api/v1/messages", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "API contract") || strings.Contains(recorder.Body.String(), "Other tenant secret") {
		t.Fatalf("messages = %d %s", recorder.Code, recorder.Body.String())
	}
	detail := httptest.NewRequest(http.MethodGet, "/api/v1/messages/api-msg-"+suffix, nil)
	detail.SetPathValue("id", "api-msg-"+suffix)
	recorder = httptest.NewRecorder()
	server.requireAPIKey("mail.read", server.apiV1GetMessage)(recorder, withKey(detail))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "The API contract body") {
		t.Fatalf("detail = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := callAPI(server.apiV1ListTasks, "tasks.read", http.MethodGet, "/api/v1/tasks", ""); recorder.Code != http.StatusOK {
		t.Fatalf("tasks scope = %d %s", recorder.Code, recorder.Body.String())
	}

	// Idempotent send queues exactly one outbox row.
	sendBody := `{"to":"client@example.test","subject":"API send","bodyText":"Hello from the API"}`
	send := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", bytes.NewBufferString(sendBody))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "send-"+suffix)
		recorder := httptest.NewRecorder()
		server.requireAPIKey("mail.send", server.apiV1SendMessage)(recorder, withKey(request))
		return recorder
	}
	first := send()
	second := send()
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("send = %d/%d %s", first.Code, second.Code, first.Body.String())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("idempotent response differs: %s / %s", first.Body.String(), second.Body.String())
	}
	var outboxCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE user_id=$1 AND subject='API send'`, user).Scan(&outboxCount)
	if outboxCount != 1 {
		t.Fatalf("outbox rows = %d", outboxCount)
	}

	// Webhook retries and signature headers.
	webhookBody := `{"url":"https://hooks.example.test/a8m","events":["message.queued"]}`
	recorder = callAdmin(server.adminCreateWebhook, http.MethodPost, "/admin/api/webhooks", webhookBody)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create webhook = %d %s", recorder.Code, recorder.Body.String())
	}
	sendAgain := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", bytes.NewBufferString(`{"to":"client@example.test","subject":"Webhook send","bodyText":"Hook"}`))
	sendAgain.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.requireAPIKey("mail.send", server.apiV1SendMessage)(recorder, withKey(sendAgain))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("webhook send = %d %s", recorder.Code, recorder.Body.String())
	}
	server.runWebhookDeliveries(ctx)
	webhookMu.Lock()
	callsAfterFirst := len(webhookCalls)
	webhookMu.Unlock()
	if callsAfterFirst != 1 {
		t.Fatalf("webhook first attempt calls = %d", callsAfterFirst)
	}
	var deliveryStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.webhook_deliveries WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 1`, tenant).Scan(&deliveryStatus)
	if deliveryStatus != "pending" {
		t.Fatalf("delivery after failure = %s", deliveryStatus)
	}
	_, _ = pool.Exec(ctx, `UPDATE public.webhook_deliveries SET next_attempt_at=now() WHERE tenant_id=$1 AND status='pending'`, tenant)
	server.runWebhookDeliveries(ctx)
	_ = pool.QueryRow(ctx, `SELECT status FROM public.webhook_deliveries WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 1`, tenant).Scan(&deliveryStatus)
	if deliveryStatus != "delivered" {
		t.Fatalf("delivery after retry = %s", deliveryStatus)
	}
	webhookMu.Lock()
	lastHeaders := webhookCalls[len(webhookCalls)-1]
	lastBody := webhookBodies[len(webhookBodies)-1]
	webhookMu.Unlock()
	if lastHeaders["X-A8M-Signature"] == "" || lastHeaders["X-A8M-Delivery"] == "" || !strings.Contains(string(lastBody), "Webhook send") {
		t.Fatalf("webhook headers/body = %#v %s", lastHeaders, lastBody)
	}

	// OpenAPI and rate limits.
	if recorder := httptest.NewRecorder(); true {
		apiV1OpenAPI(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"openapi":"3.0.3"`) || !strings.Contains(recorder.Body.String(), "/messages/send") {
			t.Fatalf("openapi = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	limitedRecorder := callAdmin(server.adminCreateAPIKey, http.MethodPost, "/admin/api/api-keys", `{"name":"limited","scopes":["mail.read"],"rateLimitPerMinute":2}`)
	var limited map[string]any
	_ = json.Unmarshal(limitedRecorder.Body.Bytes(), &limited)
	limitedToken := stateString(limited, "token")
	rateCall := func() int {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
		request.Header.Set("Authorization", "Bearer "+limitedToken)
		recorder := httptest.NewRecorder()
		server.requireAPIKey("mail.read", server.apiV1ListMessages)(recorder, request)
		return recorder.Code
	}
	if rateCall() != http.StatusOK || rateCall() != http.StatusOK || rateCall() != http.StatusTooManyRequests {
		t.Fatal("API rate limit did not trip at the configured threshold")
	}

	// Revocation is immediate.
	recorder = httptest.NewRecorder()
	revokeRequest := httptest.NewRequest(http.MethodDelete, "/admin/api/api-keys/"+keyID, nil)
	revokeRequest.SetPathValue("id", keyID)
	server.adminRevokeAPIKey(recorder, withAdmin(revokeRequest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := callAPI(server.apiV1Me, "", http.MethodGet, "/api/v1/me", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key = %d %s", recorder.Code, recorder.Body.String())
	}
}
