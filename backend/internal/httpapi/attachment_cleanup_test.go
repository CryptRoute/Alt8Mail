package httpapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidStoredAttachmentName(t *testing.T) {
	for _, name := range []string{"att_abc123", "att_-_ok"} {
		if !validStoredAttachmentName(name) {
			t.Fatalf("expected %q to be a valid stored name", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../att_x", "att_x/y", `att_x\y`, "other_id"} {
		if validStoredAttachmentName(name) {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestOrphanAttachmentCandidate(t *testing.T) {
	dir := t.TempDir()
	name := "att_orphan"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	referenced := map[string]bool{"att_referenced": true}
	if !orphanAttachmentCandidate(name, info.ModTime(), true, time.Now().Add(time.Minute), referenced) {
		t.Fatal("old unreferenced attachment should be a sweep candidate")
	}
	if orphanAttachmentCandidate(name, info.ModTime(), true, time.Now().Add(-time.Minute), referenced) {
		t.Fatal("a file newer than the grace cutoff must be retained")
	}
	if orphanAttachmentCandidate(name, info.ModTime(), true, time.Now().Add(time.Minute), map[string]bool{name: true}) {
		t.Fatal("a referenced attachment must be retained")
	}
	if orphanAttachmentCandidate("notes.txt", info.ModTime(), true, time.Now().Add(time.Minute), referenced) {
		t.Fatal("non-attachment files must be retained")
	}
}

func TestFullSyncRetryDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 5 * time.Minute},
		{1, 5 * time.Minute},
		{2, 10 * time.Minute},
		{3, 20 * time.Minute},
		{4, 40 * time.Minute},
		{5, time.Hour},
		{8, time.Hour},
	}
	for _, tc := range cases {
		if got := fullSyncRetryDelay(5*time.Minute, tc.failures); got != tc.want {
			t.Fatalf("failures=%d: got %s, want %s", tc.failures, got, tc.want)
		}
	}
}
