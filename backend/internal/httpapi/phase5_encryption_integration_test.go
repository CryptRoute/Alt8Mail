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

func TestPhase5StorageEncryptionIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 5 encryption test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4612-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4613-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "enc-" + suffix + "@gov.example.test"
	uploadDir := t.TempDir()
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 5 Encryption')`, tenant, "phase5-enc-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Encryption User','owner')`, user, tenant, email)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{UploadDir: uploadDir, SessionSecret: "phase5-encryption-secret"}}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Encryption User", Role: "owner"}}
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	}
	kek := bytes.Repeat([]byte{0x5a}, 32)
	body, _ := json.Marshal(map[string]any{"customerKey": base64.StdEncoding.EncodeToString(kek)})
	request := httptest.NewRequest(http.MethodPost, "/admin/api/encryption/keys", bytes.NewBuffer(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.adminCreateEncryptionKey(recorder, withAdmin(request))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enable BYOK = %d %s", recorder.Code, recorder.Body.String())
	}

	plaintext := []byte("Office document plaintext that must not appear on disk")
	key, checksum, err := server.officeStoreBytesForTenant(ctx, tenant, plaintext, "txt")
	if err != nil || checksum == "" {
		t.Fatalf("store office = %q %v", key, err)
	}
	path, _ := server.officeStoragePath(key)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), tenantBlobPrefix+"\n") || strings.Contains(string(raw), "plaintext that must not") {
		t.Fatalf("office blob was not encrypted: %q", raw[:min(len(raw), 80)])
	}
	decrypted, err := server.officeReadStorage(key)
	if err != nil || string(decrypted) != string(plaintext) {
		t.Fatalf("decrypt office = %q err=%v", decrypted, err)
	}

	sealed, version, err := server.sealTenantBlob(ctx, tenant, plaintext)
	if err != nil || version == 0 || strings.Contains(string(sealed), "plaintext that must not") {
		t.Fatalf("seal blob version=%d err=%v", version, err)
	}
	mustExec(`INSERT INTO public.files (id, tenant_id, user_id, provider_id, name, mime, size_bytes, storage_key, encryption_key_version, client_encrypted)
		VALUES ($1,$2,$3,'a8m','secret.txt','text/plain',$4,'blob',$5,false)`, "enc-file-"+suffix, tenant, user, len(plaintext), version)
	blobKey := "encblob-" + suffix
	if err := os.WriteFile(uploadDir+string(os.PathSeparator)+blobKey, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	mustExec(`UPDATE public.files SET storage_key=$2 WHERE id=$1`, "enc-file-"+suffix, blobKey)
	cabinet, err := server.openCabinetBlob(ctx, "enc-file-"+suffix, sealed)
	if err != nil || string(cabinet) != string(plaintext) {
		t.Fatalf("cabinet decrypt = %q err=%v", cabinet, err)
	}

	// Client-encrypted files are opaque and never server-decrypted.
	clientCipher := []byte(tenantBlobPrefix + "\nnot-a-real-tenant\n1\nAAAA")
	mustExec(`UPDATE public.files SET client_encrypted=true WHERE id=$1`, "enc-file-"+suffix)
	clientOut, err := server.openCabinetBlob(ctx, "enc-file-"+suffix, clientCipher)
	if err != nil || string(clientOut) != string(clientCipher) {
		t.Fatalf("client encrypted pass-through err=%v", err)
	}

	// Locking prevents reads; unlocking restores them.
	lockRequest := httptest.NewRequest(http.MethodPost, "/admin/api/encryption/lock", nil)
	recorder = httptest.NewRecorder()
	server.adminLockEncryption(recorder, withAdmin(lockRequest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("lock = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := server.officeReadStorage(key); err == nil {
		t.Fatal("encrypted Office blob was readable while locked")
	}
	unlockBody, _ := json.Marshal(map[string]any{"customerKey": base64.StdEncoding.EncodeToString(kek)})
	unlockRequest := httptest.NewRequest(http.MethodPost, "/admin/api/encryption/unlock", bytes.NewBuffer(unlockBody))
	unlockRequest.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.adminUnlockEncryption(recorder, withAdmin(unlockRequest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unlock = %d %s", recorder.Code, recorder.Body.String())
	}
	if decrypted, err := server.officeReadStorage(key); err != nil || string(decrypted) != string(plaintext) {
		t.Fatalf("decrypt after unlock = %q err=%v", decrypted, err)
	}
}
