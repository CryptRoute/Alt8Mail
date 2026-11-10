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
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase5BYOKIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 5 BYOK test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4504-8000-%012d", time.Now().UnixNano()%1000000000000)
	otherTenant := fmt.Sprintf("00000000-0000-4505-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	user := fmt.Sprintf("00000000-0000-4506-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := "byok-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id IN ($1,$2)`, tenant, otherTenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 5 BYOK')`, tenant, "phase5-byok-"+suffix)
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 5 Other')`, otherTenant, "phase5-other-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'BYOK User','owner')`, user, tenant, email)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "phase5-byok-secret"}}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "BYOK User", Role: "owner"}}
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	}
	call := func(handler http.HandlerFunc, method, path string, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = bytes.NewBufferString(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler(recorder, withAdmin(request))
		return recorder
	}

	if recorder := call(server.adminGetEncryption, http.MethodGet, "/admin/api/encryption", ""); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"enabled":false`) {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
	customerKey := bytes.Repeat([]byte{0x42}, 32)
	encodedKey := base64.StdEncoding.EncodeToString(customerKey)
	createBody, _ := json.Marshal(map[string]any{"customerKey": encodedKey})
	if recorder := call(server.adminCreateEncryptionKey, http.MethodPost, "/admin/api/encryption/keys", string(createBody)); recorder.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", recorder.Code, recorder.Body.String())
	}
	aiBody := `{"enabled":true,"provider":"openai","model":"test-model","apiKey":"sk-byok-test"}`
	if recorder := call(server.adminUpdateAI, http.MethodPatch, "/admin/api/ai", aiBody); recorder.Code != http.StatusOK {
		t.Fatalf("save ai = %d %s", recorder.Code, recorder.Body.String())
	}
	var sealed string
	if err := pool.QueryRow(ctx, `SELECT api_key_sealed FROM public.tenant_ai_settings WHERE tenant_id=$1`, tenant).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "byok:v1:1:") {
		t.Fatalf("sealed = %q", sealed)
	}
	settings, err := server.aiSettingsFor(ctx, tenant)
	if err != nil || settings.APIKey != "sk-byok-test" {
		t.Fatalf("settings key=%q err=%v", settings.APIKey, err)
	}

	if recorder := call(server.adminLockEncryption, http.MethodPost, "/admin/api/encryption/lock", ""); recorder.Code != http.StatusOK {
		t.Fatalf("lock = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := server.aiSettingsFor(ctx, tenant); err == nil {
		t.Fatal("locked tenant key silently fell back to the workspace key")
	}
	if recorder := call(server.adminGetAI, http.MethodGet, "/admin/api/ai", ""); recorder.Code == http.StatusOK {
		t.Fatalf("admin AI read succeeded while the key was locked: %s", recorder.Body.String())
	}
	wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32))
	wrongBody, _ := json.Marshal(map[string]any{"customerKey": wrongKey})
	if recorder := call(server.adminUnlockEncryption, http.MethodPost, "/admin/api/encryption/unlock", string(wrongBody)); recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong unlock = %d %s", recorder.Code, recorder.Body.String())
	}
	unlockBody, _ := json.Marshal(map[string]any{"customerKey": encodedKey})
	if recorder := call(server.adminUnlockEncryption, http.MethodPost, "/admin/api/encryption/unlock", string(unlockBody)); recorder.Code != http.StatusOK {
		t.Fatalf("unlock = %d %s", recorder.Code, recorder.Body.String())
	}
	settings, err = server.aiSettingsFor(ctx, tenant)
	if err != nil || settings.APIKey != "sk-byok-test" {
		t.Fatalf("unlocked key=%q err=%v", settings.APIKey, err)
	}

	if recorder := call(server.adminRotateEncryptionKey, http.MethodPost, "/admin/api/encryption/rotate", string(unlockBody)); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"version":2`) {
		t.Fatalf("rotate = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(server.adminUpdateAI, http.MethodPatch, "/admin/api/ai", `{"apiKey":"sk-rotated"}`); recorder.Code != http.StatusOK {
		t.Fatalf("save after rotate = %d %s", recorder.Code, recorder.Body.String())
	}
	_ = pool.QueryRow(ctx, `SELECT api_key_sealed FROM public.tenant_ai_settings WHERE tenant_id=$1`, tenant).Scan(&sealed)
	if !strings.HasPrefix(sealed, "byok:v1:2:") {
		t.Fatalf("rotated sealed = %q", sealed)
	}
	settings, err = server.aiSettingsFor(ctx, tenant)
	if err != nil || settings.APIKey != "sk-rotated" {
		t.Fatalf("rotated settings key=%q err=%v", settings.APIKey, err)
	}
	if _, err := server.openTenantSecret(ctx, otherTenant, sealed); err == nil {
		t.Fatal("another tenant decrypted the key")
	}
	if recorder := call(server.adminGetEncryption, http.MethodGet, "/admin/api/encryption", ""); recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), encodedKey) {
		t.Fatalf("status leaked key material: %d %s", recorder.Code, recorder.Body.String())
	}
}
