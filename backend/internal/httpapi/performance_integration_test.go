package httpapi

import (
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

// TestPerformanceBudgets is a coarse CI guard for the documented API budgets.
// It uses generous wall-clock ceilings so shared CI runners do not flake.
func TestPerformanceBudgets(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the performance budget test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4610-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4611-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "perf-" + suffix + "@gov.example.test"
	token := "a8m_perf_" + suffix
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Perf Test')`, tenant, "perf-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Perf User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, user, email, "perf-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, sent_at)
		SELECT 'perf-msg-'||g,$1,$2,m.id,'inbox','perf-thread-'||g,'Budget message '||g,'client@example.test','Searchable body '||g,now()-make_interval(mins => g)
		FROM generate_series(1,50) g, public.mailboxes m WHERE m.user_id=$2`, tenant, user)
	mustExec(`INSERT INTO public.api_keys (id, tenant_id, created_by, name, prefix, token_hash, scopes)
		VALUES ($1,$2,$3,'perf','perf',$4,ARRAY['mail.read'])`, "perf-key-"+suffix, tenant, user, mcpHash(token))
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}}
	handler := server.requireAPIKey("mail.read", server.apiV1ListMessages)
	var maxLatency time.Duration
	for iteration := 0; iteration < 5; iteration++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/messages?limit=50", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		start := time.Now()
		handler(recorder, request)
		elapsed := time.Since(start)
		if elapsed > maxLatency {
			maxLatency = elapsed
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("API budget request = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if maxLatency > 2*time.Second {
		t.Fatalf("API message list exceeded the CI budget: %s", maxLatency)
	}
	admin := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Perf User", Role: "owner"}}
	analyticsRequest := httptest.NewRequest(http.MethodGet, "/admin/api/usage/analytics", nil)
	analyticsRequest = analyticsRequest.WithContext(context.WithValue(analyticsRequest.Context(), adminSessionContextKey{}, admin))
	start := time.Now()
	recorder := httptest.NewRecorder()
	server.adminUsageAnalytics(recorder, analyticsRequest)
	if elapsed := time.Since(start); elapsed > 2*time.Second || recorder.Code != http.StatusOK {
		t.Fatalf("analytics exceeded the CI budget: %s status=%d", elapsed, recorder.Code)
	}
}
