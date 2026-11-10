package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase5FileTextIndexIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 5 file index test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4509-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4510-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "index-" + suffix + "@gov.example.test"
	uploadDir := t.TempDir()
	textKey := "index-" + suffix + ".txt"
	if err := os.WriteFile(filepath.Join(uploadDir, textKey), []byte("Quarterly forecast and board notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	imageKey := "scan-" + suffix + ".png"
	if err := os.WriteFile(filepath.Join(uploadDir, imageKey), []byte("not really a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 5 Index')`, tenant, "phase5-index-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Index User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, path, size_bytes, storage_key)
		VALUES ($1,$2,$3,'a8m','forecast.txt','text/plain','Files',35,$4)`, "index-text-"+suffix, tenant, user, textKey)
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, path, size_bytes, storage_key)
		VALUES ($1,$2,$3,'a8m','scan.png','image/png','Files',16,$4)`, "index-image-"+suffix, tenant, user, imageKey)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{UploadDir: uploadDir}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Index User", Status: "active"}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: user, TenantID: tenant, User: userValue}))
	}
	index := func(id string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/index", nil)
		request.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		server.indexFileContent(recorder, withSession(request))
		return recorder
	}
	recorder := index("index-text-" + suffix)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"source":"text"`) {
		t.Fatalf("index text = %d %s", recorder.Code, recorder.Body.String())
	}
	search := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/files/search?q="+query, nil)
		recorder := httptest.NewRecorder()
		server.searchIndexedFiles(recorder, withSession(request))
		return recorder
	}
	if recorder := search("forecast"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "forecast.txt") {
		t.Fatalf("search = %d %s", recorder.Code, recorder.Body.String())
	}
	if runtime.GOOS == "windows" {
		t.Setenv("A8M_OCR_COMMAND", "cmd /C echo SCANNED INVOICE 42")
	} else {
		t.Setenv("A8M_OCR_COMMAND", "echo SCANNED INVOICE 42")
	}
	recorder = index("index-image-" + suffix)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"source":"ocr"`) {
		t.Fatalf("index ocr = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := search("invoice"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "scan.png") {
		t.Fatalf("ocr search = %d %s", recorder.Code, recorder.Body.String())
	}
	var source string
	if err := pool.QueryRow(ctx, `SELECT source FROM public.file_text_index WHERE file_id=$1`, "index-image-"+suffix).Scan(&source); err != nil || source != "ocr" {
		t.Fatalf("stored source = %q err=%v", source, err)
	}
}
