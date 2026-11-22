package officecollabora

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

type fakeService struct {
	data    []byte
	version string
	updated time.Time
	writes  int
}

func (f *fakeService) CheckFileInfo(ctx context.Context, claims office.URLToken) (FileInfo, error) {
	return FileInfo{
		FileID:       claims.FileID,
		BaseFileName: "Notes.docx",
		Size:         int64(len(f.data)),
		Version:      f.version,
		LastModified: f.updated,
		UserID:       claims.UserID,
		UserName:     claims.DisplayName,
		CanWrite:     claims.CanWrite,
	}, nil
}

func (f *fakeService) GetFile(ctx context.Context, claims office.URLToken) (FileData, error) {
	return FileData{
		Data:         f.data,
		ContentType:  ContentType("Notes.docx"),
		Version:      f.version,
		LastModified: f.updated,
	}, nil
}

func (f *fakeService) SaveFile(ctx context.Context, claims office.URLToken, data []byte, base time.Time) (SaveResult, error) {
	if !base.IsZero() && base.Before(f.updated) {
		return SaveResult{}, &ConflictError{Current: f.updated}
	}
	f.data = data
	f.version = "v" + time.Now().UTC().Format("150405.000000000")
	f.updated = time.Now().UTC()
	f.writes++
	return SaveResult{Version: f.version, LastModified: f.updated}, nil
}

func newTestHandler(t *testing.T) (*Handler, *fakeService, string) {
	t.Helper()
	service := &fakeService{
		data:    []byte("PK\x03\x04initial"),
		version: "v1",
		updated: time.Now().UTC().Add(-time.Minute),
	}
	secret := "test-secret-that-is-long-enough-for-office"
	handler := &Handler{
		Secret:            secret,
		Service:           service,
		PostMessageOrigin: "https://mail.test",
		MaxBytes:          1 << 20,
	}
	token, err := office.SignURLToken(secret, office.URLToken{
		Kind:        office.TokenKindWOPI,
		FileID:      "doc1",
		UserID:      "user1",
		TenantID:    "tenant1",
		DisplayName: "Test User",
		CanWrite:    true,
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, service, token
}

func doRequest(handler http.HandlerFunc, method, target, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.SetPathValue("id", "doc1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestCheckFileInfo(t *testing.T) {
	handler, _, token := newTestHandler(t)
	rec := doRequest(handler.CheckFileInfo, http.MethodGet, "/api/office/wopi/files/doc1", token, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var info map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["BaseFileName"] != "Notes.docx" {
		t.Fatalf("BaseFileName = %v", info["BaseFileName"])
	}
	if info["SupportsUpdate"] != true || info["SupportsLocks"] != false {
		t.Fatalf("capabilities = %v", info)
	}
	if info["PostMessageOrigin"] != "https://mail.test" {
		t.Fatalf("PostMessageOrigin = %v", info["PostMessageOrigin"])
	}
}

func TestGetFile(t *testing.T) {
	handler, service, token := newTestHandler(t)
	rec := doRequest(handler.GetFile, http.MethodGet, "/api/office/wopi/files/doc1/contents", token, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), service.data) {
		t.Fatalf("body = %q, want %q", rec.Body.Bytes(), service.data)
	}
	if rec.Header().Get("X-WOPI-ItemVersion") != "v1" {
		t.Fatalf("version header = %q", rec.Header().Get("X-WOPI-ItemVersion"))
	}
}

func TestPutFile(t *testing.T) {
	handler, service, token := newTestHandler(t)
	rec := doRequest(handler.PutFile, http.MethodPost, "/api/office/wopi/files/doc1/contents", token,
		[]byte("new document"), map[string]string{"X-WOPI-Override": "PUT"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if service.writes != 1 || string(service.data) != "new document" {
		t.Fatalf("save did not land: writes=%d data=%q", service.writes, service.data)
	}
}

func TestPutFileStaleTimestamp(t *testing.T) {
	handler, service, token := newTestHandler(t)
	stale := service.updated.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	rec := doRequest(handler.PutFile, http.MethodPost, "/api/office/wopi/files/doc1/contents", token,
		[]byte("stale"), map[string]string{
			"X-WOPI-Override":       "PUT",
			"X-COOL-WOPI-Timestamp": stale,
		})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["COOLStatusCode"] != 1010 {
		t.Fatalf("COOLStatusCode = %d, want 1010", payload["COOLStatusCode"])
	}
	if service.writes != 0 {
		t.Fatalf("stale save wrote %d times", service.writes)
	}
}

func TestPutFileCurrentTimestamp(t *testing.T) {
	handler, service, token := newTestHandler(t)
	current := service.updated.UTC().Format(time.RFC3339Nano)
	rec := doRequest(handler.PutFile, http.MethodPost, "/api/office/wopi/files/doc1/contents", token,
		[]byte("current"), map[string]string{
			"X-WOPI-Override":       "PUT",
			"X-COOL-WOPI-Timestamp": current,
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if service.writes != 1 {
		t.Fatalf("writes = %d, want 1", service.writes)
	}
}

func TestUnauthorized(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	rec := doRequest(handler.CheckFileInfo, http.MethodGet, "/api/office/wopi/files/doc1", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTokenFileMismatch(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	other, err := office.SignURLToken(handler.Secret, office.URLToken{
		Kind:      office.TokenKindWOPI,
		FileID:    "doc2",
		UserID:    "user1",
		TenantID:  "tenant1",
		CanWrite:  true,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := doRequest(handler.CheckFileInfo, http.MethodGet, "/api/office/wopi/files/doc1", other, nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestReadOnlyTokenCannotSave(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	readOnly, err := office.SignURLToken(handler.Secret, office.URLToken{
		Kind:      office.TokenKindWOPI,
		FileID:    "doc1",
		UserID:    "user1",
		TenantID:  "tenant1",
		CanWrite:  false,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := doRequest(handler.PutFile, http.MethodPost, "/api/office/wopi/files/doc1/contents", readOnly,
		[]byte("nope"), map[string]string{"X-WOPI-Override": "PUT"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestGetFileRange(t *testing.T) {
	handler, service, token := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/office/wopi/files/doc1/contents", nil)
	req.SetPathValue("id", "doc1")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	handler.GetFile(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != string(service.data[2:6]) {
		t.Fatalf("range body = %q, want %q", got, service.data[2:6])
	}
}

func TestLockUnsupported(t *testing.T) {
	handler, _, token := newTestHandler(t)
	rec := doRequest(handler.Lock, http.MethodPost, "/api/office/wopi/files/doc1", token, nil, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}
