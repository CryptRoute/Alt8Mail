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

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMailMergeCSVAndRender(t *testing.T) {
	recipients, err := parseMergeCSV("email,name,company\nA@Example.test,Avery,Acme\nb@example.test,Bob,Beta\n")
	if err != nil || len(recipients) != 2 || recipients[0].Variables["company"] != "Acme" {
		t.Fatalf("csv = %#v err=%v", recipients, err)
	}
	if _, err := parseMergeCSV("email,name\nbroken\n"); err == nil {
		t.Fatal("malformed CSV was accepted")
	}
	normalized, err := normalizeMergeRecipients([]mergeRecipientInput{
		{Email: "a@example.test", Name: "A"},
		{Email: "A@example.test", Name: "Duplicate"},
	}, "email,name\nb@example.test,B\n")
	if err != nil || len(normalized) != 2 {
		t.Fatalf("normalized = %#v err=%v", normalized, err)
	}
	subject, text, htmlBody := renderMergeMessage(mergeRecipientInput{
		Email: "a@example.test", Name: "Avery <b>", Variables: map[string]string{"company": "Acme & Co"},
	}, "Hello {{name}}", "For {{company}}", "<p>For {{company}}</p>", "https://mail.test/u/token")
	if subject != "Hello Avery <b>" || !strings.Contains(text, "Acme & Co") || !strings.Contains(htmlBody, "Acme &amp; Co") || !strings.Contains(htmlBody, "Unsubscribe") {
		t.Fatalf("render subject=%q text=%q html=%q", subject, text, htmlBody)
	}
}

func TestMailMergeIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the mail merge integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4039-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4040-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "merge-" + suffix + "@merge.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Merge Test')`, tenant, "merge-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Merge User','member')`, user, tenant, email)
	mustExec(`INSERT INTO public.user_aliases (tenant_id, user_id, email, label, kind, can_send) VALUES ($1,$2,$3,'Primary','primary',true)`, tenant, user, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'merge.example.test',$4,$5)`,
		tenant, user, email, "merge-"+suffix, t.TempDir())
	server := &Server{
		store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{},
		license: &licensing.License{Version: licensing.Version, ID: "merge-test", Edition: "enterprise", Seats: 10,
			Features: []string{"mail_merge"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	userRecord := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Merge User", Status: "active"}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: user, TenantID: tenant, User: userRecord}))
	}
	recipients := []map[string]any{}
	for i := 0; i < 100; i++ {
		recipients = append(recipients, map[string]any{"email": fmt.Sprintf("recipient-%d-%s@example.test", i, suffix), "name": fmt.Sprintf("Recipient %d", i), "variables": map[string]string{"company": fmt.Sprintf("Company %d", i)}})
	}
	recipients = append(recipients, recipients[0])
	mustExec(`INSERT INTO public.mail_suppressions (tenant_id, user_id, email, reason) VALUES ($1,$2,$3,'manual')`, tenant, user, "recipient-3-"+suffix+"@example.test")
	body, _ := json.Marshal(map[string]any{
		"name": "Quarterly update", "subject": "Hello {{name}} at {{company}}", "bodyText": "Hi {{name}}, update for {{company}}.",
		"throttleSeconds": 2, "recipients": recipients,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/mail-merges", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.createMailMerge(recorder, withSession(request))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &created)
	mergePayload, _ := created["merge"].(map[string]any)
	mergeID := stateString(mergePayload, "id")
	if mergeID == "" {
		t.Fatal("merge id missing")
	}
	queueRequest := httptest.NewRequest(http.MethodPost, "/api/mail-merges/"+mergeID+"/queue", nil)
	queueRequest.SetPathValue("id", mergeID)
	recorder = httptest.NewRecorder()
	server.queueMailMerge(recorder, withSession(queueRequest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("queue = %d %s", recorder.Code, recorder.Body.String())
	}
	var queued, suppressed, outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.mail_merge_recipients WHERE merge_id=$1 AND status='queued'`, mergeID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.mail_merge_recipients WHERE merge_id=$1 AND status='suppressed'`, mergeID).Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE merge_id=$1`, mergeID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if queued != 99 || suppressed != 1 || outboxCount != 99 {
		t.Fatalf("queued=%d suppressed=%d outbox=%d", queued, suppressed, outboxCount)
	}
	var first, second time.Time
	if err := pool.QueryRow(ctx, `SELECT send_at FROM public.outbox WHERE merge_id=$1 ORDER BY send_at LIMIT 1`, mergeID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT send_at FROM public.outbox WHERE merge_id=$1 ORDER BY send_at OFFSET 1 LIMIT 1`, mergeID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second.Sub(first) < 2*time.Second {
		t.Fatalf("throttle spacing = %s", second.Sub(first))
	}
	var rendered string
	if err := pool.QueryRow(ctx, `SELECT subject FROM public.outbox WHERE merge_id=$1 ORDER BY send_at LIMIT 1`, mergeID).Scan(&rendered); err != nil || !strings.Contains(rendered, "Recipient 0") {
		t.Fatalf("rendered subject = %q err=%v", rendered, err)
	}
	var token string
	if err := pool.QueryRow(ctx, `SELECT token FROM public.mail_merge_recipients WHERE merge_id=$1 AND status='queued' LIMIT 1`, mergeID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	unsub := httptest.NewRequest(http.MethodGet, "/u/"+token, nil)
	unsub.SetPathValue("token", token)
	recorder = httptest.NewRecorder()
	server.unsubscribeMerge(recorder, unsub)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unsubscribe = %d", recorder.Code)
	}
	var suppressions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.mail_suppressions WHERE user_id=$1`, user).Scan(&suppressions); err != nil || suppressions != 2 {
		t.Fatalf("suppressions = %d err=%v", suppressions, err)
	}
}
