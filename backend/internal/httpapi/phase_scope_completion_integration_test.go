package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhaseScopeCompletionIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the phase scope completion test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4617-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4618-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "scope-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Scope Completion')`, tenant, "scope-completion-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Scope User','owner')`, userID, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, userID, email, "scope-"+suffix, t.TempDir())
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "scope-completion-secret", UploadDir: t.TempDir()},
		license: &licensing.License{Version: licensing.Version, ID: "scope-test", Edition: "enterprise", Seats: 10,
			Features: []string{"all"}, ExpiresAt: time.Now().Add(time.Hour)}}
	user, err := server.userByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	sess := &sessionRecord{UserID: userID, TenantID: tenant, User: user}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, sess))
	}

	t.Run("task recurrence and reminder", func(t *testing.T) {
		taskID := "recur-" + suffix
		mustExec(`INSERT INTO public.tasks (id, tenant_id, user_id, title, notes, status, priority, due_date, due_time, recurrence_rule)
			VALUES ($1,$2,$3,'Recurring task','notes','done','normal',current_date,'09:00','FREQ=DAILY;COUNT=3')`, taskID, tenant, userID)
		if err := server.createNextRecurringTask(ctx, sess, taskID); err != nil {
			t.Fatal(err)
		}
		var childID, childDue, parent string
		err := pool.QueryRow(ctx, `SELECT id, due_date::text, COALESCE(parent_task_id,'') FROM public.tasks WHERE user_id=$1 AND parent_task_id=$2 ORDER BY created_at LIMIT 1`, userID, taskID).Scan(&childID, &childDue, &parent)
		if err != nil || childID == "" || parent != taskID {
			t.Fatalf("recurring child id=%q parent=%q err=%v", childID, parent, err)
		}
		if err := server.createNextRecurringTask(ctx, sess, taskID); err != nil {
			t.Fatal(err)
		}
		var children int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.tasks WHERE parent_task_id=$1`, taskID).Scan(&children)
		if children != 1 {
			t.Fatalf("recurrence created %d children", children)
		}
		reminderID := "reminder-" + suffix
		mustExec(`INSERT INTO public.tasks (id, tenant_id, user_id, title, notes, status, priority, reminder_at, recurrence_rule)
			VALUES ($1,$2,$3,'Remind me','body','open','normal',now() - interval '1 minute','FREQ=DAILY')`, reminderID, tenant, userID)
		server.runTaskReminders(ctx)
		var notified bool
		_ = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.mail_notifications WHERE user_id=$1 AND kind='task' AND title LIKE '%Remind me%')`, userID).Scan(&notified)
		if !notified {
			t.Fatal("task reminder did not create a notification")
		}
		var sent bool
		_ = pool.QueryRow(ctx, `SELECT reminder_sent_at IS NOT NULL FROM public.tasks WHERE id=$1`, reminderID).Scan(&sent)
		if !sent {
			t.Fatal("task reminder was not marked as sent")
		}
	})

	t.Run("direct cabinet upload for share fallback", func(t *testing.T) {
		raw := []byte("large-file-share-fallback")
		body, _ := json.Marshal(map[string]any{
			"providerId": "a8m", "name": "large-share.bin", "mime": "application/octet-stream",
			"dataBase64": base64.StdEncoding.EncodeToString(raw), "path": "Files",
			"description": "Shared from compose after reaching the attachment limit",
		})
		request := httptest.NewRequest(http.MethodPost, "/api/files", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.createFile(recorder, withSession(request))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("direct cabinet upload = %d %s", recorder.Code, recorder.Body.String())
		}
		var fileID, storageKey string
		var size int64
		var payload struct {
			File map[string]any `json:"file"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		fileID = fmt.Sprint(payload.File["id"])
		if err := pool.QueryRow(ctx, `SELECT storage_key, size_bytes FROM public.files WHERE id=$1 AND user_id=$2`, fileID, userID).Scan(&storageKey, &size); err != nil {
			t.Fatal(err)
		}
		if size != int64(len(raw)) || storageKey == "" {
			t.Fatalf("direct upload size=%d key=%q", size, storageKey)
		}
	})

	t.Run("chunked cabinet upload for share fallback", func(t *testing.T) {
		raw := []byte(strings.Repeat("chunked-cabinet-upload-", 70000))
		startBody, _ := json.Marshal(map[string]any{
			"providerId": "a8m", "name": "large-chunked.bin", "mime": "application/octet-stream",
			"sizeBytes": len(raw), "path": "Files",
			"description": "Shared from compose after reaching the attachment limit",
		})
		request := httptest.NewRequest(http.MethodPost, "/api/files/uploads", strings.NewReader(string(startBody)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.startFileUpload(recorder, withSession(request))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("start chunked upload = %d %s", recorder.Code, recorder.Body.String())
		}
		var started struct {
			UploadID  string `json:"uploadId"`
			ChunkSize int64  `json:"chunkSize"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil || started.UploadID == "" || started.ChunkSize <= 0 {
			t.Fatalf("start chunked upload payload = %s", recorder.Body.String())
		}
		bad := httptest.NewRequest(http.MethodPut, "/api/files/uploads/"+started.UploadID, strings.NewReader("short"))
		bad.SetPathValue("id", started.UploadID)
		bad.Header.Set("X-A8M-Upload-Offset", "9")
		badRecorder := httptest.NewRecorder()
		server.putFileUploadChunk(badRecorder, withSession(bad))
		if badRecorder.Code != http.StatusConflict {
			t.Fatalf("mismatched chunk offset = %d %s", badRecorder.Code, badRecorder.Body.String())
		}
		chunkSize := 512 * 1024
		for offset := 0; offset < len(raw); offset += chunkSize {
			end := offset + chunkSize
			if end > len(raw) {
				end = len(raw)
			}
			chunk := httptest.NewRequest(http.MethodPut, "/api/files/uploads/"+started.UploadID, bytes.NewReader(raw[offset:end]))
			chunk.SetPathValue("id", started.UploadID)
			chunk.Header.Set("X-A8M-Upload-Offset", fmt.Sprint(offset))
			chunkRecorder := httptest.NewRecorder()
			server.putFileUploadChunk(chunkRecorder, withSession(chunk))
			if chunkRecorder.Code != http.StatusOK {
				t.Fatalf("upload chunk at %d = %d %s", offset, chunkRecorder.Code, chunkRecorder.Body.String())
			}
		}
		complete := httptest.NewRequest(http.MethodPost, "/api/files/uploads/"+started.UploadID+"/complete", strings.NewReader("{}"))
		complete.SetPathValue("id", started.UploadID)
		completeRecorder := httptest.NewRecorder()
		server.completeFileUpload(completeRecorder, withSession(complete))
		if completeRecorder.Code != http.StatusCreated {
			t.Fatalf("complete chunked upload = %d %s", completeRecorder.Code, completeRecorder.Body.String())
		}
		var payload struct {
			File map[string]any `json:"file"`
		}
		_ = json.Unmarshal(completeRecorder.Body.Bytes(), &payload)
		fileID := fmt.Sprint(payload.File["id"])
		var storageKey string
		var size int64
		if err := pool.QueryRow(ctx, `SELECT storage_key, size_bytes FROM public.files WHERE id=$1 AND user_id=$2`, fileID, userID).Scan(&storageKey, &size); err != nil {
			t.Fatal(err)
		}
		if size != int64(len(raw)) || storageKey == "" {
			t.Fatalf("chunked upload size=%d key=%q", size, storageKey)
		}
		oversized, _ := json.Marshal(map[string]any{"name": "too-big.bin", "sizeBytes": maxCabinetUploadBytes + 1})
		bigRequest := httptest.NewRequest(http.MethodPost, "/api/files/uploads", strings.NewReader(string(oversized)))
		bigRequest.Header.Set("Content-Type", "application/json")
		bigRecorder := httptest.NewRecorder()
		server.startFileUpload(bigRecorder, withSession(bigRequest))
		if bigRecorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized chunked upload = %d %s", bigRecorder.Code, bigRecorder.Body.String())
		}
	})

	t.Run("calendar resource approval", func(t *testing.T) {
		resourceID := "room-" + suffix
		eventID := "resource-event-" + suffix
		mustExec(`INSERT INTO public.calendar_resources (id, tenant_id, name, email, kind, created_by, requires_approval, approver_user_id)
			VALUES ($1,$2,'Board room',$3,'room',$4,true,$4)`, resourceID, tenant, "room-"+suffix+"@resources.local", userID)
		mustExec(`INSERT INTO public.calendar_events (id, tenant_id, user_id, calendar_id, uid, title, starts_at, ends_at, timezone, status)
			VALUES ($1,$2,$3,'default',$1,'Board meeting',now() + interval '2 days', now() + interval '2 days 1 hour','UTC','confirmed')`, eventID, tenant, userID)
		mustExec(`INSERT INTO public.calendar_event_resources (event_id, resource_id, name, email, status)
			VALUES ($1,$2,'Board room',$3,'pending')`, eventID, resourceID, "room-"+suffix+"@resources.local")
		body, _ := json.Marshal(map[string]any{"decision": "approve", "note": "Approved for the meeting"})
		request := httptest.NewRequest(http.MethodPost, "/api/calendar/resources/approvals/"+eventID+"/"+resourceID, strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("eventId", eventID)
		request.SetPathValue("resourceId", resourceID)
		recorder := httptest.NewRecorder()
		server.decideCalendarResourceApproval(recorder, withSession(request))
		if recorder.Code != http.StatusOK {
			t.Fatalf("resource approval = %d %s", recorder.Code, recorder.Body.String())
		}
		var status string
		_ = pool.QueryRow(ctx, `SELECT status FROM public.calendar_event_resources WHERE event_id=$1 AND resource_id=$2`, eventID, resourceID).Scan(&status)
		if status != "accepted" {
			t.Fatalf("resource status = %s", status)
		}
	})

	t.Run("api cursor pagination", func(t *testing.T) {
		for index := 0; index < 5; index++ {
			mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, folder, thread_id, subject, from_email, body_text, snippet, sent_at)
				VALUES ($1,$2,$3,'inbox',$1,$4,'sender@client.example.test','body','snippet',now() - ($5 * interval '1 minute'))`,
				fmt.Sprintf("cursor-%s-%d", suffix, index), tenant, userID, fmt.Sprintf("Cursor %d", index), index)
		}
		principal := apiPrincipal{KeyID: "key", TenantID: tenant, UserID: userID, User: user, Scopes: []string{"mail.read"}}
		call := func(query string) ([]map[string]any, string) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/messages?limit=2&"+query, nil)
			request = request.WithContext(context.WithValue(request.Context(), apiPrincipalContextKey{}, principal))
			recorder := httptest.NewRecorder()
			server.apiV1ListMessages(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("cursor list = %d %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Items      []map[string]any `json:"items"`
				NextCursor string           `json:"nextCursor"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			return payload.Items, payload.NextCursor
		}
		first, cursor := call("")
		if len(first) != 2 || cursor == "" {
			t.Fatalf("first page len=%d cursor=%q", len(first), cursor)
		}
		second, _ := call("cursor=" + cursor)
		if len(second) != 2 {
			t.Fatalf("second page len=%d", len(second))
		}
		if first[0]["id"] == second[0]["id"] {
			t.Fatal("cursor returned overlapping messages")
		}
	})
}
