package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type licenseAuditRecord struct {
	action   string
	target   string
	detail   string
	metadata map[string]any
}

func licenseAPISession(permissions ...string) *sessionRecord {
	return &sessionRecord{
		ID:   "license-test-session",
		CSRF: "license-test-csrf",
		User: adminUser{
			ID: "admin-1", Email: "admin@example.test", Name: "Test Admin", Role: "admin",
			PermSet: permissionMap(permissions),
		},
	}
}

func licenseAPIServer(t *testing.T, cfg Config, sess *sessionRecord) (*Server, *[]licenseAuditRecord) {
	t.Helper()
	srv := licenseTestServer(t, cfg)
	audits := &[]licenseAuditRecord{}
	srv.sessionLoader = func(*http.Request) (*sessionRecord, error) {
		if sess == nil {
			return nil, errors.New("no session")
		}
		return sess, nil
	}
	srv.auditSink = func(_ context.Context, _ *adminUser, action, target, detail string, _ net.IP, _ string, metadata any) {
		record := licenseAuditRecord{action: action, target: target, detail: detail}
		if value, ok := metadata.(map[string]any); ok {
			record.metadata = value
		}
		*audits = append(*audits, record)
	}
	return srv, audits
}

func licenseAPIRequest(t *testing.T, srv *Server, sess *sessionRecord, method, target string, body []byte, contentType string, csrf bool) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if csrf && sess != nil {
		value := srv.signCSRF(sess.CSRF)
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: value})
		request.Header.Set("X-Alt8Mail-CSRF", value)
	}
	return request
}

func licenseAPIUploadBody(t *testing.T, token string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func licenseAPIErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode error response %q: %v", recorder.Body.String(), err)
	}
	return failure.Error.Code
}

