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

func TestPhase6MigrationFidelityIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 6 migration fidelity test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4606-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4607-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	jobID := "migjob-" + suffix
	email := "mig-" + suffix + "@gov.example.test"
	source := "source-" + suffix + "@old.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 6 Migration')`, tenant, "phase6-mig-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Migration User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, user, email, "mig-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, unread, starred, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','mig-thread','Imported message',$4,'Body',true,true,now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "mig-msg-"+suffix, tenant, user, source)
	mustExec(`INSERT INTO public.attachments (id, tenant_id, user_id, name, mime, size_bytes) VALUES ($1,$2,$3,'contract.pdf','application/pdf',100)`, "mig-att-"+suffix, tenant, user)
	mustExec(`INSERT INTO public.mail_message_attachments (message_id, attachment_id) VALUES ($1,$2)`, "mig-msg-"+suffix, "mig-att-"+suffix)
	mustExec(`INSERT INTO public.calendar_events (id, tenant_id, user_id, calendar_id, uid, title, starts_at, ends_at)
		VALUES ($1,$2,$3,'default','mig-uid','Imported event',now(),now()+interval '1 hour')`, "mig-event-"+suffix, tenant, user)
	mustExec(`INSERT INTO public.mail_import_jobs (id, tenant_id, created_by, created_by_user, protocol, host, port, security, status, total_items, validated_items, completed_items, imported_messages)
		VALUES ($1,$2,'owner',$3,'imap','old.example.test',993,'ssl','completed',1,1,1,1)`, jobID, tenant, user)
	mustExec(`INSERT INTO public.mail_import_items (id, job_id, tenant_id, source_email, target_mode, target_user_id, target_email, status, imported_messages, total_messages)
		VALUES ($1,$2,$3,$4,'existing',$5,$6,'completed',1,1)`, "migitem-"+suffix, jobID, tenant, source, user, email)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Migration User", Role: "owner"}}
	request := httptest.NewRequest(http.MethodGet, "/admin/api/mail-import/jobs/"+jobID+"/fidelity", nil)
	request.SetPathValue("id", jobID)
	request = request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	recorder := httptest.NewRecorder()
	server.adminMigrationFidelity(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("fidelity = %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"targetMessages":1`) || !strings.Contains(body, `"attachments":1`) ||
		!strings.Contains(body, `"calendarEvents":1`) || !strings.Contains(body, `"messageCountMatch":true`) {
		t.Fatalf("fidelity body = %s", body)
	}
	var reports int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.migration_reports WHERE tenant_id=$1 AND job_id=$2`, tenant, jobID).Scan(&reports)
	if reports != 1 {
		t.Fatalf("migration report rows = %d", reports)
	}
}
