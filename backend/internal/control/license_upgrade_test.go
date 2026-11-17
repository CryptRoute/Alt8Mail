package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/pkg/licensing"
)

func signedCoverageLicense(t *testing.T) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := licensing.License{
		Version: licensing.Version, ID: "lic_upgrade_test", KeyID: "2026-11",
		Edition: "enterprise", Plan: "team", Seats: 25,
		Features: []string{"sso", "scim"}, IssuedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour), GraceDays: 0,
	}
	token, err := licensing.SignFunc(func(message []byte) ([]byte, error) {
		return ed25519.Sign(privateKey, message), nil
	}, claims)
	if err != nil {
		t.Fatal(err)
	}
	return token, base64.RawURLEncoding.EncodeToString(publicKey)
}

func testUpgradeServer(t *testing.T, portalURL, licenseFile, publicKey string) *Server {
	t.Helper()
	return &Server{
		cfg: Config{
			Env: "test", LicensePortalURL: portalURL, LicenseFile: licenseFile,
			LicensePublicKey: publicKey, LicenseGroup: "0",
			EnterpriseStateDir: t.TempDir(), DeploymentIDFile: filepath.Join(t.TempDir(), "deployment.id"),
			EnterpriseInstallRoot: t.TempDir(),
		},
		logger: nil,
	}
}

