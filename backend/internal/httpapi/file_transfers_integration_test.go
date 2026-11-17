package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run after applying migration 0042 to a disposable database:
//
//	A8M_TRANSFER_TEST_DSN='postgres://...' go test ./internal/httpapi -run FileTransfersIntegration -count=1
func TestFileTransfersIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_TRANSFER_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_TRANSFER_TEST_DSN to run the file transfer integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	uploadDir, err := os.MkdirTemp("", "a8m-transfer-test-*")
	if err != nil {
		t.Fatalf("create upload dir: %v", err)
	}
	defer os.RemoveAll(uploadDir)
	slug := fmt.Sprintf("transfer-test-%d", time.Now().UnixNano())
	var tenantID, userID string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, "Transfer test").Scan(&tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID)
	}()
	email := slug + "@example.test"
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, name, role) VALUES ($1,$2,$3,'owner') RETURNING id::text
	`, tenantID, email, "Transfer User").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner := userRecord{ID: userID, TenantID: tenantID, Email: email, Name: "Transfer User"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(Options{
		Config: config.Config{UploadDir: uploadDir, PublicURL: "https://mail.test", SessionSecret: "session", CSRFSecret: "csrf"},
		Store:  &store.Store{DB: pool}, Logger: logger,
	})
	withSession := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, &sessionRecord{User: owner}))
	}
	createSessionRequest := httptest.NewRequest("POST", "/api/files/upload-sessions", bytes.NewBufferString(`{"folderId":""}`))
	createSessionRequest.Header.Set("Content-Type", "application/json")
	createSessionRecorder := httptest.NewRecorder()
	server.createUploadSession(createSessionRecorder, withSession(createSessionRequest))
	if createSessionRecorder.Code != http.StatusCreated {
		t.Fatalf("create upload session status = %d; body: %s", createSessionRecorder.Code, createSessionRecorder.Body.String())
	}
	var sessionResponse struct {
		Session struct {
			ID        string `json:"id"`
			URL       string `json:"url"`
			ExpiresAt string `json:"expiresAt"`
			Uploads   []any  `json:"uploads"`
		} `json:"session"`
	}
	if err := json.Unmarshal(createSessionRecorder.Body.Bytes(), &sessionResponse); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	parsed, err := url.Parse(sessionResponse.Session.URL)
	if err != nil {
		t.Fatalf("parse session URL: %v", err)
	}
	token := path.Base(parsed.Path)
	pageRequest := httptest.NewRequest("GET", "/upload/"+token, nil)
	pageRequest.SetPathValue("token", token)
	pageRecorder := httptest.NewRecorder()
	server.mobileUploadPage(pageRecorder, pageRequest)
	if pageRecorder.Code != http.StatusOK || !strings.Contains(pageRecorder.Body.String(), "Send files to") {
		t.Fatalf("mobile upload page status=%d body=%s", pageRecorder.Code, pageRecorder.Body.String()[:min(200, pageRecorder.Body.Len())])
	}

	progressRequest := httptest.NewRequest("POST", "/api/public/upload/"+token+"/progress", bytes.NewBufferString(`{"uploadId":"up1","name":"mobile.txt","size":12,"progress":15,"status":"uploading"}`))
	progressRequest.Header.Set("Content-Type", "application/json")
	progressRequest.SetPathValue("token", token)
	progressRecorder := httptest.NewRecorder()
	server.publicUploadProgress(progressRecorder, progressRequest)
	if progressRecorder.Code != http.StatusOK {
		t.Fatalf("progress status = %d; body: %s", progressRecorder.Code, progressRecorder.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "mobile.txt")
	_, _ = part.Write([]byte("hello mobile"))
	_ = writer.WriteField("uploadId", "up1")
	_ = writer.Close()
	uploadRequest := httptest.NewRequest("POST", "/api/public/upload/"+token, &body)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	uploadRequest.SetPathValue("token", token)
	uploadRecorder := httptest.NewRecorder()
	server.publicUpload(uploadRecorder, uploadRequest)
	if uploadRecorder.Code != http.StatusCreated {
		t.Fatalf("mobile upload status = %d; body: %s", uploadRecorder.Code, uploadRecorder.Body.String())
	}
	var fileID string
	if err := pool.QueryRow(ctx, `SELECT id FROM files WHERE user_id=$1 AND name='mobile.txt'`, userID).Scan(&fileID); err != nil {
		t.Fatalf("mobile upload file row: %v", err)
	}
	var sessionUploads []byte
	if err := pool.QueryRow(ctx, `SELECT uploads FROM file_upload_sessions WHERE id=$1`, sessionResponse.Session.ID).Scan(&sessionUploads); err != nil {
		t.Fatalf("read session uploads: %v", err)
	}
	if !bytes.Contains(sessionUploads, []byte(`"status": "done"`)) && !bytes.Contains(sessionUploads, []byte(`"status":"done"`)) {
		t.Fatalf("session uploads did not record completion: %s", sessionUploads)
	}

	extendRequest := httptest.NewRequest("POST", "/api/files/upload-sessions/"+sessionResponse.Session.ID+"/extend", bytes.NewBufferString(`{"hours":2}`))
	extendRequest.Header.Set("Content-Type", "application/json")
	extendRequest.SetPathValue("id", sessionResponse.Session.ID)
	extendRecorder := httptest.NewRecorder()
	server.extendUploadSession(extendRecorder, withSession(extendRequest))
	if extendRecorder.Code != http.StatusOK || !strings.Contains(extendRecorder.Body.String(), "extendedAt") {
		t.Fatalf("extend session status=%d body=%s", extendRecorder.Code, extendRecorder.Body.String())
	}

	shareRequest := httptest.NewRequest("POST", "/api/files/"+fileID+"/share", bytes.NewBufferString(`{"access":"view","expiresDays":2,"recipientEmail":"recipient@example.com"}`))
	shareRequest.Header.Set("Content-Type", "application/json")
	shareRequest.SetPathValue("id", fileID)
	shareRecorder := httptest.NewRecorder()
	server.shareFile(shareRecorder, withSession(shareRequest))
	if shareRecorder.Code != http.StatusOK {
		t.Fatalf("share status = %d; body: %s", shareRecorder.Code, shareRecorder.Body.String())
	}
	var shareResponse struct {
		Share struct {
			URL string `json:"url"`
		} `json:"share"`
	}
	if err := json.Unmarshal(shareRecorder.Body.Bytes(), &shareResponse); err != nil {
		t.Fatalf("decode share: %v", err)
	}
	shareURL, _ := url.Parse(shareResponse.Share.URL)
	shareToken := path.Base(shareURL.Path)
	sharePageRequest := httptest.NewRequest("GET", "/files/s/"+shareToken, nil)
	sharePageRequest.SetPathValue("token", shareToken)
	sharePageRecorder := httptest.NewRecorder()
	server.publicSharePage(sharePageRecorder, sharePageRequest)
	if sharePageRecorder.Code != http.StatusOK || !strings.Contains(sharePageRecorder.Body.String(), "Confirm it's you") {
		t.Fatalf("recipient gate page status=%d body=%s", sharePageRecorder.Code, sharePageRecorder.Body.String()[:min(200, sharePageRecorder.Body.Len())])
	}
	contentRequest := httptest.NewRequest("GET", "/api/public/shares/"+shareToken+"/content", nil)
	contentRequest.SetPathValue("token", shareToken)
	contentRecorder := httptest.NewRecorder()
	server.publicShareContent(contentRecorder, contentRequest)
	if contentRecorder.Code != http.StatusForbidden {
		t.Fatalf("unverified content status = %d, want 403", contentRecorder.Code)
	}

	wrongVerify := httptest.NewRequest("POST", "/files/s/"+shareToken+"/verify", strings.NewReader(url.Values{"email": {"wrong@example.com"}}.Encode()))
	wrongVerify.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongVerify.SetPathValue("token", shareToken)
	wrongRecorder := httptest.NewRecorder()
	server.publicShareVerifyEmail(wrongRecorder, wrongVerify)
	if wrongRecorder.Code != http.StatusForbidden {
		t.Fatalf("wrong recipient verify status = %d, want 403", wrongRecorder.Code)
	}
	goodVerify := httptest.NewRequest("POST", "/files/s/"+shareToken+"/verify", strings.NewReader(url.Values{"email": {"recipient@example.com"}}.Encode()))
	goodVerify.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	goodVerify.SetPathValue("token", shareToken)
	goodRecorder := httptest.NewRecorder()
	server.publicShareVerifyEmail(goodRecorder, goodVerify)
	cookies := goodRecorder.Result().Cookies()
	if goodRecorder.Code != http.StatusSeeOther || len(cookies) == 0 {
		t.Fatalf("verified recipient status=%d cookies=%d", goodRecorder.Code, len(cookies))
	}
	verifiedContent := httptest.NewRequest("GET", "/api/public/shares/"+shareToken+"/content", nil)
	verifiedContent.SetPathValue("token", shareToken)
	verifiedContent.AddCookie(cookies[0])
	verifiedRecorder := httptest.NewRecorder()
	server.publicShareContent(verifiedRecorder, verifiedContent)
	if verifiedRecorder.Code != http.StatusOK || !strings.Contains(verifiedRecorder.Body.String(), "hello mobile") {
		t.Fatalf("verified content status=%d body=%q", verifiedRecorder.Code, verifiedRecorder.Body.String())
	}

	if _, err := pool.Exec(ctx, `UPDATE files SET locked=true WHERE id=$1`, fileID); err != nil {
		t.Fatalf("lock file: %v", err)
	}
	lockedPage := httptest.NewRequest("GET", "/files/s/"+shareToken, nil)
	lockedPage.SetPathValue("token", shareToken)
	lockedRecorder := httptest.NewRecorder()
	server.publicSharePage(lockedRecorder, lockedPage)
	if lockedRecorder.Code != http.StatusOK || !strings.Contains(lockedRecorder.Body.String(), "This file is locked") {
		t.Fatalf("locked page status=%d body=%s", lockedRecorder.Code, lockedRecorder.Body.String()[:min(200, lockedRecorder.Body.Len())])
	}
	requestAccess := httptest.NewRequest("POST", "/files/s/"+shareToken+"/request-access", strings.NewReader(url.Values{"email": {"requester@example.com"}, "message": {"Please let me in"}}.Encode()))
	requestAccess.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	requestAccess.SetPathValue("token", shareToken)
	requestRecorder := httptest.NewRecorder()
	server.publicShareRequestAccess(requestRecorder, requestAccess)
	if requestRecorder.Code != http.StatusOK || !strings.Contains(requestRecorder.Body.String(), "request was sent") {
		t.Fatalf("request access status=%d body=%s", requestRecorder.Code, requestRecorder.Body.String()[:min(200, requestRecorder.Body.Len())])
	}
	var requestCount, notificationCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM file_share_access_requests WHERE owner_user_id=$1`, userID).Scan(&requestCount)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM mail_notifications WHERE user_id=$1 AND kind='file_access_request'`, userID).Scan(&notificationCount)
	if requestCount != 1 || notificationCount != 1 {
		t.Fatalf("access request rows=%d notifications=%d", requestCount, notificationCount)
	}
}
