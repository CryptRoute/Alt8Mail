package httpapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

func TestLoadLicense(t *testing.T) {
	if license, status, err := loadLicense(config.Config{}, nil); err != nil || license != nil || status.State != "" {
		t.Fatalf("unlicensed config = %#v %#v %v", license, status, err)
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
	loaded, status, err := loadLicense(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Edition != "enterprise" || status.State != "valid" || status.SeatLimit != 25 {
		t.Fatalf("unexpected license: %#v %#v", loaded, status)
	}

	cfg.LicensePublicKey = "not-a-key"
	if _, _, err := loadLicense(cfg, nil); err == nil {
		t.Fatal("invalid public key was accepted")
	}

	if err := os.WriteFile(file, []byte("A8M1.invalid.invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.LicensePublicKey = base64.RawURLEncoding.EncodeToString(publicKey)
	if _, _, err := loadLicense(cfg, nil); err == nil {
		t.Fatal("invalid license envelope was accepted")
	}
}

func TestLicenseSnapshotAndEntitlements(t *testing.T) {
	server := &Server{}
	oss := server.licenseSnapshot()
	if oss["edition"] != "oss" || oss["state"] != "unlicensed" || server.entitled("sso") {
		t.Fatalf("unexpected OSS snapshot: %#v", oss)
	}

	expires := time.Now().Add(10 * 24 * time.Hour)
	license := &licensing.License{
		Version: licensing.Version, ID: "lic_test", Edition: "enterprise", Seats: 10,
		Features: []string{"sso", "shared_inbox"}, ExpiresAt: expires, GraceDays: 3,
	}
	server.license = license
	server.licenseStatus = license.Status(time.Now())
	snapshot := server.licenseSnapshot()
	if snapshot["edition"] != "enterprise" || snapshot["state"] != "valid" || snapshot["seats"] != 10 {
		t.Fatalf("unexpected paid snapshot: %#v", snapshot)
	}
	if !server.entitled("SSO") || server.entitled("scim") {
		t.Fatalf("unexpected feature entitlement: %#v", snapshot)
	}
	server.licenseStatus = license.Status(expires.Add(4 * 24 * time.Hour))
	if server.entitled("sso") {
		t.Fatal("expired license must not entitle paid features")
	}
}
