package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/jackc/pgx/v5"
)

func testLicenseToken(t *testing.T, privateKey ed25519.PrivateKey, license licensing.License) string {
	t.Helper()
	payload, err := json.Marshal(license)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := licensing.EnvelopePrefix + "." + encoded
	signature := ed25519.Sign(privateKey, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// mutableClock drives the injectable license clock in tests.
type mutableClock struct {
	now time.Time
}

func newMutableClock(now time.Time) *mutableClock {
	return &mutableClock{now: now}
}

func (c *mutableClock) Now() time.Time {
	return c.now
}

func (c *mutableClock) Set(now time.Time) {
	c.now = now
}

func (c *mutableClock) Advance(delta time.Duration) {
	c.now = c.now.Add(delta)
}

func TestLoadLicense(t *testing.T) {
	if license, err := loadLicense(config.Config{}, nil); err != nil || license != nil {
		t.Fatalf("unlicensed config = %#v %v", license, err)
	}
	if license, err := loadLicense(config.Config{LicenseFile: "   "}, nil); err != nil || license != nil {
		t.Fatalf("whitespace license path = %#v %v", license, err)
	}
	// The installer now provisions ALT8MAIL_LICENSE_FILE on fresh OSS installs
	// before any token exists. A configured-but-absent file must be OSS, not a
	// startup error or an "invalid" state.
	missing := filepath.Join(t.TempDir(), "missing-license.token")
	if license, err := loadLicense(config.Config{LicenseFile: missing, LicensePublicKey: "not-used"}, nil); err != nil || license != nil {
		t.Fatalf("missing license file = %#v %v; want nil, nil", license, err)
	}
	oversized := filepath.Join(t.TempDir(), "oversized-license.token")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("A", maxLicenseFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLicense(config.Config{LicenseFile: oversized, LicensePublicKey: "not-used"}, nil); err == nil {
		t.Fatal("oversized license file was accepted")
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	license := licensing.License{
		Version: licensing.Version, ID: "lic_test", Edition: "enterprise", Seats: 25,
		Features: []string{"sso", "scim"}, IssuedAt: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour), GraceDays: 7,
	}
	file := filepath.Join(t.TempDir(), "license.a8m")
	if err := os.WriteFile(file, []byte(testLicenseToken(t, privateKey, license)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		LicenseFile:      file,
		LicensePublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
	}
	loaded, err := loadLicense(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Edition != "enterprise" || loaded.Seats != 25 {
		t.Fatalf("unexpected license: %#v", loaded)
	}

	cfg.LicensePublicKey = "not-a-key"
	if _, err := loadLicense(cfg, nil); err == nil {
		t.Fatal("invalid public key was accepted")
	}

	if err := os.WriteFile(file, []byte("A8M1.invalid.invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.LicensePublicKey = base64.RawURLEncoding.EncodeToString(publicKey)
	if _, err := loadLicense(cfg, nil); err == nil {
		t.Fatal("invalid license envelope was accepted")
	}
}

// TestMissingLicenseFileIsUnlicensed covers the installer-provisioned path: a
// fresh OSS install sets ALT8MAIL_LICENSE_FILE before a token exists, and the
// webmail must report OSS rather than invalid. An existing unreadable or
// unverifiable file remains invalid.
func TestMissingLicenseFileIsUnlicensed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	missingServer := New(Options{
		Config: config.Config{
			LicenseFile:      filepath.Join(t.TempDir(), "license.token"),
			LicensePublicKey: "bm90LWEta2V5",
		},
		Logger: logger,
	})
	if missingServer.license != nil || missingServer.licenseConfigured || missingServer.licenseError != "" {
		t.Fatalf("missing file server = license %#v, configured %v, error %q; want nil, false, empty",
			missingServer.license, missingServer.licenseConfigured, missingServer.licenseError)
	}
	snapshot := missingServer.licenseSnapshot(context.Background())
	if snapshot["state"] != "unlicensed" || snapshot["edition"] != "oss" {
		t.Fatalf("missing file snapshot = %#v; want unlicensed/oss", snapshot)
	}

	invalidFile := filepath.Join(t.TempDir(), "license.token")
	if err := os.WriteFile(invalidFile, []byte("A8M1.invalid.invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidServer := New(Options{
		Config: config.Config{
			LicenseFile:      invalidFile,
			LicensePublicKey: "bm90LWEta2V5",
		},
		Logger: logger,
	})
	if invalidServer.license != nil || !invalidServer.licenseConfigured || invalidServer.licenseError == "" {
		t.Fatalf("invalid file server = license %#v, configured %v, error %q; want nil, true, non-empty",
			invalidServer.license, invalidServer.licenseConfigured, invalidServer.licenseError)
	}
	invalid := invalidServer.licenseSnapshot(context.Background())
	if invalid["state"] != "invalid" || invalid["edition"] != "oss" {
		t.Fatalf("invalid file snapshot = %#v; want invalid/oss", invalid)
	}
}

func TestLicenseSnapshotAndEntitlements(t *testing.T) {
	ctx := context.Background()
	server := &Server{}
	oss := server.licenseSnapshot(ctx)
	if oss["edition"] != "oss" || oss["state"] != "unlicensed" || server.entitled("sso") {
		t.Fatalf("unexpected OSS snapshot: %#v", oss)
	}
	if oss["usedSeats"] != 0 || oss["seatLimit"] != 0 || oss["seats"] != 0 {
		t.Fatalf("unexpected OSS seat fields: %#v", oss)
	}

	// A configured license file that exists but failed verification is reported
	// as invalid rather than unlicensed, and still grants no paid features.
	invalid := (&Server{licenseConfigured: true}).licenseSnapshot(ctx)
	if invalid["edition"] != "oss" || invalid["state"] != "invalid" {
		t.Fatalf("unexpected invalid snapshot: %#v", invalid)
	}
	if invalid["usedSeats"] != 0 || invalid["seatLimit"] != 0 {
		t.Fatalf("unexpected invalid seat fields: %#v", invalid)
	}

	now := time.Now()
	expires := now.Add(10 * 24 * time.Hour)
	clock := newMutableClock(now)
	license := &licensing.License{
		Version: licensing.Version, ID: "lic_test", Edition: "enterprise", Seats: 10,
		Features: []string{"sso", "shared_inbox"}, ExpiresAt: expires, GraceDays: 3,
	}
	server = &Server{license: license, licenseNow: clock.Now}
	snapshot := server.licenseSnapshot(ctx)
	if snapshot["edition"] != "enterprise" || snapshot["state"] != "valid" || snapshot["seats"] != 10 {
		t.Fatalf("unexpected paid snapshot: %#v", snapshot)
	}
	if snapshot["seatLimit"] != 10 || snapshot["usedSeats"] != 0 {
		t.Fatalf("unexpected paid seat fields: %#v", snapshot)
	}
	if !server.entitled("SSO") || server.entitled("scim") {
		t.Fatalf("unexpected feature entitlement: %#v", snapshot)
	}

	// Grace keeps paid features usable.
	clock.Set(expires.Add(24 * time.Hour))
	grace := server.licenseSnapshot(ctx)
	if grace["state"] != "grace" || grace["inGrace"] != true || grace["expired"] != false {
		t.Fatalf("unexpected grace snapshot: %#v", grace)
	}
	if grace["seatLimit"] != 10 || grace["usedSeats"] != 0 {
		t.Fatalf("unexpected grace seat fields: %#v", grace)
	}
	if !server.entitled("sso") {
		t.Fatal("grace-period license must keep paid features usable")
	}

	// Expiry disables entitlement and drops features from the snapshot.
	clock.Set(expires.Add(4 * 24 * time.Hour))
	expired := server.licenseSnapshot(ctx)
	if expired["state"] != "expired" || expired["expired"] != true || server.entitled("sso") {
		t.Fatalf("unexpected expired snapshot: %#v", expired)
	}
	features, ok := expired["features"].([]string)
	if !ok || len(features) != 0 {
		t.Fatalf("expired snapshot advertises features: %#v", expired["features"])
	}
}

// TestLicenseLifecycleAcrossServerLifetime builds a server through New (the
// production path) and moves its injected clock through the license lifecycle.
// It proves status is evaluated on demand rather than frozen at startup.
func TestLicenseLifecycleAcrossServerLifetime(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	license := licensing.License{
		Version: licensing.Version, ID: "lic_lifecycle", Edition: "enterprise", Seats: 5,
		Features: []string{"sso"}, IssuedAt: start.Add(-time.Hour),
		ExpiresAt: start.Add(24 * time.Hour), GraceDays: 2,
	}
	file := filepath.Join(t.TempDir(), "license.a8m")
	if err := os.WriteFile(file, []byte(testLicenseToken(t, privateKey, license)), 0o600); err != nil {
		t.Fatal(err)
	}
	server := New(Options{
		Config: config.Config{
			LicenseFile:      file,
			LicensePublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	clock := newMutableClock(start)
	server.licenseNow = clock.Now

	snapshot := func() map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/entitlements", nil)
		server.getEntitlements(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("entitlements status = %d", recorder.Code)
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode entitlements: %v", err)
		}
		return payload
	}

	valid := snapshot()
	if valid["state"] != "valid" || valid["expired"] != false || !server.entitled("sso") {
		t.Fatalf("unexpected valid lifecycle state: %#v", valid)
	}

	clock.Set(start.Add(25 * time.Hour)) // past expiry, inside the grace period
	grace := snapshot()
	if grace["state"] != "grace" || grace["inGrace"] != true || grace["expired"] != false {
		t.Fatalf("unexpected grace lifecycle state: %#v", grace)
	}
	if !server.entitled("sso") {
		t.Fatal("grace-period server stopped entitling sso")
	}

	clock.Set(start.Add(24*time.Hour + 3*24*time.Hour)) // past expiry + graceDays
	expired := snapshot()
	if expired["state"] != "expired" || expired["expired"] != true {
		t.Fatalf("unexpected expired lifecycle state: %#v", expired)
	}
	if server.entitled("sso") {
		t.Fatal("expired server still entitled sso")
	}
	features, ok := expired["features"].([]any)
	if !ok || len(features) != 0 {
		t.Fatalf("expired server advertises features: %#v", expired["features"])
	}
}

func seatTestLicense(seats int, tenant string, expires time.Time, graceDays int) *licensing.License {
	return &licensing.License{
		Version: licensing.Version, ID: "lic_seat_test", Tenant: tenant,
		Edition: "enterprise", Seats: seats, ExpiresAt: expires, GraceDays: graceDays,
	}
}

func seatTestServer(license *licensing.License, now time.Time) *Server {
	return &Server{license: license, licenseNow: func() time.Time { return now }}
}

func TestLoadLicenseTenantClaim(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name          string
		payloadTenant string
		configTenant  string
		wantErr       bool
	}{
		{name: "global payload, unconfigured expectation", payloadTenant: ""},
		{name: "tenant payload, unconfigured expectation", payloadTenant: "tenant-a"},
		{name: "matching tenant", payloadTenant: "tenant-a", configTenant: "tenant-a"},
		{name: "matching tenant with whitespace", payloadTenant: "tenant-a", configTenant: "  tenant-a  "},
		{name: "mismatched tenant", payloadTenant: "tenant-a", configTenant: "tenant-b", wantErr: true},
		{name: "expected tenant but global payload", payloadTenant: "", configTenant: "tenant-a", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			license := licensing.License{
				Version: licensing.Version, ID: "lic_tenant", Tenant: tc.payloadTenant,
				Edition: "enterprise", Seats: 5, IssuedAt: time.Now().Add(-time.Hour),
				ExpiresAt: time.Now().Add(24 * time.Hour), GraceDays: 1,
			}
			file := filepath.Join(t.TempDir(), "license.a8m")
			if err := os.WriteFile(file, []byte(testLicenseToken(t, privateKey, license)), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{
				LicenseFile:      file,
				LicensePublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				LicenseTenant:    tc.configTenant,
			}
			loaded, err := loadLicense(cfg, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("loadLicense accepted payload tenant %q for expected tenant %q", tc.payloadTenant, tc.configTenant)
				}
				if loaded != nil {
					t.Fatalf("mismatched license was returned: %#v", loaded)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadLicense: %v", err)
			}
			if loaded == nil || loaded.Tenant != tc.payloadTenant {
				t.Fatalf("loaded license = %#v, want tenant %q", loaded, tc.payloadTenant)
			}
		})
	}
}

func TestLicenseSeatEnforcementStateMatrix(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		server   *Server
		enforced bool
	}{
		{name: "oss (missing or unconfigured file)", server: &Server{}},
		{name: "invalid (configured file failed to load)", server: &Server{licenseConfigured: true, licenseError: "verify license: signature"}},
		{name: "valid", server: seatTestServer(seatTestLicense(10, "", now.Add(time.Hour), 0), now), enforced: true},
		{name: "grace", server: seatTestServer(seatTestLicense(10, "", now.Add(-time.Hour), 7), now), enforced: true},
		{name: "expired", server: seatTestServer(seatTestLicense(10, "", now.Add(-8*24*time.Hour), 7), now)},
		{name: "valid without seats", server: seatTestServer(seatTestLicense(0, "", now.Add(time.Hour), 0), now)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.server.seatEnforced(); got != tc.enforced {
				t.Fatalf("seatEnforced() = %v, want %v", got, tc.enforced)
			}
		})
	}
}

func TestSeatAvailableTenantScope(t *testing.T) {
	now := time.Now()
	ctx := context.Background()
	scoped := seatTestServer(seatTestLicense(2, "tenant-a", now.Add(time.Hour), 0), now)
	if available, err := scoped.seatAvailable(ctx, "tenant-b"); err != nil || !available {
		t.Fatalf("unlicensed tenant available = %v, %v; want true, nil", available, err)
	}
	// The licensed tenant needs a real count; without a database the helper
	// fails closed instead of granting a seat.
	if available, err := scoped.seatAvailable(ctx, "tenant-a"); err == nil || available {
		t.Fatalf("licensed tenant without database available = %v, %v; want false, error", available, err)
	}

	expired := seatTestServer(seatTestLicense(2, "tenant-a", now.Add(-8*24*time.Hour), 7), now)
	if available, err := expired.seatAvailable(ctx, "tenant-a"); err != nil || !available {
		t.Fatalf("expired license available = %v, %v; want true, nil", available, err)
	}

	global := seatTestServer(seatTestLicense(2, "", now.Add(time.Hour), 0), now)
	if available, err := global.seatAvailable(ctx, "tenant-a"); err == nil || available {
		t.Fatalf("global license without database available = %v, %v; want false, error", available, err)
	}
}

type seatCountRow struct {
	count int
	err   error
}

func (r seatCountRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("unexpected scan destinations")
	}
	value, ok := dest[0].(*int)
	if !ok {
		return errors.New("unexpected scan destination type")
	}
	*value = r.count
	return nil
}

type seatCountQuery struct {
	sql   string
	args  []any
	count int
	err   error
}

func (q *seatCountQuery) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.sql, q.args = sql, args
	return seatCountRow{count: q.count, err: q.err}
}

func TestActiveSeatCountScopesQuery(t *testing.T) {
	now := time.Now()
	ctx := context.Background()

	global := seatTestServer(seatTestLicense(5, "", now.Add(time.Hour), 0), now)
	globalQuery := &seatCountQuery{count: 3}
	count, err := global.countActiveSeats(ctx, globalQuery, "tenant-ignored")
	if err != nil || count != 3 {
		t.Fatalf("global count = %d, %v; want 3, nil", count, err)
	}
	for _, fragment := range []string{"status = 'active'", "deleted_at IS NULL", "is_system_admin = false"} {
		if !strings.Contains(globalQuery.sql, fragment) {
			t.Fatalf("seat query %q missing %q", globalQuery.sql, fragment)
		}
	}
	if strings.Contains(globalQuery.sql, "tenant_id") {
		t.Fatalf("global seat query scoped to a tenant: %q", globalQuery.sql)
	}
	if len(globalQuery.args) != 0 {
		t.Fatalf("global seat query args = %#v, want none", globalQuery.args)
	}

	scoped := seatTestServer(seatTestLicense(2, " tenant-a ", now.Add(time.Hour), 0), now)
	scopedQuery := &seatCountQuery{count: 2}
	count, err = scoped.countActiveSeats(ctx, scopedQuery, "tenant-b")
	if err != nil || count != 2 {
		t.Fatalf("scoped count = %d, %v; want 2, nil", count, err)
	}
	if !strings.Contains(scopedQuery.sql, "tenant_id = $1") {
		t.Fatalf("tenant-scoped seat query missing tenant filter: %q", scopedQuery.sql)
	}
	if len(scopedQuery.args) != 1 || scopedQuery.args[0] != "tenant-a" {
		t.Fatalf("tenant-scoped seat query args = %#v, want the trimmed license claim", scopedQuery.args)
	}

	failing := &seatCountQuery{err: errors.New("count failed")}
	if _, err := scoped.countActiveSeats(ctx, failing, "tenant-a"); err == nil {
		t.Fatal("countActiveSeats swallowed a database error")
	}
	if _, err := scoped.activeSeatCount(ctx, "tenant-a"); err == nil {
		t.Fatal("activeSeatCount succeeded without a database")
	}
}
