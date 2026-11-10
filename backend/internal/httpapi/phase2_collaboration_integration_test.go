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

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPhase2CollaborationIntegration requires A8M_SCIM_TEST_DSN with all
// migrations, including 0068_phase2_collaboration.
func TestPhase2CollaborationIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the collaboration integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4032-8000-%012d", time.Now().UnixNano()%1000000000000)
	owner := fmt.Sprintf("00000000-0000-4033-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	approver := fmt.Sprintf("00000000-0000-4034-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := func(prefix string) string { return prefix + "-" + suffix + "@collab.example.test" }
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Collab Test')`, tenant, "collab-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES
		($1,$2,$3,'Owner','owner'), ($4,$2,$5,'Approver','member')`, owner, tenant, email("owner"), approver, email("approver"))
	fileID := "file_" + suffix
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, size_bytes, storage_key, version, previewable, checksum)
		VALUES ($1,$2,$3,'a8m','Plan.pdf','application/pdf',1234,'storage/key.pdf',1,true,'abc')`, fileID, tenant, owner)
	taskOne := "task1_" + suffix
	taskTwo := "task2_" + suffix
	mustExec(`INSERT INTO public.tasks (id, tenant_id, user_id, title, status) VALUES ($1,$2,$3,'Parent','open'), ($4,$2,$3,'Child','open')`, taskOne, tenant, owner, taskTwo)
	ownerUser := userRecord{ID: owner, TenantID: tenant, Email: email("owner"), Name: "Owner", Role: "owner", Status: "active"}
	approverUser := userRecord{ID: approver, TenantID: tenant, Email: email("approver"), Name: "Approver", Role: "member", Status: "active"}
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}, mailLocks: map[string]*sync.Mutex{}}
	withSession := func(request *http.Request, user userRecord) *http.Request {
		sess := &sessionRecord{UserID: user.ID, TenantID: user.TenantID, User: user}
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, sess))
	}
	jsonRequest := func(method, target, id, body string) *http.Request {
		request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		request.SetPathValue("id", id)
		request.Header.Set("Content-Type", "application/json")
		return withSession(request, ownerUser)
	}

	// Task extras: parent/dependency validation and cycle rejection.
	parentID := taskOne
	extras, err := server.validateTaskExtras(ctx, &sessionRecord{User: ownerUser}, taskBody{ParentTaskID: &parentID}, nil, taskTwo, "open")
	if err != nil || extras.ParentID != taskOne {
		t.Fatalf("task extras = %#v err=%v", extras, err)
	}
	mustExec(`UPDATE public.tasks SET parent_task_id=$2 WHERE id=$1`, taskTwo, taskOne)
	childID := taskTwo
	if _, err := server.validateTaskExtras(ctx, &sessionRecord{User: ownerUser}, taskBody{ParentTaskID: &childID}, nil, taskOne, "open"); err == nil {
		t.Fatal("task parent cycle was accepted")
	}

	// File version snapshot and listing.
	recorder := httptest.NewRecorder()
	server.snapshotFileVersion(recorder, jsonRequest(http.MethodPost, "/api/files/"+fileID+"/versions", fileID, ""))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("snapshot status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.listFileVersions(recorder, jsonRequest(http.MethodGet, "/api/files/"+fileID+"/versions", fileID, ""))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"version":1`) {
		t.Fatalf("versions = %d %s", recorder.Code, recorder.Body.String())
	}

	// File comments and approvals.
	recorder = httptest.NewRecorder()
	server.addFileComment(recorder, jsonRequest(http.MethodPost, "/api/files/"+fileID+"/comments", fileID, `{"body":"Looks good"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("comment status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.requestFileApproval(recorder, jsonRequest(http.MethodPost, "/api/files/"+fileID+"/approvals", fileID, `{"approverEmail":"`+approverUser.Email+`","note":"Please review"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("approval status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var approvalID string
	if err := pool.QueryRow(ctx, `SELECT id FROM public.file_approvals WHERE file_id=$1`, fileID).Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files/"+fileID+"/approvals/"+approvalID+"/decide", bytes.NewBufferString(`{"status":"approved"}`))
	request.SetPathValue("id", fileID)
	request.SetPathValue("approvalId", approvalID)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.decideFileApproval(recorder, withSession(request, approverUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("decision status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM public.file_approvals WHERE id=$1`, approvalID).Scan(&status); err != nil || status != "approved" {
		t.Fatalf("approval status = %q err=%v", status, err)
	}
}
