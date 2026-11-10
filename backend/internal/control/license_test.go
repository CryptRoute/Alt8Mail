package control

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

const licenseTestSecret = "0123456789abcdef0123456789abcdef"

func licenseTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey
}

func licenseTestPublicKey(publicKey ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(publicKey)
}

func licenseTestToken(t *testing.T, privateKey ed25519.PrivateKey, license licensing.License) string {
	t.Helper()
	payload, err := json.Marshal(license)
	if err != nil {
		t.Fatal(err)
	}
	signed := licensing.EnvelopePrefix + "." + base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(privateKey, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func licenseTestLicense(now time.Time) licensing.License {
	return licensing.License{
		Version:   licensing.Version,
		ID:        "lic_control_test",
		Tenant:    "tenant_a",
		Edition:   "enterprise",
		Seats:     50,
		Features:  []string{"sso", "scim"},
		IssuedAt:  now.Add(-30 * 24 * time.Hour),
		ExpiresAt: now.Add(30 * 24 * time.Hour),
		GraceDays: 7,
	}
}

func licenseTestWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func licenseTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.SecretKey == "" {
		cfg.SecretKey = licenseTestSecret
	}
	if cfg.LicenseWarningPercent == 0 {
		cfg.LicenseWarningPercent = 80
	}
	srv, err := New(Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if srv.seatCounter == nil {
		srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
			return 0, []map[string]any{}, nil
		}
	}
	return srv
}

func licenseTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CONTROL_DATABASE_URL", "postgres://user:pass@127.0.0.1:5432/alt8mail_control")
	t.Setenv("CONTROL_SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("CONTROL_CSRF_SECRET", strings.Repeat("c", 32))
	t.Setenv("CONTROL_SECRET_KEY", strings.Repeat("k", 32))
}

func TestConfigLicenseDefaults(t *testing.T) {
	licenseTestEnvironment(t)
	t.Setenv("CONTROL_LICENSE_FILE", "")
	t.Setenv("CONTROL_LICENSE_PUBLIC_KEY", "")
	t.Setenv("CONTROL_LICENSE_WARNING_PERCENT", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseFile != "/etc/a8m/license.token" {
		t.Fatalf("LicenseFile = %q, want the default path", cfg.LicenseFile)
	}
	if cfg.LicensePublicKey != "" {
		t.Fatalf("LicensePublicKey = %q, want empty", cfg.LicensePublicKey)
	}
	if cfg.LicenseWarningPercent != 80 {
		t.Fatalf("LicenseWarningPercent = %d, want 80", cfg.LicenseWarningPercent)
	}
}

func TestConfigLicenseValues(t *testing.T) {
	licenseTestEnvironment(t)
	t.Setenv("CONTROL_LICENSE_FILE", "/srv/a8m/license.token")
	t.Setenv("CONTROL_LICENSE_PUBLIC_KEY", "unvalidated-at-load")
	t.Setenv("CONTROL_LICENSE_WARNING_PERCENT", "90")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseFile != "/srv/a8m/license.token" || cfg.LicensePublicKey != "unvalidated-at-load" || cfg.LicenseWarningPercent != 90 {
		t.Fatalf("unexpected license config: %#v", cfg)
	}
}

func TestConfigLicenseWarningPercentValidation(t *testing.T) {
	for _, value := range []string{"-5", "101", "1000"} {
		t.Run(value, func(t *testing.T) {
			licenseTestEnvironment(t)
			t.Setenv("CONTROL_LICENSE_WARNING_PERCENT", value)
			if _, err := LoadConfig(); err == nil {
				t.Fatalf("CONTROL_LICENSE_WARNING_PERCENT=%s should be rejected", value)
			}
		})
	}
	for _, value := range []string{"1", "100"} {
		t.Run(value, func(t *testing.T) {
			licenseTestEnvironment(t)
			t.Setenv("CONTROL_LICENSE_WARNING_PERCENT", value)
			cfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("CONTROL_LICENSE_WARNING_PERCENT=%s should be accepted: %v", value, err)
			}
			if cfg.LicenseWarningPercent != mustAtoi(t, value) {
				t.Fatalf("LicenseWarningPercent = %d, want %s", cfg.LicenseWarningPercent, value)
			}
		})
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestLicenseStatusUnlicensed(t *testing.T) {
	srv := licenseTestServer(t, Config{})

	status := srv.licenseStatus(context.Background())
	if status.State != "unlicensed" || status.Configured || status.Edition != "oss" {
		t.Fatalf("unexpected unlicensed status: %#v", status)
	}
	if status.Error != "not_configured" {
		t.Fatalf("error = %q, want not_configured", status.Error)
	}
	if status.Features == nil || len(status.Features) != 0 {
		t.Fatalf("features = %#v, want empty", status.Features)
	}
	if status.ExpiresAt != nil || status.LastModified != nil || status.LicenseID != "" || status.Tenant != "" {
		t.Fatalf("unlicensed status leaked license fields: %#v", status)
	}
	if status.UsedSeats != 0 || status.Seats != 0 || status.RemainingSeats != 0 || status.Warning {
		t.Fatalf("unlicensed seat fields = %#v", status)
	}
}

func TestLicenseStatusMissingFileIsUnlicensed(t *testing.T) {
	publicKey, _ := licenseTestKey(t)
	path := filepath.Join(t.TempDir(), "license.token")
	srv := licenseTestServer(t, Config{LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey)})

	status := srv.licenseStatus(context.Background())
	if !status.Configured || status.FilePath != path {
		t.Fatalf("configured/filePath wrong: %#v", status)
	}
	if status.State != "unlicensed" || status.Edition != "oss" || status.Error != "not_configured" {
		t.Fatalf("missing file should be an OSS empty state: %#v", status)
	}
	if status.LastModified != nil || status.ExpiresAt != nil {
		t.Fatalf("missing file should not report metadata: %#v", status)
	}
}

func TestLicenseStatusInvalidCategories(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)
	_, otherPrivateKey := licenseTestKey(t)
	validToken := licenseTestToken(t, privateKey, licenseTestLicense(now))
	tamperedParts := strings.Split(validToken, ".")
	tamperedParts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"version":1}`))
	tamperedToken := strings.Join(tamperedParts, ".")

	tests := []struct {
		name      string
		prepare   func(t *testing.T, dir string) (path, key string)
		wantError string
	}{
		{
			name: "malformed file",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, "not-a-license", 0o600)
				return path, licenseTestPublicKey(publicKey)
			},
			wantError: "malformed",
		},
		{
			name: "empty file",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, "", 0o600)
				return path, licenseTestPublicKey(publicKey)
			},
			wantError: "malformed",
		},
		{
			name: "tampered payload",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, tamperedToken, 0o600)
				return path, licenseTestPublicKey(publicKey)
			},
			wantError: "signature",
		},
		{
			name: "wrong key",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, licenseTestToken(t, otherPrivateKey, licenseTestLicense(now)), 0o600)
				return path, licenseTestPublicKey(publicKey)
			},
			wantError: "signature",
		},
		{
			name: "missing public key",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, validToken, 0o600)
				return path, ""
			},
			wantError: "not_configured",
		},
		{
			name: "invalid public key",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, validToken, 0o600)
				return path, "definitely-not-a-key"
			},
			wantError: "not_configured",
		},
		{
			name: "unreadable path",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license-directory")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return path, licenseTestPublicKey(publicKey)
			},
			wantError: "unreadable",
		},
		{
			name: "oversized file",
			prepare: func(t *testing.T, dir string) (string, string) {
				path := filepath.Join(dir, "license.token")
				licenseTestWriteFile(t, path, strings.Repeat("A", licenseTokenMaxBytes+1), 0o600)
				return path, licenseTestPublicKey(publicKey)
			},
			wantError: "unreadable",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			path, key := testCase.prepare(t, t.TempDir())
			srv := licenseTestServer(t, Config{LicenseFile: path, LicensePublicKey: key})
			status := srv.licenseStatus(context.Background())
			if status.State != "invalid" {
				t.Fatalf("state = %q, want invalid (%#v)", status.State, status)
			}
			if status.Edition != "oss" {
				t.Fatalf("edition = %q, want oss", status.Edition)
			}
			if status.Error != testCase.wantError {
				t.Fatalf("error = %q, want %q", status.Error, testCase.wantError)
			}
			if len(status.Features) != 0 {
				t.Fatalf("features = %#v, want empty", status.Features)
			}
			if status.ExpiresAt != nil || status.LicenseID != "" || status.Tenant != "" {
				t.Fatalf("invalid status leaked license fields: %#v", status)
			}
			// Fixed categories only: no parser output or file contents.
			for _, leak := range []string{path, "signature invalid", "base64", "json", "ed25519"} {
				if strings.Contains(strings.ToLower(status.Error), strings.ToLower(leak)) {
					t.Fatalf("error leaks internals: %q", status.Error)
				}
			}
		})
	}
}

func TestLicenseStatusLifecycle(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)

	tests := []struct {
		name         string
		expiresAt    time.Time
		graceDays    int
		wantState    string
		wantGrace    bool
		wantExpired  bool
		wantFeatures int
	}{
		{"valid", now.Add(30 * 24 * time.Hour), 7, "valid", false, false, 2},
		{"grace", now.Add(-2 * time.Hour), 7, "grace", true, false, 2},
		{"expired", now.Add(-48 * time.Hour), 0, "expired", false, true, 0},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			license := licenseTestLicense(now)
			license.ExpiresAt = testCase.expiresAt
			license.GraceDays = testCase.graceDays
			token := licenseTestToken(t, privateKey, license)
			path := filepath.Join(t.TempDir(), "license.token")
			licenseTestWriteFile(t, path, token, 0o600)

			srv := licenseTestServer(t, Config{LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey)})
			srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
				return 3, []map[string]any{}, nil
			}
			status := srv.licenseStatus(context.Background())

			if status.State != testCase.wantState || status.InGrace != testCase.wantGrace || status.Expired != testCase.wantExpired {
				t.Fatalf("lifecycle = %#v, want state %q grace=%t expired=%t", status, testCase.wantState, testCase.wantGrace, testCase.wantExpired)
			}
			if status.Edition != "enterprise" || status.Seats != 50 || status.LicenseID != "lic_control_test" || status.Tenant != "tenant_a" {
				t.Fatalf("license claims not reflected: %#v", status)
			}
			if status.ExpiresAt == nil || !status.ExpiresAt.Equal(testCase.expiresAt) {
				t.Fatalf("expiresAt = %#v, want %v", status.ExpiresAt, testCase.expiresAt)
			}
			if status.LastModified == nil {
				t.Fatalf("lastModified should be populated for a readable file")
			}
			if len(status.Features) != testCase.wantFeatures {
				t.Fatalf("features = %#v, want %d entries", status.Features, testCase.wantFeatures)
			}
			if status.UsedSeats != 3 || status.RemainingSeats != 47 || status.Warning {
				t.Fatalf("seat fields = %#v", status)
			}
			if status.Error != "" {
				t.Fatalf("error = %q, want empty", status.Error)
			}
		})
	}
}

func TestLicenseStatusSeatUsageScopes(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)

	t.Run("tenant scoped", func(t *testing.T) {
		license := licenseTestLicense(now)
		license.Tenant = "11111111-1111-1111-1111-111111111111"
		path := filepath.Join(t.TempDir(), "license.token")
		licenseTestWriteFile(t, path, licenseTestToken(t, privateKey, license), 0o600)

		srv := licenseTestServer(t, Config{LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey)})
		var seenTenant string
		srv.seatCounter = func(_ context.Context, tenant string) (int, []map[string]any, error) {
			seenTenant = tenant
			return 7, []map[string]any{{"tenantId": tenant, "name": "Tenant A", "activeSeats": 7}}, nil
		}
		status, byTenant := srv.licenseStatusView(context.Background())
		if seenTenant != license.Tenant {
			t.Fatalf("seat query tenant = %q, want %q", seenTenant, license.Tenant)
		}
		if status.UsedSeats != 7 || status.RemainingSeats != 43 {
			t.Fatalf("tenant seat usage = %#v", status)
		}
		if len(byTenant) != 0 {
			t.Fatalf("tenant-scoped license must not expose per-tenant rows: %#v", byTenant)
		}
	})

	t.Run("global", func(t *testing.T) {
		license := licenseTestLicense(now)
		license.Tenant = ""
		path := filepath.Join(t.TempDir(), "license.token")
		licenseTestWriteFile(t, path, licenseTestToken(t, privateKey, license), 0o600)

		srv := licenseTestServer(t, Config{LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey)})
		var seenTenant string
		rows := []map[string]any{
			{"tenantId": "t1", "name": "Alpha", "activeSeats": 4},
			{"tenantId": "t2", "name": "Beta", "activeSeats": 5},
		}
		srv.seatCounter = func(_ context.Context, tenant string) (int, []map[string]any, error) {
			seenTenant = tenant
			return 9, rows, nil
		}
		status, byTenant := srv.licenseStatusView(context.Background())
		if seenTenant != "" {
			t.Fatalf("global seat query tenant = %q, want empty", seenTenant)
		}
		if status.UsedSeats != 9 || status.RemainingSeats != 41 {
			t.Fatalf("global seat usage = %#v", status)
		}
		if len(byTenant) != 2 || byTenant[0]["name"] != "Alpha" || byTenant[1]["name"] != "Beta" {
			t.Fatalf("byTenant = %#v", byTenant)
		}
	})
}

func TestLicenseStatusSeatErrorDegradesSafely(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)
	path := filepath.Join(t.TempDir(), "license.token")
	licenseTestWriteFile(t, path, licenseTestToken(t, privateKey, licenseTestLicense(now)), 0o600)

	srv := licenseTestServer(t, Config{LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey)})
	srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
		return 0, nil, errors.New("database unavailable")
	}
	status, byTenant := srv.licenseStatusView(context.Background())
	if status.State != "valid" {
		t.Fatalf("seat failure should not invalidate the license: %#v", status)
	}
	if status.UsedSeats != 0 || status.RemainingSeats != 50 || status.Warning {
		t.Fatalf("seat failure should degrade to zero usage: %#v", status)
	}
	if byTenant == nil || len(byTenant) != 0 {
		t.Fatalf("byTenant = %#v, want empty slice", byTenant)
	}
}

func TestLicenseWarningThreshold(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)
	token := licenseTestToken(t, privateKey, licenseTestLicense(now))

	tests := []struct {
		name       string
		used       int
		percent    int
		wantWarn   bool
		wantRemain int
	}{
		{"below warning", 39, 80, false, 11},
		{"at warning", 40, 80, true, 10},
		{"at seat limit", 50, 80, true, 0},
		{"over seat limit", 51, 80, true, 0},
		{"full threshold below", 49, 100, false, 1},
		{"full threshold reached", 50, 100, true, 0},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "license.token")
			licenseTestWriteFile(t, path, token, 0o600)
			srv := licenseTestServer(t, Config{
				LicenseFile: path, LicensePublicKey: licenseTestPublicKey(publicKey),
				LicenseWarningPercent: testCase.percent,
			})
			srv.seatCounter = func(context.Context, string) (int, []map[string]any, error) {
				return testCase.used, []map[string]any{}, nil
			}
			status := srv.licenseStatus(context.Background())
			if status.Warning != testCase.wantWarn {
				t.Fatalf("warning = %t, want %t (used=%d percent=%d)", status.Warning, testCase.wantWarn, testCase.used, testCase.percent)
			}
			if status.RemainingSeats != testCase.wantRemain {
				t.Fatalf("remainingSeats = %d, want %d", status.RemainingSeats, testCase.wantRemain)
			}
		})
	}
}

func TestWriteLicenseTokenWritesAtomically(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	now := time.Now()
	path := filepath.Join(t.TempDir(), "license.token")
	token := licenseTestToken(t, privateKey, licenseTestLicense(now))

	if err := writeLicenseToken(path, licenseTestPublicKey(publicKey), token); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != token {
		t.Fatalf("written token does not match the verified token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}

	// Grace tokens are accepted and replace the previous file.
	grace := licenseTestLicense(now)
	grace.ExpiresAt = now.Add(-time.Hour)
	grace.GraceDays = 7
	graceToken := licenseTestToken(t, privateKey, grace)
	if err := writeLicenseToken(path, licenseTestPublicKey(publicKey), graceToken); err != nil {
		t.Fatalf("grace token should be accepted: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != graceToken {
		t.Fatalf("grace token was not written")
	}
}

func TestWriteLicenseTokenRejectsAndPreservesFile(t *testing.T) {
	now := time.Now()
	publicKey, privateKey := licenseTestKey(t)
	_, otherPrivateKey := licenseTestKey(t)
	oldToken := licenseTestToken(t, privateKey, licenseTestLicense(now))

	expired := licenseTestLicense(now)
	expired.ExpiresAt = now.Add(-48 * time.Hour)
	expired.GraceDays = 0
	expiredToken := licenseTestToken(t, privateKey, expired)

	tests := []struct {
		name       string
		token      string
		key        string
		configured bool
		wantStatus int
		wantCode   string
	}{
		{"expired", expiredToken, licenseTestPublicKey(publicKey), true, 400, "LICENSE_EXPIRED"},
		{"malformed", "not-a-license", licenseTestPublicKey(publicKey), true, 400, "LICENSE_TOKEN"},
		{"wrong key", licenseTestToken(t, otherPrivateKey, licenseTestLicense(now)), licenseTestPublicKey(publicKey), true, 400, "LICENSE_TOKEN"},
		{"missing key", oldToken, "", true, 503, "LICENSE_NOT_CONFIGURED"},
		{"unconfigured path", oldToken, licenseTestPublicKey(publicKey), false, 503, "LICENSE_NOT_CONFIGURED"},
		{"oversized token", strings.Repeat("A", licenseTokenMaxBytes+1), licenseTestPublicKey(publicKey), true, 400, "LICENSE_TOKEN"},
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

			configuredPath := path
			if !testCase.configured {
				configuredPath = ""
			}
			err = writeLicenseToken(configuredPath, testCase.key, testCase.token)
			licenseTestAssertAPIError(t, err, testCase.wantStatus, testCase.wantCode)

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
			licenseTestAssertNoTempFiles(t, dir)
		})
	}
}

func TestWriteLicenseTokenCleansUpFailedRename(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	now := time.Now()
	dir := t.TempDir()
	path := filepath.Join(dir, "license-directory")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	err := writeLicenseToken(path, licenseTestPublicKey(publicKey), licenseTestToken(t, privateKey, licenseTestLicense(now)))
	licenseTestAssertAPIError(t, err, 500, "LICENSE_WRITE")
	info, statErr := os.Stat(path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("rename failure disturbed the destination: %v", statErr)
	}
	licenseTestAssertNoTempFiles(t, dir)
}

func licenseTestAssertAPIError(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with code %s", wantCode)
	}
	var failure *apiFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not an apiFailure", err)
	}
	if failure.Status != wantStatus || failure.Code != wantCode {
		t.Fatalf("error = %d/%s, want %d/%s", failure.Status, failure.Code, wantStatus, wantCode)
	}
	if strings.Contains(failure.Message, "A8M1") {
		t.Fatalf("error message leaks the token: %q", failure.Message)
	}
}

func licenseTestAssertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".license-token-") {
			t.Fatalf("temporary license file was left behind: %s", entry.Name())
		}
	}
}
