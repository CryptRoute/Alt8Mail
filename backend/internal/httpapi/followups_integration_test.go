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

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFollowUpIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the follow-up integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4037-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4038-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	client := "customer-" + suffix + "@example.test"
	email := "follow-" + suffix + "@follow.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Follow Test')`, tenant, "follow-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Follow User','member')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'follow.example.test',$4,$5)`,
		tenant, user, email, "follow-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','follow-thread','Waiting on pricing',$4,now()-interval '1 hour'
		FROM public.mailboxes m WHERE m.user_id=$3`, "follow-msg-"+suffix, tenant, user, client)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}, license: &licensing.License{Version: licensing.Version, ID: "follow-test", Edition: "enterprise", Seats: 10, Features: []string{FeatureFollowUps}, ExpiresAt: time.Now().Add(time.Hour)}}
	userRecord := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Follow User", Status: "active"}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: user, TenantID: tenant, User: userRecord}))
	}
	create := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/followups", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.createFollowUp(recorder, withSession(request))
		return recorder
	}
	if recorder := create(`{"threadId":"follow-thread","kind":"awaiting_reply","days":1}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	// A reply on the same thread after the follow-up was created resolves it.
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','follow-thread','Re: Waiting on pricing',$4,now()+interval '1 second'
		FROM public.mailboxes m WHERE m.user_id=$3`, "reply-msg-"+suffix, tenant, user, client)
	server.runFollowUpChecks(ctx)
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM public.follow_ups WHERE user_id=$1 AND kind='awaiting_reply'`, user).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("reply resolution status = %q err=%v", status, err)
	}
	// A due reminder notifies once and does not create a second notification.
	mustExec(`INSERT INTO public.follow_ups (id, tenant_id, user_id, owner_user_id, thread_id, kind, due_at, status)
		VALUES ($1,$2,$3,$3,'follow-thread','remind_me',now()-interval '1 minute','pending')`, "due-"+suffix, tenant, user)
	server.runFollowUpChecks(ctx)
	server.runFollowUpChecks(ctx)
	var notifications, notified int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.mail_notifications WHERE kind='followup' AND user_id=$1`, user).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("follow-up notifications = %d err=%v", notifications, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.follow_ups WHERE id=$1 AND status='notified'`, "due-"+suffix).Scan(&notified); err != nil || notified != 1 {
		t.Fatalf("notified count = %d err=%v", notified, err)
	}
}
