package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadStorageSwapEntriesAndMemory(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "swaps")
	meminfo := filepath.Join(dir, "meminfo")
	swappiness := filepath.Join(dir, "swappiness")
	if err := os.WriteFile(proc, []byte("Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n/swapfile                               file\t\t2097148\t\t1024\t\t-2\n/dev/vdc                                partition\t4194300\t\t0\t\t-3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meminfo, []byte("MemTotal:        2048000 kB\nMemAvailable:     512000 kB\nSwapTotal:       6291448 kB\nSwapFree:        6290424 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(swappiness, []byte("10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldProc, oldMem, oldSwap := storageSwapProcPath, storageSwapMeminfoPath, storageSwapSwappinessPath
	storageSwapProcPath, storageSwapMeminfoPath, storageSwapSwappinessPath = proc, meminfo, swappiness
	t.Cleanup(func() {
		storageSwapProcPath, storageSwapMeminfoPath, storageSwapSwappinessPath = oldProc, oldMem, oldSwap
	})

	entries, err := readStorageSwapEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "/swapfile" || entries[0].SizeBytes != 2097148*1024 || entries[0].UsedBytes != 1024*1024 || entries[0].Priority != -2 {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	total, free, swapTotal, swapFree := readStorageSwapMemory()
	if total != 2048000*1024 || free != 512000*1024 || swapTotal != 6291448*1024 || swapFree != 6290424*1024 {
		t.Fatalf("unexpected memory: total=%d free=%d swapTotal=%d swapFree=%d", total, free, swapTotal, swapFree)
	}
	if got := readStorageSwappiness(); got != 10 {
		t.Fatalf("swappiness = %d, want 10", got)
	}
}

func TestStorageSwapFstabBlocks(t *testing.T) {
	initial := "/etc/fstab\nUUID=root / ext4 defaults 0 1\n"
	block := storageSwapFstabBlock("swapfile-mail-stack", "/srv/a8m-storage/mail-stack/.a8m-swapfile", "/srv/a8m-storage/mail-stack/.a8m-swapfile")
	updated, err := upsertStorageSwapFstabBlock(initial, block, "swapfile-mail-stack", "/srv/a8m-storage/mail-stack/.a8m-swapfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated, "/srv/a8m-storage/mail-stack/.a8m-swapfile none swap sw 0 0") {
		t.Fatalf("fstab block missing swap line:\n%s", updated)
	}
	managed := storageSwapManagedDevices(updated)
	if !managed["/srv/a8m-storage/mail-stack/.a8m-swapfile"] {
		t.Fatalf("managed devices = %#v", managed)
	}
	if !storageSwapFstabHasSource(updated, "/srv/a8m-storage/mail-stack/.a8m-swapfile") {
		t.Fatal("managed source should be considered persistent")
	}
	removed, err := removeStorageSwapFstabBlocks(updated, "swapfile-mail-stack", "/srv/a8m-storage/mail-stack/.a8m-swapfile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(removed, "A8M SWAP") || strings.Contains(removed, ".a8m-swapfile") {
		t.Fatalf("swap block was not removed:\n%s", removed)
	}
	if !strings.Contains(removed, "UUID=root / ext4 defaults 0 1") {
		t.Fatalf("unmanaged fstab line was lost:\n%s", removed)
	}
}

func TestStorageSwapNamesAndRecommendation(t *testing.T) {
	name := storageSwapTargetName("swapfile", "Mail Stack")
	if name != "swapfile-mail-stack" {
		t.Fatalf("name = %q", name)
	}
	if label := storageSwapLabel(name); len(label) > 16 || !strings.HasPrefix(label, "a8m-") {
		t.Fatalf("label = %q", label)
	}
	if got := recommendedSwapSize(0); got != 2<<30 {
		t.Fatalf("recommended empty memory = %d", got)
	}
	if got := recommendedSwapSize(64 << 30); got != 8<<30 {
		t.Fatalf("recommended large memory = %d", got)
	}
	policy := storagePolicy{MinFreeBytes: 512 << 20}
	if got := storageSwapFileReserve(policy, 100<<30); got != 5<<30 {
		t.Fatalf("reserve = %d, want 5 GiB", got)
	}
	if err := storageSwapToolError(map[string]bool{"mkswap": true}, "mkswap", "swapon"); err == nil {
		t.Fatal("missing tool should fail")
	}
}

func TestStorageSwapFstabRejectsNestedMarkers(t *testing.T) {
	content := storageSwapFstabBeginPrefix + " name=a device=/dev/vda\n" +
		storageSwapFstabBeginPrefix + " name=b device=/dev/vdb\n" +
		storageSwapFstabEndPrefix + "\n"
	if _, err := removeStorageSwapFstabBlocks(content, "a", "/dev/vda"); err == nil {
		t.Fatal("nested markers should fail")
	}
}

func TestStorageSwapOperationSummary(t *testing.T) {
	raw := []byte(`{"kind":"swap_create","mode":"file","name":"swapfile-mail","volumeName":"mail","mountPath":"/srv/a8m-storage/mail","filePath":"/srv/a8m-storage/mail/.a8m-swapfile","sizeBytes":2147483648,"target":"/srv/a8m-storage/mail/.a8m-swapfile","confirmValue":"CREATE SWAP","commands":["swapon /srv/a8m-storage/mail/.a8m-swapfile"],"warnings":["warning"]}`)
	view := storageOperationSummary(storageOperation{Kind: "swap_create", Status: "planned", Plan: raw})
	if view["scope"] != "swap" || view["swapMode"] != "file" || view["swapTarget"] != "/srv/a8m-storage/mail/.a8m-swapfile" {
		t.Fatalf("unexpected swap summary: %#v", view)
	}
	if view["canRollback"] != false || view["canFinalize"] != false || view["canCancel"] != true {
		t.Fatalf("unexpected swap controls: %#v", view)
	}
	commands, ok := view["planCommands"].([]string)
	if !ok || len(commands) != 1 {
		t.Fatalf("commands = %#v", view["planCommands"])
	}
}

func TestStorageSwapRoutesRegister(t *testing.T) {
	server, err := New(Options{Config: Config{SecretKey: "storage-swap-route-test-secret-0123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	if handler := server.Handler(); handler == nil {
		t.Fatal("control handler is nil")
	}
}
