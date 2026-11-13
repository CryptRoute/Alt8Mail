package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStorageSwapFilePlanAndRunner exercises the file-swap planner and runner
// without touching the host swap state. It requires A8M_STORAGE_TEST_DSN.
func TestStorageSwapFilePlanAndRunner(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the swap integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	root := t.TempDir()
	fstabPath := filepath.Join(root, "fstab")
	procPath := filepath.Join(root, "swaps")
	meminfoPath := filepath.Join(root, "meminfo")
	swappinessPath := filepath.Join(root, "swappiness")
	if err := os.WriteFile(fstabPath, []byte("UUID=root / ext4 defaults 0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(procPath, []byte("Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meminfoPath, []byte("MemTotal: 8388608 kB\nMemAvailable: 3145728 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(swappinessPath, []byte("60\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldGOOS := storageHostGOOS
	oldLookup := storageLookPath
	oldCommand := storageHostCommand
	oldStat := filesystemStatLookup
	oldProc, oldMem, oldSwappiness := storageSwapProcPath, storageSwapMeminfoPath, storageSwapSwappinessPath
	storageHostGOOS = "linux"
	storageLookPath = func(string) (string, error) { return "/usr/bin/tool", nil }
	storageSwapProcPath, storageSwapMeminfoPath, storageSwapSwappinessPath = procPath, meminfoPath, swappinessPath
	filesystemStatLookup = func(string) (filesystemStat, error) {
		return filesystemStat{TotalBytes: 20 << 30, UsedBytes: 5 << 30, FreeBytes: 15 << 30, UsedPercent: 25}, nil
	}
	storageHostCommand = func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
		switch name {
		case "findmnt":
			if len(args) > 0 && args[len(args)-1] == "--verify" {
				return commandResult{}, nil
			}
			return commandResult{Stdout: "ext4\n"}, nil
		case "fallocate":
			if len(args) < 3 {
				return commandResult{}, fmt.Errorf("bad fallocate arguments")
			}
			if err := os.WriteFile(args[len(args)-1], []byte("swap"), 0o644); err != nil {
				return commandResult{}, err
			}
			return commandResult{}, nil
		case "chmod":
			return commandResult{}, nil
		case "mkswap":
			return commandResult{}, nil
		case "swapon":
			path := args[len(args)-1]
			file, err := os.OpenFile(procPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return commandResult{}, err
			}
			defer file.Close()
			_, err = fmt.Fprintf(file, "%s file 2097152 0 -2\n", path)
			return commandResult{}, err
		case "swapoff", "systemctl":
			return commandResult{}, nil
		default:
			return commandResult{}, nil
		}
	}
	t.Cleanup(func() {
		storageHostGOOS = oldGOOS
		storageLookPath = oldLookup
		storageHostCommand = oldCommand
		filesystemStatLookup = oldStat
		storageSwapProcPath, storageSwapMeminfoPath, storageSwapSwappinessPath = oldProc, oldMem, oldSwappiness
	})

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	mountPath := filepath.Join(root, "mail-stack")
	if err := os.MkdirAll(mountPath, 0o750); err != nil {
		t.Fatal(err)
	}
	var volumeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes (name, mount_path, fs_type, role, managed, state, created_by)
		VALUES ($1, $2, 'ext4', 'mail', true, 'online', 'test') RETURNING id::text
	`, "swap-test-"+suffix, mountPath).Scan(&volumeID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE id = $1`, volumeID) }()
	server := &Server{db: pool, cfg: Config{StorageRoot: root, StorageFstab: fstabPath, BackupDir: filepath.Join(root, "backup")}}

	plan, err := server.buildStorageSwapPlan(ctx, storageSwapPlanRequest{Mode: "file", VolumeID: volumeID, SizeBytes: 2 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "file" || plan.FilePath != filepath.Join(mountPath, ".a8m-swapfile") || plan.ConfirmValue != "CREATE SWAP" || plan.RequireDestroy {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if _, err := server.buildStorageSwapPlan(ctx, storageSwapPlanRequest{Mode: "file", VolumeID: volumeID, SizeBytes: 100 << 30}); err == nil {
		t.Fatal("oversized swap plan should fail")
	}

	if err := server.runSwapFileCreate(ctx, storageOperation{ID: "swap-test-op"}, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.FilePath); err != nil {
		t.Fatalf("swap file was not created: %v", err)
	}
	fstab, _ := os.ReadFile(fstabPath)
	if !strings.Contains(string(fstab), "A8M SWAP") || !strings.Contains(string(fstab), plan.FilePath+" none swap sw 0 0") {
		t.Fatalf("managed swap fstab block missing:\n%s", fstab)
	}
	entries, err := readStorageSwapEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != plan.FilePath {
		t.Fatalf("swap entries = %#v", entries)
	}
}

func TestStorageSwapDevicePlanAndRunner(t *testing.T) {
	root := t.TempDir()
	fstabPath := filepath.Join(root, "fstab")
	procPath := filepath.Join(root, "swaps")
	if err := os.WriteFile(fstabPath, []byte("UUID=root / ext4 defaults 0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(procPath, []byte("Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldGOOS := storageHostGOOS
	oldLookup := storageLookPath
	oldCommand := storageHostCommand
	oldDiscovery := storageDiscoveryCommand
	oldProc := storageSwapProcPath
	storageHostGOOS = "linux"
	storageLookPath = func(string) (string, error) { return "/usr/bin/tool", nil }
	storageSwapProcPath = procPath
	storageDiscoveryCommand = func(_ context.Context, _ time.Duration, _ string, _ ...string) (commandResult, error) {
		return commandResult{Stdout: `{"blockdevices":[{"name":"vdd","path":"/dev/vdd","type":"disk","size":107374182400,"fstype":null,"label":null,"uuid":null,"mountpoint":null,"rota":false,"model":"QEMU HARDDISK","serial":"SWAP-DISK-1","wwn":"","tran":"virtio","pkname":null,"children":[]}]}`}, nil
	}
	storageHostCommand = func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
		switch name {
		case "findmnt":
			return commandResult{Stdout: "ext4\n"}, nil
		case "blkid":
			return commandResult{Stdout: "swap-uuid-1234\n"}, nil
		case "swapon":
			file, err := os.OpenFile(procPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return commandResult{}, err
			}
			defer file.Close()
			_, err = fmt.Fprintf(file, "%s partition 209715200 0 -2\n", args[0])
			return commandResult{}, err
		default:
			return commandResult{}, nil
		}
	}
	t.Cleanup(func() {
		storageHostGOOS = oldGOOS
		storageLookPath = oldLookup
		storageHostCommand = oldCommand
		storageDiscoveryCommand = oldDiscovery
		storageSwapProcPath = oldProc
	})
	server := &Server{cfg: Config{StorageRoot: root, StorageFstab: fstabPath, BackupDir: filepath.Join(root, "backup")}}
	plan, err := server.buildStorageSwapDevicePlan(context.Background(), storageSwapPlanRequest{Mode: "device", DevicePath: "/dev/vdd", DeviceSerial: "SWAP-DISK-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequireDestroy || plan.ConfirmValue != "ERASE /dev/vdd" || plan.Target != "/dev/vdd" {
		t.Fatalf("unexpected device plan: %#v", plan)
	}
	if err := server.runSwapDeviceCreate(context.Background(), storageOperation{ID: "swap-device-test"}, plan); err != nil {
		t.Fatal(err)
	}
	fstab, _ := os.ReadFile(fstabPath)
	if !strings.Contains(string(fstab), "UUID=swap-uuid-1234 none swap sw 0 0") {
		t.Fatalf("device swap fstab line missing:\n%s", fstab)
	}
	entries, err := readStorageSwapEntries()
	if err != nil || len(entries) != 1 || entries[0].Path != "/dev/vdd" {
		t.Fatalf("device swap entry = %#v, err=%v", entries, err)
	}
}
