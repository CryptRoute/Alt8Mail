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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase4OfficeAIIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 Office AI test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4417-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4418-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	other := fmt.Sprintf("00000000-0000-4419-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := "officeai-" + suffix + "@ai.example.test"
	uploadDir := t.TempDir()
	storageKey := "officeai-" + suffix + ".txt"
	documentText := "Project Atlas launches on 2026-12-01. The SECRET budget is 40k. Alice will prepare the launch checklist."
	if err := os.WriteFile(filepath.Join(uploadDir, storageKey), []byte(documentText), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name, settings) VALUES ($1,$2,'Phase 4 Office AI',$3)`, tenant, "phase4-officeai-"+suffix,
		[]byte(`{"dlp":{"enabled":true,"rules":[{"id":"secret-rule","name":"Secrets","terms":["SECRET"],"fields":["body"],"classification":"Restricted","action":"block","direction":"outbound","enabled":true}]}}`))
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Office AI User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','member')`, other, tenant, "other-officeai-"+suffix+"@ai.example.test")
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, size_bytes, storage_key)
		VALUES ($1,$2,$3,'a8m','notes.txt','text/plain',$4,$5)`, "file-"+suffix, tenant, user, len(documentText), storageKey)
	mustExec(`INSERT INTO public.office_documents (id, tenant_id, user_id, file_id, format, document_type, current_version, current_key)
		VALUES ($1,$2,$3,$4,'txt','word',1,'key-1')`, "doc-"+suffix, tenant, user, "file-"+suffix)
	mustExec(`INSERT INTO public.office_document_versions (id, document_id, version, storage_key, size_bytes, checksum, source)
		VALUES ($1,$2,1,$3,$4,'sum','initial')`, "ver-"+suffix, "doc-"+suffix, storageKey, len(documentText))

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := string(raw)
		content := "The document does not contain that information."
		if strings.Contains(request, "You answer questions") {
			content = `{"answer":"Project Atlas launches on 2026-12-01.","citations":[{"quote":"Project Atlas launches on 2026-12-01."},{"quote":"The budget is one million dollars."}]}`
		} else if strings.Contains(request, "You extract action items") {
			content = `{"items":[{"task":"Prepare the launch checklist","owner":"Alice","due":"","quote":"Alice will prepare the launch checklist."},{"task":"Book a venue","owner":"","due":"","quote":"Book the Grand Hall for the launch."}]}`
		} else if strings.Contains(request, "You draft a professional reply") {
			if strings.Contains(request, "SAFE_DRAFT") {
				content = "Thanks, noted. I will review the launch plan."
			} else {
				content = "The SECRET budget is 40k."
			}
		}
		encoded, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, encoded)
	}))
	defer aiServer.Close()
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model) VALUES ($1,true,'openai',$2,'office-model')`, tenant, aiServer.URL)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{UploadDir: uploadDir}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Office AI User", Status: "active"}
	withSession := func(request *http.Request, value userRecord) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: value.ID, TenantID: tenant, User: value}))
	}
	call := func(value userRecord, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPost, "/api/office/ai", bytes.NewBuffer(raw))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.officeAI(recorder, withSession(request, value))
		return recorder
	}
	recorder := call(userValue, map[string]any{"action": "ask", "documentId": "doc-" + suffix, "prompt": "When does Atlas launch?"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("ask = %d %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	citations, _ := response["citations"].([]any)
	if fmt.Sprint(response["text"]) != "Project Atlas launches on 2026-12-01." || len(citations) != 1 {
		t.Fatalf("ask response = %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "one million dollars") {
		t.Fatalf("fabricated citation survived verification: %s", recorder.Body.String())
	}

	recorder = call(userValue, map[string]any{"action": "action_items", "documentId": "doc-" + suffix})
	if recorder.Code != http.StatusOK {
		t.Fatalf("action items = %d %s", recorder.Code, recorder.Body.String())
	}
	response = map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	items, _ := response["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("action items = %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Book the Grand Hall") {
		t.Fatalf("fabricated action-item citation survived: %s", recorder.Body.String())
	}

	recorder = call(userValue, map[string]any{"action": "draft_reply", "documentId": "doc-" + suffix, "prompt": "Write a reply"})
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "OFFICE_DLP_BLOCKED") || !strings.Contains(recorder.Body.String(), `"blocked":true`) {
		t.Fatalf("blocked draft = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = call(userValue, map[string]any{"action": "draft_reply", "documentId": "doc-" + suffix, "prompt": "SAFE_DRAFT acknowledgement"})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Thanks, noted") || strings.Contains(recorder.Body.String(), `"blocked":true`) {
		t.Fatalf("safe draft = %d %s", recorder.Code, recorder.Body.String())
	}

	otherValue := userRecord{ID: other, TenantID: tenant, Email: "other-officeai-" + suffix + "@ai.example.test", Status: "active"}
	if recorder := call(otherValue, map[string]any{"action": "ask", "documentId": "doc-" + suffix, "prompt": "What is the budget?"}); recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-user document access = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(userValue, map[string]any{"action": "ask", "prompt": "No document"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing document = %d %s", recorder.Code, recorder.Body.String())
	}
}
