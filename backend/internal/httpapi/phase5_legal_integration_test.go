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

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase5LegalGovernanceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 5 legal test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4502-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4503-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "legal-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name, settings) VALUES ($1,$2,'Phase 5 Legal',$3)`, tenant, "phase5-legal-"+suffix, []byte(`{
		"files":{"retentionDays":1,"versionRetentionDays":1,"shareApprovalRequired":true,"dlpOnShare":true,"watermarkDownloads":true},
		"sharing":{"externalSharing":true,"linkDefault":"view"}
	}`))
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Legal User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, user, email, "legal-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, classification, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','legal-thread','Settlement memo','client@example.test','Privileged settlement details','Confidential',now()-interval '2 hours'
		FROM public.mailboxes m WHERE m.user_id=$3`, "legal-msg-"+suffix, tenant, user)
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, path, size_bytes, metadata, updated_at)
		VALUES ($1,$2,$3,'a8m','held.pdf','application/pdf','Files',100,$4,now()-interval '10 days')`,
		"legal-held-"+suffix, tenant, user, []byte(`{"dlpClassification":"Confidential"}`))
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, path, size_bytes, metadata, updated_at)
		VALUES ($1,$2,$3,'a8m','old.pdf','application/pdf','Files',100,$4,now()-interval '10 days')`,
		"legal-old-"+suffix, tenant, user, []byte(`{"dlpClassification":"Internal"}`))
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, path, size_bytes, metadata, updated_at)
		VALUES ($1,$2,$3,'a8m','share.pdf','application/pdf','Files',100,$4,now())`,
		"legal-share-"+suffix, tenant, user, []byte(`{"dlpClassification":"Internal"}`))
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, path, size_bytes, metadata, updated_at)
		VALUES ($1,$2,$3,'a8m','secret.pdf','application/pdf','Files',100,$4,now())`,
		"legal-secret-"+suffix, tenant, user, []byte(`{"dlpClassification":"Restricted"}`))
	mustExec(`INSERT INTO public.office_documents (id, tenant_id, user_id, file_id, format, document_type, current_version, current_key)
		VALUES ($1,$2,$3,$4,'pdf','pdf',1,'key')`, "legal-doc-"+suffix, tenant, user, "legal-held-"+suffix)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "phase5-session-secret"}}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Legal User", Role: "owner"}}
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	}

	holdBody, _ := json.Marshal(map[string]any{"scope": "file", "fileId": "legal-held-" + suffix, "name": "Matter 42", "reason": "Litigation"})
	holdRequest := withAdmin(httptest.NewRequest(http.MethodPost, "/admin/api/files/holds", bytes.NewBuffer(holdBody)))
	holdRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.adminCreateFileHold(recorder, holdRequest)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create hold = %d %s", recorder.Code, recorder.Body.String())
	}

	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Legal User", Role: "owner", Status: "active"}
	withUser := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: user, TenantID: tenant, User: userValue}))
	}
	deleteHeld := httptest.NewRequest(http.MethodDelete, "/api/files/legal-held-"+suffix, nil)
	deleteHeld.SetPathValue("id", "legal-held-"+suffix)
	recorder = httptest.NewRecorder()
	server.deleteFile(recorder, withUser(deleteHeld))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "LEGAL_HOLD") {
		t.Fatalf("delete held file = %d %s", recorder.Code, recorder.Body.String())
	}

	stats, err := server.runFileRetention(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if stats["files"] != 1 || stats["skippedHolds"] < 1 {
		t.Fatalf("retention stats = %#v", stats)
	}
	var heldCount, oldCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.files WHERE id=$1`, "legal-held-"+suffix).Scan(&heldCount)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.files WHERE id=$1`, "legal-old-"+suffix).Scan(&oldCount)
	if heldCount != 1 || oldCount != 0 {
		t.Fatalf("retention held=%d old=%d", heldCount, oldCount)
	}

	// Share requests require administrator approval.
	shareBody := []byte(`{"access":"view","expiresDays":7,"recipientEmail":"client@example.test"}`)
	shareRequest := httptest.NewRequest(http.MethodPost, "/api/files/legal-share-"+suffix+"/share", bytes.NewBuffer(shareBody))
	shareRequest.Header.Set("Content-Type", "application/json")
	shareRequest.SetPathValue("id", "legal-share-"+suffix)
	recorder = httptest.NewRecorder()
	server.shareFile(recorder, withUser(shareRequest))
	if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), `"pendingApproval":true`) {
		t.Fatalf("share request = %d %s", recorder.Code, recorder.Body.String())
	}
	var pending map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &pending)
	requestID := stateString(pending, "requestId")
	decide := withAdmin(httptest.NewRequest(http.MethodPost, "/admin/api/files/share-requests/"+requestID+"/decide", bytes.NewBufferString(`{"decision":"approve"}`)))
	decide.Header.Set("Content-Type", "application/json")
	decide.SetPathValue("id", requestID)
	recorder = httptest.NewRecorder()
	server.adminDecideFileShareRequest(recorder, decide)
	if recorder.Code != http.StatusOK {
		t.Fatalf("approve share = %d %s", recorder.Code, recorder.Body.String())
	}
	shareRequest = httptest.NewRequest(http.MethodPost, "/api/files/legal-share-"+suffix+"/share", bytes.NewBuffer(shareBody))
	shareRequest.Header.Set("Content-Type", "application/json")
	shareRequest.SetPathValue("id", "legal-share-"+suffix)
	recorder = httptest.NewRecorder()
	server.shareFile(recorder, withUser(shareRequest))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"share"`) {
		t.Fatalf("approved share = %d %s", recorder.Code, recorder.Body.String())
	}

	secretShare := httptest.NewRequest(http.MethodPost, "/api/files/legal-secret-"+suffix+"/share", bytes.NewBuffer(shareBody))
	secretShare.Header.Set("Content-Type", "application/json")
	secretShare.SetPathValue("id", "legal-secret-"+suffix)
	recorder = httptest.NewRecorder()
	server.shareFile(recorder, withUser(secretShare))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "SHARE_DLP_BLOCKED") {
		t.Fatalf("restricted share = %d %s", recorder.Code, recorder.Body.String())
	}

	// Cross-domain discovery and reproducible signed JSON export.
	discovery := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/api/legal/discovery/all?q=settlement", nil))
	recorder = httptest.NewRecorder()
	server.adminDiscoveryAll(recorder, discovery)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Settlement memo") {
		t.Fatalf("discovery all = %d %s", recorder.Code, recorder.Body.String())
	}
	export := func() map[string]any {
		request := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/api/legal/export?format=json&case=Matter42&q=settlement", nil))
		recorder := httptest.NewRecorder()
		server.adminLegalExport(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("export = %d %s", recorder.Code, recorder.Body.String())
		}
		var response map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return response
	}
	firstExport := export()
	secondExport := export()
	firstManifest, _ := firstExport["manifest"].(map[string]any)
	secondManifest, _ := secondExport["manifest"].(map[string]any)
	if stateString(firstManifest, "contentSha256") == "" || stateString(firstManifest, "contentSha256") != stateString(secondManifest, "contentSha256") {
		t.Fatalf("export hashes differ: %s / %s", stateString(firstManifest, "contentSha256"), stateString(secondManifest, "contentSha256"))
	}
	if stateString(firstExport, "signature") == "" {
		t.Fatal("export signature missing")
	}
	var exportCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.discovery_exports WHERE tenant_id=$1`, tenant).Scan(&exportCount)
	if exportCount != 2 {
		t.Fatalf("export rows = %d", exportCount)
	}
	mbox := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/api/legal/export?format=mbox&q=settlement", nil))
	recorder = httptest.NewRecorder()
	server.adminLegalExport(recorder, mbox)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Subject: Settlement memo") || recorder.Header().Get("X-A8M-Signature") == "" {
		t.Fatalf("mbox export = %d %s", recorder.Code, recorder.Body.String())
	}

	// Review queue.
	reviewBody, _ := json.Marshal(map[string]any{"items": []map[string]any{
		{"itemType": "mail", "itemId": "legal-msg-" + suffix, "subject": "Settlement memo"},
		{"itemType": "file", "itemId": "legal-held-" + suffix, "subject": "held.pdf"},
	}})
	reviewRequest := withAdmin(httptest.NewRequest(http.MethodPost, "/admin/api/legal/reviews", bytes.NewBuffer(reviewBody)))
	reviewRequest.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.adminCreateDiscoveryReviews(recorder, reviewRequest)
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"created":2`) {
		t.Fatalf("review create = %d %s", recorder.Code, recorder.Body.String())
	}
	var reviewID string
	_ = pool.QueryRow(ctx, `SELECT id FROM public.discovery_review_items WHERE tenant_id=$1 AND item_type='mail'`, tenant).Scan(&reviewID)
	decision := withAdmin(httptest.NewRequest(http.MethodPatch, "/admin/api/legal/reviews/"+reviewID, bytes.NewBufferString(`{"status":"approved","note":"Responsive"}`)))
	decision.Header.Set("Content-Type", "application/json")
	decision.SetPathValue("id", reviewID)
	recorder = httptest.NewRecorder()
	server.adminDecideDiscoveryReview(recorder, decision)
	if recorder.Code != http.StatusOK {
		t.Fatalf("review decide = %d %s", recorder.Code, recorder.Body.String())
	}
	var reviewStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.discovery_review_items WHERE id=$1`, reviewID).Scan(&reviewStatus)
	if reviewStatus != "approved" {
		t.Fatalf("review status = %s", reviewStatus)
	}
}