func TestLicenseAPIGetStatus(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	license := licenseTestLicense(time.Now())
	license.Tenant = ""
	token := licenseTestToken(t, privateKey, license)
	path := filepath.Join(t.TempDir(), "license.token")
	licenseTestWriteFile(t, path, token, 0o600)

	srv, _ := licenseAPIServer(t, Config{
		LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey), LicenseWarningPercent: 80,
	}, licenseAPISession("settings.write"))
	srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
		return 40, []map[string]any{
			{"tenantId": "t1", "name": "Alpha", "activeSeats": 25},
			{"tenantId": "t2", "name": "Beta", "activeSeats": 15},
		}, nil
	}

	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, nil, http.MethodGet, "/control/api/license", nil, "", false))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		License  licenseStatus    `json:"license"`
		ByTenant []map[string]any `json:"byTenant"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	status := response.License
	if !status.Configured || status.FilePath != path || status.State != "valid" || status.Edition != "enterprise" {
		t.Fatalf("unexpected status payload: %#v", status)
	}
	if status.Seats != 50 || status.UsedSeats != 40 || status.RemainingSeats != 10 || !status.Warning {
		t.Fatalf("unexpected seat payload: %#v", status)
	}
	if status.LicenseID != "lic_control_test" || status.Tenant != "" || len(status.Features) != 2 {
		t.Fatalf("unexpected license claims: %#v", status)
	}
	if status.ExpiresAt == nil || status.LastModified == nil || status.Error != "" {
		t.Fatalf("unexpected timestamps or error: %#v", status)
	}
	if len(response.ByTenant) != 2 || response.ByTenant[0]["name"] != "Alpha" || response.ByTenant[1]["name"] != "Beta" {
		t.Fatalf("byTenant = %#v", response.ByTenant)
	}

	body := recorder.Body.String()
	signature := strings.Split(token, ".")[2]
	for _, secret := range []string{token, signature, "signature"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status response leaked license material: %q", secret)
		}
	}
}

func TestLicenseAPIGetRequiresSettingsWrite(t *testing.T) {
	srv, _ := licenseAPIServer(t, Config{}, licenseAPISession("dashboard.read"))
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, nil, http.MethodGet, "/control/api/license", nil, "", false))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if code := licenseAPIErrorCode(t, recorder); code != "FORBIDDEN" {
		t.Fatalf("code = %q, want FORBIDDEN", code)
	}
}

func TestLicenseAPIGetRequiresAuthentication(t *testing.T) {
	srv, _ := licenseAPIServer(t, Config{}, nil)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, nil, http.MethodGet, "/control/api/license", nil, "", false))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if code := licenseAPIErrorCode(t, recorder); code != "UNAUTHENTICATED" {
		t.Fatalf("code = %q, want UNAUTHENTICATED", code)
	}
}

func TestLicenseAPIUploadValidToken(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	license := licenseTestLicense(time.Now())
	token := licenseTestToken(t, privateKey, license)
	dir := t.TempDir()
	path := filepath.Join(dir, "license.token")

	sess := licenseAPISession("settings.write")
	srv, audits := licenseAPIServer(t, Config{
		LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey), LicenseWarningPercent: 80,
	}, sess)
	srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
		return 12, []map[string]any{}, nil
	}

	recorder := httptest.NewRecorder()
	request := licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
		licenseAPIUploadBody(t, token), "application/json", true)
	srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		OK              bool          `json:"ok"`
		RestartRequired bool          `json:"restartRequired"`
		License         licenseStatus `json:"license"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !response.OK || !response.RestartRequired {
		t.Fatalf("ok=%t restartRequired=%t, want both true", response.OK, response.RestartRequired)
	}
	if response.License.State != "valid" || response.License.LicenseID != "lic_control_test" || response.License.UsedSeats != 12 {
		t.Fatalf("unexpected license snapshot: %#v", response.License)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != token {
		t.Fatalf("stored license does not match the uploaded token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 0640", info.Mode().Perm())
	}
	licenseTestAssertNoTempFiles(t, dir)

	if len(*audits) != 1 {
		t.Fatalf("audit records = %d, want 1", len(*audits))
	}
	audit := (*audits)[0]
	if audit.action != "license.uploaded" || audit.target != path {
		t.Fatalf("unexpected audit record: %#v", audit)
	}
	if len(audit.metadata) != 4 {
		t.Fatalf("audit metadata must contain only licenseId/edition/seats/expiresAt: %#v", audit.metadata)
	}
	for _, key := range []string{"licenseId", "edition", "seats", "expiresAt"} {
		if _, ok := audit.metadata[key]; !ok {
			t.Fatalf("audit metadata missing %q: %#v", key, audit.metadata)
		}
	}
	if audit.metadata["licenseId"] != "lic_control_test" || audit.metadata["edition"] != "enterprise" {
		t.Fatalf("audit metadata claims wrong: %#v", audit.metadata)
	}
	if seats, ok := audit.metadata["seats"].(int); !ok || seats != 50 {
		t.Fatalf("audit seats = %#v", audit.metadata["seats"])
	}
	if expiresAt, ok := audit.metadata["expiresAt"].(*time.Time); !ok || expiresAt == nil || expiresAt.IsZero() {
		t.Fatalf("audit expiresAt = %#v", audit.metadata["expiresAt"])
	}
	metadataJSON, err := json.Marshal(audit.metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadataJSON), token) || strings.Contains(string(metadataJSON), strings.Split(token, ".")[2]) {
		t.Fatalf("audit metadata leaked license material: %s", metadataJSON)
	}
	if strings.Contains(recorder.Body.String(), token) || strings.Contains(recorder.Body.String(), strings.Split(token, ".")[2]) {
		t.Fatalf("upload response leaked license material")
	}
}

