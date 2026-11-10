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

func TestPhase5AdvancedDLPIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 5 DLP test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4500-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4501-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "dlp-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name, settings) VALUES ($1,$2,'Phase 5 DLP',$3)`, tenant, "phase5-dlp-"+suffix, []byte(`{
		"mail":{"preventForward":true,"watermarkExternal":true,"retentionByClassification":{"Confidential":30}},
		"dlp":{"enabled":true,"defaultClassification":"Internal","rules":[
		  {"id":"card","name":"Card numbers","detector":"credit_card","terms":[],"fields":["body"],"classification":"Restricted","action":"block","direction":"outbound","enabled":true},
		  {"id":"iban","name":"IBAN","detector":"iban","terms":[],"fields":["body"],"classification":"Confidential","action":"warn","direction":"outbound","enabled":true}
		]}
	}`))
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'DLP User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, user, email, "dlp-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, classification, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','dlp-thread','Confidential plan','client@example.test','Confidential',now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "dlp-msg-"+suffix, tenant, user)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, classification, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','dlp-thread','Public plan','client@example.test','Public',now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "dlp-public-"+suffix, tenant, user)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "DLP User", Role: "owner", Status: "active"}
	policy := server.tenantPolicyFor(ctx, tenant)
	if blockedClass, blocked := server.forwardBlocked(ctx, userValue, "forward", "dlp-msg-"+suffix, policy); !blocked || blockedClass != "Confidential" {
		t.Fatalf("confidential forward blocked=%v class=%q", blocked, blockedClass)
	}
	if _, blocked := server.forwardBlocked(ctx, userValue, "forward", "dlp-public-"+suffix, policy); blocked {
		t.Fatal("public message forward was blocked")
	}
	if _, blocked := server.forwardBlocked(ctx, userValue, "reply", "dlp-msg-"+suffix, policy); blocked {
		t.Fatal("reply was blocked by prevent-forward")
	}

	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "DLP User"}}
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	}
	simulateBody, _ := json.Marshal(map[string]any{"subject": "Payment", "bodyText": "Use card 4111 1111 1111 1111 and IBAN GB82WEST12345698765432"})
	simulate := withAdmin(httptest.NewRequest(http.MethodPost, "/admin/api/dlp/simulate", bytes.NewBuffer(simulateBody)))
	simulate.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.adminSimulateDLP(recorder, simulate)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"blocked":true`) {
		t.Fatalf("simulate = %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "4111 1111 1111 1111") || strings.Contains(body, "GB82WEST") {
		t.Fatalf("simulation leaked detector values: %s", body)
	}
	if !strings.Contains(body, "credit_card") || !strings.Contains(body, "iban") {
		t.Fatalf("simulation detectors = %s", body)
	}

	reviewBody, _ := json.Marshal(map[string]any{"ruleId": "card", "detector": "credit_card", "matchPreview": "•••• 1111", "context": "Test simulation"})
	reviewRequest := withAdmin(httptest.NewRequest(http.MethodPost, "/admin/api/dlp/reviews", bytes.NewBuffer(reviewBody)))
	reviewRequest.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.adminCreateDLPReview(recorder, reviewRequest)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create review = %d %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	reviewID := stateString(response, "id")
	update := withAdmin(httptest.NewRequest(http.MethodPatch, "/admin/api/dlp/reviews/"+reviewID, bytes.NewBufferString(`{"status":"false_positive"}`)))
	update.Header.Set("Content-Type", "application/json")
	update.SetPathValue("id", reviewID)
	recorder = httptest.NewRecorder()
	server.adminUpdateDLPReview(recorder, update)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update review = %d %s", recorder.Code, recorder.Body.String())
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM public.dlp_false_positives WHERE id=$1`, reviewID).Scan(&status); err != nil || status != "false_positive" {
		t.Fatalf("review status = %q err=%v", status, err)
	}
}
