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

func TestAIPolicyRedaction(t *testing.T) {
	redacted := redactAIPolicyTerms("The TOPSECRET code and topsecret copy", []string{"TOPSECRET"})
	if strings.Contains(strings.ToUpper(redacted), "TOPSECRET") || !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("redaction = %q", redacted)
	}
	if again := redactAIPolicyTerms("[REDACTED]", []string{"[redacted]"}); again == "" {
		t.Fatal("redaction of the marker itself produced an empty result")
	}
}

func TestPhase4AIPolicyAssistIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 policy assist test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4420-8000-%012d", time.Now().UnixNano()%1000000000000)
	member := fmt.Sprintf("00000000-0000-4421-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	owner := fmt.Sprintf("00000000-0000-4422-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name, settings) VALUES ($1,$2,'Phase 4 Policy',$3)`, tenant, "phase4-policy-"+suffix,
		[]byte(`{"dlp":{"enabled":true,"defaultClassification":"Internal","rules":[{"id":"secret-rule","name":"Secrets","terms":["TOPSECRET"],"fields":["subject","body"],"classification":"Restricted","action":"block","direction":"outbound","enabled":true}]}}`))
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Policy Member','member')`, member, tenant, "policy-member-"+suffix+"@ai.example.test")
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Policy Owner','owner')`, owner, tenant, "policy-owner-"+suffix+"@ai.example.test")

	var seenBody strings.Builder
	var seenMu sync.Mutex
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seenMu.Lock()
		seenBody.Write(raw)
		seenMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"classification\":\"Confidential\",\"explanation\":\"Confidential content.\"}"}}]}`)
	}))
	defer aiServer.Close()
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model) VALUES ($1,true,'openai',$2,'policy-model')`, tenant, aiServer.URL)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	memberValue := userRecord{ID: member, TenantID: tenant, Email: "policy-member-" + suffix + "@ai.example.test", Name: "Policy Member", Role: "member", Status: "active"}
	ownerValue := userRecord{ID: owner, TenantID: tenant, Email: "policy-owner-" + suffix + "@ai.example.test", Name: "Policy Owner", Role: "owner", Status: "active"}
	withSession := func(request *http.Request, value userRecord) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: value.ID, TenantID: tenant, User: value}))
	}
	call := func(value userRecord, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPost, "/api/ai/policy-assist", bytes.NewBuffer(raw))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.aiPolicyAssist(recorder, withSession(request, value))
		return recorder
	}
	recorder := call(memberValue, map[string]any{"subject": "Code", "bodyText": "My TOPSECRET code is 1234"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("assist = %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "TOPSECRET") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("sensitive term leaked in response: %s", body)
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	if response["classification"] != "Restricted" || response["suggestedClassification"] != "Confidential" || response["aiUsed"] != true {
		t.Fatalf("assist response = %#v", response)
	}
	seenMu.Lock()
	sent := seenBody.String()
	seenMu.Unlock()
	if strings.Contains(sent, "TOPSECRET") || !strings.Contains(sent, "REDACTED") {
		t.Fatalf("sensitive content sent to the AI provider: %s", sent)
	}

	if recorder := call(memberValue, map[string]any{"bodyText": "My TOPSECRET code is 1234", "overrideClassification": "Public"}); recorder.Code != http.StatusForbidden {
		t.Fatalf("member override = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = call(ownerValue, map[string]any{"bodyText": "My TOPSECRET code is 1234", "overrideClassification": "Internal", "overrideReason": "Known test data"})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"overridden":true`) || !strings.Contains(recorder.Body.String(), `"effectiveClassification":"Internal"`) {
		t.Fatalf("owner override = %d %s", recorder.Code, recorder.Body.String())
	}
	var auditCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_log WHERE user_id=$1 AND action='ai.policy_override'`, owner).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("override audit rows = %d", auditCount)
	}
}