func TestLicenseAPIUploadRejectedPreservesFile(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)
	_, otherPrivateKey := licenseTestKey(t)
	oldToken := licenseTestToken(t, privateKey, licenseTestLicense(now))

	expired := licenseTestLicense(now)
	expired.ExpiresAt = now.Add(-48 * time.Hour)
	expired.GraceDays = 0
	expiredToken := licenseTestToken(t, privateKey, expired)

	tamperedParts := strings.Split(oldToken, ".")
	tamperedParts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"version":1}`))
	tamperedToken := strings.Join(tamperedParts, ".")

	oversizedBody := []byte(`{"token":"` + strings.Repeat("A", licenseTokenMaxBytes) + `"}`)

	tests := []struct {
		name        string
		key         string
		configured  bool
		body        []byte
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{"tampered token", licenseTestPublicKey(publicKey), true, licenseAPIUploadBody(t, tamperedToken), "application/json", 400, "LICENSE_TOKEN"},
		{"expired token", licenseTestPublicKey(publicKey), true, licenseAPIUploadBody(t, expiredToken), "application/json", 400, "LICENSE_EXPIRED"},
		{"malformed token", licenseTestPublicKey(publicKey), true, licenseAPIUploadBody(t, "not-a-license"), "application/json", 400, "LICENSE_TOKEN"},
		{"wrong key", licenseTestPublicKey(publicKey), true, licenseAPIUploadBody(t, licenseTestToken(t, otherPrivateKey, licenseTestLicense(now))), "application/json", 400, "LICENSE_TOKEN"},
		{"missing public key", "", true, licenseAPIUploadBody(t, oldToken), "application/json", 503, "LICENSE_NOT_CONFIGURED"},
		{"missing file path", licenseTestPublicKey(publicKey), false, licenseAPIUploadBody(t, oldToken), "application/json", 503, "LICENSE_NOT_CONFIGURED"},
		{"empty token", licenseTestPublicKey(publicKey), true, licenseAPIUploadBody(t, "   "), "application/json", 400, "LICENSE_TOKEN"},
		{"oversized body", licenseTestPublicKey(publicKey), true, oversizedBody, "application/json", 413, "LICENSE_TOO_LARGE"},
		{"malformed JSON", licenseTestPublicKey(publicKey), true, []byte("{not-json"), "application/json", 400, "BAD_JSON"},
		{"trailing data", licenseTestPublicKey(publicKey), true, append(licenseAPIUploadBody(t, oldToken), []byte("trailing")...), "application/json", 400, "BAD_JSON"},
		{"trailing JSON object", licenseTestPublicKey(publicKey), true, append(licenseAPIUploadBody(t, oldToken), licenseAPIUploadBody(t, oldToken)...), "application/json", 400, "BAD_JSON"},
		{"trailing oversized data", licenseTestPublicKey(publicKey), true, append(licenseAPIUploadBody(t, oldToken), []byte(strings.Repeat("A", licenseTokenMaxBytes))...), "application/json", 400, "BAD_JSON"},
		{"wrong content type", licenseTestPublicKey(publicKey), true, licenseAPIUploadBody(t, oldToken), "text/plain", 415, "CONTENT_TYPE"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "license.token")
			licenseTestWriteFile(t, path, oldToken, 0o640)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			beforeInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			cfg := Config{LicensePublicKey: testCase.key, LicenseWarningPercent: 80}
			if testCase.configured {
				cfg.LicenseFile = path
			}
			sess := licenseAPISession("settings.write")
			srv, audits := licenseAPIServer(t, cfg, sess)

			recorder := httptest.NewRecorder()
			request := licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
				testCase.body, testCase.contentType, true)
			srv.Handler().ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if code := licenseAPIErrorCode(t, recorder); code != testCase.wantCode {
				t.Fatalf("code = %q, want %q", code, testCase.wantCode)
			}
			if testCase.configured {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatalf("failed upload changed the existing file")
				}
				afterInfo, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
					t.Fatalf("failed upload changed the file mode: %o -> %o", beforeInfo.Mode().Perm(), afterInfo.Mode().Perm())
				}
			}
			licenseTestAssertNoTempFiles(t, dir)
			if len(*audits) != 0 {
				t.Fatalf("rejected upload must not be audited: %#v", *audits)
			}
		})
	}
}

func TestLicenseAPIUploadAcceptsJSONContentTypeParameters(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	token := licenseTestToken(t, privateKey, licenseTestLicense(time.Now()))
	path := filepath.Join(t.TempDir(), "license.token")
	sess := licenseAPISession("settings.write")
	srv, _ := licenseAPIServer(t, Config{
		LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey), LicenseWarningPercent: 80,
	}, sess)

	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
		licenseAPIUploadBody(t, token), "application/json; charset=utf-8", true))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("license was not written: %v", err)
	}
}

func TestLicenseAPIGetRereadsTheFile(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	now := time.Now()
	first := licenseTestLicense(now)
	second := licenseTestLicense(now)
	second.ID = "lic_second"
	second.Edition = "starter"
	second.Seats = 10
	path := filepath.Join(t.TempDir(), "license.token")
	licenseTestWriteFile(t, path, licenseTestToken(t, privateKey, first), 0o640)

	srv, _ := licenseAPIServer(t, Config{
		LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey), LicenseWarningPercent: 80,
	}, licenseAPISession("settings.write"))

	getStatus := func() licenseStatus {
		t.Helper()
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, nil, http.MethodGet, "/control/api/license", nil, "", false))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			License licenseStatus `json:"license"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.License
	}

	if status := getStatus(); status.LicenseID != "lic_control_test" || status.Seats != 50 || status.Edition != "enterprise" {
		t.Fatalf("unexpected first status: %#v", status)
	}
	licenseTestWriteFile(t, path, licenseTestToken(t, privateKey, second), 0o640)
	if status := getStatus(); status.LicenseID != "lic_second" || status.Seats != 10 || status.Edition != "starter" {
		t.Fatalf("GET did not re-read the replaced license file: %#v", status)
	}
}

