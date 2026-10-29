package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProvisionMaildirCreatesStandardFolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Maildir ownership is POSIX-specific")
	}
	base := t.TempDir()
	maildir, err := provisionMaildir(base, "a8m.test", "avery.chen", os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	if maildir != filepath.Join(base, "a8m.test", "avery.chen", "Maildir") {
		t.Fatalf("unexpected maildir %q", maildir)
	}
	for _, relative := range []string{"cur", "new", "tmp", ".Sent/cur", ".Drafts/new", ".Archive/tmp", ".Junk", ".Trash/cur"} {
		info, err := os.Stat(filepath.Join(maildir, relative))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
}

func TestProvisionMaildirRejectsTraversal(t *testing.T) {
	for _, parts := range [][2]string{{"../etc", "avery"}, {"a8m.test", ".."}, {"a8m.test", "a/b"}} {
		if _, err := provisionMaildir(t.TempDir(), parts[0], parts[1], os.Getuid(), os.Getgid()); err == nil {
			t.Fatalf("expected rejection for %q / %q", parts[0], parts[1])
		}
	}
}
