package httpapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
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

func TestLicenseSnapshotAndEntitlements(t *testing.T) {
	server := &Server{}
	oss := server.licenseSnapshot()
	if oss["edition"] != "oss" || oss["state"] != "unlicensed" || server.entitled("sso") {
		t.Fatalf("unexpected OSS snapshot: %#v", oss)
	}

	// A configured license file that failed verification is reported as
	// invalid rather than unlicensed, and still grants no paid features.
	invalid := (&Server{licenseConfigured: true}).licenseSnapshot()
	if invalid["edition"] != "oss" || invalid["state"] != "invalid" {
		t.Fatalf("unexpected invalid snapshot: %#v", invalid)
	}

	now := time.Now()
	expires := now.Add(10 * 24 * time.Hour)
	clock := newMutableClock(now)
	license := &licensing.License{
		Version: licensing.Version, ID: "lic_test", Edition: "enterprise", Seats: 10,
		Features: []string{"sso", "shared_inbox"}, ExpiresAt: expires, GraceDays: 3,
	}
	server = &Server{license: license, licenseNow: clock.Now}
	snapshot := server.licenseSnapshot()
	if snapshot["edition"] != "enterprise" || snapshot["state"] != "valid" || snapshot["seats"] != 10 {
		t.Fatalf("unexpected paid snapshot: %#v", snapshot)
	}
	if !server.entitled("SSO") || server.entitled("scim") {
		t.Fatalf("unexpected feature entitlement: %#v", snapshot)
	}

	// Grace keeps paid features usable.
	clock.Set(expires.Add(24 * time.Hour))
	grace := server.licenseSnapshot()
	if grace["state"] != "grace" || grace["inGrace"] != true || grace["expired"] != false {
		t.Fatalf("unexpected grace snapshot: %#v", grace)
	}
	if !server.entitled("sso") {
		t.Fatal("grace-period license must keep paid features usable")
	}

	// Expiry disables entitlement and drops features from the snapshot.
	clock.Set(expires.Add(4 * 24 * time.Hour))
	expired := server.licenseSnapshot()
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