func TestLicenseAPILogsNeverContainToken(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	token := licenseTestToken(t, privateKey, licenseTestLicense(time.Now()))
	path := filepath.Join(t.TempDir(), "license.token")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	sess := licenseAPISession("settings.write")
	srv := licenseTestServerWithLogger(t, Config{
		LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey), LicenseWarningPercent: 80,
	}, logger)
	srv.sessionLoader = func(*http.Request) (*sessionRecord, error) { return sess, nil }
	srv.auditSink = func(context.Context, *adminUser, string, string, string, net.IP, string, any) {}
	srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
		return 0, []map[string]any{}, nil
	}

	tamperedParts := strings.Split(token, ".")
	tamperedParts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"version":1}`))
	tampered := strings.Join(tamperedParts, ".")
	failure := httptest.NewRecorder()
	srv.Handler().ServeHTTP(failure, licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
		licenseAPIUploadBody(t, tampered), "application/json", true))
	if failure.Code != http.StatusBadRequest {
		t.Fatalf("tampered upload status = %d, want 400", failure.Code)
	}

	success := httptest.NewRecorder()
	srv.Handler().ServeHTTP(success, licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
		licenseAPIUploadBody(t, token), "application/json", true))
	if success.Code != http.StatusOK {
		t.Fatalf("valid upload status = %d, want 200: %s", success.Code, success.Body.String())
	}

	output := logs.String()
	signature := strings.Split(token, ".")[2]
	for _, secret := range []string{token, signature} {
		if strings.Contains(output, secret) {
			t.Fatalf("logger leaked license material: %s", output)
		}
	}
	if !strings.Contains(output, "license upload rejected") || !strings.Contains(output, "license uploaded") {
		t.Fatalf("expected fixed license log messages, got: %s", output)
	}
}

func TestLicenseAPIUploadRequiresPermissionAndCSRF(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	token := licenseTestToken(t, privateKey, licenseTestLicense(time.Now()))
	path := filepath.Join(t.TempDir(), "license.token")
	cfg := Config{LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey), LicenseWarningPercent: 80}

	t.Run("permission", func(t *testing.T) {
		sess := licenseAPISession("dashboard.read")
		srv, audits := licenseAPIServer(t, cfg, sess)
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
			licenseAPIUploadBody(t, token), "application/json", true))
		if recorder.Code != http.StatusForbidden || licenseAPIErrorCode(t, recorder) != "FORBIDDEN" {
			t.Fatalf("status = %d, want 403 FORBIDDEN", recorder.Code)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("forbidden upload wrote a license file")
		}
		if len(*audits) != 0 {
			t.Fatalf("forbidden upload was audited")
		}
	})

	t.Run("csrf", func(t *testing.T) {
		sess := licenseAPISession("settings.write")
		srv, _ := licenseAPIServer(t, cfg, sess)
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, licenseAPIRequest(t, srv, sess, http.MethodPost, "/control/api/license",
			licenseAPIUploadBody(t, token), "application/json", false))
		if recorder.Code != http.StatusForbidden || licenseAPIErrorCode(t, recorder) != "CSRF" {
			t.Fatalf("status = %d, want 403 CSRF", recorder.Code)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("CSRF-rejected upload wrote a license file")
		}
	})
}
