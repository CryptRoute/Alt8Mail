package control

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testMailOwner(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		mailOwnerLookup = func() (int, int, error) { return -1, -1, nil }
		return
	}
	uid, gid := os.Getuid(), os.Getgid()
	mailOwnerLookup = func() (int, int, error) { return uid, gid, nil }
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCopyMailHomeFallbackModes(t *testing.T) {
	oldLookup := mailOwnerLookup
	testMailOwner(t)
	defer func() { mailOwnerLookup = oldLookup }()

	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	writeTestFile(t, filepath.Join(source, "Maildir", "cur", "one"), "one")
	writeTestFile(t, filepath.Join(source, "Maildir", "new", "two"), "two")
	writeTestFile(t, filepath.Join(source, "sieve", "rules.sieve"), "rules")

	if err := copyMailHomeFallback(source, target, "initial"); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"Maildir/cur/one", "Maildir/new/two", "sieve/rules.sieve"} {
		content, err := os.ReadFile(filepath.Join(target, relative))
		if err != nil || string(content) != map[string]string{
			"Maildir/cur/one": "one", "Maildir/new/two": "two", "sieve/rules.sieve": "rules",
		}[relative] {
			t.Fatalf("unexpected copy for %s: %q err=%v", relative, content, err)
		}
	}

	// A delta must add new files without overwriting target data.
	writeTestFile(t, filepath.Join(target, "Maildir", "cur", "one"), "target-newer")
	writeTestFile(t, filepath.Join(source, "Maildir", "cur", "three"), "three")
	if err := copyMailHomeFallback(source, target, "delta"); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(target, "Maildir", "cur", "one")); string(content) != "target-newer" {
		t.Fatalf("delta overwrote target file: %q", content)
	}
	if _, err := os.Stat(filepath.Join(target, "Maildir", "cur", "three")); err != nil {
		t.Fatalf("delta did not copy new file: %v", err)
	}

	// An authoritative sync is allowed to remove target-only files.
	if err := os.Remove(filepath.Join(source, "Maildir", "new", "two")); err != nil {
		t.Fatal(err)
	}
	if err := copyMailHomeFallback(source, target, "authoritative"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "Maildir", "new", "two")); !os.IsNotExist(err) {
		t.Fatalf("authoritative sync retained a deleted source file: %v", err)
	}
}

func TestMailMessageStats(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Maildir", "cur", "one"), "12345")
	writeTestFile(t, filepath.Join(root, "Maildir", "new", "two"), "123")
	writeTestFile(t, filepath.Join(root, "Maildir", "dovecot.index"), "index")
	writeTestFile(t, filepath.Join(root, "Maildir", "maildirsize"), "quota")
	files, bytes, err := mailMessageStats(root)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 8 {
		t.Fatalf("message stats = %d files/%d bytes, want 2/8", files, bytes)
	}
}

func TestVerifyMailHomeFallback(t *testing.T) {
	oldLookup := mailOwnerLookup
	testMailOwner(t)
	defer func() { mailOwnerLookup = oldLookup }()

	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	writeTestFile(t, filepath.Join(source, "Maildir", "cur", "one"), "one")
	if err := copyMailHomeFallback(source, target, "initial"); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	if err := server.verifyMailHome(t.Context(), storageMoveItem{SourceHome: source, TargetHome: target}, "standard"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "Maildir", "cur", "extra"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.verifyMailHome(t.Context(), storageMoveItem{SourceHome: source, TargetHome: target}, "standard"); err != nil {
		t.Fatalf("a new target-only delivery must not fail verification: %v", err)
	}
	if err := os.Remove(filepath.Join(target, "Maildir", "cur", "one")); err != nil {
		t.Fatal(err)
	}
	if err := server.verifyMailHome(t.Context(), storageMoveItem{SourceHome: source, TargetHome: target}, "standard"); err == nil {
		t.Fatal("verification should fail when a source message is missing from the target")
	}
}

func TestRenameDomainHomes(t *testing.T) {
	oldLookup := mailOwnerLookup
	testMailOwner(t)
	defer func() { mailOwnerLookup = oldLookup }()

	root := t.TempDir()
	oldHome := filepath.Join(root, "old.example", "alice")
	writeTestFile(t, filepath.Join(oldHome, "Maildir", "cur", "one"), "one")
	server := &Server{}
	moved, err := server.renameDomainHomes([]domainMailboxHome{{
		Local: "alice", OldHome: oldHome, VolumeRoot: root,
	}}, "old.example", "new.example")
	if err != nil {
		t.Fatal(err)
	}
	newHome := filepath.Join(root, "new.example", "alice")
	if content, err := os.ReadFile(filepath.Join(newHome, "Maildir", "cur", "one")); err != nil || string(content) != "one" {
		t.Fatalf("renamed home missing content: %q err=%v", content, err)
	}
	if _, err := os.Stat(oldHome); !os.IsNotExist(err) {
		t.Fatalf("old home still exists: %v", err)
	}
	server.rollbackDomainHomes(moved)
	if _, err := os.Stat(oldHome); err != nil {
		t.Fatalf("rollback did not restore source: %v", err)
	}
}
