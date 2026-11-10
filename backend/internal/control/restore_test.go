package control

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"
)

func writeRestoreFixture(t *testing.T, root string, modTime time.Time, domain, local string) string {
	t.Helper()
	archive := filepath.Join(root, "backup.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	base := path.Join("mail", "vmail", domain, local, "Maildir")
	add := func(name, body string, mode byte) {
		t.Helper()
		header := &tar.Header{Name: path.Join(base, name), Mode: 0o600, Size: int64(len(body)), ModTime: modTime, Typeflag: mode}
		if err := tw.WriteHeader(header); err == nil && mode == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	add("cur/inbox1", "inbox", tar.TypeReg)
	add(".Sent/cur/sent1", "sent-one", tar.TypeReg)
	add(".Sent/new/sent2", "sent-two", tar.TypeReg)
	add(".Sent/tmp/tmp1", "tmp", tar.TypeReg)
	add(".Sent/cur/link", "", tar.TypeSymlink)
	add("../../escape", "nope", tar.TypeReg)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestRestoreFolderMapping(t *testing.T) {
	cases := map[string][3]string{
		"":         {"inbox", "", "INBOX"},
		"inbox":    {"inbox", "", "INBOX"},
		"Sent":     {"sent", ".Sent", "Sent"},
		"drafts":   {"drafts", ".Drafts", "Drafts"},
		"trash":    {"trash", ".Trash", "Trash"},
		"spam":     {"spam", ".Junk", "Junk"},
		"archive":  {"archive", ".Archive", "Archive"},
		"Projects": {"projects", ".projects", "projects"},
	}
	for raw, want := range cases {
		folder, dir, dest, err := restoreFolderMapping(raw)
		if err != nil || folder != want[0] || dir != want[1] || dest != want[2] {
			t.Errorf("restoreFolderMapping(%q) = %q %q %q %v; want %q", raw, folder, dir, dest, err, want)
		}
	}
	if _, _, _, err := restoreFolderMapping("../bad"); err == nil {
		t.Fatal("traversal folder was accepted")
	}
}

func TestParseRestoreBound(t *testing.T) {
	bound, err := parseRestoreBound("2026-11-09", false)
	if err != nil || bound == nil || bound.Hour() != 0 || bound.Minute() != 0 {
		t.Fatalf("start bound = %v %v", bound, err)
	}
	end, err := parseRestoreBound("2026-11-09", true)
	if err != nil || end == nil || end.Hour() != 23 || end.Minute() != 59 {
		t.Fatalf("end bound = %v %v", end, err)
	}
	rfc, err := parseRestoreBound("2026-11-09T12:00:00Z", false)
	if err != nil || rfc == nil || !rfc.Equal(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("rfc bound = %v %v", rfc, err)
	}
	if _, err := parseRestoreBound("not-a-date", false); err == nil {
		t.Fatal("invalid date was accepted")
	}
}

func TestScanAndExtractMailboxArchive(t *testing.T) {
	root := t.TempDir()
	modTime := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	archive := writeRestoreFixture(t, root, modTime, "example.test", "alice")
	live := filepath.Join(root, "live")
	if err := os.MkdirAll(filepath.Join(live, ".Sent", "cur"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, ".Sent", "cur", "sent1"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := mailboxRestoreTarget{
		Email: "alice@example.test", Domain: "example.test", Local: "alice",
		MaildirPath: live, Folder: "sent", MaildirDir: ".Sent", Destination: "Sent",
	}
	preview, err := (&Server{}).scanMailboxArchive(archive, target, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Messages != 2 || preview.Conflicts != 1 || preview.Bytes != int64(len("sent-one")+len("sent-two")) {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Earliest == "" || preview.Latest == "" {
		t.Fatalf("preview time bounds missing: %#v", preview)
	}
	since := modTime.Add(-time.Hour)
	until := modTime.Add(-2 * time.Hour)
	filtered, err := (&Server{}).scanMailboxArchive(archive, target, &since, &until)
	if err != nil || filtered.Messages != 0 {
		t.Fatalf("out-of-range scan = %#v %v", filtered, err)
	}

	stage := filepath.Join(root, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	count, err := (&Server{}).extractMaildirArchive(archive, target, stage, nil, nil)
	if err != nil || count != 1 {
		t.Fatalf("extract count = %d err=%v", count, err)
	}
	if _, err := os.Stat(filepath.Join(stage, "cur", "sent1")); !os.IsNotExist(err) {
		t.Fatal("conflicting live message was extracted")
	}
	for _, rel := range []string{"new/sent2"} {
		if _, err := os.Stat(filepath.Join(stage, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected extracted file %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "tmp", "tmp1")); !os.IsNotExist(err) {
		t.Fatal("tmp entry was extracted")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatal("traversal entry escaped the staging directory")
	}
	if _, ok := restoreEntryRelative("mail/vmail/example.test/alice/Maildir/.Sent/cur/../../escape", "mail/vmail/example.test/alice/Maildir/.Sent"); ok {
		t.Fatal("traversal entry was accepted")
	}
}

func TestRestoreBackupPathAndResolveRequireDatabase(t *testing.T) {
	// These paths require PostgreSQL and are covered by the DSN-gated
	// integration tests when A8M_SCIM_TEST_DSN is configured.
	if os.Getenv("A8M_SCIM_TEST_DSN") == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run restore database integration")
	}
}
