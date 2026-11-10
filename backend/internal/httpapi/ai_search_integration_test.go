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

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAISearchParser(t *testing.T) {
	filters, err := parseAISearchFilters(`{"keywords":"invoice","unread":true,"after":"not-a-date"}`)
	if err != nil || filters.Keywords != "invoice" || filters.Unread == nil || !*filters.Unread || filters.After != "" {
		t.Fatalf("filters = %#v err=%v", filters, err)
	}
	if _, err := parseAISearchFilters(`{"keywords":"","unread":null}`); err == nil {
		t.Fatal("empty filters accepted")
	}
	fallback := deterministicSearchFilters("show me unread invoices from alice last week")
	if fallback.Keywords != "invoices" || fallback.From != "alice" || fallback.Unread == nil || !*fallback.Unread || fallback.After == "" {
		t.Fatalf("fallback = %#v", fallback)
	}
}

func TestPhase4AISearchIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 AI search test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4411-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4412-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	other := fmt.Sprintf("00000000-0000-4413-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := "search-" + suffix + "@ai.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 4 Search')`, tenant, "phase4-search-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Search User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','member')`, other, tenant, "other-search-"+suffix+"@ai.example.test")
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'ai.example.test',$4,$5)`,
		tenant, user, email, "search-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'ai.example.test',$4,$5)`,
		tenant, other, "other-search-"+suffix+"@ai.example.test", "other-search-"+suffix, t.TempDir())
	insertMessage := func(id, userID, subject, from, folder string, unread bool, sent time.Time) {
		mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, from_name, body_text, unread, sent_at)
			SELECT $1,$2,$3,m.id,$4,$1,$5,$6,'Sender',$7,$8,$9
			FROM public.mailboxes m WHERE m.user_id=$3`, id, tenant, userID, folder, subject, from, subject+" body text", unread, sent)
	}
	insertMessage("search-invoice-"+suffix, user, "Invoice 42", "billing@example.test", "inbox", true, time.Now().Add(-48*time.Hour))
	insertMessage("search-meeting-"+suffix, user, "Meeting notes", "alice@example.test", "inbox", false, time.Now().Add(-time.Hour))
	insertMessage("search-old-"+suffix, user, "Old invoice", "alice@example.test", "archive", false, time.Now().AddDate(0, 0, -40))
	insertMessage("search-other-"+suffix, other, "Invoice 99", "billing@example.test", "inbox", true, time.Now().Add(-time.Hour))

	lastWeek := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		content := `{"keywords":"invoice","subject":"","from":"","to":"","folder":"","unread":null,"starred":null,"hasAttachment":null,"after":"","before":""}`
		if strings.Contains(string(raw), "from alice last week") {
			content = fmt.Sprintf(`{"keywords":"","subject":"","from":"alice","to":"","folder":"","unread":null,"starred":null,"hasAttachment":null,"after":"%s","before":"%s"}`, lastWeek, today)
		} else if strings.Contains(string(raw), "nonexistent") {
			content = `{"keywords":"nonexistent-term-xyz","subject":"","from":"","to":"","folder":"","unread":null,"starred":null,"hasAttachment":null,"after":"","before":""}`
		} else if strings.Contains(string(raw), "DROP TABLE") {
			content = `{"keywords":"'; DROP TABLE mail_messages; --","subject":"","from":"","to":"","folder":"","unread":null,"starred":null,"hasAttachment":null,"after":"","before":""}`
		}
		encoded, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, encoded)
	}))
	defer aiServer.Close()
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model)
		VALUES ($1,true,'openai',$2,'search-model')`, tenant, aiServer.URL)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Search User", Status: "active"}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: user, TenantID: tenant, User: userValue}))
	}
	search := func(query string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"query": query})
		request := httptest.NewRequest(http.MethodPost, "/api/ai/search", bytes.NewBuffer(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.aiSearch(recorder, withSession(request))
		return recorder
	}
	recorder := search("invoice emails from this month")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":2`) || !strings.Contains(recorder.Body.String(), `"aiUsed":true`) {
		t.Fatalf("keyword search = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Invoice 99") {
		t.Fatal("another user's message leaked into search results")
	}
	if !strings.Contains(recorder.Body.String(), `"citations"`) || !strings.Contains(recorder.Body.String(), `"excerpt"`) {
		t.Fatalf("citations missing: %s", recorder.Body.String())
	}
	recorder = search("from alice last week")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":1`) || !strings.Contains(recorder.Body.String(), "Meeting notes") {
		t.Fatalf("date search = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = search("nonexistent")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":0`) {
		t.Fatalf("empty search = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = search("please find '; DROP TABLE mail_messages; --")
	if recorder.Code != http.StatusOK {
		t.Fatalf("injection-shaped query = %d %s", recorder.Code, recorder.Body.String())
	}
	var messageCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.mail_messages WHERE tenant_id=$1`, tenant).Scan(&messageCount); err != nil || messageCount != 4 {
		t.Fatalf("message table changed: count=%d err=%v", messageCount, err)
	}
}