func TestLicenseUpgradeStartBuildsPortalURL(t *testing.T) {
	server := testUpgradeServer(t, "https://licenses.example.test", filepath.Join(t.TempDir(), "license.token"), "unused")
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/control/api/license/upgrade/start", nil)
	recorder := httptest.NewRecorder()
	server.handleLicenseUpgradeStart(recorder, request, &sessionRecord{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("start = %d %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		PortalURL    string `json:"portalUrl"`
		DeploymentID string `json:"deploymentId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.PortalURL, "https://licenses.example.test/upgrade?") {
		t.Fatalf("portal url = %q", body.PortalURL)
	}
	if !strings.Contains(body.PortalURL, "deployment=") || !strings.Contains(body.PortalURL, "return=https%3A%2F%2Fadmin.example.test%2Fcontrol%2Fapi%2Flicense%2Fupgrade%2Fcallback") {
		t.Fatalf("portal url = %q", body.PortalURL)
	}
	job, err := server.loadUpgradeJob()
	if err != nil || job == nil || job.DeploymentID != body.DeploymentID {
		t.Fatalf("job = %#v err=%v", job, err)
	}
	if _, err := os.Stat(server.cfg.DeploymentIDFile); err != nil {
		t.Fatalf("deployment id file: %v", err)
	}
}

func TestLicenseUpgradeRunInstallsToken(t *testing.T) {
	token, publicKey := signedCoverageLicense(t)
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/upgrades/claim" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"license": map[string]any{"id": "lic_upgrade_test", "token": token},
		})
	}))
	defer portal.Close()

	licenseFile := filepath.Join(t.TempDir(), "license.token")
	server := testUpgradeServer(t, portal.URL, licenseFile, publicKey)
	installed := false
	server.enterpriseInstall = func(context.Context, string, string) error {
		installed = true
		return nil
	}
	job := &licenseUpgradeJob{DeploymentID: "dep_test", Phase: "claiming", ExpiresAt: time.Now().Add(time.Hour)}
	server.runLicenseUpgrade(job, "claim-code")
	if job.Phase != "complete" || job.Error != "" {
		t.Fatalf("job = %#v", job)
	}
	if installed {
		t.Fatal("installer ran without a bundle")
	}
	raw, err := os.ReadFile(licenseFile)
	if err != nil || strings.TrimSpace(string(raw)) != token {
		t.Fatalf("license file = %q err=%v", raw, err)
	}
}

func TestLicenseUpgradeDownloadsAndInstallsBundle(t *testing.T) {
	token, publicKey := signedCoverageLicense(t)
	bundle := buildTestEnterpriseBundle(t, "9.9.9")
	sum := sha256.Sum256(bundle)
	var bundleServer *httptest.Server
	bundleServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bundle)
	}))
	defer bundleServer.Close()
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"license": map[string]any{"id": "lic_upgrade_test", "token": token},
			"bundle": map[string]any{
				"url": bundleServer.URL + "/enterprise.tar.gz", "sha256": hex.EncodeToString(sum[:]),
				"version": "9.9.9",
			},
		})
	}))
	defer portal.Close()
	licenseFile := filepath.Join(t.TempDir(), "license.token")
	server := testUpgradeServer(t, portal.URL, licenseFile, publicKey)
	var installedPath, installedVersion string
	server.enterpriseInstall = func(_ context.Context, path, version string) error {
		installedPath, installedVersion = path, version
		if _, err := os.Stat(path); err != nil {
			return err
		}
		return nil
	}
	job := &licenseUpgradeJob{DeploymentID: "dep_test", Phase: "claiming", ExpiresAt: time.Now().Add(time.Hour)}
	server.runLicenseUpgrade(job, "claim-code")
	if job.Phase != "complete" || !job.Enterprise || job.Version != "9.9.9" {
		t.Fatalf("job = %#v", job)
	}
	if installedPath == "" || installedVersion != "9.9.9" {
		t.Fatalf("installer call = %q %q", installedPath, installedVersion)
	}
}

func TestLicenseUpgradeRejectsBadBundleChecksum(t *testing.T) {
	token, publicKey := signedCoverageLicense(t)
	bundle := buildTestEnterpriseBundle(t, "9.9.9")
	bundleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bundle)
	}))
	defer bundleServer.Close()
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"license": map[string]any{"id": "lic_upgrade_test", "token": token},
			"bundle": map[string]any{
				"url": bundleServer.URL + "/enterprise.tar.gz", "sha256": strings.Repeat("0", 64),
				"version": "9.9.9",
			},
		})
	}))
	defer portal.Close()
	server := testUpgradeServer(t, portal.URL, filepath.Join(t.TempDir(), "license.token"), publicKey)
	server.enterpriseInstall = func(context.Context, string, string) error {
		t.Fatal("installer ran with a bad checksum")
		return nil
	}
	job := &licenseUpgradeJob{DeploymentID: "dep_test", Phase: "claiming", ExpiresAt: time.Now().Add(time.Hour)}
	server.runLicenseUpgrade(job, "claim-code")
	if job.Phase != "error" || !strings.Contains(job.Error, "checksum") {
		t.Fatalf("job = %#v", job)
	}
}

func TestVerifyEnterpriseBundleSignature(t *testing.T) {
	bundle := []byte("enterprise release")
	sum := sha256.Sum256(bundle)
	checksum := hex.EncodeToString(sum[:])
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString(publicKey)
	signature := base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(privateKey, []byte("a8m-enterprise-release-sha256:"+checksum)))
	if err := verifyEnterpriseBundleSignature(key, signature, checksum); err != nil {
		t.Fatalf("valid signature = %v", err)
	}
	if err := verifyEnterpriseBundleSignature("", signature, checksum); err == nil {
		t.Fatal("signed bundle accepted without a key")
	}
	if err := verifyEnterpriseBundleSignature(key, "", checksum); err == nil {
		t.Fatal("unsigned bundle accepted with a strict key")
	}
	if err := verifyEnterpriseBundleSignature(key, signature, strings.Repeat("0", 64)); err == nil {
		t.Fatal("signature accepted for a different checksum")
	}
	if err := verifyEnterpriseBundleSignature("", "", checksum); err != nil {
		t.Fatalf("optional unsigned bundle = %v", err)
	}
}

func TestLicenseUpgradeRejectsBadBundleSignature(t *testing.T) {
	token, licenseKey := signedCoverageLicense(t)
	bundle := buildTestEnterpriseBundle(t, "9.9.9")
	sum := sha256.Sum256(bundle)
	bundleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bundle)
	}))
	defer bundleServer.Close()
	releaseKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"license": map[string]any{"id": "lic_upgrade_test", "token": token},
			"bundle": map[string]any{
				"url": bundleServer.URL + "/enterprise.tar.gz", "sha256": hex.EncodeToString(sum[:]),
				"version": "9.9.9", "signature": base64.RawURLEncoding.EncodeToString([]byte("not-a-signature")),
				"publicKey": base64.RawURLEncoding.EncodeToString(releaseKey),
			},
		})
	}))
	defer portal.Close()
	server := testUpgradeServer(t, portal.URL, filepath.Join(t.TempDir(), "license.token"), licenseKey)
	server.enterpriseInstall = func(context.Context, string, string) error {
		t.Fatal("installer ran with a bad signature")
		return nil
	}
	job := &licenseUpgradeJob{DeploymentID: "dep_test", Phase: "claiming", ExpiresAt: time.Now().Add(time.Hour)}
	server.runLicenseUpgrade(job, "claim-code")
	if job.Phase != "error" || !strings.Contains(job.Error, "signature") {
		t.Fatalf("job = %#v", job)
	}
}

func TestExtractEnterpriseBundleRejectsTraversal(t *testing.T) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 3})
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()
	path := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(path, t.TempDir()); err == nil {
		t.Fatal("traversal archive was accepted")
	}
}

func TestInstallEnterpriseReleaseOverlaysCurrentRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release symlink semantics are POSIX")
	}
	root := t.TempDir()
	base := filepath.Join(root, "releases", "base")
	if err := os.MkdirAll(filepath.Join(base, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"bin/a8m-server":         "community-server",
		"bin/a8m-admin-server":   "community-admin",
		"bin/alt8mail-control":   "community-control",
		"bin/a8m-storage-runner": "community-storage",
		"VERSION":                "3.9.3",
	} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(base, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "enterprise.tar.gz")
	if err := os.WriteFile(bundlePath, buildTestEnterpriseBundle(t, "9.9.9"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousRestart := enterpriseRestart
	restarted := ""
	enterpriseRestart = func(_ context.Context, version string) error {
		restarted = version
		return nil
	}
	defer func() { enterpriseRestart = previousRestart }()
	if err := installEnterpriseRelease(context.Background(), root, bundlePath, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "9.9.9-enterprise-") {
		t.Fatalf("current -> %q", target)
	}
	server, _ := os.ReadFile(filepath.Join(target, "bin", "a8m-server"))
	control, _ := os.ReadFile(filepath.Join(target, "bin", "alt8mail-control"))
	storage, _ := os.ReadFile(filepath.Join(target, "bin", "a8m-storage-runner"))
	if string(server) != "server" || string(control) != "control" || string(storage) != "community-storage" {
		t.Fatalf("overlay server=%q control=%q storage=%q", server, control, storage)
	}
	marker, err := os.ReadFile(filepath.Join(target, "enterprise.json"))
	if err != nil || !strings.Contains(string(marker), `"9.9.9"`) {
		t.Fatalf("enterprise marker = %q err=%v", marker, err)
	}
	if restarted != "9.9.9" {
		t.Fatalf("restart = %q", restarted)
	}
}

func buildTestEnterpriseBundle(t *testing.T, version string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	base := fmt.Sprintf("alt8mail-enterprise-%s-linux-amd64", version)
	add := func(name, body string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	add(base+"/bin/a8m-server", "server")
	add(base+"/bin/a8m-admin-server", "admin")
	add(base+"/bin/alt8mail-control", "control")
	_ = tw.Close()
	_ = gz.Close()
	return buffer.Bytes()
}
