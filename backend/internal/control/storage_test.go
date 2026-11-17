package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageNameFromPath(t *testing.T) {
	cases := map[string]string{
		"/srv/a8m-storage/mail-02": "mail-02",
		"/srv/a8m-storage/Mail 03": "mail-03",
		"/":                        "volume",
		"/srv/___":                 "volume",
	}
	for input, want := range cases {
		if got := storageNameFromPath(input); got != want {
			t.Fatalf("storageNameFromPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsPseudoFilesystem(t *testing.T) {
	for _, fs := range []string{"proc", "sysfs", "tmpfs", "overlay", "cgroup2"} {
		if !isPseudoFilesystem(fs) {
			t.Fatalf("expected %s to be pseudo", fs)
		}
	}
	for _, fs := range []string{"ext4", "xfs", "btrfs"} {
		if isPseudoFilesystem(fs) {
			t.Fatalf("expected %s to be real", fs)
		}
	}
}

func TestNormalizeStorageDiskClassification(t *testing.T) {
	candidate := normalizeStorageDisk(lsblkNode{
		Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: float64(5 << 30),
		Serial: storageString("SN-1"), Model: storageString("Virtual Disk"),
	}, "")
	if !candidate.Candidate || candidate.Protected {
		t.Fatalf("blank disk should be a candidate: %+v", candidate)
	}

	withChildren := normalizeStorageDisk(lsblkNode{
		Name: "sda", Path: "/dev/sda", Type: "disk", Size: float64(500 << 30),
		Children: []lsblkNode{{Name: "sda1", Path: "/dev/sda1", Type: "part", Size: float64(499 << 30)}},
	}, "")
	if !withChildren.Protected || withChildren.Candidate {
		t.Fatalf("disk with children must be protected: %+v", withChildren)
	}

	system := normalizeStorageDisk(lsblkNode{
		Name: "sda1", Path: "/dev/sda1", Type: "part", Size: float64(500 << 30),
		Mountpoint: storageString("/"), FSType: storageString("ext4"),
	}, "sda")
	if !system.Protected || system.Candidate || !system.HasSignatures {
		t.Fatalf("root filesystem must be protected: %+v", system)
	}

	partition := normalizeStorageDisk(lsblkNode{
		Name: "sdb1", Path: "/dev/sdb1", Type: "part", Size: float64(100 << 30),
	}, "sdb")
	if partition.Candidate {
		t.Fatalf("a partition is not a whole-disk candidate: %+v", partition)
	}
}

func storageString(value string) *string { return &value }

func TestStorageDoctorDetectsLegacyUserQuery(t *testing.T) {
	root := t.TempDir()
	vmail := filepath.Join(root, "vmail")
	dovecot := filepath.Join(root, "dovecot")
	if err := os.MkdirAll(vmail, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dovecot, 0o750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dovecot, "dovecot-sql.conf.ext")
	legacy := "user_query = SELECT '/var/vmail/' || m.domain || '/' || m.local_part AS home, 5000 AS uid, 5000 AS gid FROM mailboxes m;\n"
	if err := os.WriteFile(configPath, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{VmailRoot: vmail, StorageRoot: filepath.Join(root, "storage"), DovecotDir: dovecot}}
	doctor := server.storageDoctor(context.Background())
	if doctor.UserDBReady {
		t.Fatal("legacy user_query must not be ready")
	}
	if !hasStorageIssue(doctor, "dovecot_userdb_legacy") {
		t.Fatalf("expected dovecot_userdb_legacy issue: %+v", doctor.Issues)
	}
	managed := strings.Replace(legacy, "'/var/vmail/' || m.domain || '/' || m.local_part AS home", "COALESCE(NULLIF(m.mail_home, ''), '/var/vmail/' || m.domain || '/' || m.local_part) AS home", 1)
	if err := os.WriteFile(configPath, []byte(managed), 0o640); err != nil {
		t.Fatal(err)
	}
	doctor = server.storageDoctor(context.Background())
	if !doctor.UserDBReady {
		t.Fatalf("managed user_query should be ready: %+v", doctor)
	}
	if hasStorageIssue(doctor, "dovecot_userdb_legacy") {
		t.Fatalf("managed query still reports legacy: %+v", doctor.Issues)
	}
}

func TestStorageDoctorReportsMissingLegacyRoot(t *testing.T) {
	server := &Server{cfg: Config{
		VmailRoot:   filepath.Join(t.TempDir(), "missing-vmail"),
		StorageRoot: filepath.Join(t.TempDir(), "missing-storage"),
		DovecotDir:  t.TempDir(),
	}}
	doctor := server.storageDoctor(context.Background())
	if !hasStorageIssue(doctor, "legacy_root_missing") {
		t.Fatalf("expected missing legacy root issue: %+v", doctor.Issues)
	}
}

func TestSameStoragePath(t *testing.T) {
	if !sameStoragePath("/var/vmail", "/var/vmail/") {
		t.Fatal("trailing slash should compare equal")
	}
	if sameStoragePath("/var/vmail", "/var/vmail-other") {
		t.Fatal("prefix paths must not compare equal")
	}
}

func TestPlacementPermissionMatrix(t *testing.T) {
	admin := permissionMap(rolePermissions("admin"))
	if !admin["storage.manage"] {
		t.Fatal("admin should manage storage placements")
	}
	if admin["storage.destroy"] {
		t.Fatal("admin must not have the destructive storage permission")
	}
	operator := permissionMap(rolePermissions("operator"))
	if !operator["storage.read"] {
		t.Fatal("operator should read storage")
	}
	if operator["storage.manage"] {
		t.Fatal("operator must not manage storage")
	}
	if !permissionMap(rolePermissions("superadmin"))["*"] {
		t.Fatal("superadmin should have the wildcard")
	}
	if !permissionMap(allPermissions)["storage.destroy"] {
		t.Fatal("storage.destroy must be a declared permission")
	}
}

func hasStorageIssue(doctor storageDoctor, code string) bool {
	for _, issue := range doctor.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
