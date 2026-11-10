package httpapi

import (
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

func TestPhase5AuditExportIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 5 audit export test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4507-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4508-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "audit-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 5 Audit')`, tenant, "phase5-audit-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Audit User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.audit_log (tenant_id, user_id, actor, action, target, detail, created_at)
		VALUES ($1,$2,$3,'mail.sent','Message','Sent a message',now()-interval '1 hour')`, tenant, user, email)
	mustExec(`INSERT INTO public.admin_audit_log (tenant_id, actor_id, actor_email, action, target, detail, created_at)
		VALUES ($1,$2,$3,'admin.policy_updated','Policy','Updated policy',now()-interval '1 hour')`, tenant, user, email)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "phase5-audit-secret"}}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Audit User", Role: "owner"}}
	run := func() map[string]any {
		to := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
		request := httptest.NewRequest(http.MethodGet, "/admin/api/audit/export?to="+to, nil)
		request = request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
		recorder := httptest.NewRecorder()
		server.adminExportAuditBundle(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("export = %d %s", recorder.Code, recorder.Body.String())
		}
		var response map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return response
	}
	first := run()
	second := run()
	if fmt.Sprint(first["manifest"].(map[string]any)["eventCount"]) != "2" {
		t.Fatalf("event count = %#v", first["manifest"])
	}
	if stateString(first, "signature") == "" {
		t.Fatal("signature missing")
	}
	events, _ := first["events"].([]any)
	body := fmt.Sprint(events)
	if !strings.Contains(body, "mail.sent") || !strings.Contains(body, "admin.policy_updated") {
		t.Fatalf("events incomplete: %s", body)
	}
	firstHash := stateString(first["manifest"].(map[string]any), "contentSha256")
	secondHash := stateString(second["manifest"].(map[string]any), "contentSha256")
	if firstHash == "" || firstHash != secondHash {
		t.Fatalf("audit hashes differ: %s / %s", firstHash, secondHash)
	}
	var exportCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_exports WHERE tenant_id=$1`, tenant).Scan(&exportCount)
	if exportCount != 2 {
		t.Fatalf("audit export rows = %d", exportCount)
	}
}
