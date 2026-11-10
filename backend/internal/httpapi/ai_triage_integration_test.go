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

func TestAITriageParser(t *testing.T) {
	result, err := parseAITriage("```json\n{\"category\":\"action\",\"confidence\":0.92,\"explanation\":\"Needs approval.\"}\n```")
	if err != nil || result.Category != "action" || result.Confidence != 0.92 {
		t.Fatalf("parse = %#v err=%v", result, err)
	}
	if _, err := parseAITriage(`{"category":"delete_everything","confidence":0.9}`); err == nil {
		t.Fatal("unsupported category accepted")
	}
	clamped, err := parseAITriage(`{"category":"fyi","confidence":7}`)
	if err != nil || clamped.Confidence != 1 {
		t.Fatalf("clamped = %#v err=%v", clamped, err)
	}
}

func TestPhase4AITriageIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 AI triage test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4405-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4406-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	other := fmt.Sprintf("00000000-0000-4407-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := "triage-" + suffix + "@ai.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 4 Triage')`, tenant, "phase4-triage-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Triage User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','member')`, other, tenant, "other-triage-"+suffix+"@ai.example.test")
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'ai.example.test',$4,$5)`,
		tenant, user, email, "triage-"+suffix, t.TempDir())
	actionMessage := "triage-action-" + suffix
	newsMessage := "triage-news-" + suffix
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, snippet, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','thread-a','Please approve the invoice','finance@example.test','Invoice attached',now()-interval '1 hour'
		FROM public.mailboxes m WHERE m.user_id=$3`, actionMessage, tenant, user)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, snippet, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','thread-b','Weekly newsletter','news@example.test','Product updates',now()-interval '2 hours'
		FROM public.mailboxes m WHERE m.user_id=$3`, newsMessage, tenant, user)

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		content := `{"category":"newsletter","confidence":0.4,"explanation":"Bulk update."}`
		if strings.Contains(string(raw), "Please approve the invoice") {
			content = `{"category":"action","confidence":0.92,"explanation":"Approval is requested."}`
		}
		encoded, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, encoded)
	}))
	defer aiServer.Close()
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model, triage_auto_label, triage_min_confidence)
		VALUES ($1,true,'openai',$2,'triage-model',true,0.800)`, tenant, aiServer.URL)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Triage User", Status: "active"}
	withSession := func(request *http.Request, value userRecord) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: value.ID, TenantID: tenant, User: value}))
	}
	triage := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/ai/triage", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.aiTriageBatch(recorder, withSession(request, userValue))
		return recorder
	}
	recorder := triage(fmt.Sprintf(`{"messageIds":["%s","%s"]}`, actionMessage, newsMessage))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":2`) {
		t.Fatalf("triage = %d %s", recorder.Code, recorder.Body.String())
	}
	var actionCategory, newsCategory string
	var actionConfidence float64
	_ = pool.QueryRow(ctx, `SELECT category, confidence::float8 FROM public.ai_triage WHERE user_id=$1 AND message_id=$2`, user, actionMessage).Scan(&actionCategory, &actionConfidence)
	_ = pool.QueryRow(ctx, `SELECT category FROM public.ai_triage WHERE user_id=$1 AND message_id=$2`, user, newsMessage).Scan(&newsCategory)
	if actionCategory != "action" || actionConfidence != 0.92 || newsCategory != "newsletter" {
		t.Fatalf("stored triage = %s %.2f %s", actionCategory, actionConfidence, newsCategory)
	}
	var labels []string
	_ = pool.QueryRow(ctx, `SELECT categories FROM public.mail_messages WHERE id=$1`, actionMessage).Scan(&labels)
	if !containsString(labels, "AI/Action") {
		t.Fatalf("auto labels = %#v", labels)
	}
	_ = pool.QueryRow(ctx, `SELECT categories FROM public.mail_messages WHERE id=$1`, newsMessage).Scan(&labels)
	if containsString(labels, "AI/Newsletter") {
		t.Fatalf("low-confidence message was auto-labeled: %#v", labels)
	}

	// Re-triage is idempotent on (user, message) and list returns both.
	recorder = triage(fmt.Sprintf(`{"messageIds":["%s"]}`, actionMessage))
	if recorder.Code != http.StatusOK {
		t.Fatalf("re-triage = %d %s", recorder.Code, recorder.Body.String())
	}
	var triageCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.ai_triage WHERE user_id=$1`, user).Scan(&triageCount)
	if triageCount != 2 {
		t.Fatalf("triage rows = %d", triageCount)
	}
	listRequest := withSession(httptest.NewRequest(http.MethodGet, "/api/ai/triage", nil), userValue)
	recorder = httptest.NewRecorder()
	server.listAITriage(recorder, listRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":2`) {
		t.Fatalf("list = %d %s", recorder.Code, recorder.Body.String())
	}

	// Feedback records the correction and is tenant/user scoped.
	var triageID string
	_ = pool.QueryRow(ctx, `SELECT id FROM public.ai_triage WHERE user_id=$1 AND message_id=$2`, user, actionMessage).Scan(&triageID)
	feedback := func(value userRecord, category string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/ai/triage/"+triageID+"/feedback", bytes.NewBufferString(fmt.Sprintf(`{"feedback":"incorrect","category":"%s"}`, category)))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("id", triageID)
		recorder := httptest.NewRecorder()
		server.feedbackAITriage(recorder, withSession(request, value))
		return recorder
	}
	if recorder := feedback(userValue, "fyi"); recorder.Code != http.StatusOK {
		t.Fatalf("feedback = %d %s", recorder.Code, recorder.Body.String())
	}
	var storedFeedback, corrected string
	_ = pool.QueryRow(ctx, `SELECT feedback, corrected_category FROM public.ai_triage WHERE id=$1`, triageID).Scan(&storedFeedback, &corrected)
	if storedFeedback != "incorrect" || corrected != "fyi" {
		t.Fatalf("feedback stored = %q/%q", storedFeedback, corrected)
	}
	otherValue := userRecord{ID: other, TenantID: tenant, Email: "other-triage-" + suffix + "@ai.example.test", Status: "active"}
	if recorder := feedback(otherValue, "fyi"); recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-user feedback = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := feedback(userValue, "not_a_category"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid category feedback = %d", recorder.Code)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
