//go:build !windows

package control

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func licenseTestStat(t *testing.T, path string) (os.FileInfo, *syscall.Stat_t) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat for %s did not expose syscall.Stat_t", path)
	}
	return info, stat
}

func TestLicenseFileModeIsGroupReadable(t *testing.T) {
	publicKey, privateKey := licenseTestKey(t)
	path := filepath.Join(t.TempDir(), "license.token")
	token := licenseTestToken(t, privateKey, licenseTestLicense(time.Now()))
	if err := writeLicenseTokenFile(path, licenseTestPublicKey(publicKey), token, strconv.Itoa(os.Getegid()), nil); err != nil {
		t.Fatal(err)
	}
	info, stat := licenseTestStat(t, path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 0640", info.Mode().Perm())
	}
	// Non-root development installs cannot chown; the file stays owned by the
	// invoking user but must still be group-readable.
	if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
		t.Fatalf("owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, os.Geteuid(), os.Getegid())
	}
}

func TestLicenseFileUsesConfiguredGroupWhenRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to change file ownership")
	}
	publicKey, privateKey := licenseTestKey(t)
	path := filepath.Join(t.TempDir(), "license.token")
	gid := os.Getegid()
	token := licenseTestToken(t, privateKey, licenseTestLicense(time.Now()))
	if err := writeLicenseTokenFile(path, licenseTestPublicKey(publicKey), token, strconv.Itoa(gid), nil); err != nil {
		t.Fatal(err)
	}
	info, stat := licenseTestStat(t, path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 0640", info.Mode().Perm())
	}
	if int(stat.Uid) != 0 {
		t.Fatalf("uid = %d, want 0 (root)", stat.Uid)
	}
	if int(stat.Gid) != gid {
		t.Fatalf("gid = %d, want %d", stat.Gid, gid)
	}
}

func TestLicenseFilePreservesOwnerAndNormalizesGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to change file ownership")
	}
	publicKey, privateKey := licenseTestKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "license.token")
	licenseTestWriteFile(t, path, "old", 0o600)
	const wantUID = 12345
	// A legacy token may be owned by an unrelated group. The upload must keep
	// the owner uid but replace the group with the configured webmail group so
	// the unprivileged webmail can read the new token.
	originalGID := os.Getegid() + 1
	if err := os.Chown(path, wantUID, originalGID); err != nil {
		t.Skipf("cannot chown test file: %v", err)
	}
	configuredGID := os.Getegid()
	token := licenseTestToken(t, privateKey, licenseTestLicense(time.Now()))
	if err := writeLicenseTokenFile(path, licenseTestPublicKey(publicKey), token, strconv.Itoa(configuredGID), nil); err != nil {
		t.Fatal(err)
	}
	info, stat := licenseTestStat(t, path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 0640", info.Mode().Perm())
	}
	if int(stat.Uid) != wantUID || int(stat.Gid) != configuredGID {
		t.Fatalf("owner = %d:%d, want uid %d with normalized gid %d", stat.Uid, stat.Gid, wantUID, configuredGID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != token {
		t.Fatalf("license file was not replaced")
	}
}

func TestLicenseFileUnknownGroupFailsUpload(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("group lookup is only enforced for root installs")
	}
	publicKey, privateKey := licenseTestKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "license.token")
	err := writeLicenseTokenFile(path, licenseTestPublicKey(publicKey),
		licenseTestToken(t, privateKey, licenseTestLicense(time.Now())),
		"a8m-license-group-that-does-not-exist-xyz", nil)
	licenseTestAssertAPIError(t, err, 500, "LICENSE_WRITE")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed ownership resolution left a license file behind")
	}
	licenseTestAssertNoTempFiles(t, dir)
}
