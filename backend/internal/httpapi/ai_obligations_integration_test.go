package httpapi

import (
	"bytes"
	"context"
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

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAIObligationParser(t *testing.T) {
	drafts := parseAIObligations(`{"obligations":[
		{"kind":"request","text":"Send the deck to the client","quote":"Could you send the deck?","due":"2026-11-13","confidence":0.9},
		{"kind":"other","text":"","confidence":0.9},
		{"kind":"unknown","text":"This has a date so it becomes a deadline","due":"2026-11-14","confidence":0.7},
		{"kind":"fyi","text":"ignore me","confidence":0.99},
		{"kind":"promise","text":"short","confidence":0.9}
	]}`)
	if len(drafts) != 2 || drafts[0].Kind != "request" || drafts[1].Kind != "deadline" {
		t.Fatalf("drafts = %#v", drafts)
	}
	if aiObligationKey("request", "Send  the deck.", "") != aiObligationKey("request", "send the deck", "") {
		t.Fatal("dedupe key is not stable")
	}
}

func TestPhase4AIObligationsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 obligations test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4408-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4409-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	other := fmt.Sprintf("00000000-0000-4410-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := "oblig-" + suffix + "@ai.example.test"
	clientEmail := "client-" + suffix + "@example.test"
	threadID := "oblig-thread-" + suffix
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 4 Obligations')`, tenant, "phase4-oblig-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Obligation User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','member')`, other, tenant, "other-oblig-"+suffix+"@ai.example.test")
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'ai.example.test',$4,$5)`,
		tenant, user, email, "oblig-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'inbox',$4,'Deck for Friday',$5,'Could you send the deck by Friday? I will send the updated pricing sheet tomorrow.',now()-interval '2 hours'
		FROM public.mailboxes m WHERE m.user_id=$3`, "oblig-in-"+suffix, tenant, user, threadID, clientEmail)

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"obligations\":[{\"kind\":\"request\",\"text\":\"Send the deck to the client\",\"quote\":\"Could you send the deck by Friday?\",\"due\":\"2026-11-13\",\"confidence\":0.9},{\"kind\":\"promise\",\"text\":\"Send the updated pricing sheet\",\"quote\":\"I will send the updated pricing sheet tomorrow\",\"due\":\"\",\"confidence\":0.8},{\"kind\":\"other\",\"text\":\"\",\"confidence\":0.9}]}"}}]}`)
	}))
	defer aiServer.Close()
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model, obligations_auto_task)
		VALUES ($1,true,'openai',$2,'obligation-model',true)`, tenant, aiServer.URL)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Obligation User", Status: "active"}
	withSession := func(request *http.Request, value userRecord) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: value.ID, TenantID: tenant, User: value}))
	}
	extract := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/ai/obligations/extract", bytes.NewBufferString(fmt.Sprintf(`{"threadId":"%s"}`, threadID)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.aiExtractObligations(recorder, withSession(request, userValue))
		return recorder
	}
	recorder := extract()
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":2`) {
		t.Fatalf("extract = %d %s", recorder.Code, recorder.Body.String())
	}
	var obligationCount, taskCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.ai_obligations WHERE user_id=$1`, user).Scan(&obligationCount)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.tasks WHERE user_id=$1 AND source='ai_obligation'`, user).Scan(&taskCount)
	if obligationCount != 2 || taskCount != 2 {
		t.Fatalf("obligations=%d tasks=%d", obligationCount, taskCount)
	}
	// Re-extraction dedupes instead of duplicating.
	if recorder := extract(); recorder.Code != http.StatusOK {
		t.Fatalf("second extract = %d %s", recorder.Code, recorder.Body.String())
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.ai_obligations WHERE user_id=$1`, user).Scan(&obligationCount)
	if obligationCount != 2 {
		t.Fatalf("dedupe failed: %d obligations", obligationCount)
	}

	// A later reply from the mailbox owner resolves the inbound request.
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'sent',$4,'Re: Deck for Friday',$5,'Here is the deck.',now()+interval '1 minute'
		FROM public.mailboxes m WHERE m.user_id=$3`, "oblig-reply-"+suffix, tenant, user, threadID, email)
	server.resolveRepliedObligations(ctx, userValue, threadID)
	var requestStatus, promiseStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.ai_obligations WHERE user_id=$1 AND kind='request'`, user).Scan(&requestStatus)
	_ = pool.QueryRow(ctx, `SELECT status FROM public.ai_obligations WHERE user_id=$1 AND kind='promise'`, user).Scan(&promiseStatus)
	if requestStatus != "completed" || promiseStatus != "open" {
		t.Fatalf("reply resolution statuses = %s/%s", requestStatus, promiseStatus)
	}

	// Waiting view only shows the still-open inbound request category.
	listRequest := httptest.NewRequest(http.MethodGet, "/api/ai/obligations?status=open&view=waiting", nil)
	listRequest = withSession(listRequest, userValue)
	recorder = httptest.NewRecorder()
	server.listAIObligations(recorder, listRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":0`) {
		t.Fatalf("waiting view = %d %s", recorder.Code, recorder.Body.String())
	}

	var promiseID, promiseTask string
	_ = pool.QueryRow(ctx, `SELECT id, COALESCE(task_id,'') FROM public.ai_obligations WHERE user_id=$1 AND kind='promise'`, user).Scan(&promiseID, &promiseTask)
	complete := httptest.NewRequest(http.MethodPatch, "/api/ai/obligations/"+promiseID, bytes.NewBufferString(`{"status":"completed"}`))
	complete.Header.Set("Content-Type", "application/json")
	complete.SetPathValue("id", promiseID)
	complete = withSession(complete, userValue)
	recorder = httptest.NewRecorder()
	server.updateAIObligation(recorder, complete)
	if recorder.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", recorder.Code, recorder.Body.String())
	}
	var taskStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.tasks WHERE id=$1`, promiseTask).Scan(&taskStatus)
	if taskStatus != "done" {
		t.Fatalf("linked task status = %s", taskStatus)
	}

	otherValue := userRecord{ID: other, TenantID: tenant, Email: "other-oblig-" + suffix + "@ai.example.test", Status: "active"}
	crossUser := httptest.NewRequest(http.MethodPatch, "/api/ai/obligations/"+promiseID, bytes.NewBufferString(`{"status":"dismissed"}`))
	crossUser.Header.Set("Content-Type", "application/json")
	crossUser.SetPathValue("id", promiseID)
	crossUser = withSession(crossUser, otherValue)
	recorder = httptest.NewRecorder()
	server.updateAIObligation(recorder, crossUser)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-user update = %d %s", recorder.Code, recorder.Body.String())
	}
}
