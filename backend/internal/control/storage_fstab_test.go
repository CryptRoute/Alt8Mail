package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageFstabRoundTrip(t *testing.T) {
	base := "# /etc/fstab\nUUID=root / ext4 defaults 0 1\n"
	block := storageFstabBlock("mail-02", "6f0c2a1e-1111-2222-3333-444455556666", "/srv/a8m-storage/mail-02", "ext4")
	updated, err := upsertStorageFstabBlock(base, block, "mail-02", "6f0c2a1e-1111-2222-3333-444455556666")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated, "UUID=root / ext4") {
		t.Fatal("unmanaged fstab line was removed")
	}
	if !strings.Contains(updated, "/srv/a8m-storage/mail-02 ext4") {
		t.Fatalf("managed line missing: %s", updated)
	}
	again, err := upsertStorageFstabBlock(updated, block, "mail-02", "6f0c2a1e-1111-2222-3333-444455556666")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again, storageFstabBeginPrefix) != 1 {
		t.Fatalf("expected one managed block, got %d", strings.Count(again, storageFstabBeginPrefix))
	}
	removed, err := removeStorageFstabBlocks(again, "mail-02", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(removed, "a8m-storage/mail-02") || !strings.Contains(removed, "UUID=root / ext4") {
		t.Fatalf("unexpected fstab after removal: %s", removed)
	}
}

func TestStorageFstabMarkerNameIsExact(t *testing.T) {
	block := storageFstabBlock("mail-02", "uuid-2", "/srv/a8m-storage/mail-02", "ext4")
	content, err := upsertStorageFstabBlock("", block, "mail-02", "uuid-2")
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := removeStorageFstabBlocks(content, "mail", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(untouched, "mail-02") {
		t.Fatal("a shorter volume name matched a different managed block")
	}
}

func TestStorageFstabRejectsMalformedMarkers(t *testing.T) {
	if _, err := removeStorageFstabBlocks(storageFstabBeginPrefix+" volume=x name=mail\nUUID=x /mnt ext4 defaults 0 2\n", "", "x"); err == nil {
		t.Fatal("unterminated managed block was accepted")
	}
	if _, err := removeStorageFstabBlocks(storageFstabEndPrefix+" volume=x name=mail\n", "", "x"); err == nil {
		t.Fatal("orphan end marker was accepted")
	}
}

func TestStorageMountPathValidation(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "mail-02"), filepath.Join(root, "archive", "mail-03")} {
		if _, err := validateStorageMountPath(root, path); err != nil {
			t.Fatalf("valid path %q rejected: %v", path, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	for _, path := range []string{root, outside, filepath.Join(root, "bad path"), "relative"} {
		if _, err := validateStorageMountPath(root, path); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}
}

func TestStoragePartitionDevice(t *testing.T) {
	cases := map[string]string{
		"/dev/sdb":     "/dev/sdb1",
		"/dev/nvme0n1": "/dev/nvme0n1p1",
		"/dev/mmcblk0": "/dev/mmcblk0p1",
	}
	for disk, want := range cases {
		if got := storagePartitionDevice(disk, 1); got != want {
			t.Fatalf("partition path for %s = %s, want %s", disk, got, want)
		}
	}
}

func TestStorageFstabHasMountPath(t *testing.T) {
	content := "UUID=x /srv/a8m-storage/mail-02 ext4 defaults 0 2\n"
	if !storageFstabHasMountPath(content, "/srv/a8m-storage/mail-02") {
		t.Fatal("existing mount path not detected")
	}
	if storageFstabHasMountPath(content, "/srv/a8m-storage/mail-03") {
		t.Fatal("different mount path matched")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	if err := writeFileAtomic(path, "one\n", 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, "two\n", 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "two\n" {
		t.Fatalf("atomic write = %q err=%v", raw, err)
	}
}
