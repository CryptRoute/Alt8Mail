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

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase4AISenderAndMeetingInsights(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 insights test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4414-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4415-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	other := fmt.Sprintf("00000000-0000-4416-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := "insight-" + suffix + "@ai.example.test"
	alice := "alice-" + suffix + "@client.example.test"
	threadID := "insight-thread-" + suffix
	eventID := "insight-event-" + suffix
	obligationID := "insight-oblig-" + suffix
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 4 Insights')`, tenant, "phase4-insight-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Insight User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','member')`, other, tenant, "other-insight-"+suffix+"@ai.example.test")
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'ai.example.test',$4,$5)`,
		tenant, user, email, "insight-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'ai.example.test',$4,$5)`,
		tenant, other, "other-insight-"+suffix+"@ai.example.test", "other-insight-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, from_name, snippet, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'inbox',$4,'Proposal for Q1',$5,'Alice','Here is the proposal','Here is the proposal for Q1',now()-interval '60 days'
		FROM public.mailboxes m WHERE m.user_id=$3`, "insight-old-"+suffix, tenant, user, threadID, alice)
	mustExec(`INSERT INTO public.attachments (id, tenant_id, user_id, name, mime, size_bytes) VALUES ($1,$2,$3,'proposal.pdf','application/pdf',1234)`, "att-"+suffix, tenant, user)
	mustExec(`INSERT INTO public.mail_message_attachments (message_id, attachment_id) VALUES ($1,$2)`, "insight-old-"+suffix, "att-"+suffix)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, from_name, snippet, unread, sent_at)
		SELECT $1,$2,$3,m.id,'inbox',$4,'Re: Proposal for Q1',$5,'Alice','Can we review this?',false,now()-interval '5 days'
		FROM public.mailboxes m WHERE m.user_id=$3`, "insight-new-"+suffix, tenant, user, threadID, alice)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, to_json, snippet, unread, sent_at)
		SELECT $1,$2,$3::uuid,m.id,'sent',$4,'Re: Proposal for Q1',$3::citext,jsonb_build_array(jsonb_build_object('email',$5::text)),'Sending the note',false,now()-interval '3 days'
		FROM public.mailboxes m WHERE m.user_id=$3::uuid`, "insight-out-"+suffix, tenant, user, threadID, alice)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, snippet, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','other-thread','Other user mail',$4,'private',now()-interval '1 day'
		FROM public.mailboxes m WHERE m.user_id=$3`, "insight-other-"+suffix, tenant, other, alice)
	mustExec(`INSERT INTO public.ai_obligations (id, tenant_id, user_id, thread_id, kind, text, due, dedupe_key)
		VALUES ($1,$2,$3,$4,'request','Review the Q1 proposal','2026-12-01',$5)`, obligationID, tenant, user, threadID, "oblig-"+suffix)
	mustExec(`INSERT INTO public.calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, starts_at, ends_at)
		VALUES ($1,$2,$3,'default',$4,'Quarterly review','Discuss the Q1 proposal',now()+interval '1 day',now()+interval '1 day 1 hour')`, eventID, tenant, user, "uid-"+suffix)
	mustExec(`INSERT INTO public.calendar_attendees (event_id, email, name, status) VALUES ($1,$2,'Alice','accepted')`, eventID, alice)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Insight User", Status: "active"}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: user, TenantID: tenant, User: userValue}))
	}
	profile := func(target string) *httptest.ResponseRecorder {
		request := withSession(httptest.NewRequest(http.MethodGet, "/api/ai/sender-profile?email="+target, nil))
		recorder := httptest.NewRecorder()
		server.aiSenderProfile(recorder, request)
		return recorder
	}
	recorder := profile(alice)
	if recorder.Code != http.StatusOK {
		t.Fatalf("profile = %d %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	counts, _ := response["counts"].(map[string]any)
	if fmt.Sprint(counts["total"]) != "3" || fmt.Sprint(counts["unread"]) != "1" {
		t.Fatalf("profile counts = %#v", counts)
	}
	if !strings.Contains(recorder.Body.String(), "proposal.pdf") || !strings.Contains(recorder.Body.String(), "Review the Q1 proposal") {
		t.Fatalf("profile missing attachments/obligations: %s", recorder.Body.String())
	}
	unknown := profile("nobody-" + suffix + "@nowhere.example.test")
	if unknown.Code != http.StatusOK || !strings.Contains(unknown.Body.String(), `"missing":true`) || strings.Contains(unknown.Body.String(), "Q1") {
		t.Fatalf("unknown profile = %d %s", unknown.Code, unknown.Body.String())
	}

	// The meeting brief sees the attendee's mailbox history and obligations.
	meeting := withSession(httptest.NewRequest(http.MethodGet, "/api/ai/meeting-prep?eventId="+eventID, nil))
	recorder = httptest.NewRecorder()
	server.aiMeetingPrep(recorder, meeting)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Quarterly review") ||
		!strings.Contains(recorder.Body.String(), alice) || !strings.Contains(recorder.Body.String(), "Review the Q1 proposal") {
		t.Fatalf("meeting prep = %d %s", recorder.Code, recorder.Body.String())
	}
	missingEvent := withSession(httptest.NewRequest(http.MethodGet, "/api/ai/meeting-prep?eventId=missing-"+suffix, nil))
	recorder = httptest.NewRecorder()
	server.aiMeetingPrep(recorder, missingEvent)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing event = %d %s", recorder.Code, recorder.Body.String())
	}

	// Enabling the provider adds a grounded summary, but structured facts remain if AI fails.
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Alice last wrote five days ago about the Q1 proposal. A review remains open."}}]}`)
	}))
	defer aiServer.Close()
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model) VALUES ($1,true,'openai',$2,'insights-model')`, tenant, aiServer.URL)
	recorder = profile(alice)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"aiUsed":true`) || !strings.Contains(recorder.Body.String(), "A review remains open") {
		t.Fatalf("AI profile = %d %s", recorder.Code, recorder.Body.String())
	}
}
